package substrate

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/generic"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/rpc"
	subtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"
	"gitlab.com/thorchain/thornode/v3/config"
	thorTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

func TestClientSuite(t *testing.T) { TestingT(t) }

type ClientSuite struct{}

var _ = Suite(&ClientSuite{})

// mockBridge is a mock implementation of ThorchainBridge for testing
type mockBridge struct {
	thorclient.ThorchainBridge
	asgards              thorTypes.Vaults
	blockTimestamp       time.Time
	blockTimestampErr    error
	blockTimestampHeight int64
}

func (m *mockBridge) GetBlockTimestamp(height int64) (time.Time, error) {
	m.blockTimestampHeight = height
	if m.blockTimestampErr != nil {
		return time.Time{}, m.blockTimestampErr
	}
	if m.blockTimestamp.IsZero() {
		return time.UnixMilli(height * 1_000), nil
	}
	return m.blockTimestamp, nil
}

type rpcError struct {
	Code    int
	Message string
	Data    string
}

func (e *rpcError) Error() string {
	return e.Message
}

// ---------------------------------------------------------------------------
// nonceMetadata JSON serialization
// ---------------------------------------------------------------------------

func (s *ClientSuite) TestNonceMetadataJSON(c *C) {
	meta := nonceMetadata{
		Nonce:          42,
		ReservedHeight: 1000,
	}

	data, err := json.Marshal(meta)
	c.Assert(err, IsNil)

	var restored nonceMetadata
	err = json.Unmarshal(data, &restored)
	c.Assert(err, IsNil)

	c.Check(restored.Nonce, Equals, meta.Nonce)
	c.Check(restored.ReservedHeight, Equals, meta.ReservedHeight)
}

func (s *ClientSuite) TestNonceMetadataJSONBackwardsCompat(c *C) {
	// Ensure the JSON field names match the old NonceMetadata format
	// so that checkpoints serialized before the refactor still deserialize.
	raw := `{"Nonce":7,"BlockHeight":500}`
	var meta nonceMetadata
	err := json.Unmarshal([]byte(raw), &meta)
	c.Assert(err, IsNil)
	c.Check(meta.Nonce, Equals, uint32(7))
	c.Check(meta.ReservedHeight, Equals, int64(500))
}

// ---------------------------------------------------------------------------
// Mock RPC helpers
// ---------------------------------------------------------------------------

// rpcHandler dispatches RPC method calls to registered handlers.
type rpcHandler struct {
	mu       sync.Mutex
	handlers map[string]func(params json.RawMessage) (interface{}, error)
}

func newRPCHandler() *rpcHandler {
	return &rpcHandler{handlers: make(map[string]func(json.RawMessage) (interface{}, error))}
}

func (h *rpcHandler) on(method string, fn func(json.RawMessage) (interface{}, error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers[method] = fn
}

func (h *rpcHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		ID     int             `json:"id"`
	}
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	fn, ok := h.handlers[req.Method]
	h.mu.Unlock()

	if !ok {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"method not found"}}`, req.ID)
		return
	}

	result, err := fn(req.Params)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")

		var rpcErr *rpcError
		if errors.As(err, &rpcErr) {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":%d,"message":"%s","data":"%s"}}`,
				req.ID, rpcErr.Code, rpcErr.Message, rpcErr.Data)
		} else {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":%d,"message":"%s"}}`,
				req.ID, -32000, err.Error())
		}

		return
	}

	resultJSON, _ := json.Marshal(result)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, resultJSON)
}

// testClientOpt customizes the BifrostChainConfiguration produced by
// newTestClient. Use withMaxPendingNonces / etc. to override defaults.
type testClientOpt func(*config.BifrostChainConfiguration)

// withMaxPendingNonces overrides the per-test MaxPendingNonces cap used by
// reserveNonce.
func withMaxPendingNonces(n uint64) testClientOpt {
	return func(cfg *config.BifrostChainConfiguration) {
		cfg.MaxPendingNonces = n
	}
}

func newTestClient(rpcClient *rpc.Client, opts ...testClientOpt) *Client {
	logger := log.Logger

	ns := strconv.Itoa(time.Now().Nanosecond())

	tmpdir := filepath.Join(os.TempDir(), ns, ".thorcli")

	clientConfig := config.BifrostClientConfiguration{
		ChainID:         "thorchain",
		ChainHost:       "localhost",
		SignerName:      "bob",
		SignerPasswd:    "password",
		ChainHomeFolder: tmpdir,
	}

	chainConfig := config.BifrostChainConfiguration{
		ChainID: common.TAOChain,
		// This helper bypasses NewClient, which replaces a zero limit with 3.
		MaxPendingNonces: 16,
	}
	for _, opt := range opts {
		opt(&chainConfig)
	}

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)

	kb := keyring.NewInMemory(
		codec.NewProtoCodec(registry),
		func(opts *keyring.Options) {
			opts.SupportedAlgos = append(opts.SupportedAlgos, ed25519.Ed25519)
		},
	)

	_, mnemonic, err := kb.NewMnemonic(
		"ed-"+clientConfig.SignerName,
		keyring.English,
		"", clientConfig.SignerPasswd,
		ed25519.Ed25519,
	)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to generate mnemonic")
	}

	_, err = kb.NewAccount(
		clientConfig.SignerName,
		mnemonic,
		clientConfig.SignerPasswd,
		cmd.THORChainHDPath,
		hd.Secp256k1,
	)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to generate account")
	}

	keys := thorclient.NewKeysWithKeybase(
		kb, clientConfig.SignerName, clientConfig.SignerPasswd,
	)

	// Use mock bridge for tests to avoid needing a real THORChain node
	bridge := &mockBridge{}

	var storage *blockscanner.BlockScannerStorage
	storage, err = blockscanner.NewBlockScannerStorage(tmpdir, config.LevelDBOptions{})
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create storage")
	}

	genericClient, err := generic.NewClient(
		logger, keys, chainConfig, nil, bridge, storage.GetInternalDb(),
	)
	if err != nil {
		logger.Fatal().Err(err).Msg("could not create generic client")
	}
	stateStore, err := newStateStore(genericClient.StateStore())
	if err != nil {
		logger.Fatal().Err(err).Msg("could not create substrate state store")
	}
	genericScanner, err := generic.NewScanner(
		genericClient.StateStore(),
		int(ChainParams[common.TAOChain].MortalEraPeriod)*2,
		ChainParams[common.TAOChain].Lookback*2,
	)
	if err != nil {
		logger.Fatal().Err(err).Msg("could not create generic scanner")
	}

	return &Client{
		Client:           genericClient,
		logger:           log.Logger,
		storage:          storage,
		rpcClient:        rpcClient,
		nonces:           make(map[string]nonceMetadata),
		migrationTxCache: newMigrationTxCache(),
		stateStore:       stateStore,
		chainScanner: &Scanner{
			Scanner:    genericScanner,
			params:     ChainParams[common.TAOChain],
			nonceCache: newNonceCache(),
			runtime: runtimeContext{
				layout: mustTestExtrinsicLayout(common.TAOChain),
			},
		},
	}
}

