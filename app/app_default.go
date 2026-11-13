//go:build !regtest
// +build !regtest

package app

import (
	"os"

	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/config"
)

// wrapPrepareProposal returns the handler unchanged; regtest builds park block
// production here (see app_regtest.go).
func wrapPrepareProposal(h sdk.PrepareProposalHandler) sdk.PrepareProposalHandler { return h }

// setPrepareCheckStater installs the app's end-of-Commit hook; regtest builds
// also use it to report committed blocks.
func setPrepareCheckStater(bApp *baseapp.BaseApp, hook sdk.PrepareCheckStater) {
	bApp.SetPrepareCheckStater(hook)
}

// BeginBlocker application updates every begin block
func (app *THORChainApp) BeginBlocker(ctx sdk.Context) (sdk.BeginBlock, error) {
	haltHeight := config.GetThornode().Cosmos.HaltHeight
	if haltHeight > 0 && ctx.BlockHeight() >= haltHeight {
		ctx.Logger().Info("halt height reached", "height", ctx.BlockHeight(), "halt height", haltHeight)
		os.Exit(0)
	}
	return app.ModuleManager.BeginBlock(ctx)
}

// EndBlocker application updates every end block
func (app *THORChainApp) EndBlocker(ctx sdk.Context) (sdk.EndBlock, error) {
	return app.ModuleManager.EndBlock(ctx)
}
