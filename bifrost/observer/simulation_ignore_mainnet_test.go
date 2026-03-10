//go:build !mocknet
// +build !mocknet

package observer

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

const nonMocknetSimulationIgnoreMemo = "MOCKNET_SIMULATION_ONLY_SKIP_OBSERVATION"

func TestShouldIgnoreSimulationTxNonMocknet(t *testing.T) {
	vault := common.Address("vault")
	item := &types.TxInItem{
		Memo:   nonMocknetSimulationIgnoreMemo,
		Sender: "vault",
		To:     "user",
	}

	if shouldIgnoreSimulationTx(item, vault) {
		t.Fatal("shouldIgnoreSimulationTx() returned true in a non-mocknet build")
	}
}