func makeBroadcastTestTx(c *C, nonce uint64) (stypes.TxOutItem, []byte) {
	signer := defaultTestSigner()
	cmtPub := cmted25519.PubKey(signer)
	cosmosPubAny, err := cryptocodec.FromCmtPubKeyInterface(cmtPub)
	c.Assert(err, IsNil)
	bech32, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, cosmosPubAny)
	c.Assert(err, IsNil)
	vaultPubKey, err := common.NewPubKey(bech32)
	c.Assert(err, IsNil)

	params := ChainParams[common.TAOChain]
	ext := &subtypes.Extrinsic{
		Signer:        signer,
		SignatureType: subtypes.SignatureTypeEd25519,
		Signature:     make([]byte, 64),
		Era:           subtypes.MortalEra(uint64(params.MortalEraPeriod), 100),
		Nonce:         nonce,
		Tip:           0,
		MetadataMode:  subtypes.MetadataModeDisabled,
		CallData: subtypes.BalanceTransferKeepAlive(
			params.Index(BalancesPallet),
			params.Index(BalancesTransferKeepAliveCall),
			bytes.Repeat([]byte{0x01}, 32),
			cosmos.NewUint(100),
		),
	}
	return stypes.TxOutItem{
		Chain:            common.TAOChain,
		VaultPubKeyEddsa: vaultPubKey,
		Memo:             "OUT:hash",
	}, mustEncodeTestExtrinsic(ext, common.TAOChain)
}

func seedSignedExtrinsic(
	c *C,
	cl *Client,
	nonce uint32,
	recheckHeight int64,
) (stypes.TxOutItem, string, string) {
	txOutItem, rawTx := makeBroadcastTestTx(c, uint64(nonce))
	txHash := subtypes.ExtrinsicHash(rawTx)
	vaultAddr := cl.GetAddress(txOutItem.GetVaultPubKey())
	c.Assert(cl.SetSigned(txOutItem, txHash), IsNil)
	c.Assert(cl.stateStore.PutExtrinsic(signedExtrinsicRecord{
		TxHash:        txHash,
		VaultAddress:  vaultAddr,
		Nonce:         nonce,
		RecheckHeight: recheckHeight,
	}), IsNil)
	return txOutItem, txHash, vaultAddr
}

// ---------------------------------------------------------------------------
// reserveNonce tests
// ---------------------------------------------------------------------------

func (s *ClientSuite) TestReserveNonce_Fresh(c *C) {
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		return uint32(0), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	nonce, checkpoint, err := cl.reserveNonce(tx, "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY", 100)
	c.Assert(err, IsNil)
	c.Check(nonce, Equals, uint32(0))
	c.Assert(checkpoint, NotNil, Commentf("checkpoint should not be nil"))
}

func (s *ClientSuite) TestReserveNonce_Increment(c *C) {
	var calls atomic.Int32
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		calls.Add(1)
		return uint32(5), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	n1, _, err := cl.reserveNonce(tx, vault, 100)
	c.Assert(err, IsNil, Commentf("first reserveNonce"))
	c.Check(n1, Equals, uint32(5), Commentf("first nonce"))

	// Second call at same height should return incremented nonce
	n2, _, err := cl.reserveNonce(tx, vault, 100)
	c.Assert(err, IsNil, Commentf("second reserveNonce"))
	c.Check(n2, Equals, uint32(6), Commentf("second nonce"))
	c.Check(calls.Load(), Equals, int32(1))

	_, _, err = cl.reserveNonce(tx, vault, 101)
	c.Assert(err, IsNil)
	_, _, err = cl.reserveNonce(tx, vault, 101)
	c.Assert(err, IsNil)
	c.Check(calls.Load(), Equals, int32(2))
	c.Check(cl.getCachedNonce(vault), Equals, nonceMetadata{Nonce: 9, ReservedHeight: 101})
}

func (s *ClientSuite) TestReserveNonce_Checkpoint(c *C) {
	cl := newTestClient(nil) // RPC not needed for checkpoint path
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	cached := nonceMetadata{Nonce: 44, ReservedHeight: 110}
	cl.nonces[vault] = cached

	checkpointData, _ := json.Marshal(nonceMetadata{Nonce: 42, ReservedHeight: 100})
	tx := stypes.TxOutItem{
		Chain:      common.TAOChain,
		Checkpoint: checkpointData,
	}

	nonce, cp, err := cl.reserveNonce(tx, vault, 200)
	c.Assert(err, IsNil)
	c.Check(nonce, Equals, uint32(42))
	// Checkpoint should be returned as-is
	c.Check(string(cp), Equals, string(checkpointData), Commentf("checkpoint mismatch"))

	c.Check(cl.getCachedNonce(vault), Equals, cached)
}

