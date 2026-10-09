package thorclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	cKeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type BlockTimestampCovSuite struct{}

var _ = Suite(&BlockTimestampCovSuite{})

type rpcReqCov struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Height string `json:"height"`
	} `json:"params"`
}

func newBridgeForServerCov(c *C, srv *httptest.Server) *thorchainBridge {
	cfg, _, kb := SetupThorchainForTest(c)
	cfg.ChainHost = srv.Listener.Addr().String()
	cfg.ChainRPC = srv.Listener.Addr().String()
	bridge, err := NewThorchainBridge(cfg, GetMetricForTest(c), NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd))
	c.Assert(err, IsNil)
	b, ok := bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	b.httpClient.RetryMax = 0
	return b
}

func (s *BlockTimestampCovSuite) TestGetBlockTimestamp(c *C) {
	blockTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var r rpcReqCov
		_ = json.Unmarshal(body, &r)
		id := string(r.ID)
		if id == "" {
			id = "0"
		}
		rw.Header().Set("Content-Type", "application/json")
		var resp string
		switch r.Params.Height {
		case "5":
			resp = fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"block_id":{"hash":"","parts":{"total":0,"hash":""}},"block":{"header":{"version":{"block":"11","app":"0"},"chain_id":"thorchain","height":"5","time":"%s","last_block_id":{"hash":"","parts":{"total":0,"hash":""}}},"data":{"txs":[]},"evidence":{"evidence":[]},"last_commit":null}}}`,
				id, blockTime.Format(time.RFC3339Nano))
		case "7":
			resp = fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"block_id":{"hash":"","parts":{"total":0,"hash":""}},"block":null}}`, id)
		default:
			resp = fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"height not available","data":""}}`, id)
		}
		_, _ = rw.Write([]byte(resp))
	}))
	defer srv.Close()

	b := newBridgeForServerCov(c, srv)

	ts, err := b.GetBlockTimestamp(5)
	c.Assert(err, IsNil)
	c.Check(ts.Equal(blockTime), Equals, true, Commentf("got %s", ts))

	_, err = b.GetBlockTimestamp(6)
	c.Assert(err, NotNil)
	c.Check(strings.Contains(err.Error(), "failed to query block at height 6"), Equals, true, Commentf("%s", err))

	_, err = b.GetBlockTimestamp(7)
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "block result is nil for height 7")
}

func (s *BlockTimestampCovSuite) TestBroadcastWithBlocking(c *C) {
	var height atomic.Int64
	height.Store(10)
	lastBlock := func() string {
		return fmt.Sprintf(`[{"chain":"ETH","last_observed_in":1,"last_signed_out":1,"thorchain":%d}]`, height.Load())
	}
	successFixture := "../../test/fixtures/endpoints/txs/success.json"
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasPrefix(req.RequestURI, AuthAccountEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/auth/accounts/template.json")
		case strings.HasPrefix(req.RequestURI, LastBlockEndpoint):
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(lastBlock()))
		case req.RequestURI == BroadcastTxsEndpoint:
			// once the tx is accepted, the chain advances a block and the shared
			// response cache is dropped so the next height poll is fresh
			height.Add(1)
			httpResponseCachesMu.Lock()
			for k := range httpResponseCaches {
				if strings.Contains(k, req.Host) {
					delete(httpResponseCaches, k)
				}
			}
			httpResponseCachesMu.Unlock()
			httpTestHandler(c, rw, successFixture)
		}
	}))
	defer srv.Close()

	b := newBridgeForServerCov(c, srv)
	msg := types.NewMsgNetworkFee(1, common.ETHChain, 1, 37500, types.GetRandomBech32Addr())

	start := time.Now()
	txID, err := b.BroadcastWithBlocking(msg)
	c.Assert(err, IsNil)
	c.Check(txID.String(), Equals, "D97E8A81417E293F5B28DDB53A4AD87B434CA30F51D683DA758ECC2168A7A005")
	// returned on the first poll, not after the 30 s timeout
	c.Check(time.Since(start) < 5*time.Second, Equals, true)
	c.Check(b.blockHeight, Equals, int64(10))
	c.Check(b.accountNumber, Equals, uint64(3))
	c.Check(b.seqNumber, Equals, uint64(6))
}

func (s *BlockTimestampCovSuite) TestBroadcastWithBlockingError(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b := newBridgeForServerCov(c, srv)
	msg := types.NewMsgNetworkFee(1, common.ETHChain, 1, 37500, types.GetRandomBech32Addr())
	txID, err := b.BroadcastWithBlocking(msg)
	c.Assert(err, NotNil)
	c.Check(txID.IsEmpty(), Equals, true)
}

func newEDDSAKeyringCov(c *C) cKeys.Keyring {
	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	cryptocodec.RegisterInterfaces(cdc.InterfaceRegistry())
	return cKeys.NewInMemory(cdc, func(options *cKeys.Options) {
		options.SupportedAlgos = append(options.SupportedAlgos, ed25519.Ed25519)
	})
}

func (s *BlockTimestampCovSuite) TestGetPrivateKeyEDDSA(c *C) {
	const name, pass = "alice", "password"
	kb := newEDDSAKeyringCov(c)
	_, mnemonic, err := kb.NewMnemonic(ed25519.SignerNameEDDSA(name), cKeys.English, "", pass, ed25519.Ed25519)
	c.Assert(err, IsNil)
	_, err = kb.NewAccount(name, mnemonic, pass, cmd.THORChainHDPath, hd.Secp256k1)
	c.Assert(err, IsNil)

	k := NewKeysWithKeybase(kb, name, pass)
	edKey, err := k.GetPrivateKeyEDDSA()
	c.Assert(err, IsNil)
	c.Assert(edKey, NotNil)
	rec, err := kb.Key(ed25519.SignerNameEDDSA(name))
	c.Assert(err, IsNil)
	pub, err := rec.GetPubKey()
	c.Assert(err, IsNil)
	c.Check(edKey.PubKey().Equals(pub), Equals, true)

	secpKey, err := k.GetPrivateKey()
	c.Assert(err, IsNil)
	c.Check(secpKey.Bytes(), HasLen, 32)

	// a signer with no eddsa key cannot export one
	_, err = NewKeysWithKeybase(kb, "missing", pass).GetPrivateKeyEDDSA()
	c.Assert(err, NotNil)
	_, err = NewKeysWithKeybase(kb, "missing", pass).GetPrivateKey()
	c.Assert(err, NotNil)
}

func (s *BlockTimestampCovSuite) TestGetSignerInfoPanicsOnMissingKey(c *C) {
	kb := newEDDSAKeyringCov(c)
	k := NewKeysWithKeybase(kb, "nobody", "password")
	c.Check(func() { k.GetSignerInfo() }, PanicMatches, ".*nobody.*")
}

func (s *BlockTimestampCovSuite) TestPrivateKeyWrongAlgo(c *C) {
	const name, pass = "bob", "password"
	kb := newEDDSAKeyringCov(c)
	edName := ed25519.SignerNameEDDSA(name)
	// eddsa slot holds a secp256k1 key, and the secp slot holds an ed25519 key
	_, mnemonic, err := kb.NewMnemonic(edName, cKeys.English, cmd.THORChainHDPath, pass, hd.Secp256k1)
	c.Assert(err, IsNil)
	_, err = kb.NewAccount(name, mnemonic, pass, "", ed25519.Ed25519)
	c.Assert(err, IsNil)

	k := NewKeysWithKeybase(kb, name, pass)
	_, err = k.GetPrivateKeyEDDSA()
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "fail to cast private key to eddsa private key")

	_, err = k.GetPrivateKey()
	c.Assert(err, NotNil)
	c.Check(err.Error(), Equals, "fail to cast private key to secp256k1 private key")
}
