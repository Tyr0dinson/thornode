//go:build mocknet
// +build mocknet

package observer

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestShouldIgnoreSimulationTxMocknet(t *testing.T) {
	vault := common.Address("VaUlT")

	cases := []struct {
		name string
		item *types.TxInItem
		want bool
	}{
		{
			name: "nil item",
			item: nil,
			want: false,
		},
		{
			name: "ignore exact memo when sender is vault",
			item: &types.TxInItem{
				Memo:   simulationIgnoreMemo,
				Sender: "vault",
				To:     "user",
			},
			want: true,
		},
		{
			name: "ignore exact memo when recipient is vault",
			item: &types.TxInItem{
				Memo:   simulationIgnoreMemo,
				Sender: "user",
				To:     "vault",
			},
			want: true,
		},
		{
			name: "do not ignore wrong memo",
			item: &types.TxInItem{
				Memo:   "MOCKNET_SIMULATION_ONLY_SKIP_OBSERVATION_EXTRA",
				Sender: "vault",
				To:     "user",
			},
			want: false,
		},
		{
			name: "do not ignore unrelated tx",
			item: &types.TxInItem{
				Memo:   simulationIgnoreMemo,
				Sender: "user-a",
				To:     "user-b",
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldIgnoreSimulationTx(tc.item, vault)
			if got != tc.want {
				t.Fatalf("shouldIgnoreSimulationTx() = %t, want %t", got, tc.want)
			}
		})
	}
}
