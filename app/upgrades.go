package app

import (
	"fmt"

	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"

	"gitlab.com/thorchain/thornode/app/upgrades"
	"gitlab.com/thorchain/thornode/app/upgrades/standard"
)

// Upgrades is a list of chain upgrades.
var Upgrades = []upgrades.Upgrade{
	standard.NewUpgrade("2.138.0"), // standard upgrade, handler only runs module migrations (if any), and updates active validator versions.
}

// RegisterUpgradeHandlers registers the chain upgrade handlers.
func (app *THORChainApp) RegisterUpgradeHandlers() {
	if len(Upgrades) == 0 {
		// always have a unique upgrade registered for the current version to test in system tests
		Upgrades = append(Upgrades, standard.NewUpgrade(app.Version()))
	}

	keepers := upgrades.AppKeepers{
		ThorchainKeeper: app.thorchainKeeper,
		AccountKeeper:   &app.AccountKeeper,
		ParamsKeeper:    &app.ParamsKeeper,
		Codec:           app.appCodec,
		GetStoreKey:     app.GetKey,
	}

	// register all upgrade handlers
	for _, upgrade := range Upgrades {
		app.UpgradeKeeper.SetUpgradeHandler(
			upgrade.UpgradeName,
			upgrade.CreateUpgradeHandler(
				app.mm,
				app.configurator,
				&keepers,
			),
		)
	}

	upgradeInfo, err := app.UpgradeKeeper.ReadUpgradeInfoFromDisk()
	if err != nil {
		panic(fmt.Sprintf("failed to read upgrade info from disk %s", err))
	}

	if app.UpgradeKeeper.IsSkipHeight(upgradeInfo.Height) {
		return
	}

	// register store loader for current upgrade
	for _, upgrade := range Upgrades {
		if upgradeInfo.Name == upgrade.UpgradeName {
			app.SetStoreLoader(upgradetypes.UpgradeStoreLoader(upgradeInfo.Height, &upgrade.StoreUpgrades)) // nolint:gosec
			break
		}
	}
}
