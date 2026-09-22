package gaia

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"

	sdkmath "cosmossdk.io/math"
	cmtcoretypes "github.com/cometbft/cometbft/rpc/core/types"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/types"
	errortypes "github.com/cosmos/cosmos-sdk/types/errors"
	txtypes "github.com/cosmos/cosmos-sdk/types/tx"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/tx"
	btypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	. "gopkg.in/check.v1"
)

func TestPackage(t *testing.T) { TestingT(t) }

// fakeCometRPC is an injectable cometMempoolClient for verifyFn mempool tests.
type fakeCometRPC struct {
	res *cmtcoretypes.ResultUnconfirmedTxs
	err error
}

func (f fakeCometRPC) UnconfirmedTxs(context.Context, *int) (*cmtcoretypes.ResultUnconfirmedTxs, error) {
	return f.res, f.err
}

// TestGaiaMempoolLookup covers the CometBFT mempool fallback used by the signer-
// cache verifyFn once GetTx reports the tx as not committed: present → respect
// cache, definitively absent → clear + re-sign, and any uncertainty (RPC error,
// nil client, or a mempool larger than we could fetch) → fail-closed.
func TestGaiaMempoolLookup(t *testing.T) {
	pending := cmttypes.Tx([]byte("pending-broadcast-bytes"))
	sig := fmt.Sprintf("%X", pending.Hash())
	ctx := context.Background()

	cases := []struct {
		name string
		rpc  cometMempoolClient
		want signercache.LookupResult
	}{
		{"nil client", nil, signercache.LookupAmbiguous},
		{"rpc error", fakeCometRPC{err: errors.New("boom")}, signercache.LookupAmbiguous},
		{"present in mempool", fakeCometRPC{res: &cmtcoretypes.ResultUnconfirmedTxs{Txs: []cmttypes.Tx{pending}, Total: 1}}, signercache.LookupFound},
		{"absent, full mempool scanned", fakeCometRPC{res: &cmtcoretypes.ResultUnconfirmedTxs{Txs: []cmttypes.Tx{}, Total: 0}}, signercache.LookupNotFound},
		{"mempool larger than fetched", fakeCometRPC{res: &cmtcoretypes.ResultUnconfirmedTxs{Txs: []cmttypes.Tx{[]byte("other")}, Total: 99}}, signercache.LookupAmbiguous},
	}
	for _, tc := range cases {
		cc := CosmosClient{cometRPC: tc.rpc}
		if got := cc.mempoolLookup(ctx, sig); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestGaiaVerifyBroadcast covers the signer-cache verifyFn routing: a committed tx
// (GetTx ok) is present; a not-committed tx falls back to the mempool; any other
// gRPC error is ambiguous (fail-closed).
func TestGaiaVerifyBroadcast(t *testing.T) {
	pending := cmttypes.Tx([]byte("pending-broadcast-bytes"))
	sig := fmt.Sprintf("%X", pending.Hash())
	notCommitted := status.Error(codes.NotFound, "tx not found")
	inMempool := fakeCometRPC{res: &cmtcoretypes.ResultUnconfirmedTxs{Txs: []cmttypes.Tx{pending}, Total: 1}}
	emptyMempool := fakeCometRPC{res: &cmtcoretypes.ResultUnconfirmedTxs{Txs: []cmttypes.Tx{}, Total: 0}}

	cases := []struct {
		name     string
		getTxErr error
		rpc      cometMempoolClient
		want     signercache.LookupResult
	}{
		{"committed", nil, emptyMempool, signercache.LookupFound},
		{"not committed, in mempool", notCommitted, inMempool, signercache.LookupFound},
		{"not committed, absent", notCommitted, emptyMempool, signercache.LookupNotFound},
		{"grpc error", errors.New("boom"), emptyMempool, signercache.LookupAmbiguous},
	}
	for _, tc := range cases {
		cc := CosmosClient{txClient: mockTxServiceClient{getTxErr: tc.getTxErr}, cometRPC: tc.rpc}
		if got := cc.verifyBroadcast(sig); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

type mockTxServiceClient struct {
	resp     *txtypes.BroadcastTxResponse
	err      error
	getTxErr error
}

func (m mockTxServiceClient) Simulate(context.Context, *txtypes.SimulateRequest, ...grpc.CallOption) (*txtypes.SimulateResponse, error) {
	return nil, nil
}

func (m mockTxServiceClient) GetTx(context.Context, *txtypes.GetTxRequest, ...grpc.CallOption) (*txtypes.GetTxResponse, error) {
	return nil, m.getTxErr
}

func (m mockTxServiceClient) BroadcastTx(context.Context, *txtypes.BroadcastTxRequest, ...grpc.CallOption) (*txtypes.BroadcastTxResponse, error) {
	return m.resp, m.err
}

func (m mockTxServiceClient) GetTxsEvent(context.Context, *txtypes.GetTxsEventRequest, ...grpc.CallOption) (*txtypes.GetTxsEventResponse, error) {
	return nil, nil
}

func (m mockTxServiceClient) GetBlockWithTxs(context.Context, *txtypes.GetBlockWithTxsRequest, ...grpc.CallOption) (*txtypes.GetBlockWithTxsResponse, error) {
	return nil, nil
}

func (m mockTxServiceClient) TxDecode(context.Context, *txtypes.TxDecodeRequest, ...grpc.CallOption) (*txtypes.TxDecodeResponse, error) {
	return nil, nil
}

func (m mockTxServiceClient) TxEncode(context.Context, *txtypes.TxEncodeRequest, ...grpc.CallOption) (*txtypes.TxEncodeResponse, error) {
	return nil, nil
}

func (m mockTxServiceClient) TxEncodeAmino(context.Context, *txtypes.TxEncodeAminoRequest, ...grpc.CallOption) (*txtypes.TxEncodeAminoResponse, error) {
	return nil, nil
}

func (m mockTxServiceClient) TxDecodeAmino(context.Context, *txtypes.TxDecodeAminoRequest, ...grpc.CallOption) (*txtypes.TxDecodeAminoResponse, error) {
	return nil, nil
}

func TestBroadcastTxAllowsWrongSequence(t *testing.T) {
	t.Parallel()

	client := CosmosClient{
		txClient: mockTxServiceClient{
			resp: &txtypes.BroadcastTxResponse{
				TxResponse: &types.TxResponse{
					Code:   errortypes.ErrWrongSequence.ABCICode(),
					TxHash: "WRONGSEQ",
				},
			},
		},
		accts: NewCosmosMetaDataStore(),
	}

	txid, err := client.BroadcastTx(stypes.TxOutItem{}, []byte("signed"))
	if err != nil {
		t.Fatalf("expected wrong-sequence broadcast to be tolerated, got %v", err)
	}
	if txid != "WRONGSEQ" {
		t.Fatalf("expected returned txid to be preserved, got %q", txid)
	}
}

func TestBroadcastTxAllowsTxInMempoolCache(t *testing.T) {
	t.Parallel()

	client := CosmosClient{
		txClient: mockTxServiceClient{
			resp: &txtypes.BroadcastTxResponse{
				TxResponse: &types.TxResponse{
					Code:   errortypes.ErrTxInMempoolCache.ABCICode(),
					TxHash: "INMEMPOOL",
				},
			},
		},
		accts: NewCosmosMetaDataStore(),
	}

	txid, err := client.BroadcastTx(stypes.TxOutItem{}, []byte("signed"))
	if err != nil {
		t.Fatalf("expected mempool-cache broadcast to be tolerated, got %v", err)
	}
	if txid != "INMEMPOOL" {
		t.Fatalf("expected returned txid to be preserved, got %q", txid)
	}
}

type CosmosTestSuite struct {
	thordir  string
	thorKeys *thorclient.Keys
	bridge   thorclient.ThorchainBridge
	m        *metrics.Metrics
}

var _ = Suite(&CosmosTestSuite{})

var m *metrics.Metrics

func GetMetricForTest(c *C) *metrics.Metrics {
	if m == nil {
		var err error
		m, err = metrics.NewMetrics(config.BifrostMetricsConfiguration{
			Enabled:      false,
			ListenPort:   9000,
			ReadTimeout:  time.Second,
			WriteTimeout: time.Second,
			Chains:       common.Chains{common.GAIAChain},
		})
		c.Assert(m, NotNil)
		c.Assert(err, IsNil)
	}
	return m
}

func (s *CosmosTestSuite) SetUpSuite(c *C) {
	cosmosSDKConfg := cosmos.GetConfig()
	cosmosSDKConfg.SetBech32PrefixForAccount("sthor", "sthorpub")

	s.m = GetMetricForTest(c)
	c.Assert(s.m, NotNil)
	ns := strconv.Itoa(time.Now().Nanosecond())
	c.Assert(os.Setenv("NET", "stagenet"), IsNil)

	s.thordir = filepath.Join(os.TempDir(), ns, ".thorcli")
	cfg := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: s.thordir,
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb := cKeys.NewInMemory(cdc)
	_, _, err := kb.NewMnemonic(cfg.SignerName, cKeys.English, cmd.THORChainHDPath, cfg.SignerPasswd, hd.Secp256k1)
	c.Assert(err, IsNil)
	s.thorKeys = thorclient.NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd)
	c.Assert(err, IsNil)
	s.bridge, err = thorclient.NewThorchainBridge(cfg, s.m, s.thorKeys)
	c.Assert(err, IsNil)
}

func (s *CosmosTestSuite) TearDownSuite(c *C) {
	c.Assert(os.Unsetenv("NET"), IsNil)
	if err := os.RemoveAll(s.thordir); err != nil {
		c.Error(err)
	}
}

func testCosmosScannerWithLastFee(scanner *CosmosBlockScanner, fee sdkmath.Uint) *CosmosBlockScanner {
	scanner.setLastFee(fee)
	return scanner
}

// TestGetGasEstimateForSolvency verifies the observed Cosmos fee is converted
// from native gas units into THORChain 1e8 units without extra multipliers.
func (s *CosmosTestSuite) TestGetGasEstimateForSolvency(c *C) {
	client := &CosmosClient{
		cfg: config.BifrostChainConfiguration{
			ChainID: common.GAIAChain,
		},
		cosmosScanner: &CosmosBlockScanner{},
	}
	client.cosmosScanner.setLastFee(sdkmath.NewUint(25_000))

	got := client.GetGasEstimateForSolvency()
	c.Assert(got.Equal(cosmos.NewUint(2_500_000)), Equals, true)
}

func (s *CosmosTestSuite) TestGetAddress(c *C) {
	mockBankServiceClient := NewMockBankServiceClient()
	mockAccountServiceClient := NewMockAccountServiceClient()

	cfg := config.BifrostBlockScannerConfiguration{
		ChainID: common.GAIAChain,
		WhitelistCosmosAssets: []config.WhitelistCosmosAsset{
			{Denom: "uatom", Decimals: 6, THORChainSymbol: "ATOM"},
		},
	}

	cc := CosmosClient{
		cfg:           config.BifrostChainConfiguration{ChainID: common.GAIAChain},
		bankClient:    mockBankServiceClient,
		accountClient: mockAccountServiceClient,
		cosmosScanner: &CosmosBlockScanner{cfg: cfg},
	}

	addr := "cosmos10tjz4ave7znpctgd2rfu6v2r6zkeup2dlmqtuz"
	atom, _ := common.NewAsset("GAIA.ATOM")
	expectedCoins := common.NewCoins(
		common.NewCoin(atom, cosmos.NewUint(496694100)),
	)

	acc, err := cc.GetAccountByAddress(addr, big.NewInt(0))
	c.Assert(err, IsNil)
	c.Check(acc.AccountNumber, Equals, int64(3530305))
	c.Check(acc.Sequence, Equals, int64(3))
	c.Check(acc.Coins.EqualsEx(expectedCoins), Equals, true)

	pk := common.PubKey("sthorpub1addwnpepqf72ur2e8zk8r5augtrly40cuy94f7e663zh798tyms6pu2k8qdswf4es66")
	acc, err = cc.GetAccount(pk, big.NewInt(0))
	c.Assert(err, IsNil)
	c.Check(acc.AccountNumber, Equals, int64(3530305))
	c.Check(acc.Sequence, Equals, int64(3))
	c.Check(acc.Coins.EqualsEx(expectedCoins), Equals, true)

	resultAddr := cc.GetAddress(pk)

	c.Logf(resultAddr)
	c.Check(addr, Equals, resultAddr)
}

func (s *CosmosTestSuite) TestProcessOutboundTx(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
	}))

	// This test does not actually contact the gRPC endpoint,
	// but gRPC throws an error if a TCP connection cannot be establish.
	// This hack switches the schema for the mock HTTP server
	// to keep gRPC happy.
	fakeGRPCHost := strings.ReplaceAll(server.URL, "http://", "grpc://")

	client, err := NewCosmosClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID:        common.GAIAChain,
		RPCHost:        server.URL,
		CosmosGRPCHost: fakeGRPCHost,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			CosmosGRPCHost:   fakeGRPCHost,
			StartBlockHeight: 1, // avoids querying thorchain for block height
			WhitelistCosmosAssets: []config.WhitelistCosmosAsset{
				{Denom: "uatom", Decimals: 6, THORChainSymbol: "ATOM"},
			},
		},
	}, nil, s.bridge, s.m, nil)
	c.Assert(err, IsNil)

	vaultPubKey, err := common.NewPubKey("sthorpub1addwnpepqda0q2avvxnferqasee42lu5492jlc4zvf6u264famvg9dywgq2kz0zaecw")
	c.Assert(err, IsNil)
	outAsset, err := common.NewAsset("GAIA.ATOM")
	c.Assert(err, IsNil)
	toAddress, err := common.NewAddress("cosmos10tjz4ave7znpctgd2rfu6v2r6zkeup2dlmqtuz")
	c.Assert(err, IsNil)
	txOut := stypes.TxOutItem{
		Chain:       common.GAIAChain,
		ToAddress:   toAddress,
		VaultPubKey: vaultPubKey,
		Coins:       common.Coins{common.NewCoin(outAsset, cosmos.NewUint(24528352))},
		Memo:        "memo",
		MaxGas:      common.Gas{common.NewCoin(outAsset, cosmos.NewUint(235824))},
		GasRate:     750000,
		InHash:      "hash",
	}

	msg, err := client.processOutboundTx(txOut, 1)
	c.Assert(err, IsNil)

	expectedAmount := int64(245283)
	expectedDenom := "uatom"
	c.Check(msg.Amount[0].Amount.Int64(), Equals, expectedAmount)
	c.Check(msg.Amount[0].Denom, Equals, expectedDenom)
	c.Logf(msg.FromAddress)
	c.Check(msg.FromAddress, Equals, "cosmos126kpfewtlc7agqjrwdl2wfg0txkphsawus338n")
	c.Check(msg.ToAddress, Equals, toAddress.String())
}

