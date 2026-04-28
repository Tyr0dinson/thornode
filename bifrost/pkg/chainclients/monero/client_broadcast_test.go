package monero

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

func newBroadcastTestClient(t *testing.T, handler http.HandlerFunc) (*Client, common.PubKey) {
	t.Helper()

	signer, _ := newTestFrostSignerHTTPServer(t, handler)
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	return newTestXMRClientWithFrostSigner(t, signer), vaultPubKey
}

func requireBroadcastRequestIdentity(t *testing.T, gotReq xmrFrostBroadcastRequest, payload signedTxPayload, vaultPubKey common.PubKey) {
	t.Helper()

	if strings.TrimSpace(gotReq.MsgID) == "" {
		t.Fatalf("expected broadcast request to include msg_id")
	}
	if gotReq.MsgID != payload.MsgID {
		t.Fatalf("expected msg_id %q, got %q", payload.MsgID, gotReq.MsgID)
	}
	if strings.TrimSpace(gotReq.VaultPubKey) == "" {
		t.Fatalf("expected broadcast request to include vault_pub_key")
	}
	if gotReq.VaultPubKey != vaultPubKey.String() {
		t.Fatalf("expected vault_pub_key %q, got %q", vaultPubKey, gotReq.VaultPubKey)
	}
}

func TestBroadcastTxPreservesSignedHashWhenSidecarHashEmpty(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		gotReq xmrFrostBroadcastRequest
	)
	client, vaultPubKey := newBroadcastTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/broadcast" {
			http.NotFound(w, r)
			return
		}
		var req xmrFrostBroadcastRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotReq = req
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(xmrFrostBroadcastResponse{TxHash: ""})
	}))
	tx := newTestXMRTxOutItem(vaultPubKey)
	payload := signedTxPayload{
		TxHash:     "signed-hash",
		TxMetadata: "deadbeef",
		MsgID:      "broadcast-msg-id-1",
	}

	txHash, err := client.broadcastTx(tx, payload)
	if err != nil {
		t.Fatalf("broadcast tx: %v", err)
	}
	if txHash != payload.TxHash {
		t.Fatalf("expected signed tx hash %q, got %q", payload.TxHash, txHash)
	}
	mu.Lock()
	defer mu.Unlock()
	requireBroadcastRequestIdentity(t, gotReq, payload, vaultPubKey)
}

func TestBroadcastTxUsesSidecarHashWhenProvided(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		gotReq xmrFrostBroadcastRequest
	)
	client, vaultPubKey := newBroadcastTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/broadcast" {
			http.NotFound(w, r)
			return
		}
		var req xmrFrostBroadcastRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotReq = req
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(xmrFrostBroadcastResponse{TxHash: "daemon-hash"})
	}))
	tx := newTestXMRTxOutItem(vaultPubKey)
	payload := signedTxPayload{
		TxHash:     "signed-hash",
		TxMetadata: "deadbeef",
		MsgID:      "broadcast-msg-id-2",
	}

	txHash, err := client.broadcastTx(tx, payload)
	if err != nil {
		t.Fatalf("broadcast tx: %v", err)
	}
	if txHash != "daemon-hash" {
		t.Fatalf("expected daemon tx hash override, got %q", txHash)
	}
	mu.Lock()
	defer mu.Unlock()
	requireBroadcastRequestIdentity(t, gotReq, payload, vaultPubKey)
}

func TestBroadcastTxRequiresMsgID(t *testing.T) {
	t.Parallel()

	var sidecarCalled atomic.Bool
	client, vaultPubKey := newBroadcastTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sidecarCalled.Store(true)
		_ = json.NewEncoder(w).Encode(xmrFrostBroadcastResponse{TxHash: "daemon-hash"})
	}))
	tx := newTestXMRTxOutItem(vaultPubKey)
	payload := signedTxPayload{
		TxHash:     "signed-hash",
		TxMetadata: "deadbeef",
		MsgID:      "   ",
	}

	_, err := client.broadcastTx(tx, payload)
	if err == nil {
		t.Fatalf("expected broadcast tx to fail when msg_id is missing")
	}
	if !strings.Contains(err.Error(), "missing msg_id") {
		t.Fatalf("expected missing msg_id error, got %v", err)
	}
	if sidecarCalled.Load() {
		t.Fatalf("expected no sidecar broadcast call when msg_id is missing")
	}
}

