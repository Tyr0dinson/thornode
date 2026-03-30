package solana

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestGetAccountUsesFinalizedCommitment(t *testing.T) {
	var commitment string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Method != "getBalance" {
			t.Fatalf("unexpected method %s", req.Method)
		}

		var opts map[string]any
		if err := json.Unmarshal(req.Params[1], &opts); err != nil {
			t.Fatalf("failed to decode balance params: %v", err)
		}
		commitment, _ = opts["commitment"].(string)

		if err := json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]any{
				"value": "1000000000",
			},
		}); err != nil {
			t.Fatalf("failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := &Client{
		chain:   common.SOLChain,
		rpc:     rpc.NewSolRPC(server.URL, time.Second),
		address: common.Address("sol-test-address"),
	}

	account, err := client.GetAccount(nil)
	if err != nil {
		t.Fatalf("expected balance lookup to succeed, got %v", err)
	}
	if commitment != solanaAccountCommitment {
		t.Fatalf("expected balance lookup commitment %q, got %q", solanaAccountCommitment, commitment)
	}

	got := account.Coins.GetCoin(common.SOLAsset).Amount.Uint64()
	if got != 100000000 {
		t.Fatalf("expected converted SOL balance 100000000, got %d", got)
	}
}

func TestBroadcastTxUsesConfirmedCommitment(t *testing.T) {
	prevAttempts := solanaBroadcastConfirmationAttempts
	prevInterval := solanaBroadcastConfirmationPollInterval
	prevRebroadcastEvery := solanaBroadcastRebroadcastEvery
	solanaBroadcastConfirmationAttempts = 2
	solanaBroadcastConfirmationPollInterval = 0
	solanaBroadcastRebroadcastEvery = 0
	defer func() {
		solanaBroadcastConfirmationAttempts = prevAttempts
		solanaBroadcastConfirmationPollInterval = prevInterval
		solanaBroadcastRebroadcastEvery = prevRebroadcastEvery
	}()

	var getTransactionCommitment string
	getTransactionCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch req.Method {
		case "sendTransaction":
			if err := json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  "sol-sig",
			}); err != nil {
				t.Fatalf("failed to encode sendTransaction response: %v", err)
			}
		case "getTransaction":
			getTransactionCalls++

			var opts map[string]any
			if err := json.Unmarshal(req.Params[1], &opts); err != nil {
				t.Fatalf("failed to decode getTransaction params: %v", err)
			}
			getTransactionCommitment, _ = opts["commitment"].(string)

			result := any(nil)
			if getTransactionCalls >= 2 {
				result = map[string]any{
					"slot": 1,
					"meta": map[string]any{
						"err": nil,
					},
					"transaction": map[string]any{
						"message": map[string]any{
							"accountKeys":         []any{},
							"addressTableLookups": []any{},
							"header": map[string]any{
								"numReadonlySignedAccounts":   0,
								"numReadonlyUnsignedAccounts": 0,
								"numRequiredSignatures":       1,
							},
							"instructions":    []any{},
							"recentBlockhash": "bh",
						},
						"signatures": []any{"sol-sig"},
					},
				}
			}

			if err := json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  result,
			}); err != nil {
				t.Fatalf("failed to encode getTransaction response: %v", err)
			}
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()

	client := &Client{
		rpc: rpc.NewSolRPC(server.URL, time.Second),
	}

	txid, err := client.BroadcastTx([]byte("signed"))
	if err != nil {
		t.Fatalf("expected broadcast to succeed, got %v", err)
	}
	if txid != "sol-sig" {
		t.Fatalf("expected txid sol-sig, got %s", txid)
	}
	if getTransactionCommitment != solanaBroadcastConfirmationCommitment {
		t.Fatalf("expected getTransaction commitment %q, got %q", solanaBroadcastConfirmationCommitment, getTransactionCommitment)
	}
}

func TestBroadcastTxReturnsErrorWhenTransactionNeverConfirms(t *testing.T) {
	prevAttempts := solanaBroadcastConfirmationAttempts
	prevInterval := solanaBroadcastConfirmationPollInterval
	prevRebroadcastEvery := solanaBroadcastRebroadcastEvery
	solanaBroadcastConfirmationAttempts = 2
	solanaBroadcastConfirmationPollInterval = 0
	solanaBroadcastRebroadcastEvery = 0
	defer func() {
		solanaBroadcastConfirmationAttempts = prevAttempts
		solanaBroadcastConfirmationPollInterval = prevInterval
		solanaBroadcastRebroadcastEvery = prevRebroadcastEvery
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		switch req.Method {
		case "sendTransaction":
			if err := json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  "sol-sig",
			}); err != nil {
				t.Fatalf("failed to encode sendTransaction response: %v", err)
			}
		case "getTransaction":
			if err := json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  nil,
			}); err != nil {
				t.Fatalf("failed to encode getTransaction response: %v", err)
			}
		default:
			t.Fatalf("unexpected method %s", req.Method)
		}
	}))
	defer server.Close()

	client := &Client{
		rpc: rpc.NewSolRPC(server.URL, time.Second),
	}

	_, err := client.BroadcastTx([]byte("signed"))
	if err == nil {
		t.Fatal("expected broadcast to fail when transaction never confirms")
	}
	if !strings.Contains(err.Error(), "not confirmed on solana node") {
		t.Fatalf("expected confirmation failure, got %v", err)
	}
}