func (s *CosmosTestSuite) TestSign(c *C) {
	priv, err := s.thorKeys.GetPrivateKey()
	c.Assert(err, IsNil)

	temp, err := cryptocodec.ToCmtPubKeyInterface(priv.PubKey())
	c.Assert(err, IsNil)

	pk, err := common.NewPubKeyFromCrypto(temp)
	c.Assert(err, IsNil)

	localKm := &keyManager{
		privKey: priv,
		addr:    types.AccAddress(priv.PubKey().Address()),
		pubkey:  pk,
	}

	interfaceRegistry := codectypes.NewInterfaceRegistry()
	interfaceRegistry.RegisterImplementations((*types.Msg)(nil), &btypes.MsgSend{})
	marshaler := codec.NewProtoCodec(interfaceRegistry)

	clientConfig := config.BifrostChainConfiguration{ChainID: common.GAIAChain}
	scannerConfig := config.BifrostBlockScannerConfiguration{ChainID: common.GAIAChain}
	txConfig := tx.NewTxConfig(marshaler, []signingtypes.SignMode{signingtypes.SignMode_SIGN_MODE_DIRECT})

	mockAccountServiceClient := NewMockAccountServiceClient()
	mockBankServiceClient := NewMockBankServiceClient()

	client := CosmosClient{
		cfg:             clientConfig,
		txConfig:        txConfig,
		cosmosScanner:   &CosmosBlockScanner{cfg: scannerConfig, rpc: &mockTendermintRPC{}},
		bankClient:      mockBankServiceClient,
		accountClient:   mockAccountServiceClient,
		chainID:         "columbus-5",
		localKeyManager: localKm,
		accts:           NewCosmosMetaDataStore(),
	}

	vaultPubKey, err := common.NewPubKey(pk.String())
	c.Assert(err, IsNil)
	outAsset, err := common.NewAsset("GAIA.ATOM")
	c.Assert(err, IsNil)
	toAddress, err := common.NewAddress("cosmos10tjz4ave7znpctgd2rfu6v2r6zkeup2dlmqtuz")
	c.Assert(err, IsNil)
	txOut := stypes.TxOutItem{
		Chain:       common.GAIAChain,
		ToAddress:   toAddress,
		VaultPubKey: vaultPubKey,
		Coins:       common.Coins{common.NewCoin(outAsset, cosmos.NewUint(24528352))},
		Memo:        "memo",
		MaxGas:      common.Gas{common.NewCoin(outAsset, cosmos.NewUint(235824))},
		GasRate:     750000,
		InHash:      "hash",
	}

	msg, err := client.processOutboundTx(txOut, 1)
	c.Assert(err, IsNil)

	meta := client.accts.Get(pk)
	c.Check(meta.AccountNumber, Equals, int64(0))
	c.Check(meta.SeqNumber, Equals, int64(0))

	gas := types.NewCoins(types.NewCoin("uatom", sdkmath.NewInt(100)))

	txb, err := buildUnsigned(
		txConfig,
		msg,
		vaultPubKey,
		"memo",
		gas,
		uint64(meta.AccountNumber),
		uint64(meta.SeqNumber),
	)
	c.Assert(err, IsNil)

	c.Check(txb.GetTx().GetFee().Equal(gas), Equals, true)
	c.Check(txb.GetTx().GetMemo(), Equals, "memo")
	pks, err := txb.GetTx().GetPubKeys()
	c.Assert(err, IsNil)

	c.Check(pks[0].Address().String(), Equals, priv.PubKey().Address().String())

	// Ensure the signature is present but tranasaction has not been signed yet
	sigs, err := txb.GetTx().GetSignaturesV2()
	c.Assert(err, IsNil)
	c.Check(sigs[0].PubKey.String(), Equals, priv.PubKey().String())
	sigData, ok := sigs[0].Data.(*signingtypes.SingleSignatureData)
	c.Check(ok, Equals, true)
	c.Check(sigData.SignMode, Equals, signingtypes.SignMode_SIGN_MODE_DIRECT)
	c.Check(len(sigData.Signature), Equals, 0)

	// Sign the message
	_, err = client.signMsg(
		txb,
		vaultPubKey,
		uint64(meta.AccountNumber),
		uint64(meta.SeqNumber),
		0,
	)
	c.Assert(err, IsNil)
}

