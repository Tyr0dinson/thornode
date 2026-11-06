package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
	thornodecmd "gitlab.com/thorchain/thornode/v3/cmd"
	"gopkg.in/yaml.v3"
)

func TestFiroMocknetFundingMatchesSimulationKey(t *testing.T) {
	derived, err := hd.Secp256k1.Derive()(mocknetMasterMnemonic, "", thornodecmd.THORChainHDPath)
	if err != nil {
		t.Fatal(err)
	}
	address, err := firoutil.NewExchangeAddress(hd.Secp256k1.Generate()(derived).PubKey().Bytes(), firoutil.RegTest)
	if err != nil {
		t.Fatal(err)
	}
	script := readRepoFile(t, "build/docker/mocknet/firo/entrypoint.py")
	if !strings.Contains(script, `MASTER_ADDRESS = "`+address.String()+`"`) {
		t.Fatalf("FIRO bootstrap must fund the simulation key's exchange address %s", address)
	}
}

func TestFiroMocknetCompose(t *testing.T) {
	var compose struct {
		Services map[string]struct {
			Environment yaml.Node `yaml:"environment"`
			Profiles    []string  `yaml:"profiles"`
			Ports       []string  `yaml:"ports"`
			Volumes     []string  `yaml:"volumes"`
			NetworkMode string    `yaml:"network_mode"`
			Hostname    string    `yaml:"hostname"`
		} `yaml:"services"`
	}
	err := yaml.Unmarshal([]byte(readRepoFile(t, "build/docker/docker-compose.yml")), &compose)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"firo1", "firo2", "firo3", "firo4", "firo5", "firo6"} {
		service, ok := compose.Services[name]
		if !ok {
			t.Fatalf("missing quorum node %s", name)
		}
		for _, profile := range []string{"firo", "mocknet", "mocknet-cluster"} {
			if !slices.Contains(service.Profiles, profile) {
				t.Fatalf("%s must start with the %s profile", name, profile)
			}
		}
		if environment(t, service.Environment)["FIRO_NODE"] != strings.TrimPrefix(name, "firo") {
			t.Fatalf("wrong node identity for %s", name)
		}
		quorumMount := "firo-quorum:/quorum"
		if name != "firo1" {
			quorumMount += ":ro"
		}
		for _, mount := range []string{name + ":/data", quorumMount, "./mocknet/firo:/mocknet:ro"} {
			if !slices.Contains(service.Volumes, mount) {
				t.Fatalf("%s is missing required mount %s", name, mount)
			}
		}
		if name != "firo1" && len(service.Ports) != 0 {
			t.Fatalf("%s must not inherit published RPC port", name)
		}
		if service.Hostname != "" {
			t.Fatalf("%s cannot set a hostname in the shared network namespace", name)
		}
		if name == "firo1" {
			if service.NetworkMode != "" {
				t.Fatal("firo1 must own the shared network namespace")
			}
		} else if service.NetworkMode != "service:firo1" {
			t.Fatalf("%s must share loopback with firo1 for Core masternode identity", name)
		}
	}
	ports := compose.Services["firo1"].Ports
	if len(ports) != 1 || ports[0] != "127.0.0.1:28888:28888" {
		t.Fatal("FIRO mocknet RPC must be loopback-only")
	}
	for _, name := range []string{"bifrost", "bifrost-cat", "bifrost-fox", "bifrost-pig", "bifrost-frog"} {
		env := environment(t, compose.Services[name].Environment)
		if env["BIFROST_CHAINS_FIRO_BLOCK_SCANNER_START_BLOCK_HEIGHT"] != "1" || env["BIFROST_CHAINS_FIRO_DISABLED"] != "${BIFROST_CHAINS_FIRO_DISABLED:-false}" {
			t.Fatalf("%s must inherit enabled FIRO bootstrap configuration", name)
		}
		// InstantSend is disabled by default; Compose must not enable it.
		if enabled, ok := env["BIFROST_CHAINS_FIRO_INSTANT_SEND_ENABLED"]; ok && enabled != "false" {
			t.Fatalf("%s must keep customer deposits ChainLock-only", name)
		}
	}
}

func environment(t *testing.T, node yaml.Node) map[string]string {
	t.Helper()
	var result map[string]string
	err := node.Decode(&result)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
