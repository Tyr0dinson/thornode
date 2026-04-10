package substrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	taorpc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/bittensor/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// ---------------------------------------------------------------------------
// convertToRao
// ---------------------------------------------------------------------------

func TestConvertToRao(t *testing.T) {
	tests := []struct {
		name     string
		amount   cosmos.Uint
		expected uint64
	}{
		{"zero", cosmos.ZeroUint(), 0},
		{"one", cosmos.NewUint(1), 10},
		{"1e8", cosmos.NewUint(100_000_000), 1_000_000_000},
		{"large", cosmos.NewUint(1_000_000_000_000), 10_000_000_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertToRao(tt.amount)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("got %d, want %d", result, tt.expected)
			}
		})
	}
}

func TestConvertToRao_Overflow(t *testing.T) {
	// Value that overflows uint64 when multiplied by 10
	huge := cosmos.NewUintFromString("99999999999999999999")
	_, err := convertToRao(huge)
	if err == nil {
		t.Error("expected overflow error for huge amount")
	}
}

func TestConvertToRao_BoundaryOverflow(t *testing.T) {
	// math.MaxUint64/10 should succeed, math.MaxUint64/10+1 should overflow on multiply
	maxSafe := cosmos.NewUint(1844674407370955161) // math.MaxUint64 / 10
	_, err := convertToRao(maxSafe)
	if err != nil {
		t.Errorf("expected success for max safe value, got: %v", err)
	}

	justOver := cosmos.NewUint(1844674407370955162) // math.MaxUint64/10 + 1
	_, err = convertToRao(justOver)
	if err == nil {
		t.Error("expected overflow error for boundary+1 value")
	}
}

func TestConvertToRaoAndBack(t *testing.T) {
	// Round-trip: amount → rao → thorchain should preserve value
	// (assuming amount * 10 / 10 = amount for exact multiples)
	original := cosmos.NewUint(42_000_000)
	rao, err := convertToRao(original)
	if err != nil {
		t.Fatalf("convertToRao: %v", err)
	}
	back := convertRaoToThorchain(rao)
	if !back.Equal(original) {
		t.Errorf("round trip: got %s, want %s", back, original)
	}
}

// ---------------------------------------------------------------------------
// nonceMetadata JSON serialization
// ---------------------------------------------------------------------------

func TestNonceMetadataJSON(t *testing.T) {
	meta := nonceMetadata{
		Nonce:       42,
		BlockHeight: 1000,
	}

	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var restored nonceMetadata
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if restored.Nonce != meta.Nonce {
		t.Errorf("nonce: got %d, want %d", restored.Nonce, meta.Nonce)
	}
	if restored.BlockHeight != meta.BlockHeight {
		t.Errorf("block height: got %d, want %d", restored.BlockHeight, meta.BlockHeight)
	}
}

