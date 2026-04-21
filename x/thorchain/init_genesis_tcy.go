//go:build !stagenet
// +build !stagenet

package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// setupInitialTCYBalances is a no-op on non-stagenet networks; TCY is
// minted via the Migrate4to5 upgrade path there.
func setupInitialTCYBalances(_ cosmos.Context, _ keeper.Keeper) error {
	return nil
}
