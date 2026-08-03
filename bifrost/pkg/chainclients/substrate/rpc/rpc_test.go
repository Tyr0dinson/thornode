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
	. "gopkg.in/check.v1"
)

func TestRPCSuite(t *testing.T) { TestingT(t) }

type RPCSuite struct{}

var _ = Suite(&RPCSuite{})

// ---------------------------------------------------------------------------
// ss58DecodeAccountID
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestSS58DecodeAccountID_Alice(c *C) {
	// Alice's well-known dev account (prefix 42):
	// 5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY
	// Pubkey: d43593c715fdd31c61141abd04a99fd6822c8558854ccde39a5684e7a56da27d
	accountID, err := ss58DecodeAccountID("5GrwvaEF5zXb26Fz9rcQpDWS57CtERHpNehXCPcNoHGKutQY")
	c.Assert(err, IsNil)

	expected := "d43593c715fdd31c61141abd04a99fd6822c8558854ccde39a5684e7a56da27d"
	c.Check(hex.EncodeToString(accountID), Equals, expected)
}

func (s *RPCSuite) TestSS58DecodeAccountID_Bob(c *C) {
	// Bob's well-known dev account (prefix 42):
	// 5FHneW46xGXgs5mUiveU4sbTyGBzmstUspZC92UhjJM694ty
	// Pubkey: 8eaf04151687736326c9fea17e25fc5287613693c912909cb226aa4794f26a48
	accountID, err := ss58DecodeAccountID("5FHneW46xGXgs5mUiveU4sbTyGBzmstUspZC92UhjJM694ty")
	c.Assert(err, IsNil)

	expected := "8eaf04151687736326c9fea17e25fc5287613693c912909cb226aa4794f26a48"
	c.Check(hex.EncodeToString(accountID), Equals, expected)
}

func (s *RPCSuite) TestSS58DecodeAccountID_InvalidBase58(c *C) {
	_, err := ss58DecodeAccountID("0OIl-not-base58")
	c.Check(err, NotNil, Commentf("expected error for invalid base58"))
}

func (s *RPCSuite) TestSS58DecodeAccountID_WrongLength(c *C) {
	// Valid base58 but decodes to wrong length (not 35 bytes)
	_, err := ss58DecodeAccountID("111111") // decodes to 6 zero bytes
	c.Check(err, NotNil, Commentf("expected error for wrong SS58 length"))
}

// ---------------------------------------------------------------------------
// decodeAccountInfoFreeBalance
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestDecodeAccountInfoFreeBalance(c *C) {
	// Build a minimal AccountInfo: 4 × u32 (16 bytes) + free balance (u128 LE)
	// nonce=1, consumers=0, providers=1, sufficients=0
	// free=1_000_000_000 (1 TAO in rao)
	data := make([]byte, 32)                                  // 16 header + 16 u128
	binary.LittleEndian.PutUint32(data[0:4], 1)               // nonce
	binary.LittleEndian.PutUint32(data[4:8], 0)               // consumers
	binary.LittleEndian.PutUint32(data[8:12], 1)              // providers
	binary.LittleEndian.PutUint32(data[12:16], 0)             // sufficients
	binary.LittleEndian.PutUint64(data[16:24], 1_000_000_000) // free

	for _, balanceSize := range []int{8, 16} {
		free, err := decodeAccountInfoFreeBalance(data, balanceSize)
		c.Assert(err, IsNil)
		c.Check(free.Equal(cosmos.NewUint(1_000_000_000)), Equals, true,
			Commentf("got %s, want 1000000000", free))
	}
}

func (s *RPCSuite) TestDecodeAccountInfoFreeBalance_WithTrailingData(c *C) {
	// Full AccountInfo with reserved, frozen, flags fields after free
	data := make([]byte, 56)                                   // 16 + 8 + 8 + 8 + 16
	binary.LittleEndian.PutUint64(data[16:24], 42_000_000_000) // free = 42 TAO

	free, err := decodeAccountInfoFreeBalance(data, 8)
	c.Assert(err, IsNil)
	c.Check(free.Equal(cosmos.NewUint(42_000_000_000)), Equals, true,
		Commentf("got %s, want 42000000000", free))
}

func (s *RPCSuite) TestDecodeAccountInfoFreeBalance_ZeroBalance(c *C) {
	data := make([]byte, 32) // all zeros

	free, err := decodeAccountInfoFreeBalance(data, 16)
	c.Assert(err, IsNil)
	c.Check(free.IsZero(), Equals, true, Commentf("got %s, want 0", free))
}

func (s *RPCSuite) TestDecodeAccountInfoFreeBalance_TooShort(c *C) {
	data := make([]byte, 20) // 16 + 4, not enough for u128 (need 32)

	_, err := decodeAccountInfoFreeBalance(data, 16)
	c.Check(err, NotNil, Commentf("expected error for data too short"))
}