func (s *ClientSuite) TestReserveNonce_OnChainRefresh(c *C) {
	callCount := 0
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		callCount++
		if callCount == 1 {
			return uint32(3), nil
		}
		// On-chain nonce advanced (confirmed tx)
		return uint32(5), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	// First call at height 100
	n1, _, err := cl.reserveNonce(tx, vault, 100)
	c.Assert(err, IsNil, Commentf("first"))
	c.Check(n1, Equals, uint32(3), Commentf("first nonce"))

	// Second call at higher height — should fetch fresh on-chain nonce
	n2, _, err := cl.reserveNonce(tx, vault, 200)
	c.Assert(err, IsNil, Commentf("second"))
	c.Check(n2, Equals, uint32(5), Commentf("second nonce: should advance to on-chain"))
}

func (s *ClientSuite) TestReserveNonce_MortalEraExpiry(c *C) {
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		// On-chain nonce stays at 5 (broadcast failed, tx expired)
		return uint32(5), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	period := int64(ChainParams[common.TAOChain].MortalEraPeriod)
	for _, tt := range []struct {
		height int64
		nonce  uint32
	}{
		{100 + period, 7},
		{110 + period - 1, 7},
		{110 + period, 5},
	} {
		// Each boundary starts with the same two reservations.
		cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
		for i, height := range []int64{100, 110} {
			nonce, checkpoint, err := cl.reserveNonce(tx, vault, height)
			c.Assert(err, IsNil)
			c.Check(nonce, Equals, uint32(5+i))
			var meta nonceMetadata
			c.Assert(json.Unmarshal(checkpoint, &meta), IsNil)
			c.Check(meta, Equals, nonceMetadata{Nonce: nonce, ReservedHeight: height})
			c.Check(cl.getCachedNonce(vault), Equals, nonceMetadata{Nonce: nonce + 1, ReservedHeight: height})
		}

		nonce, _, err := cl.reserveNonce(tx, vault, tt.height)
		c.Assert(err, IsNil)
		c.Check(nonce, Equals, tt.nonce, Commentf("height %d", tt.height))
	}
}

func (s *ClientSuite) TestReserveNonce_OutOfOrder(c *C) {
	for _, rpcNonce := range []uint32{5, 6, 8} {
		func() {
			started := make(chan struct{})
			release := make(chan struct{})
			var calls atomic.Int32
			h := newRPCHandler()
			h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
				if calls.Add(1) == 1 {
					close(started)
					<-release
					return rpcNonce, nil
				}
				return uint32(5), nil
			})
			srv := httptest.NewServer(h)
			defer srv.Close()
			defer close(release)

			cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
			vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
			cl.nonces[vault] = nonceMetadata{Nonce: 5, ReservedHeight: 100}
			tx := stypes.TxOutItem{Chain: common.TAOChain}
			type result struct {
				nonce      uint32
				checkpoint []byte
				err        error
			}
			done := make(chan result, 1)
			go func() {
				nonce, checkpoint, err := cl.reserveNonce(tx, vault, 101)
				done <- result{nonce, checkpoint, err}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				c.Fatal("older reservation did not reach RPC")
			}

			nonce, _, err := cl.reserveNonce(tx, vault, 102)
			c.Assert(err, IsNil)
			c.Check(nonce, Equals, uint32(5))
			release <- struct{}{}
			older := <-done
			c.Assert(older.err, IsNil)
			c.Check(older.nonce, Equals, max(uint32(6), rpcNonce))
			var checkpoint nonceMetadata
			c.Assert(json.Unmarshal(older.checkpoint, &checkpoint), IsNil)
			c.Check(checkpoint, Equals, nonceMetadata{Nonce: older.nonce, ReservedHeight: 102})
			c.Check(cl.getCachedNonce(vault), Equals, nonceMetadata{Nonce: older.nonce + 1, ReservedHeight: 102})
		}()
	}
}

func (s *ClientSuite) TestReserveNonce_ScannerNotReady(c *C) {
	// When the scanner hasn't observed any blocks, reserveNonce is called with
	// currentHeight=0. This reserves nonce 0 without on-chain verification.
	// Verify that the nonce self-corrects once the scanner starts.
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		return uint32(10), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	// First call at height 0 (scanner not ready) — doesn't fetch on-chain nonce
	n1, _, err := cl.reserveNonce(tx, vault, 0)
	c.Assert(err, IsNil, Commentf("first"))
	c.Check(n1, Equals, uint32(0), Commentf("first nonce: empty cache, height 0 skips fetch"))

	// Second call once scanner is running (height 100) — fetches on-chain nonce
	// and the mortal era expiry resets the cache (100 >= 0 + 64)
	n2, _, err := cl.reserveNonce(tx, vault, 100)
	c.Assert(err, IsNil, Commentf("second"))
	c.Check(n2, Equals, uint32(10), Commentf("second nonce: should reset to on-chain nonce"))
}

// ---------------------------------------------------------------------------
// GetAccountByAddress test
// ---------------------------------------------------------------------------

func (s *ClientSuite) TestGetAccountByAddress(c *C) {
	h := newRPCHandler()
	// Mock balance: 5 TAO = 5_000_000_000 rao
	// SCALE-encoded AccountInfo: nonce(4) + consumers(4) + providers(4) + sufficients(4)
	//   + free(u128 LE) + reserved(u128 LE) + frozen(u128 LE) + flags(4)
	// 5_000_000_000 = 0x12A05F200, LE u128 = 00f2052a01000000 0000000000000000
	accountHex := "0x" +
		"01000000" + // nonce = 1
		"00000000" + // consumers
		"01000000" + // providers
		"00000000" + // sufficients
		"00f2052a01000000" + "0000000000000000" + // free = 5_000_000_000 as u128 LE
		"0000000000000000" + "0000000000000000" + // reserved = 0
		"0000000000000000" + "0000000000000000" + // frozen = 0
		"00000000" // flags
	h.on("state_getStorage", func(raw json.RawMessage) (interface{}, error) {
		var params []string
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, err
		}
		if len(params) != 1 {
			return nil, fmt.Errorf("expected latest-state read, got %s", raw)
		}
		return accountHex, nil
	})
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		return uint32(9), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))

	acct, err := cl.GetAccountByAddress("5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY", nil)
	c.Assert(err, IsNil)

	c.Check(acct.Sequence, Equals, int64(9))

	// 5_000_000_000 rao / 10 = 500_000_000 thorchain units
	expectedAmount := cosmos.NewUint(500_000_000)
	c.Assert(len(acct.Coins), Equals, 1)
	c.Check(acct.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", acct.Coins[0].Amount, expectedAmount))
	c.Check(acct.Coins[0].Asset.Equals(common.TAOAsset), Equals, true,
		Commentf("asset: got %s, want %s", acct.Coins[0].Asset, common.TAOAsset))
	c.Check(acct.Coins[0].Decimals, Equals, int64(9))
}