func (s *CosmosTestSuite) TestEndpointIdentityMustMatchCosmosChainID(c *C) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		var body struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(req.Body).Decode(&body)
		rw.Header().Set("Content-Type", "application/json")
		if body.Method == "status" {
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"node_info":{"network":"theta-testnet-001"},"sync_info":{"latest_block_height":"1"}}}`))
			return
		}
		_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`))
	}))
	defer server.Close()
	fakeGRPCHost := strings.ReplaceAll(server.URL, "http://", "grpc://")
	cfg := config.BifrostChainConfiguration{
		ChainID:       common.GAIAChain,
		CosmosChainID: "cosmoshub-4",
		// an explicit endpoints list opts into the identity gate
		Endpoints: []config.ChainEndpointConfig{
			{Name: "a", RPCHost: server.URL, CosmosGRPCHost: fakeGRPCHost, Priority: 1},
		},
		BlockScanner: config.BifrostBlockScannerConfiguration{
			CosmosGRPCHost:   fakeGRPCHost,
			StartBlockHeight: 1,
		},
	}
	cfg.Failover.HealthCheckTimeout = 500 * time.Millisecond
	_, err := NewCosmosClient(s.thorKeys, cfg, nil, s.bridge, s.m, nil)
	c.Assert(err, ErrorMatches, `(?s).*priority-1 endpoint a reports identity "theta-testnet-001" but expected_identity is "cosmoshub-4".*`)
}

