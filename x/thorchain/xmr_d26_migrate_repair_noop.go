//go:build !chainnet
// +build !chainnet

package thorchain

import "gitlab.com/thorchain/thornode/v3/common/cosmos"

func runChainnetXMRD26MigrateRepairOnce(_ cosmos.Context, _ Manager) error {
	return nil
}