// ---------------------------------------------------------------------------
// BroadcastTx tests
// ---------------------------------------------------------------------------

func (s *ClientSuite) TestBroadcastTx_Success(c *C) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))

	txOutItem, rawTx := makeBroadcastTestTx(c, 1)

	txHash, err := cl.BroadcastTx(txOutItem, rawTx)
	c.Assert(err, IsNil)
	c.Check(txHash, Not(Equals), "", Commentf("expected non-empty tx hash"))
	record, found, err := cl.stateStore.GetExtrinsic(txHash)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(record.Nonce, Equals, uint32(1))
}

func (s *ClientSuite) TestBroadcastTxPreservesSigningEraExpiry(c *C) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return "", nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
	txOutItem, rawTx := makeBroadcastTestTx(c, 7)
	txHash := subtypes.ExtrinsicHash(rawTx)
	vaultAddr := cl.GetAddress(txOutItem.GetVaultPubKey())
	record := signedExtrinsicRecord{
		TxHash:         txHash,
		VaultAddress:   vaultAddr,
		Nonce:          7,
		EraBirthHeight: 100,
		RecheckHeight:  100 + int64(ChainParams[common.TAOChain].MortalEraPeriod),
	}
	c.Assert(cl.stateStore.PutExtrinsic(record), IsNil)
	cl.chainScanner.setLatestBlock("0xlocal-tip", 150)

	_, err := cl.BroadcastTx(txOutItem, rawTx)
	c.Assert(err, IsNil)
	got, found, err := cl.stateStore.GetExtrinsic(txHash)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(got, DeepEquals, record)
}

func (s *ClientSuite) TestBlockReferenceAppliesTimeLookback(c *C) {
	cl := newTestClient(nil)
	bridge, ok := cl.GetBridge().(*mockBridge)
	c.Assert(ok, Equals, true)
	bridge.blockTimestamp = time.UnixMilli(61_050)
	for _, reference := range []generic.BlockReference{
		{Height: 10, Hash: "0x10", Timestamp: 1_000},
		{Height: 11, Hash: "0x11", Timestamp: 1_100},
	} {
		c.Assert(cl.chainScanner.SetBlockReference(reference), IsNil)
	}

	reference, err := cl.blockReference(100)
	c.Assert(err, IsNil)
	c.Check(bridge.blockTimestampHeight, Equals, int64(100))
	c.Check(reference.Height, Equals, int64(10))
	c.Check(reference.Hash, Equals, "0x10")
}

func (s *ClientSuite) TestBlockReferenceHasNoTipFallback(c *C) {
	cl := newTestClient(nil)
	bridge, ok := cl.GetBridge().(*mockBridge)
	c.Assert(ok, Equals, true)
	bridge.blockTimestamp = time.UnixMilli(61_050)
	c.Assert(cl.chainScanner.SetBlockReference(generic.BlockReference{
		Height: 10, Hash: "0x10", Timestamp: 1_000,
	}), IsNil)
	cl.chainScanner.setLatestBlock("0xlocal-tip", 1_000)

	_, err := cl.blockReference(100)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, generic.ErrBlockReferenceUnavailable), Equals, true)
}

func (s *ClientSuite) TestSignTxUsesReferenceEraAndPersistsExactExpiry(c *C) {
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		return uint32(7), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
	bridge, ok := cl.GetBridge().(*mockBridge)
	c.Assert(ok, Equals, true)
	bridge.blockTimestamp = time.UnixMilli(61_050)
	eraHash := "0x" + strings.Repeat("11", 32)
	for _, reference := range []generic.BlockReference{
		{Height: 100, Hash: eraHash, Timestamp: 1_000},
		{Height: 101, Hash: "0x" + strings.Repeat("22", 32), Timestamp: 1_100},
	} {
		c.Assert(cl.chainScanner.SetBlockReference(reference), IsNil)
	}
	cl.chainScanner.genesisHash = "0x" + strings.Repeat("00", 32)
	cl.chainScanner.setLatestBlock("0x"+strings.Repeat("33", 32), 120)

	destination, err := ss58.Encode(common.TAOChain.SS58Prefix(), bytes.Repeat([]byte{0x44}, 32))
	c.Assert(err, IsNil)
	toAddress, err := common.NewAddress(destination)
	c.Assert(err, IsNil)
	tx := stypes.TxOutItem{
		Chain:            common.TAOChain,
		ToAddress:        toAddress,
		VaultPubKeyEddsa: cl.GetPubKey(),
		Coins: common.Coins{
			common.NewCoin(common.TAOAsset, cosmos.NewUint(100_000_000)),
		},
		GasRate: 100_000,
	}

	rawTx, checkpoint, _, err := cl.SignTx(tx, 100)
	c.Assert(err, IsNil)
	extrinsic, err := subtypes.ParseSignedExtrinsic(
		rawTx,
		nil,
		cl.chainScanner.getRuntimeContext().layout,
	)
	c.Assert(err, IsNil)
	era := uint64(ChainParams[common.TAOChain].MortalEraPeriod)
	c.Check(extrinsic.Era, DeepEquals, subtypes.MortalEra(era, 100))
	c.Check(extrinsic.Nonce, Equals, uint64(7))

	txHash := subtypes.ExtrinsicHash(rawTx)
	record, found, err := cl.stateStore.GetExtrinsic(txHash)
	c.Assert(err, IsNil)
	c.Assert(found, Equals, true)
	c.Check(record.EraBirthHeight, Equals, int64(100))
	c.Check(record.RecheckHeight, Equals, 100+int64(era))

	// A checkpoint retry cannot extend the original signing era.
	vault, err := tx.VaultPubKeyEddsa.GetAddress(common.TAOChain)
	c.Assert(err, IsNil)
	cached := cl.getCachedNonce(vault.String())
	tx.Checkpoint = checkpoint
	cl.chainScanner.setLatestBlock("0x"+strings.Repeat("44", 32), record.RecheckHeight)
	rawTx, _, _, err = cl.SignTx(tx, 100)
	c.Assert(err, ErrorMatches, "deterministic signing block 100 has expired at finalized height .*")
	c.Check(rawTx, IsNil)
	c.Check(cl.getCachedNonce(vault.String()), Equals, cached)
}