func (s *RPCSuite) TestDecodeAccountInfoFreeBalance_U128(c *C) {
	// AccountInfo with u128 free balance where the high 8 bytes are non-zero.
	// value = (1 << 64) + 5_000_000_000 = 18446744078709551616 + 5e9
	data := make([]byte, 32)
	binary.LittleEndian.PutUint64(data[16:24], 5_000_000_000) // low
	binary.LittleEndian.PutUint64(data[24:32], 1)             // high

	free, err := decodeAccountInfoFreeBalance(data, 16)
	c.Assert(err, IsNil)

	// Expected: (1 << 64) + 5_000_000_000
	expected := cosmos.NewUintFromString("18446744078709551616")
	c.Check(free.Equal(expected), Equals, true,
		Commentf("got %s, want %s", free.String(), expected.String()))
}

func (s *RPCSuite) TestDecodeAccountInfoFreeBalance_U64WithReserved(c *C) {
	// AccountInfo with u64 free and reserved balance
	// (Balance=u64): header(16) + free(8) + reserved(8) + frozen(8) + flags(16)
	data := make([]byte, 56)
	binary.LittleEndian.PutUint64(data[16:24], 5_000_000_000) // free = 5 TAO
	binary.LittleEndian.PutUint64(data[24:32], 1_000_000_000) // reserved = 1 TAO

	free, err := decodeAccountInfoFreeBalance(data, 8)
	c.Assert(err, IsNil)
	c.Check(free.Equal(cosmos.NewUint(5_000_000_000)), Equals, true,
		Commentf("got %s, want 5000000000 — reserved must not leak into free", free))
}

// ---------------------------------------------------------------------------
// systemAccountStorageKey
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestSystemAccountStorageKey_Alice(c *C) {
	// Alice's account ID
	accountID, _ := hex.DecodeString("d43593c715fdd31c61141abd04a99fd6822c8558854ccde39a5684e7a56da27d")

	key := systemAccountStorageKey(accountID)

	// Key must start with 0x + twox128("System") + twox128("Account")
	expectedPrefix := "0x26aa394eea5630e07c48ae0c9558cef7b99d880ec681799c0cf30e8886371da9"
	c.Assert(len(key) >= len(expectedPrefix), Equals, true, Commentf("key too short: %s", key))
	c.Check(key[:len(expectedPrefix)], Equals, expectedPrefix,
		Commentf("key prefix mismatch"))

	// Key must end with the account ID (blake2_128_concat = blake2b_128(id) ++ id)
	expectedSuffix := hex.EncodeToString(accountID)
	c.Check(key[len(key)-64:], Equals, expectedSuffix,
		Commentf("key suffix should end with account ID"))

	// Total key length: "0x" + 32 (system) + 32 (account) + 32 (blake2_128) + 64 (account_id) = 162
	c.Check(len(key), Equals, 162, Commentf("key length mismatch"))
}

func (s *RPCSuite) TestSystemAccountStorageKey_Deterministic(c *C) {
	accountID := make([]byte, 32)
	key1 := systemAccountStorageKey(accountID)
	key2 := systemAccountStorageKey(accountID)
	c.Check(key1, Equals, key2, Commentf("storage key should be deterministic"))
}

// ---------------------------------------------------------------------------
// jsonRPCError formatting
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestJSONRPCError_WithData(c *C) {
	// Substrate returns error details in the "data" field. A stale nonce is
	// code 1010 "Invalid Transaction" with data "Transaction is outdated"
	// (sp-runtime transaction_validity.rs, InvalidTransaction::Stale).
	raw := `{"code":1010,"message":"Invalid Transaction","data":"Transaction is outdated"}`
	var rpcErr jsonRPCError
	err := json.Unmarshal([]byte(raw), &rpcErr)
	c.Assert(err, IsNil)

	errMsg := rpcErr.Error()
	c.Check(strings.Contains(errMsg, "Transaction is outdated"), Equals, true,
		Commentf("error should contain 'Transaction is outdated', got: %s", errMsg))
	c.Check(strings.Contains(errMsg, "Invalid Transaction"), Equals, true,
		Commentf("error should contain 'Invalid Transaction', got: %s", errMsg))
}

func (s *RPCSuite) TestJSONRPCError_WithoutData(c *C) {
	raw := `{"code":-32601,"message":"method not found"}`
	var rpcErr jsonRPCError
	err := json.Unmarshal([]byte(raw), &rpcErr)
	c.Assert(err, IsNil)

	errMsg := rpcErr.Error()
	c.Check(strings.Contains(errMsg, "null"), Equals, false,
		Commentf("error without data should not contain 'null', got: %s", errMsg))
	c.Check(strings.Contains(errMsg, "method not found"), Equals, true,
		Commentf("error should contain 'method not found', got: %s", errMsg))
}

