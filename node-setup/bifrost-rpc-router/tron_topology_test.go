package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TRON's P2 is tron-rpc-proxy, so only P1 carries the chain: REST and JSON-RPC
// switch together between the direct provider and that proxy.
func TestTRONExampleSwitchesNativeAndProxyAtP1Only(t *testing.T) {
	load := func(name string) Config {
		data, err := os.ReadFile(filepath.Join("examples", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var c Config
		if err := strictJSON(data, &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	if _, ok := load("adapter").Chains["tron"]; ok {
		t.Fatal("the generic P2 must not carry TRON")
	}
	chain, ok := load("gateway").Chains["tron"]
	if !ok {
		t.Fatal("TRON must be configured in P1")
	}
	wantBackends := map[string]map[string]string{
		"native": {
			"rest": "/etc/rpc-gateway/targets/tron-rest-native.json",
			"rpc":  "/etc/rpc-gateway/targets/tron-rpc-native.json",
		},
		"adapter": {
			"rest": "/etc/rpc-gateway/targets/tron-rest-adapter.json",
			"rpc":  "/etc/rpc-gateway/targets/tron-rpc-adapter.json",
		},
	}
	if !reflect.DeepEqual(chain.Backends, wantBackends) {
		t.Fatalf("TRON backend ownership mismatch: got %+v, want %+v", chain.Backends, wantBackends)
	}
	wantEndpoints := map[string]Endpoint{
		"rest": {Path: "/tron-rest", Suffix: true},
		"rpc":  {Path: "/tron-rpc"},
	}
	if !reflect.DeepEqual(chain.Endpoints, wantEndpoints) || chain.Default != "adapter" {
		t.Fatal("REST/RPC must remain a complete paired TRON route defaulting to the proxy")
	}
	if chain.Active != 32 || chain.Waiting != 32 {
		t.Fatal("TRON must retain bounded 32-active/32-waiting admission")
	}
}