func (s *ClientSuite) TestBroadcastTx_InvalidVaultAddress(c *C) {
	var submissions atomic.Int32
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		submissions.Add(1)
		return "unused", nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	for _, chain := range []common.Chain{common.TAOChain, common.DOTChain} {
		cl := newTestClient(rpc.NewClient(srv.URL, time.Second), func(cfg *config.BifrostChainConfiguration) {
			cfg.ChainID = chain
		})
		if chain == common.DOTChain {
			cl.chainScanner = newAssetHubTestScanner(c)
		}
		for _, tt := range []struct {
			pubkey  common.PubKey
			pattern string
		}{
			{common.PubKey("invalid"), "failed to derive vault address: .*"},
			{common.EmptyPubKey, "vault address is empty"},
		} {
			// An address error must be returned before recovery storage is read.
			state := &memoryStateStore{getErr: errors.New("unexpected recovery read")}
			var err error
			cl.stateStore, err = newStateStore(state)
			c.Assert(err, IsNil)
			txOutItem, rawTx := makeBroadcastTestTx(c, 1)
			extrinsic, err := subtypes.ParseSignedExtrinsic(rawTx, nil, mustTestExtrinsicLayout(common.TAOChain))
			c.Assert(err, IsNil)
			rawTx = mustEncodeTestExtrinsic(extrinsic, chain)
			txOutItem.Chain = chain
			txOutItem.VaultPubKeyEddsa = tt.pubkey

			txHash, err := cl.BroadcastTx(txOutItem, rawTx)
			c.Assert(err, ErrorMatches, tt.pattern, Commentf("%s: %q", chain, tt.pubkey))
			c.Check(txHash, Equals, "")
			c.Check(state.values, HasLen, 0)
			c.Check(submissions.Load(), Equals, int32(0))
		}
	}
}

func (s *ClientSuite) TestBroadcastTx_PersistenceFailurePreventsSubmission(c *C) {
	h := newRPCHandler()
	called := false
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		called = true
		return "unused", nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
	state := &memoryStateStore{putErr: errors.New("disk unavailable")}
	cl.stateStore, _ = newStateStore(state)
	txOutItem, rawTx := makeBroadcastTestTx(c, 1)

	_, err := cl.BroadcastTx(txOutItem, rawTx)
	c.Assert(err, NotNil)
	c.Check(called, Equals, false)
}

func (s *ClientSuite) TestBroadcastTx_AlreadyImported(c *C) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return nil, &rpcError{
			Code:    1013,
			Message: "Transaction Already Imported",
			Data:    "0x1234",
		}
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))

	txOutItem, rawTx := makeBroadcastTestTx(c, 1)

	// Should succeed — AlreadyImported is treated as success
	txHash, err := cl.BroadcastTx(txOutItem, rawTx)
	c.Assert(err, IsNil, Commentf("BroadcastTx should succeed for AlreadyImported"))
	c.Check(txHash, Not(Equals), "", Commentf("expected non-empty tx hash"))
}

