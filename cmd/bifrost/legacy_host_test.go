package main

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// A chain whose client takes only rpc_host (no endpoints list) is refused at startup
// without one, as on develop; chains with endpoint support are checked by
// ValidateEndpoints instead.
func TestRequireLegacyRPCHost(t *testing.T) {
	if err := requireLegacyRPCHost(config.BifrostChainConfiguration{ChainID: common.XMRChain}); err == nil {
		t.Error("XMR without rpc_host: want an error")
	}
	if err := requireLegacyRPCHost(config.BifrostChainConfiguration{ChainID: common.XMRChain, RPCHost: "monerod:18081"}); err != nil {
		t.Errorf("XMR with rpc_host: %v", err)
	}
	if err := requireLegacyRPCHost(config.BifrostChainConfiguration{ChainID: common.BTCChain}); err != nil {
		t.Errorf("BTC is checked by ValidateEndpoints, not here: %v", err)
	}
}
