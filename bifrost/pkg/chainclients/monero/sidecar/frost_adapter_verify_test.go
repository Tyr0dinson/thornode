package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/config"
)

// TestFrostAdapterImplementsVerificationInterfaces proves the PRODUCTION adapter
// type (not a test double) satisfies the go-tss verification interfaces, so
// below-quorum FROST adoption can type-assert them in prod, and that the two
// methods translate the real sidecar endpoints end to end.
func TestFrostAdapterImplementsVerificationInterfaces(t *testing.T) {
	t.Parallel()

	// Compile-time: the production adapter is both verification clients.
	var (
		_ tssp.FrostTxExistsClient     = (*FrostAdapter)(nil)
		_ tssp.FrostVaultOutputsClient = (*FrostAdapter)(nil)
	)

	txHash := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/v1/monero/chain/tx/" + txHash + "/exists":
			_ = json.NewEncoder(w).Encode(TxExistsResp{Exists: true})
		case "/v1/monero/outputs/vault-pk":
			_ = json.NewEncoder(w).Encode(Outputs{Outputs: []Output{
				{TxHash: txHash, OutputIndex: 2, Amount: 7, KeyImage: "ki", State: "reserved"},
				{TxHash: txHash, OutputIndex: 5, Amount: 9, Spent: true}, // no explicit state → derived
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	adapter := NewFrostAdapter(client)

	exists, err := adapter.FrostTxExists(context.Background(), txHash)
	if err != nil {
		t.Fatalf("FrostTxExists: %v", err)
	}
	if !exists {
		t.Fatalf("FrostTxExists = false, want true")
	}

	outs, err := adapter.FrostVaultOutputs(context.Background(), "vault-pk")
	if err != nil {
		t.Fatalf("FrostVaultOutputs: %v", err)
	}
	if len(outs) != 2 {
		t.Fatalf("FrostVaultOutputs len = %d, want 2", len(outs))
	}
	if outs[0].OutputIndex != 2 || outs[0].Amount != 7 || outs[0].KeyImage != "ki" || outs[0].State != "reserved" {
		t.Fatalf("output[0] mismapped: %+v", outs[0])
	}
	// Explicit sidecar state is preserved; a missing state derives from Spent.
	if outs[1].State != "spent" {
		t.Fatalf("output[1] state = %q, want spent (derived from Spent flag)", outs[1].State)
	}
}

// TestFrostAdapterVaultOutputsUnspentDerived checks the unspent fallback when
// the sidecar sets neither an explicit state nor the spent flag.
func TestFrostAdapterVaultOutputsUnspentDerived(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == "/v1/monero/outputs/v" {
			_ = json.NewEncoder(w).Encode(Outputs{Outputs: []Output{{TxHash: strings.Repeat("b", 64), OutputIndex: 1, Amount: 3}}})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client, err := NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	outs, err := NewFrostAdapter(client).FrostVaultOutputs(context.Background(), "v")
	if err != nil {
		t.Fatalf("FrostVaultOutputs: %v", err)
	}
	if len(outs) != 1 || outs[0].State != "unspent" {
		t.Fatalf("want one unspent output, got %+v", outs)
	}
}
