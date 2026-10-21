//go:build !regtest
// +build !regtest

package main

import (
	"context"

	"golang.org/x/sync/errgroup"

	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/server"
	servertypes "github.com/cosmos/cosmos-sdk/server/types"

	"gitlab.com/thorchain/thornode/v3/app/blocktime"
)

// startCmdOptions returns the SDK's default start command behaviour, plus
// binding the in-process CometBFT's consensus config to the BLOCKTIMEMS
// commit-wait hook.
func startCmdOptions(addStartFlags servertypes.ModuleInitFlags) server.StartCmdOptions {
	return server.StartCmdOptions{
		AddFlags: addStartFlags,
		PostSetup: func(svrCtx *server.Context, _ client.Context, _ context.Context, _ *errgroup.Group) error {
			// CometBFT's consensus state holds this same pointer
			blocktime.Attach(svrCtx.Config.Consensus)
			return nil
		},
	}
}