// A legacy flat config (no endpoints list) keeps starting even when the node's
// reported network disagrees with cosmos_chain_id: the check is advisory there, as
// it was before endpoint failover existed.
func (s *CosmosTestSuite) TestLegacyFlatConfigStartsAndBuildsOneEndpoint(c *C) {
	mk := func(network string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			var body struct {
				Method string          `json:"method"`
				ID     json.RawMessage `json:"id"`
			}
			_ = json.NewDecoder(req.Body).Decode(&body)
			id := string(body.ID)
			if id == "" {
				id = "1"
			}
			rw.Header().Set("Content-Type", "application/json")
			if body.Method == "status" {
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":{"node_info":{"network":"` + network +
					`"},"sync_info":{"latest_block_height":"10"}}}`))
				return
			}
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32601,"message":"nope"}}`))
		}))
	}
	for _, network := range []string{"cosmoshub-4", "theta-testnet-001"} {
		server := mk(network)
		fakeGRPCHost := strings.ReplaceAll(server.URL, "http://", "grpc://")
		cfg := config.BifrostChainConfiguration{
			ChainID:        common.GAIAChain,
			CosmosChainID:  "cosmoshub-4",
			RPCHost:        server.URL,
			CosmosGRPCHost: fakeGRPCHost,
			BlockScanner: config.BifrostBlockScannerConfiguration{
				CosmosGRPCHost:   fakeGRPCHost,
				StartBlockHeight: 1,
			},
		}
		cfg.Failover.HealthCheckTimeout = 500 * time.Millisecond
		client, err := NewCosmosClient(s.thorKeys, cfg, nil, s.bridge, s.m, nil)
		c.Assert(err, IsNil, Commentf("network %s", network))
		c.Assert(client.EndpointManager().Len(), Equals, 1)
		c.Assert(client.EndpointManager().Active().Name, Equals, "primary")
		server.Close()
	}
}

func (s *CosmosTestSuite) TestEndpointManagerExposedAndMempoolLookupUsesEveryEndpoint(c *C) {
	// two CometBFT RPC mocks: A knows tx INMEMPOOL, B does not; the gRPC hosts are lazy
	mk := func(has bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			var body struct {
				Method string          `json:"method"`
				ID     json.RawMessage `json:"id"`
			}
			_ = json.NewDecoder(req.Body).Decode(&body)
			id := string(body.ID) // the CometBFT client rejects replies whose id differs from the request
			if id == "" {
				id = "1"
			}
			rw.Header().Set("Content-Type", "application/json")
			switch body.Method {
			case "status":
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":{"node_info":{"network":"cosmoshub-4"},"sync_info":{"latest_block_height":"10"}}}`))
			case "unconfirmed_txs":
				if has {
					_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":{"n_txs":"1","total":"1","total_bytes":"2","txs":["AQI="]}}`))
				} else {
					_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"result":{"n_txs":"0","total":"0","total_bytes":"0","txs":[]}}`))
				}
			default:
				_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":` + id + `,"error":{"code":-32601,"message":"nope"}}`))
			}
		}))
	}
	a := mk(true)
	defer a.Close()
	b := mk(false)
	defer b.Close()
	cfg := config.BifrostChainConfiguration{
		ChainID:       common.GAIAChain,
		CosmosChainID: "cosmoshub-4",
		Endpoints: []config.ChainEndpointConfig{
			{Name: "a", RPCHost: a.URL, CosmosGRPCHost: "127.0.0.1:1", Priority: 1},
			{Name: "b", RPCHost: b.URL, CosmosGRPCHost: "127.0.0.1:2", Priority: 2},
		},
		BlockScanner: config.BifrostBlockScannerConfiguration{StartBlockHeight: 1},
	}
	cfg.Failover.HealthCheckTimeout = 500 * time.Millisecond
	client, err := NewCosmosClient(s.thorKeys, cfg, nil, s.bridge, s.m, nil)
	c.Assert(err, IsNil)
	c.Assert(client.EndpointManager(), NotNil)
	c.Assert(client.EndpointManager().ReferenceIdentity(), Equals, "cosmoshub-4")
	c.Assert(client.EndpointManager().Len(), Equals, 2)
	// the gRPC liveness half of the compound probe fails (nothing listens), so both
	// endpoints are unhealthy at bootstrap; the mempool lookup still asks every endpoint
	txHash := fmt.Sprintf("%X", cmttypes.Tx([]byte{1, 2}).Hash())
	c.Assert(client.mempoolLookup(context.Background(), txHash), Equals, signercache.LookupFound)
	c.Assert(client.mempoolLookup(context.Background(), "DEADBEEF"), Equals, signercache.LookupNotFound)
}