func (s *ClientSuite) TestBroadcastTx_Stale(c *C) {
	// Stale on Substrate has two outcomes depending on whether the scanner
	// has already observed our extrinsic land on-chain:
	//   - cache miss → genuine stale, abandon the txout to avoid double-spend
	//                  on a fresh-nonce re-broadcast.
	//   - cache hit → our prior submission already confirmed (e.g. response
	//                 lost from bifrost POV but the node received it); treat
	//                 the Stale as success so internal Migrate/Consolidate
	//                 flows aren't rescheduled into a duplicate.
	tests := []struct {
		name        string
		prePopulate bool
		wantHashSet bool
		wantErr     error
	}{
		{
			name:        "cache miss → abandon",
			prePopulate: false,
			wantHashSet: false,
			wantErr:     types.ErrAbandonTxOut,
		},
		{
			name:        "cache hit → treat as success",
			prePopulate: true,
			wantHashSet: true,
			wantErr:     nil,
		},
	}

	signerPubkey := bytes.Repeat([]byte{0xAB}, 32)
	c.Assert(len(signerPubkey), Equals, 32)

	cmtPub := cmted25519.PubKey(signerPubkey)
	cosmosPubAny, err := cryptocodec.FromCmtPubKeyInterface(cmtPub)
	c.Assert(err, IsNil)
	bech32, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, cosmosPubAny)
	c.Assert(err, IsNil)
	vaultPubKey, err := common.NewPubKey(bech32)
	c.Assert(err, IsNil)

	expectedSenderSS58, err := ss58.Encode(common.TAOChain.SS58Prefix(), signerPubkey)
	c.Assert(err, IsNil)
	vaultAddr, err := vaultPubKey.GetAddress(common.TAOChain)
	c.Assert(err, IsNil)
	c.Assert(vaultAddr.String(), Equals, expectedSenderSS58,
		Commentf("vault pubkey and signer pubkey must produce the same SS58"))

	for _, tt := range tests {
		h := newRPCHandler()
		h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
			return nil, &rpcError{
				Code:    1010,
				Message: "Invalid Transaction",
				Data:    "Transaction is outdated",
			}
		})
		srv := httptest.NewServer(h)

		cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))

		params := ChainParams[common.TAOChain]
		ext := &subtypes.Extrinsic{
			Signer:        signerPubkey,
			SignatureType: subtypes.SignatureTypeEd25519,
			Signature:     make([]byte, 64),
			Era:           subtypes.MortalEra(uint64(params.MortalEraPeriod), 100),
			Nonce:         1,
			Tip:           0,
			MetadataMode:  subtypes.MetadataModeDisabled,
			CallData: subtypes.BalanceTransferKeepAlive(
				params.Index(BalancesPallet),
				params.Index(BalancesTransferKeepAliveCall),
				bytes.Repeat([]byte{0x01}, 32), // non-zero destination
				cosmos.NewUint(100),
			),
		}

		rawTx := mustEncodeTestExtrinsic(ext, common.TAOChain)
		expectedHash := subtypes.ExtrinsicHash(rawTx)

		if tt.prePopulate {
			cacheKey := migrationTxCacheKey(expectedSenderSS58, ext.Nonce)
			cl.migrationTxCache.Add(cacheKey, 123456)
		}

		txOutItem := stypes.TxOutItem{
			Chain:            common.TAOChain,
			VaultPubKeyEddsa: vaultPubKey,
			Memo:             "OUT:hash",
		}
		gotHash, err := cl.BroadcastTx(txOutItem, rawTx)

		if tt.wantErr != nil {
			c.Assert(err, NotNil, Commentf("%s: expected error", tt.name))
			c.Check(errors.Is(err, tt.wantErr), Equals, true,
				Commentf("%s: got %v", tt.name, err))
		} else {
			c.Check(err, IsNil, Commentf("%s: expected nil error, got %v", tt.name, err))
		}

		if tt.wantHashSet {
			c.Check(gotHash, Equals, expectedHash,
				Commentf("%s: hash mismatch (want %s, got %s)", tt.name, expectedHash, gotHash))
		} else {
			c.Check(gotHash, Equals, "",
				Commentf("%s: hash must be empty on error", tt.name))
		}

		srv.Close()
	}
}

func (s *ClientSuite) TestBroadcastTx_OtherError(c *C) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("rpc error (code: -32000): Some other error")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))

	txOutItem, rawTx := makeBroadcastTestTx(c, 1)

	_, err := cl.BroadcastTx(txOutItem, rawTx)
	c.Check(err, NotNil, Commentf("BroadcastTx should fail for unknown errors"))
}

func accountInfoHex(nonce uint32) string {
	data := make([]byte, 24) // four u32 fields followed by one u64 free balance
	binary.LittleEndian.PutUint32(data[:4], nonce)
	return "0x" + hex.EncodeToString(data)
}

func (s *ClientSuite) TestExpiredSignedExtrinsicRecovery(c *C) {
	tests := []struct {
		name          string
		finalized     int64
		nonce         uint32
		missing       bool
		expectRPC     bool
		expectRetry   bool
		expectRemoved bool
	}{
		{
			name:      "before recheck height",
			finalized: 99,
		},
		{
			name:          "nonce unchanged after expiry",
			finalized:     100,
			nonce:         7,
			expectRPC:     true,
			expectRetry:   true,
			expectRemoved: true,
		},
		{
			name:        "nonce advanced after expiry",
			finalized:   100,
			nonce:       8,
			expectRPC:   true,
			expectRetry: false,
		},
		{
			name:          "existing account with nonce zero",
			finalized:     100,
			expectRPC:     true,
			expectRetry:   true,
			expectRemoved: true,
		},
		{
			name:      "absent account after expiry",
			finalized: 100,
			missing:   true,
			expectRPC: true,
		},
	}

	for _, tt := range tests {
		h := newRPCHandler()
		called := false
		h.on("state_getStorage", func(params json.RawMessage) (interface{}, error) {
			called = true
			if !strings.Contains(string(params), "0xfinalized") {
				return nil, fmt.Errorf("query was not anchored to finalized block: %s", params)
			}
			if tt.missing {
				return nil, nil
			}
			return accountInfoHex(tt.nonce), nil
		})
		srv := httptest.NewServer(h)
		cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
		txOutItem, txHash, vaultAddr := seedSignedExtrinsic(c, cl, 7, 100)

		canRetry, err := cl.canRetrySignedTx(txHash, vaultAddr, "0xfinalized", tt.finalized)
		c.Assert(err, IsNil, Commentf("%s", tt.name))
		c.Check(canRetry, Equals, tt.expectRetry, Commentf("%s", tt.name))
		c.Check(called, Equals, tt.expectRPC, Commentf("%s", tt.name))
		c.Check(cl.HasSigned(txOutItem), Equals, true, Commentf("%s: check must not mutate state", tt.name))
		if canRetry {
			c.Assert(cl.releaseExpiredSignedTx(txHash), IsNil, Commentf("%s", tt.name))
		}
		c.Check(cl.HasSigned(txOutItem), Equals, !tt.expectRemoved, Commentf("%s", tt.name))
		_, found, readErr := cl.stateStore.GetExtrinsic(txHash)
		c.Assert(readErr, IsNil)
		c.Check(found, Equals, !tt.expectRemoved, Commentf("%s: recovery record", tt.name))
		srv.Close()
	}
}

