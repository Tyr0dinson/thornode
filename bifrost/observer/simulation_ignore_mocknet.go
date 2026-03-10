//go:build mocknet
// +build mocknet

package observer

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

const simulationIgnoreMemo = "MOCKNET_SIMULATION_ONLY_SKIP_OBSERVATION"

// shouldIgnoreSimulationTx is a mocknet-only escape hatch for simulation flows
// that intentionally move funds to or from a vault without creating any
// observation on THORChain/MAYANode. The tx is ignored only when the memo is an
// exact match and either side of the transfer is the observed vault address.
func shouldIgnoreSimulationTx(item *types.TxInItem, vaultAddr common.Address) bool {
	if item == nil {
		return false
	}
	if item.Memo != simulationIgnoreMemo {
		return false
	}

	vault := strings.ToLower(vaultAddr.String())
	sender := strings.ToLower(item.Sender)
	to := strings.ToLower(item.To)

	return sender == vault || to == vault
}