func TestBroadcastTxRequiresEddsaVaultPubKey(t *testing.T) {
	t.Parallel()

	var sidecarCalled atomic.Bool
	client, vaultPubKey := newBroadcastTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sidecarCalled.Store(true)
		_ = json.NewEncoder(w).Encode(xmrFrostBroadcastResponse{TxHash: "daemon-hash"})
	}))
	tx := newTestXMRTxOutItem(vaultPubKey)
	tx.VaultPubKeyEddsa = common.EmptyPubKey
	payload := signedTxPayload{
		TxHash:     "signed-hash",
		TxMetadata: "deadbeef",
		MsgID:      "broadcast-msg-id-missing-vault",
	}

	_, err := client.broadcastTx(tx, payload)
	if err == nil {
		t.Fatalf("expected broadcast tx to fail when EDDSA vault pubkey is missing")
	}
	if !strings.Contains(err.Error(), "missing EDDSA vault_pub_key") {
		t.Fatalf("expected missing EDDSA vault_pub_key error, got %v", err)
	}
	if sidecarCalled.Load() {
		t.Fatalf("expected no sidecar broadcast call when EDDSA vault pubkey is missing")
	}
}

func TestBroadcastTxIgnoresBroadcastedFlagAndStillCallsSidecar(t *testing.T) {
	t.Parallel()

	var (
		sidecarCalls int32
		mu           sync.Mutex
		gotReq       xmrFrostBroadcastRequest
	)
	client, vaultPubKey := newBroadcastTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&sidecarCalls, 1)
		if r.URL.Path != "/v1/monero/broadcast" {
			http.NotFound(w, r)
			return
		}
		var req xmrFrostBroadcastRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotReq = req
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(xmrFrostBroadcastResponse{TxHash: "daemon-hash"})
	}))
	tx := newTestXMRTxOutItem(vaultPubKey)
	payload := signedTxPayload{
		TxHash:      "signed-hash",
		TxMetadata:  "deadbeef",
		MsgID:       "broadcast-msg-id-3",
		Broadcasted: true,
	}

	txHash, err := client.broadcastTx(tx, payload)
	if err != nil {
		t.Fatalf("broadcast tx: %v", err)
	}
	if txHash != "daemon-hash" {
		t.Fatalf("expected daemon tx hash override, got %q", txHash)
	}
	if got := atomic.LoadInt32(&sidecarCalls); got != 1 {
		t.Fatalf("expected exactly one sidecar broadcast call, got %d", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotReq.MsgID != payload.MsgID {
		t.Fatalf("expected msg_id %q, got %q", payload.MsgID, gotReq.MsgID)
	}
	if gotReq.TxHex != payload.TxMetadata {
		t.Fatalf("expected tx hex %q, got %q", payload.TxMetadata, gotReq.TxHex)
	}
}

func TestBroadcastTxBroadcastedFlagStillRequiresTxMetadata(t *testing.T) {
	t.Parallel()

	var sidecarCalled atomic.Bool
	client, vaultPubKey := newBroadcastTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sidecarCalled.Store(true)
		_ = json.NewEncoder(w).Encode(xmrFrostBroadcastResponse{TxHash: "daemon-hash"})
	}))
	tx := newTestXMRTxOutItem(vaultPubKey)
	payload := signedTxPayload{
		TxHash:      "signed-hash",
		TxMetadata:  "   ",
		MsgID:       "broadcast-msg-id-4",
		Broadcasted: true,
	}

	_, err := client.broadcastTx(tx, payload)
	if err == nil {
		t.Fatalf("expected broadcast tx to fail when tx_metadata is missing")
	}
	if !strings.Contains(err.Error(), "missing tx_hex") {
		t.Fatalf("expected missing tx_hex error, got %v", err)
	}
	if sidecarCalled.Load() {
		t.Fatalf("expected no sidecar broadcast call when tx_metadata is missing")
	}
}