func (s *ClientSuite) TestExpiredSignedExtrinsicKeepsCacheOnMissingState(c *C) {
	cl := newTestClient(nil)
	txOutItem, rawTx := makeBroadcastTestTx(c, 7)
	txHash := subtypes.ExtrinsicHash(rawTx)
	vaultAddr := cl.GetAddress(txOutItem.GetVaultPubKey())
	c.Assert(cl.SetSigned(txOutItem, txHash), IsNil)

	canRetry, err := cl.canRetrySignedTx(txHash, vaultAddr, "0xfinalized", 100)
	c.Assert(err, NotNil)
	c.Check(canRetry, Equals, false)
	c.Check(cl.HasSigned(txOutItem), Equals, true)
}

func (s *ClientSuite) TestExpiredSignedExtrinsicKeepsCacheOnRPCError(c *C) {
	h := newRPCHandler()
	h.on("state_getStorage", func(json.RawMessage) (interface{}, error) {
		return nil, errors.New("node unavailable")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
	txOutItem, txHash, vaultAddr := seedSignedExtrinsic(c, cl, 7, 100)

	canRetry, err := cl.canRetrySignedTx(txHash, vaultAddr, "0xfinalized", 100)
	c.Assert(err, NotNil)
	c.Check(canRetry, Equals, false)
	c.Check(cl.HasSigned(txOutItem), Equals, true)
}

func (s *ClientSuite) TestFinalizedFailureRemovesBothStores(c *C) {
	cl := newTestClient(nil)
	txOutItem, txHash, _ := seedSignedExtrinsic(c, cl, 7, 100)

	c.Assert(cl.handleFinalizedFailure(txHash), IsNil)
	c.Check(cl.HasSigned(txOutItem), Equals, false)
	_, found, err := cl.stateStore.GetExtrinsic(txHash)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)
}

func (s *ClientSuite) TestFailedOutboundObservationDoesNotRecreateSignerCache(c *C) {
	cl := newTestClient(nil)
	cl.chainScanner.pubkeyMgr = pubkeymanager.NewMockPoolAddressValidator()
	txHash := "0x" + strings.Repeat("a", 64)
	txID, err := common.NewTxID(txHash)
	c.Assert(err, IsNil)
	item := stypes.TxInItem{
		Tx:     txHash,
		Memo:   "OUT:" + txID.String(),
		Sender: "sender",
		To:     "destination",
		Coins: common.NewCoins(common.NewCoin(
			common.TAOChain.GetGasAsset(), cosmos.OneUint(),
		)),
	}
	cacheItem := stypes.TxOutItem{
		Chain:     common.TAOChain,
		ToAddress: common.Address(item.To),
		Coins:     item.Coins,
		Memo:      item.Memo,
		InHash:    txID,
	}

	cl.OnObservedTxIn(item, 100)
	c.Check(cl.HasSigned(cacheItem), Equals, false)
}

// ---------------------------------------------------------------------------
// createAndSignExtrinsic — null-address destination guard
// ---------------------------------------------------------------------------

func (s *ClientSuite) TestCreateAndSignExtrinsic_NullAddressGuard(c *C) {
	tests := []struct {
		name        string
		pubkey      []byte
		wantNullErr bool
	}{
		{
			name:        "all-zero pubkey rejected",
			pubkey:      make([]byte, 32),
			wantNullErr: true,
		},
		{
			name:        "valid pubkey passes the null-address gate",
			pubkey:      defaultTestSigner(),
			wantNullErr: false,
		},
		{
			// Boundary: a single non-zero byte must be enough to pass the
			// gate. Confirms the check uses bytes.Equal (all-bytes-zero),
			// not a weaker comparison.
			name: "single non-zero byte passes the gate",
			pubkey: func() []byte {
				b := make([]byte, 32)
				b[31] = 0x01
				return b
			}(),
			wantNullErr: false,
		},
	}

	cl := newTestClient(nil)
	for _, tt := range tests {
		addr, err := ss58.Encode(common.TAOChain.SS58Prefix(), tt.pubkey)
		c.Assert(err, IsNil, Commentf("%s: encode SS58", tt.name))

		tx := stypes.TxOutItem{
			Chain:     common.TAOChain,
			ToAddress: common.Address(addr),
			Coins:     common.NewCoins(common.NewCoin(common.TAOAsset, cosmos.NewUint(1_000))),
		}

		_, err = cl.createAndSignExtrinsic(tx, "vault", 0, generic.BlockReference{}, 1)

		if tt.wantNullErr {
			c.Assert(err, NotNil, Commentf("%s: expected error", tt.name))
			c.Check(strings.Contains(err.Error(), "null address"), Equals, true,
				Commentf("%s: expected null-address error, got: %v", tt.name, err))
			continue
		}
		// The function may still error for unrelated reasons further down
		// (genesis hash, signing, etc.) — what matters here is that the
		// null-address guard didn't fire.
		if err != nil {
			c.Check(strings.Contains(err.Error(), "null address"), Equals, false,
				Commentf("%s: unexpected null-address error: %v", tt.name, err))
		}
	}
}

// ---------------------------------------------------------------------------
// OnObservedTxIn — substrate-specific migration cache population
// ---------------------------------------------------------------------------

func (s *ClientSuite) TestOnObservedTxIn_PopulatesMigrationCache(c *C) {
	const vaultAddr = "vault-addr"
	const userAddr = "user-addr"

	tests := []struct {
		name    string
		memo    string
		sender  string
		wantHas bool
	}{
		{
			name:    "MIGRATE memo from vault sender → cached",
			memo:    "MIGRATE:1234",
			sender:  vaultAddr,
			wantHas: true,
		},
		{
			name:    "CONSOLIDATE memo from vault sender → cached",
			memo:    "CONSOLIDATE:1234",
			sender:  vaultAddr,
			wantHas: true,
		},
		{
			name:    "MIGRATE memo from non-vault sender → not cached",
			memo:    "MIGRATE:1234",
			sender:  userAddr,
			wantHas: false,
		},
		{
			// SWAP is IsInbound, not IsInternal — the migration cache only
			// tracks internal flows.
			name:    "SWAP memo from vault sender → not cached",
			memo:    "SWAP:BTC.BTC:bc1qtest",
			sender:  vaultAddr,
			wantHas: false,
		},
		{
			name:    "unparsable memo from vault sender → not cached",
			memo:    "garbage:::",
			sender:  vaultAddr,
			wantHas: false,
		},
	}

	for i, tt := range tests {
		cl := newTestClient(nil)
		cl.chainScanner.pubkeyMgr = &vaultPubKeyStub{
			MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
			vaults:                   map[string]bool{vaultAddr: true},
		}

		// Use a unique txid per row so prior iterations don't bleed in via
		// the shared signer cache (the generic OnObservedTxIn may write to it
		// for OUT memos; not at issue here, but defensive).
		txID := fmt.Sprintf("0x%064x", i+1)
		nonce := uint64(1)

		// Pre-populate the nonce cache the way the scanner would have, since
		// OnObservedTxIn reads (txID -> nonce) out of it before writing to the
		// migration cache.
		cl.chainScanner.nonceCache.Add(txID, nonce, 100)

		txIn := stypes.TxInItem{
			Tx:     txID,
			Memo:   tt.memo,
			Sender: tt.sender,
		}

		cl.OnObservedTxIn(txIn, 100)

		cacheKey := migrationTxCacheKey(tt.sender, nonce)
		got := cl.migrationTxCache.Has(cacheKey)
		c.Check(got, Equals, tt.wantHas,
			Commentf("%s: cache.Has(%s) = %v, want %v", tt.name, txID, got, tt.wantHas))
	}
}

// ---------------------------------------------------------------------------
// reserveNonce — MaxPendingNonces cap
// ---------------------------------------------------------------------------

func (s *ClientSuite) TestReserveNonce_MaxPendingNoncesCap(c *C) {
	const vault = "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	const onChainNonce uint32 = 5

	tests := []struct {
		name        string
		maxPending  uint64
		cachedNonce uint32 // pre-populated cache nonce
		wantErr     bool
	}{
		{
			name:        "gap below cap → success",
			maxPending:  16,
			cachedNonce: onChainNonce + 1, // gap = 1
			wantErr:     false,
		},
		{
			name:        "gap exactly at cap → success",
			maxPending:  16,
			cachedNonce: onChainNonce + 16, // gap = 16, condition is `> max`
			wantErr:     false,
		},
		{
			name:        "gap exceeds cap → error",
			maxPending:  16,
			cachedNonce: onChainNonce + 17, // gap = 17 > 16
			wantErr:     true,
		},
		{
			// Only the test helper preserves zero; NewClient defaults it to 3.
			name:        "zero cap rejects a positive nonce gap",
			maxPending:  0,
			cachedNonce: onChainNonce + 1,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		h := newRPCHandler()
		h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
			return onChainNonce, nil
		})
		srv := httptest.NewServer(h)

		cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second), withMaxPendingNonces(tt.maxPending))
		// Pre-populate the cache ahead of on-chain. ReservedHeight is set so
		// mortal-era expiry does NOT trigger first (would otherwise reset
		// the cache and bypass the cap branch).
		cl.nonces[vault] = nonceMetadata{
			Nonce:          tt.cachedNonce,
			ReservedHeight: 100,
		}

		// currentHeight 101: just past the cache's ReservedHeight so the
		// on-chain fetch fires, but well within MortalEraPeriod (64) so the
		// expiry branch doesn't preempt the cap check.
		_, _, err := cl.reserveNonce(stypes.TxOutItem{Chain: common.TAOChain}, vault, 101)

		if tt.wantErr {
			c.Assert(err, NotNil, Commentf("%s: expected error", tt.name))
			c.Check(cl.getCachedNonce(vault), Equals, nonceMetadata{Nonce: tt.cachedNonce, ReservedHeight: 100})
			c.Check(strings.Contains(err.Error(), "pending nonce"), Equals, true,
				Commentf("%s: error %q does not mention pending nonce", tt.name, err))
		} else {
			c.Check(err, IsNil, Commentf("%s: unexpected error: %v", tt.name, err))
		}

		srv.Close()
	}
}

