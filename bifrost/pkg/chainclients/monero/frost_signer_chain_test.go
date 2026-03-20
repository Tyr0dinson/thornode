package monero

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/config"
)

func TestFrostSignerGetChainHeight(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/chain/height" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(xmrFrostChainHeightResponse{Height: 123})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{SignerStoreSecret: testSignerStoreSecret},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	height, err := signer.GetChainHeight()
	if err != nil {
		t.Fatalf("GetChainHeight failed: %v", err)
	}
	if height != 123 {
		t.Fatalf("expected height 123, got %d", height)
	}
}

func TestFrostSignerGetChainBlockMapsConflictToUnavailable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"requested height above tip"}`))
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{SignerStoreSecret: testSignerStoreSecret},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	_, err = signer.GetChainBlock(200)
	if !errors.Is(err, ErrFrostUnavailableBlock) {
		t.Fatalf("expected ErrFrostUnavailableBlock, got %v", err)
	}
}

func TestFrostSignerTxExists(t *testing.T) {
	t.Parallel()

	expectedPath := "/v1/monero/chain/tx/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/exists"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != expectedPath {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(xmrFrostChainTxExistsResponse{Exists: true})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{SignerStoreSecret: testSignerStoreSecret},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	exists, err := signer.TxExists("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("TxExists failed: %v", err)
	}
	if !exists {
		t.Fatal("expected tx to exist")
	}
}

func TestFrostSignerTxExistsHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{SignerStoreSecret: testSignerStoreSecret},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	_, err = signer.TxExists("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err == nil {
		t.Fatal("expected TxExists error")
	}
	if !strings.Contains(err.Error(), "status 503") {
		t.Fatalf("unexpected error: %v", err)
	}
}