func TestNonceMetadataJSONBackwardsCompat(t *testing.T) {
	// Ensure the JSON field names match the old NonceMetadata format
	// so that checkpoints serialized before the refactor still deserialize.
	raw := `{"Nonce":7,"BlockHeight":500}`
	var meta nonceMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if meta.Nonce != 7 {
		t.Errorf("nonce: got %d, want 7", meta.Nonce)
	}
	if meta.BlockHeight != 500 {
		t.Errorf("block height: got %d, want 500", meta.BlockHeight)
	}
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

func newTestClient(rpcClient *taorpc.Client) *TAOClient {
	return &TAOClient{
		logger:    log.Logger,
		rpcClient: rpcClient,
		nonces:    make(map[string]nonceMetadata),
	}
}

// ---------------------------------------------------------------------------
// reserveNonce tests
// ---------------------------------------------------------------------------

func TestReserveNonce_Fresh(t *testing.T) {
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		return uint32(0), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := newTestClient(taorpc.NewClient(srv.URL, 5*time.Second))
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	nonce, checkpoint, err := c.reserveNonce(tx, "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY", 100)
	if err != nil {
		t.Fatalf("reserveNonce: %v", err)
	}
	if nonce != 0 {
		t.Errorf("nonce: got %d, want 0", nonce)
	}
	if checkpoint == nil {
		t.Fatal("checkpoint should not be nil")
	}
}

func TestReserveNonce_Increment(t *testing.T) {
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		return uint32(5), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := newTestClient(taorpc.NewClient(srv.URL, 5*time.Second))
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	n1, _, err := c.reserveNonce(tx, vault, 100)
	if err != nil {
		t.Fatalf("first reserveNonce: %v", err)
	}
	if n1 != 5 {
		t.Errorf("first nonce: got %d, want 5", n1)
	}

	// Second call at same height should return incremented nonce
	n2, _, err := c.reserveNonce(tx, vault, 100)
	if err != nil {
		t.Fatalf("second reserveNonce: %v", err)
	}
	if n2 != 6 {
		t.Errorf("second nonce: got %d, want 6", n2)
	}
}

func TestReserveNonce_Checkpoint(t *testing.T) {
	c := newTestClient(nil) // RPC not needed for checkpoint path
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"

	checkpointData, _ := json.Marshal(nonceMetadata{Nonce: 42, BlockHeight: 100})
	tx := stypes.TxOutItem{
		Chain:      common.TAOChain,
		Checkpoint: checkpointData,
	}

	nonce, cp, err := c.reserveNonce(tx, vault, 200)
	if err != nil {
		t.Fatalf("reserveNonce: %v", err)
	}
	if nonce != 42 {
		t.Errorf("nonce: got %d, want 42", nonce)
	}
	// Checkpoint should be returned as-is
	if string(cp) != string(checkpointData) {
		t.Errorf("checkpoint mismatch")
	}

	// The nonce map should NOT have been incremented (checkpoint bypasses increment)
	if meta, ok := c.nonces[vault]; ok && meta.Nonce != 0 {
		t.Errorf("nonce map should not be modified by checkpoint path, got nonce=%d", meta.Nonce)
	}
}

func TestReserveNonce_OnChainRefresh(t *testing.T) {
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

	c := newTestClient(taorpc.NewClient(srv.URL, 5*time.Second))
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	// First call at height 100
	n1, _, err := c.reserveNonce(tx, vault, 100)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if n1 != 3 {
		t.Errorf("first nonce: got %d, want 3", n1)
	}

	// Second call at higher height — should fetch fresh on-chain nonce
	n2, _, err := c.reserveNonce(tx, vault, 200)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if n2 != 5 {
		t.Errorf("second nonce: got %d, want 5 (on-chain advanced)", n2)
	}
}

func TestReserveNonce_MortalEraExpiry(t *testing.T) {
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		// On-chain nonce stays at 5 (broadcast failed, tx expired)
		return uint32(5), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := newTestClient(taorpc.NewClient(srv.URL, 5*time.Second))
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	// First call at height 100: gets on-chain nonce 5, reserves it, cache goes to 6
	n1, _, err := c.reserveNonce(tx, vault, 100)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if n1 != 5 {
		t.Errorf("first nonce: got %d, want 5", n1)
	}

	// Simulate: the broadcast failed, on-chain nonce is still 5, but cache is at 6.
	// At height 101 (same block epoch), cache nonce (6) > on-chain (5) and
	// mortalEraPeriod hasn't passed, so cache wins — returns 6.
	n2, _, err := c.reserveNonce(tx, vault, 101)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if n2 != 6 {
		t.Errorf("second nonce: got %d, want 6 (cache ahead, era not expired)", n2)
	}

	// Now jump past mortalEraPeriod (64 blocks from height 101 when cache was updated).
	// At height 101+64=165, the mortal era expiry triggers reset to on-chain nonce 5.
	n3, _, err := c.reserveNonce(tx, vault, 165)
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	if n3 != 5 {
		t.Errorf("third nonce: got %d, want 5 (should reset after mortal era expiry)", n3)
	}
}

func TestReserveNonce_ScannerNotReady(t *testing.T) {
	// When the scanner hasn't observed any blocks, reserveNonce is called with
	// currentHeight=0. This reserves nonce 0 without on-chain verification.
	// Verify that the nonce self-corrects once the scanner starts.
	h := newRPCHandler()
	h.on("system_accountNextIndex", func(_ json.RawMessage) (interface{}, error) {
		return uint32(10), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := newTestClient(taorpc.NewClient(srv.URL, 5*time.Second))
	vault := "5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY"
	tx := stypes.TxOutItem{Chain: common.TAOChain}

	// First call at height 0 (scanner not ready) — doesn't fetch on-chain nonce
	n1, _, err := c.reserveNonce(tx, vault, 0)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if n1 != 0 {
		t.Errorf("first nonce: got %d, want 0 (empty cache, height 0 skips fetch)", n1)
	}

	// Second call once scanner is running (height 100) — fetches on-chain nonce
	// and the mortal era expiry resets the cache (100 >= 0 + 64)
	n2, _, err := c.reserveNonce(tx, vault, 100)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if n2 != 10 {
		t.Errorf("second nonce: got %d, want 10 (should reset to on-chain nonce)", n2)
	}
}

// ---------------------------------------------------------------------------
// GetAccountByAddress test
// ---------------------------------------------------------------------------

func TestGetAccountByAddress(t *testing.T) {
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

	c := newTestClient(taorpc.NewClient(srv.URL, 5*time.Second))

	acct, err := c.GetAccountByAddress("5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY", nil)
	if err != nil {
		t.Fatalf("GetAccountByAddress: %v", err)
	}

	if acct.Sequence != 1 {
		t.Errorf("sequence: got %d, want 1", acct.Sequence)
	}

	// 5_000_000_000 rao / 10 = 500_000_000 thorchain units
	expectedAmount := cosmos.NewUint(500_000_000)
	if len(acct.Coins) != 1 {
		t.Fatalf("coins len: got %d, want 1", len(acct.Coins))
	}
	if !acct.Coins[0].Amount.Equal(expectedAmount) {
		t.Errorf("amount: got %s, want %s", acct.Coins[0].Amount, expectedAmount)
	}
	if !acct.Coins[0].Asset.Equals(common.TAOAsset) {
		t.Errorf("asset: got %s, want %s", acct.Coins[0].Asset, common.TAOAsset)
	}
	if acct.Coins[0].Decimals != 9 {
		t.Errorf("decimals: got %d, want 9", acct.Coins[0].Decimals)
	}
}

// ---------------------------------------------------------------------------
// BroadcastTx tests
// ---------------------------------------------------------------------------

// newTestSignerCache creates a signercache.CacheManager backed by in-memory LevelDB.
func newTestSignerCache(t *testing.T) *signercache.CacheManager {
	t.Helper()
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	if err != nil {
		t.Fatalf("fail to open in-memory leveldb: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	cm, err := signercache.NewSignerCacheManager(db)
	if err != nil {
		t.Fatalf("fail to create signer cache: %v", err)
	}
	return cm
}

func TestBroadcastTx_Success(t *testing.T) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := newTestClient(taorpc.NewClient(srv.URL, 5*time.Second))
	c.signerCacheManager = newTestSignerCache(t)

	txOutItem := stypes.TxOutItem{Chain: common.TAOChain, Memo: "OUT:hash"}
	rawTx := []byte{0x01, 0x02, 0x03}

	txHash, err := c.BroadcastTx(txOutItem, rawTx)
	if err != nil {
		t.Fatalf("BroadcastTx: %v", err)
	}
	if txHash == "" {
		t.Error("expected non-empty tx hash")
	}
}

func TestBroadcastTx_AlreadyImported(t *testing.T) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("rpc error (code: 1013): Transaction already imported")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := newTestClient(taorpc.NewClient(srv.URL, 5*time.Second))
	c.signerCacheManager = newTestSignerCache(t)

	txOutItem := stypes.TxOutItem{Chain: common.TAOChain, Memo: "OUT:hash"}
	rawTx := []byte{0x01, 0x02, 0x03}

	// Should succeed — AlreadyImported is treated as success
	txHash, err := c.BroadcastTx(txOutItem, rawTx)
	if err != nil {
		t.Fatalf("BroadcastTx should succeed for AlreadyImported: %v", err)
	}
	if txHash == "" {
		t.Error("expected non-empty tx hash")
	}
}

func TestBroadcastTx_Stale(t *testing.T) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("rpc error (code: 1010): Stale")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := newTestClient(taorpc.NewClient(srv.URL, 5*time.Second))
	c.signerCacheManager = newTestSignerCache(t)

	txOutItem := stypes.TxOutItem{Chain: common.TAOChain, Memo: "OUT:hash"}
	rawTx := []byte{0x01, 0x02, 0x03}

	// Should succeed — Stale nonce means tx already confirmed
	txHash, err := c.BroadcastTx(txOutItem, rawTx)
	if err != nil {
		t.Fatalf("BroadcastTx should succeed for Stale: %v", err)
	}
	if txHash == "" {
		t.Error("expected non-empty tx hash")
	}
}

func TestBroadcastTx_OtherError(t *testing.T) {
	h := newRPCHandler()
	h.on("author_submitExtrinsic", func(_ json.RawMessage) (interface{}, error) {
		return nil, fmt.Errorf("rpc error (code: -32000): Some other error")
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := newTestClient(taorpc.NewClient(srv.URL, 5*time.Second))
	c.signerCacheManager = newTestSignerCache(t)

	txOutItem := stypes.TxOutItem{Chain: common.TAOChain, Memo: "OUT:hash"}
	rawTx := []byte{0x01, 0x02, 0x03}

	_, err := c.BroadcastTx(txOutItem, rawTx)
	if err == nil {
		t.Fatal("BroadcastTx should fail for unknown errors")
	}
}
