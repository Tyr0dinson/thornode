package substrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/generic"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ed25519"
	"gitlab.com/thorchain/thornode/v3/config"

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
}

// ---------------------------------------------------------------------------
// nonceMetadata JSON serialization
// ---------------------------------------------------------------------------

func (s *ClientSuite) TestNonceMetadataJSON(c *C) {
	meta := nonceMetadata{
		Nonce:       42,
		BlockHeight: 1000,
	}

	data, err := json.Marshal(meta)
	c.Assert(err, IsNil)

	var restored nonceMetadata
	err = json.Unmarshal(data, &restored)
	c.Assert(err, IsNil)

	c.Check(restored.Nonce, Equals, meta.Nonce)
	c.Check(restored.BlockHeight, Equals, meta.BlockHeight)
}

func (s *ClientSuite) TestNonceMetadataJSONBackwardsCompat(c *C) {
	// Ensure the JSON field names match the old NonceMetadata format
	// so that checkpoints serialized before the refactor still deserialize.
	raw := `{"Nonce":7,"BlockHeight":500}`
	var meta nonceMetadata
	err := json.Unmarshal([]byte(raw), &meta)
	c.Assert(err, IsNil)
	c.Check(meta.Nonce, Equals, uint32(7))
	c.Check(meta.BlockHeight, Equals, int64(500))
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32000,"message":"%s"}}`, req.ID, err.Error())
		return
	}

	resultJSON, _ := json.Marshal(result)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, resultJSON)
}

func newTestClient(rpcClient *rpc.Client) *Client {
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

	return &Client{
		Client:    genericClient,
		logger:    log.Logger,
		rpcClient: rpcClient,
		nonces:    make(map[string]nonceMetadata),
		chainScanner: &Scanner{
			params: ChainParams[common.TAOChain],
		},
	}
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
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
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
}

func (s *ClientSuite) TestReserveNonce_Checkpoint(c *C) {
	cl := newTestClient(nil) // RPC not needed for checkpoint path
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"

	checkpointData, _ := json.Marshal(nonceMetadata{Nonce: 42, BlockHeight: 100})
	tx := stypes.TxOutItem{
		Chain:      common.TAOChain,
		Checkpoint: checkpointData,
	}

	nonce, cp, err := cl.reserveNonce(tx, vault, 200)
	c.Assert(err, IsNil)
	c.Check(nonce, Equals, uint32(42))
	// Checkpoint should be returned as-is
	c.Check(string(cp), Equals, string(checkpointData), Commentf("checkpoint mismatch"))

	// The nonce map should NOT have been incremented (checkpoint bypasses increment)
	if meta, ok := cl.nonces[vault]; ok {
		c.Check(meta.Nonce, Equals, uint32(0),
			Commentf("nonce map should not be modified by checkpoint path, got nonce=%d", meta.Nonce))
	}
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

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	// First call at height 100: gets on-chain nonce 5, reserves it, cache goes to 6
	n1, _, err := cl.reserveNonce(tx, vault, 100)
	c.Assert(err, IsNil, Commentf("first"))
	c.Check(n1, Equals, uint32(5), Commentf("first nonce"))

	// Simulate: the broadcast failed, on-chain nonce is still 5, but cache is at 6.
	// At height 101 (same block epoch), cache nonce (6) > on-chain (5) and
	// mortalEraPeriod hasn't passed, so cache wins — returns 6.
	n2, _, err := cl.reserveNonce(tx, vault, 101)
	c.Assert(err, IsNil, Commentf("second"))
	c.Check(n2, Equals, uint32(6), Commentf("second nonce: cache ahead, era not expired"))

	// Now jump past mortalEraPeriod (64 blocks from height 101 when cache was updated).
	// At height 101+64=165, the mortal era expiry triggers reset to on-chain nonce 5.
	n3, _, err := cl.reserveNonce(tx, vault, 165)
	c.Assert(err, IsNil, Commentf("third"))
	c.Check(n3, Equals, uint32(5), Commentf("third nonce: should reset after mortal era expiry"))
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
	h.on("state_getStorage", func(_ json.RawMessage) (interface{}, error) {
		return accountHex, nil
	})
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		return uint32(1), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))

	acct, err := cl.GetAccountByAddress("5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY", nil)
	c.Assert(err, IsNil)

	c.Check(acct.Sequence, Equals, int64(1))

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

	txOutItem := stypes.TxOutItem{Chain: common.TAOChain, Memo: "OUT:hash"}
	rawTx := []byte{0x01, 0x02, 0x03}

	txHash, err := cl.BroadcastTx(txOutItem, rawTx)
	c.Assert(err, IsNil)
	c.Check(txHash, Not(Equals), "", Commentf("expected non-empty tx hash"))
}

func (s *ClientSuite) TestBroadcastTx_AlreadyImported(c *C) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("rpc error (code: 1013): Transaction already imported")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))

	txOutItem := stypes.TxOutItem{Chain: common.TAOChain, Memo: "OUT:hash"}
	rawTx := []byte{0x01, 0x02, 0x03}

	// Should succeed — AlreadyImported is treated as success
	txHash, err := cl.BroadcastTx(txOutItem, rawTx)
	c.Assert(err, IsNil, Commentf("BroadcastTx should succeed for AlreadyImported"))
	c.Check(txHash, Not(Equals), "", Commentf("expected non-empty tx hash"))
}

func (s *ClientSuite) TestBroadcastTx_Stale(c *C) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("rpc error (code: 1010): Stale")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))

	txOutItem := stypes.TxOutItem{Chain: common.TAOChain, Memo: "OUT:hash"}
	rawTx := []byte{0x01, 0x02, 0x03}

	// Should succeed — Stale nonce means tx already confirmed
	txHash, err := cl.BroadcastTx(txOutItem, rawTx)
	c.Assert(err, IsNil, Commentf("BroadcastTx should succeed for Stale"))
	c.Check(txHash, Not(Equals), "", Commentf("expected non-empty tx hash"))
}

func (s *ClientSuite) TestBroadcastTx_OtherError(c *C) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("rpc error (code: -32000): Some other error")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	cl := newTestClient(rpc.NewClient(srv.URL, 5*time.Second))

	txOutItem := stypes.TxOutItem{Chain: common.TAOChain, Memo: "OUT:hash"}
	rawTx := []byte{0x01, 0x02, 0x03}

	_, err := cl.BroadcastTx(txOutItem, rawTx)
	c.Check(err, NotNil, Commentf("BroadcastTx should fail for unknown errors"))
}
