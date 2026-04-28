package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestRunResetsScannerWithoutDrivingSync(t *testing.T) {
	t.Parallel()

	const canonicalVaultPubKey = "cthorpub1zcjdruntestvault"
	var resetCalls atomic.Int32
	var syncCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/thorchain/vault/thorpub1testvault":
			fmt.Fprint(w, `{"pub_key_frost":"`+canonicalVaultPubKey+`","pub_key_eddsa":"","chains":["XMR"],"vault_creation_height_xmr":3652000}`)
		case "/v1/monero/scanner/reset":
			if r.Method != http.MethodPost {
				t.Fatalf("expected POST reset, got %s", r.Method)
			}
			resetCalls.Add(1)
			var req struct {
				VaultPubKey string `json:"vault_pub_key"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode reset request: %v", err)
			}
			if req.VaultPubKey != canonicalVaultPubKey {
				t.Fatalf("unexpected vault pubkey %q", req.VaultPubKey)
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
		apiURL:         server.URL,
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
		if r.URL.Path == "/thorchain/vault/thorpub1testvault" {
			fmt.Fprint(w, `{"pub_key_frost":"cthorpub1zcjddryruntestvault","pub_key_eddsa":"","chains":["XMR"],"vault_creation_height_xmr":3652000}`)
			return
		}
		if r.URL.Path == "/v1/monero/scanner/reset" {
			resetCalls.Add(1)
		}
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	err := run(toolOptions{
		apiURL:         server.URL,
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

func TestDiscoverVaultsCanonicalizesVaultPubKey(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"pub_key_frost":"cthorpub1zcjduepqa7canonical","pub_key_eddsa":"","chains":["BTC","XMR"],"vault_creation_height_xmr":3652752}`)
	}))
	defer server.Close()

	vaults, err := discoverVaults(toolOptions{
		apiURL:      server.URL,
		vaultPubKey: "cthorpub1addwngeneric",
	}, nil)
	if err != nil {
		t.Fatalf("discoverVaults returned error: %v", err)
	}
	if len(vaults) != 1 {
		t.Fatalf("expected 1 vault, got %d", len(vaults))
	}
	if got := vaults[0].PubKey; got != "cthorpub1zcjduepqa7canonical" {
		t.Fatalf("expected canonical pubkey, got %q", got)
	}
	if got := vaults[0].Birthday; got != 3652752 {
		t.Fatalf("expected birthday 3652752, got %d", got)
	}
}

func TestDiscoverVaultsKeepsHeightOverrideWhileCanonicalizing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"pub_key_frost":"cthorpub1zcjduepqdgcanonical","pub_key_eddsa":"","chains":["XMR"],"vault_creation_height_xmr":3652752}`)
	}))
	defer server.Close()

	vaults, err := discoverVaults(toolOptions{
		apiURL:      server.URL,
		vaultPubKey: "cthorpub1addwngeneric",
		height:      3653000,
	}, nil)
	if err != nil {
		t.Fatalf("discoverVaults returned error: %v", err)
	}
	if len(vaults) != 1 {
		t.Fatalf("expected 1 vault, got %d", len(vaults))
	}
	if got := vaults[0].PubKey; got != "cthorpub1zcjduepqdgcanonical" {
		t.Fatalf("expected canonical pubkey, got %q", got)
	}
	if got := vaults[0].Birthday; got != 3653000 {
		t.Fatalf("expected override birthday 3653000, got %d", got)
	}
}

func TestDiscoverVaultsAllowsCanonicalOverrideWithoutAPI(t *testing.T) {
	t.Parallel()

	vaults, err := discoverVaults(toolOptions{
		apiURL:      "http://127.0.0.1:1",
		vaultPubKey: "cthorpub1zcjduepqcanonical",
		height:      3653000,
	}, nil)
	if err != nil {
		t.Fatalf("discoverVaults returned error: %v", err)
	}
	if len(vaults) != 1 {
		t.Fatalf("expected 1 vault, got %d", len(vaults))
	}
	if got := vaults[0].PubKey; got != "cthorpub1zcjduepqcanonical" {
		t.Fatalf("expected canonical pubkey, got %q", got)
	}
}

func TestDiscoverVaultsRejectsGenericOverrideWithoutAPI(t *testing.T) {
	t.Parallel()

	_, err := discoverVaults(toolOptions{
		apiURL:      "http://127.0.0.1:1",
		vaultPubKey: "cthorpub1addwngeneric",
		height:      3653000,
	}, nil)
	if err == nil {
		t.Fatal("expected error for generic pubkey without API canonicalization")
	}
}

// Non-signing nodes still need to rescan vaults they aren't FROST members of,
// because they must validate key-image attestations from the signing subset
// before co-attesting to the finalized XMR observation. If --all-vaults filters
// these out, consensus on the finalized=true observation never reaches
// supermajority and the derived key-image never lands in vault metadata.
func TestDiscoverVaultsAllVaultsIncludesNonMemberVaults(t *testing.T) {
	t.Parallel()

	const memberVault = "cthorpub1zcjduepqmembervault"
	const nonMemberVault = "cthorpub1zcjduepqnonmembervault"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/keys":
			fmt.Fprintf(w, `{"vaults":[`+
				`{"vault_pub_key":"%s","has_frost_keys":true},`+
				`{"vault_pub_key":"%s","has_frost_keys":false}`+
				`]}`, memberVault, nonMemberVault)
		case "/thorchain/vault/" + memberVault:
			fmt.Fprintf(w, `{"pub_key_frost":"%s","pub_key_eddsa":"","chains":["XMR"],"vault_creation_height_xmr":3652000}`, memberVault)
		case "/thorchain/vault/" + nonMemberVault:
			fmt.Fprintf(w, `{"pub_key_frost":"%s","pub_key_eddsa":"","chains":["XMR"],"vault_creation_height_xmr":3653000}`, nonMemberVault)
		default:
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := monero.NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("NewFrostSignerWithEndpoint: %v", err)
	}

	vaults, err := discoverVaults(toolOptions{apiURL: server.URL}, signer)
	if err != nil {
		t.Fatalf("discoverVaults returned error: %v", err)
	}
	if len(vaults) != 2 {
		t.Fatalf("expected both member and non-member vaults, got %d: %+v", len(vaults), vaults)
	}
	pubKeys := map[string]bool{}
	for _, v := range vaults {
		pubKeys[v.PubKey] = true
	}
	if !pubKeys[memberVault] {
		t.Fatalf("expected member vault %s in result", memberVault)
	}
	if !pubKeys[nonMemberVault] {
		t.Fatalf("expected non-member vault %s in result (has_frost_keys=false must not be filtered)", nonMemberVault)
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
