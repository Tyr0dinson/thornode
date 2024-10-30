package standard

import (
	"fmt"

	storetypes "github.com/cosmos/cosmos-sdk/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"

	"gitlab.com/thorchain/thornode/app/upgrades"
	keeperv1 "gitlab.com/thorchain/thornode/x/thorchain/keeper/v1"
)

// This is a standard upgrade handler that runs module migrations (if any), and updates active validator versions
// Copy this directory as a template for upgrade handlers that need to perform additional operations.

// NewUpgrade constructor
func NewUpgrade(semver string) upgrades.Upgrade {
	return upgrades.Upgrade{
		UpgradeName:          semver,
		CreateUpgradeHandler: CreateUpgradeHandler,
		StoreUpgrades: storetypes.StoreUpgrades{
			Added:   []string{},
			Deleted: []string{},
		},
	}
}

func CreateUpgradeHandler(
	mm upgrades.ModuleManager,
	configurator module.Configurator,
	ak *upgrades.AppKeepers,
) upgradetypes.UpgradeHandler {
	return func(ctx sdk.Context, plan upgradetypes.Plan, fromVM module.VersionMap) (module.VersionMap, error) {
		// Perform SDK module migrations
		vm, err := mm.RunMigrations(ctx, configurator, fromVM)
		if err != nil {
			return vm, err
		}

		// Active validator versions need to be updated since consensus
		// on the new version is required to resume the chain.
		// This is a THORChain specific upgrade step that should be
		// done in every upgrade handler.
		if err = keeperv1.UpdateActiveValidatorVersions(ctx, ak.ThorchainKeeper, plan.Name); err != nil {
			return vm, fmt.Errorf("failed to update active validator versions: %w", err)
		}

		return vm, nil
	}
}
