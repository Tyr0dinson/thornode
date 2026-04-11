package rpc

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// ---------------------------------------------------------------------------
// ss58DecodeAccountID
// ---------------------------------------------------------------------------

func TestSS58DecodeAccountID_Alice(t *testing.T) {
	// Alice's well-known dev account (prefix 42):
	// 5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY
	// Pubkey: d43593c715fdd31c61141abd04a99fd6822c8558854ccde39a5684e7a56da27d
	accountID, err := ss58DecodeAccountID("5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "d43593c715fdd31c61141abd04a99fd6822c8558854ccde39a5684e7a56da27d"
	if hex.EncodeToString(accountID) != expected {
		t.Errorf("got %s, want %s", hex.EncodeToString(accountID), expected)
	}
}

func TestSS58DecodeAccountID_Bob(t *testing.T) {
	// Bob's well-known dev account (prefix 42):
	// 5FHneW46xGXgs5mUiveU4sbTyGBzmstUspZC92UhjJM694ty
	// Pubkey: 8eaf04151687736326c9fea17e25fc5287613693c912909cb226aa4794f26a48
	accountID, err := ss58DecodeAccountID("5FHneW46xGXgs5mUiveU4sbTyGBzmstUspZC92UhjJM694ty")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := "8eaf04151687736326c9fea17e25fc5287613693c912909cb226aa4794f26a48"
	if hex.EncodeToString(accountID) != expected {
		t.Errorf("got %s, want %s", hex.EncodeToString(accountID), expected)
	}
}

func TestSS58DecodeAccountID_InvalidBase58(t *testing.T) {
	_, err := ss58DecodeAccountID("0OIl-not-base58")
	if err == nil {
		t.Error("expected error for invalid base58")
	}
}

func TestSS58DecodeAccountID_WrongLength(t *testing.T) {
	// Valid base58 but decodes to wrong length (not 35 bytes)
	_, err := ss58DecodeAccountID("111111") // decodes to 6 zero bytes
	if err == nil {
		t.Error("expected error for wrong SS58 length")
	}
}

// ---------------------------------------------------------------------------
// decodeAccountInfoFreeBalance
// ---------------------------------------------------------------------------

func TestDecodeAccountInfoFreeBalance(t *testing.T) {
	// Build a minimal AccountInfo: 4 × u32 (16 bytes) + free balance (u128 LE)
	// nonce=1, consumers=0, providers=1, sufficients=0
	// free=1_000_000_000 (1 TAO in rao)
	data := make([]byte, 32)                                  // 16 header + 16 u128
	binary.LittleEndian.PutUint32(data[0:4], 1)               // nonce
	binary.LittleEndian.PutUint32(data[4:8], 0)               // consumers
	binary.LittleEndian.PutUint32(data[8:12], 1)              // providers
	binary.LittleEndian.PutUint32(data[12:16], 0)             // sufficients
	binary.LittleEndian.PutUint64(data[16:24], 1_000_000_000) // free

	free, err := decodeAccountInfoFreeBalance(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !free.Equal(cosmos.NewUint(1_000_000_000)) {
		t.Errorf("got %d, want 1000000000", free)
	}
}

func TestDecodeAccountInfoFreeBalance_WithTrailingData(t *testing.T) {
	// Full AccountInfo with reserved, frozen, flags fields after free
	data := make([]byte, 56)                                   // 16 + 8 + 8 + 8 + 16
	binary.LittleEndian.PutUint64(data[16:24], 42_000_000_000) // free = 42 TAO

	free, err := decodeAccountInfoFreeBalance(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !free.Equal(cosmos.NewUint(42_000_000_000)) {
		t.Errorf("got %d, want 42000000000", free)
	}
}

func TestDecodeAccountInfoFreeBalance_ZeroBalance(t *testing.T) {
	data := make([]byte, 32) // all zeros

	free, err := decodeAccountInfoFreeBalance(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !free.IsZero() {
		t.Errorf("got %d, want 0", free)
	}
}

func TestDecodeAccountInfoFreeBalance_TooShort(t *testing.T) {
	data := make([]byte, 20) // 16 + 4, not enough for u128 (need 32)

	_, err := decodeAccountInfoFreeBalance(data)
	if err == nil {
		t.Error("expected error for data too short")
	}
}

func TestDecodeAccountInfoFreeBalance_U128(t *testing.T) {
	// AccountInfo with a u128 free balance where the high 8 bytes are non-zero.
	// value = (1 << 64) + 5_000_000_000 = 18446744078709551616 + 5e9
	data := make([]byte, 32)
	binary.LittleEndian.PutUint64(data[16:24], 5_000_000_000) // low
	binary.LittleEndian.PutUint64(data[24:32], 1)             // high

	free, err := decodeAccountInfoFreeBalance(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expected: (1 << 64) + 5_000_000_000
	expected := cosmos.NewUintFromString("18446744078709551616")
	if !free.Equal(expected) {
		t.Errorf("got %s, want %s", free.String(), expected.String())
	}
}

// ---------------------------------------------------------------------------
// systemAccountStorageKey
// ---------------------------------------------------------------------------

func TestSystemAccountStorageKey_Alice(t *testing.T) {
	// Alice's account ID
	accountID, _ := hex.DecodeString("d43593c715fdd31c61141abd04a99fd6822c8558854ccde39a5684e7a56da27d")

	key := systemAccountStorageKey(accountID)

	// Key must start with 0x + twox128("System") + twox128("Account")
	expectedPrefix := "0x26aa394eea5630e07c48ae0c9558cef7b99d880ec681799c0cf30e8886371da9"
	if len(key) < len(expectedPrefix) {
		t.Fatalf("key too short: %s", key)
	}
	if key[:len(expectedPrefix)] != expectedPrefix {
		t.Errorf("key prefix mismatch: got %s..., want %s...", key[:len(expectedPrefix)], expectedPrefix)
	}

	// Key must end with the account ID (blake2_128_concat = blake2b_128(id) ++ id)
	expectedSuffix := hex.EncodeToString(accountID)
	if key[len(key)-64:] != expectedSuffix {
		t.Errorf("key suffix should end with account ID: got ...%s, want ...%s", key[len(key)-64:], expectedSuffix)
	}

	// Total key length: "0x" + 32 (system) + 32 (account) + 32 (blake2_128) + 64 (account_id) = 162
	if len(key) != 162 {
		t.Errorf("key length: got %d, want 162", len(key))
	}
}

func TestSystemAccountStorageKey_Deterministic(t *testing.T) {
	accountID := make([]byte, 32)
	key1 := systemAccountStorageKey(accountID)
	key2 := systemAccountStorageKey(accountID)
	if key1 != key2 {
		t.Error("storage key should be deterministic")
	}
}

// ---------------------------------------------------------------------------
// jsonRPCError formatting
// ---------------------------------------------------------------------------

func TestJSONRPCError_WithData(t *testing.T) {
	// Substrate returns error details in the "data" field
	raw := `{"code":1010,"message":"Invalid Transaction","data":"Stale"}`
	var rpcErr jsonRPCError
	if err := json.Unmarshal([]byte(raw), &rpcErr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	errMsg := rpcErr.Error()
	if !strings.Contains(errMsg, "Stale") {
		t.Errorf("error should contain 'Stale', got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "Invalid Transaction") {
		t.Errorf("error should contain 'Invalid Transaction', got: %s", errMsg)
	}
}

func TestJSONRPCError_WithoutData(t *testing.T) {
	raw := `{"code":-32601,"message":"method not found"}`
	var rpcErr jsonRPCError
	if err := json.Unmarshal([]byte(raw), &rpcErr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	errMsg := rpcErr.Error()
	if strings.Contains(errMsg, "null") {
		t.Errorf("error without data should not contain 'null', got: %s", errMsg)
	}
	if !strings.Contains(errMsg, "method not found") {
		t.Errorf("error should contain 'method not found', got: %s", errMsg)
	}
}

func TestJSONRPCError_AlreadyImported(t *testing.T) {
	// Substrate returns "Already imported" in the data field for pool errors
	raw := `{"code":1013,"message":"Already Imported","data":"AlreadyImported"}`
	var rpcErr jsonRPCError
	if err := json.Unmarshal([]byte(raw), &rpcErr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	errMsg := rpcErr.Error()
	if !strings.Contains(errMsg, "AlreadyImported") {
		t.Errorf("error should contain 'AlreadyImported', got: %s", errMsg)
	}
}

// ---------------------------------------------------------------------------
// SubmitExtrinsic error propagation
// ---------------------------------------------------------------------------

func TestSubmitExtrinsic_SubstrateErrorDataPropagated(t *testing.T) {
	// Simulate Substrate returning a pool error with "Stale" in the data field
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":1010,"message":"Invalid Transaction","data":"Stale"}}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, 5*time.Second)
	_, err := client.SubmitExtrinsic("0xdeadbeef")
	if err == nil {
		t.Fatal("expected error from SubmitExtrinsic")
	}

	// The error must contain "Stale" for BroadcastTx to classify it correctly
	if !strings.Contains(err.Error(), "Stale") {
		t.Errorf("error should propagate 'Stale' from data field, got: %s", err.Error())
	}
}

func TestSubmitExtrinsic_AlreadyImportedPropagated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":1013,"message":"Already Imported","data":"AlreadyImported"}}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, 5*time.Second)
	_, err := client.SubmitExtrinsic("0xdeadbeef")
	if err == nil {
		t.Fatal("expected error from SubmitExtrinsic")
	}

	if !strings.Contains(err.Error(), "AlreadyImported") {
		t.Errorf("error should propagate 'AlreadyImported' from data field, got: %s", err.Error())
	}
}