// pinnedTxClient answers GetTx per pinned endpoint name (see endpoints.PinContext).
type pinnedTxClient struct {
	mockTxServiceClient
	byEndpoint map[string]error
}

func (p pinnedTxClient) GetTx(ctx context.Context, _ *txtypes.GetTxRequest, _ ...grpc.CallOption) (*txtypes.GetTxResponse, error) {
	name, _ := endpoints.PinnedEndpoint(ctx)
	err, ok := p.byEndpoint[name]
	if !ok {
		return nil, errors.New("unexpected endpoint " + name)
	}
	return nil, err
}

// TestGaiaVerifyBroadcastManaged covers the managed path: every non-disabled endpoint
// is asked, a tx known to only one endpoint is Found, an incomplete answer is
// Ambiguous, and with every endpoint disabled nothing can prove absence.
func TestGaiaVerifyBroadcastManaged(t *testing.T) {
	notCommitted := status.Error(codes.NotFound, "tx not found")
	emptyMempool := fakeCometRPC{res: &cmtcoretypes.ResultUnconfirmedTxs{Txs: []cmttypes.Tx{}, Total: 0}}
	newMgr := func() *endpoints.Manager {
		m, err := endpoints.NewManager(common.GAIAChain, []config.ChainEndpointConfig{
			{Name: "a", RPCHost: "http://a", CosmosGRPCHost: "a:9090", Priority: 1},
			{Name: "b", RPCHost: "http://b", CosmosGRPCHost: "b:9090", Priority: 2},
		}, config.BifrostChainConfiguration{}.GetFailoverConfig(), zerolog.Nop())
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range m.Entries() {
			e.SetIdentity("cosmoshub-4") // bootstrap is not run in this unit test
		}
		return m
	}
	cases := []struct {
		name       string
		byEndpoint map[string]error
		disableAll bool
		want       signercache.LookupResult
	}{
		{"committed only on the secondary", map[string]error{"a": notCommitted, "b": nil}, false, signercache.LookupFound},
		{"absent on both, empty mempools", map[string]error{"a": notCommitted, "b": notCommitted}, false, signercache.LookupNotFound},
		{"secondary incomplete", map[string]error{"a": notCommitted, "b": errors.New("boom")}, false, signercache.LookupAmbiguous},
		{"every endpoint disabled", map[string]error{"a": notCommitted, "b": notCommitted}, true, signercache.LookupAmbiguous},
	}
	for _, tc := range cases {
		m := newMgr()
		if tc.disableAll {
			m.Disable("a", "identity")
			m.Disable("b", "identity")
		}
		cc := CosmosClient{txClient: pinnedTxClient{byEndpoint: tc.byEndpoint}, cometRPC: emptyMempool, endpointMgr: m}
		if got := cc.verifyBroadcast("ABCD"); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