func (s *RPCSuite) TestJSONRPCError_AlreadyImported(c *C) {
	// Substrate returns code 1013 "Transaction Already Imported" with the tx
	// hash in the data field (sc-rpc-api author/error.rs).
	raw := `{"code":1013,"message":"Transaction Already Imported","data":"0x1234"}`
	var rpcErr jsonRPCError
	err := json.Unmarshal([]byte(raw), &rpcErr)
	c.Assert(err, IsNil)

	errMsg := rpcErr.Error()
	c.Check(strings.Contains(errMsg, "Transaction Already Imported"), Equals, true,
		Commentf("error should contain 'Transaction Already Imported', got: %s", errMsg))
}

// ---------------------------------------------------------------------------
// SubmitExtrinsic error propagation
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestSubmitExtrinsic_SubstrateErrorDataPropagated(c *C) {
	// Simulate Substrate rejecting a stale-nonce tx as a real node does
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":1010,"message":"Invalid Transaction","data":"Transaction is outdated"}}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, 5*time.Second)
	_, err := client.SubmitExtrinsic("0xdeadbeef")
	c.Assert(err, NotNil, Commentf("expected error from SubmitExtrinsic"))

	// BroadcastTx classifies stale nonces via IsStaleNonceErr, which must
	// still match after SubmitExtrinsic wraps the error
	c.Check(IsStaleNonceErr(err), Equals, true,
		Commentf("IsStaleNonceErr should match wrapped pool error, got: %s", err.Error()))
	c.Check(IsAlreadyImportedErr(err), Equals, false,
		Commentf("stale error must not classify as already-imported"))
}

func (s *RPCSuite) TestSubmitExtrinsic_AlreadyImportedPropagated(c *C) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":1013,"message":"Transaction Already Imported","data":"0x1234"}}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, 5*time.Second)
	_, err := client.SubmitExtrinsic("0xdeadbeef")
	c.Assert(err, NotNil, Commentf("expected error from SubmitExtrinsic"))

	c.Check(IsAlreadyImportedErr(err), Equals, true,
		Commentf("IsAlreadyImportedErr should match wrapped pool error, got: %s", err.Error()))
	c.Check(IsStaleNonceErr(err), Equals, false,
		Commentf("already-imported error must not classify as stale"))
}

// ---------------------------------------------------------------------------
// Broadcast error classification predicates
// ---------------------------------------------------------------------------

func (s *RPCSuite) TestIsStaleNonceErr(c *C) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "stale nonce",
			err:  &jsonRPCError{Code: 1010, Message: "Invalid Transaction", Data: json.RawMessage(`"Transaction is outdated"`)},
			want: true,
		},
		{
			name: "stale nonce wrapped",
			err:  fmt.Errorf("failed to submit extrinsic: %w", &jsonRPCError{Code: 1010, Message: "Invalid Transaction", Data: json.RawMessage(`"Transaction is outdated"`)}),
			want: true,
		},
		{
			// Code 1010 covers every InvalidTransaction variant; only the
			// Stale variant ("outdated") may be treated as a burned nonce.
			name: "1010 bad signature is not stale",
			err:  &jsonRPCError{Code: 1010, Message: "Invalid Transaction", Data: json.RawMessage(`"Transaction has a bad signature"`)},
			want: false,
		},
		{
			name: "1010 payment failure is not stale",
			err:  &jsonRPCError{Code: 1010, Message: "Invalid Transaction", Data: json.RawMessage(`"Inability to pay some fees (e.g. account balance too low)"`)},
			want: false,
		},
		{
			name: "already imported is not stale",
			err:  &jsonRPCError{Code: 1013, Message: "Transaction Already Imported", Data: json.RawMessage(`"0x1234"`)},
			want: false,
		},
		{
			name: "plain error is not stale",
			err:  fmt.Errorf("rpc error (code: 1010): Invalid Transaction: Transaction is outdated"),
			want: false,
		},
	}
	for _, tt := range tests {
		c.Check(IsStaleNonceErr(tt.err), Equals, tt.want, Commentf(tt.name))
	}
}

func (s *RPCSuite) TestIsAlreadyImportedErr(c *C) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "already imported",
			err:  &jsonRPCError{Code: 1013, Message: "Transaction Already Imported", Data: json.RawMessage(`"0x1234"`)},
			want: true,
		},
		{
			name: "already imported wrapped",
			err:  fmt.Errorf("failed to submit extrinsic: %w", &jsonRPCError{Code: 1013, Message: "Transaction Already Imported", Data: json.RawMessage(`"0x1234"`)}),
			want: true,
		},
		{
			name: "invalid tx is not already imported",
			err:  &jsonRPCError{Code: 1010, Message: "Invalid Transaction", Data: json.RawMessage(`"Transaction is outdated"`)},
			want: false,
		},
		{
			name: "plain error is not already imported",
			err:  fmt.Errorf("rpc error (code: 1013): Transaction Already Imported"),
			want: false,
		},
	}
	for _, tt := range tests {
		c.Check(IsAlreadyImportedErr(tt.err), Equals, tt.want, Commentf(tt.name))
	}
}
