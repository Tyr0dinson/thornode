package v137

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
	keeperv1 "gitlab.com/thorchain/thornode/x/thorchain/keeper/v1"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	cfg module.Configurator,
	thorchainKeeper keeper.Keeper,
) upgradetypes.UpgradeHandler {
	return func(ctx sdk.Context, _ upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		// Perform SDK module migrations
		vm, err := mm.RunMigrations(ctx, cfg, vm)
		if err != nil {
			return vm, err
		}

		// Active validator versions need to be updated since consensus
		// on the new version is required to resume the chain.
		// This is a THORChain specific upgrade step that should be
		// done in every upgrade handler.
		keeperv1.UpdateActiveValidatorVersions(ctx, thorchainKeeper, UpgradeName)

		return vm, nil
	}
}
