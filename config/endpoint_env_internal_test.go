package config

import "testing"

// TestApplyEndpointEnvOverridesTo exercises the explicit environment-variable handling for
// chain endpoint lists, which viper cannot bind to slice elements on its own.
func TestApplyEndpointEnvOverridesTo(t *testing.T) {
	btc := &BifrostChainConfiguration{}
	gaia := &BifrostChainConfiguration{
		// pre-existing yaml endpoint that env should be able to override in place
		Endpoints: []ChainEndpointConfig{{Name: "yaml", CosmosGRPCHost: "old:9090"}},
	}
	chains := map[string]*BifrostChainConfiguration{"BTC": btc, "GAIA": gaia}

	environ := []string{
		"BIFROST_CHAINS_BTC_ENDPOINTS_1_URL=http://localhost:8332",
		"BIFROST_CHAINS_BTC_ENDPOINTS_1_USERNAME=user",
		"BIFROST_CHAINS_BTC_ENDPOINTS_1_PASSWORD=pass",
		"BIFROST_CHAINS_BTC_ENDPOINTS_2_RPC_HOST=https://rpc.ankr.com/btc",
		"BIFROST_CHAINS_BTC_ENDPOINTS_2_PRIORITY=5",
		"BIFROST_CHAINS_GAIA_ENDPOINTS_1_COSMOS_GRPC_HOST=new:9090",
		"BIFROST_CHAINS_GAIA_ENDPOINTS_1_COSMOS_GRPC_TLS=true",
		// a typo in a TLS flag must fail safe (TLS stays on), never downgrade to plaintext
		"BIFROST_CHAINS_GAIA_ENDPOINTS_2_COSMOS_GRPC_HOST=typo:9090",
		"BIFROST_CHAINS_GAIA_ENDPOINTS_2_COSMOS_GRPC_TLS=yes",
		// unrelated and malformed keys that must be ignored
		"BIFROST_CHAINS_BTC_RPC_HOST=should-be-ignored",
		"BIFROST_CHAINS_ZZZ_ENDPOINTS_1_URL=unknown-chain",
		"NOT_AN_ENDPOINT=x",
	}

	applyEndpointEnvOverridesTo(chains, environ)

	if len(btc.Endpoints) != 2 {
		t.Fatalf("expected 2 BTC endpoints, got %d", len(btc.Endpoints))
	}
	if got := btc.Endpoints[0].RPCHost; got != "http://localhost:8332" {
		t.Errorf("BTC endpoint 1 rpc_host = %q", got)
	}
	if got := btc.Endpoints[0].UserName; got != "user" {
		t.Errorf("BTC endpoint 1 username = %q", got)
	}
	if got := btc.Endpoints[0].Password; got != "pass" {
		t.Errorf("BTC endpoint 1 password = %q", got)
	}
	// endpoint 1 had no explicit priority, so it defaults to its position (1)
	if got := btc.Endpoints[0].Priority; got != 1 {
		t.Errorf("BTC endpoint 1 priority = %d, want 1", got)
	}
	if got := btc.Endpoints[1].RPCHost; got != "https://rpc.ankr.com/btc" {
		t.Errorf("BTC endpoint 2 rpc_host = %q", got)
	}
	// explicit priority must be honored
	if got := btc.Endpoints[1].Priority; got != 5 {
		t.Errorf("BTC endpoint 2 priority = %d, want 5", got)
	}

	if len(gaia.Endpoints) != 2 {
		t.Fatalf("expected 2 GAIA endpoints, got %d", len(gaia.Endpoints))
	}
	if !gaia.Endpoints[1].CosmosGRPCTLS {
		t.Errorf("GAIA endpoint 2 cosmos_grpc_tls = false after an unparsable value, want TLS kept on")
	}
	if got := gaia.Endpoints[0].CosmosGRPCHost; got != "new:9090" {
		t.Errorf("GAIA endpoint 1 cosmos_grpc_host = %q, want override", got)
	}
	if !gaia.Endpoints[0].CosmosGRPCTLS {
		t.Errorf("GAIA endpoint 1 cosmos_grpc_tls = false, want true")
	}
	if got := gaia.Endpoints[0].Name; got != "yaml" {
		t.Errorf("GAIA endpoint 1 name = %q, want preserved yaml value", got)
	}
}

// An endpoint number past the limit (a typo such as _99999999_) must not allocate a
// list that large before validation: the list stops one past the limit, so
// ValidateEndpoints still refuses it with its clear error. Number 0 is ignored.
func TestApplyEndpointEnvOverridesBoundsTheIndex(t *testing.T) {
	btc := &BifrostChainConfiguration{ChainID: "BTC"}
	applyEndpointEnvOverridesTo(map[string]*BifrostChainConfiguration{"BTC": btc}, []string{
		"BIFROST_CHAINS_BTC_ENDPOINTS_1_URL=http://localhost:8332",
		"BIFROST_CHAINS_BTC_ENDPOINTS_99999999_URL=http://typo:8332",
		"BIFROST_CHAINS_BTC_ENDPOINTS_0_URL=http://zero:8332",
	})
	if len(btc.Endpoints) != MaxChainEndpoints+1 {
		t.Fatalf("expected the list to stop at %d entries, got %d", MaxChainEndpoints+1, len(btc.Endpoints))
	}
	for _, e := range btc.Endpoints {
		if e.RPCHost == "http://zero:8332" {
			t.Fatal("endpoint number 0 must be ignored")
		}
	}
	if err := btc.ValidateEndpoints(); err == nil {
		t.Fatal("expected ValidateEndpoints to refuse more endpoints than the limit")
	}
}
