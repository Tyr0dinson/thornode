package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRunResetsScannerWithoutDrivingSync(t *testing.T) {
	t.Parallel()

	var resetCalls atomic.Int32
	var syncCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/scanner/reset":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST reset, got %s", r.Method)
			}
			resetCalls.Add(1)
			var req struct {
				VaultPubKey    string `json:"vault_pub_key"`
				BirthdayHeight int64  `json:"birthday_height"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode reset request: %v", err)
			}
			if req.VaultPubKey != "thorpub1testvault" {
				t.Fatalf("unexpected vault pubkey %q", req.VaultPubKey)
			}
			if req.BirthdayHeight != 3652000 {
				t.Fatalf("unexpected birthday %d", req.BirthdayHeight)
			}
			w.WriteHeader(http.StatusOK)
		case "/v1/monero/scanner/sync":
			syncCalls.Add(1)
			http.Error(w, "unexpected scanner sync call", http.StatusInternalServerError)
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	err := run(toolOptions{
		chainNetwork:   "mocknet",
		signerEndpoint: server.URL,
		signerSeed:     "seed",
		vaultPubKey:    "thorpub1testvault",
		height:         3652000,
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if resetCalls.Load() != 1 {
		t.Fatalf("expected one reset call, got %d", resetCalls.Load())
	}
	if syncCalls.Load() != 0 {
		t.Fatalf("expected no scanner sync calls, got %d", syncCalls.Load())
	}
}

func TestRunDryRunSkipsReset(t *testing.T) {
	t.Parallel()

	var resetCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/monero/scanner/reset" {
			resetCalls.Add(1)
		}
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	err := run(toolOptions{
		chainNetwork:   "mocknet",
		signerEndpoint: server.URL,
		signerSeed:     "seed",
		vaultPubKey:    "thorpub1testvault",
		height:         3652000,
		dryRun:         true,
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if resetCalls.Load() != 0 {
		t.Fatalf("expected no reset calls during dry-run, got %d", resetCalls.Load())
	}
}

func TestHasChain(t *testing.T) {
	t.Parallel()

	if !hasChain([]string{"THOR", "xmr"}, "XMR") {
		t.Fatal("expected XMR to be found case-insensitively")
	}
	if hasChain([]string{"THOR", "BTC"}, "XMR") {
		t.Fatal("did not expect XMR to be found")
	}
}

func TestEffectiveChainNetwork(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		chain      string
		endpoint   string
		allowLocal bool
		want       string
	}{
		{name: "explicit mainnet endpoint stays mainnet", chain: "mainnet", endpoint: "https://signer.example.com:8283", allowLocal: false, want: "mainnet"},
		{name: "docker bridge http gets local override", chain: "mainnet", endpoint: "http://172.18.0.5:8283", allowLocal: true, want: "mocknet"},
		{name: "loopback http does not need override", chain: "mainnet", endpoint: "http://127.0.0.1:8283", allowLocal: true, want: "mainnet"},
		{name: "remote http without explicit allow stays mainnet", chain: "mainnet", endpoint: "http://198.51.100.10:8283", allowLocal: false, want: "mainnet"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveChainNetwork(tc.chain, tc.endpoint, tc.allowLocal); got != tc.want {
				t.Fatalf("effectiveChainNetwork(%q, %q, %t) = %q, want %q", tc.chain, tc.endpoint, tc.allowLocal, got, tc.want)
			}
		})
	}
}
