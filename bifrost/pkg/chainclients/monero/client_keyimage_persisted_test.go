package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitlab.com/thorchain/thornode/v3/config"
)

func TestKeyImageCatchupNeedsReplayUsesPersistedOutputs(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/outputs/thorpub1vault" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("view") != "all" {
			http.Error(w, "missing all view", http.StatusBadRequest)
			return
		}

		keyImage := ""
		if r.URL.Query().Get("source") == "store" {
			keyImage = "known-key-image"
		}

		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
			Outputs: []xmrFrostOutputEntry{{
				TxHash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				OutputIndex: 0,
				KeyImage:    keyImage,
				Spent:       false,
			}},
		})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	client := &Client{frostSigner: signer}
	needsReplay, err := client.keyImageCatchupNeedsReplay("thorpub1vault", []xmrFrostApplyKeyImageRef{{
		TxHash:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		OutputIndex: 0,
		KeyImage:    "known-key-image",
	}})
	if err != nil {
		t.Fatalf("keyImageCatchupNeedsReplay failed: %v", err)
	}
	if needsReplay {
		t.Fatal("expected persisted outputs to satisfy key-image replay verification")
	}
}
