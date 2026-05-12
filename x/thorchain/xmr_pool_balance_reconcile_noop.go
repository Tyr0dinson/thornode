//go:build !chainnet
// +build !chainnet

package thorchain

import "gitlab.com/thorchain/thornode/v3/common/cosmos"

func runChainnetXMRPoolBalanceReconcileOnce(_ cosmos.Context, _ Manager) error {
	return nil
}
