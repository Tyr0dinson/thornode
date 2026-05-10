package suites

import (
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// AltNativeFees
////////////////////////////////////////////////////////////////////////////////////////

func AltNativeFees(rng *rand.Rand) *Actor {
	a := NewActor("Alt Native Fees", rng)

	// Use ETH to exercise:
	// - trade asset alt-fee swap
	// - secured asset alt-fee send
	// - secured asset alt-fee swap
	a.Children[core.NewAltNativeFeeActor(common.ETHAsset, core.AltNativeFeeModeTrade, rng)] = true
	a.Children[core.NewAltNativeFeeActor(common.ETHAsset, core.AltNativeFeeModeSecured, rng)] = true

	return a
}