func (s *ClientSuite) TestReserveNonce_PendingLimitOnRefresh(c *C) {
	const vault = "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	var rpcNonce atomic.Uint32
	rpcNonce.Store(5)
	var calls atomic.Int32
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		calls.Add(1)
		return rpcNonce.Load(), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second), withMaxPendingNonces(3))
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	// Same-height reservations can exceed the gap limit without refreshing.
	for want := uint32(5); want <= 9; want++ {
		nonce, _, err := cl.reserveNonce(tx, vault, 100)
		c.Assert(err, IsNil)
		c.Check(nonce, Equals, want)
	}
	c.Check(calls.Load(), Equals, int32(1))
	c.Check(cl.getCachedNonce(vault), Equals, nonceMetadata{Nonce: 10, ReservedHeight: 100})

	// An unchanged RPC nonce blocks reservations at the next height.
	_, checkpoint, err := cl.reserveNonce(tx, vault, 101)
	c.Assert(err, ErrorMatches, "pending nonce too far in future")
	c.Check(checkpoint, IsNil)
	c.Check(calls.Load(), Equals, int32(2))
	c.Check(cl.getCachedNonce(vault), Equals, nonceMetadata{Nonce: 10, ReservedHeight: 100})

	// Retry succeeds once the RPC nonce catches up.
	rpcNonce.Store(10)
	nonce, _, err := cl.reserveNonce(tx, vault, 101)
	c.Assert(err, IsNil)
	c.Check(nonce, Equals, uint32(10))
	c.Check(calls.Load(), Equals, int32(3))
	c.Check(cl.getCachedNonce(vault), Equals, nonceMetadata{Nonce: 11, ReservedHeight: 101})
}

func (s *ClientSuite) TestReportSolvency_DoesNotRecurse(c *C) {
	cl := newTestClient(nil)
	// only check that the function returns and not recurse infinitely
	_ = cl.ReportSolvency(1)
}
