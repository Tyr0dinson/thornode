package suites

import (
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/features"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// BondYield
////////////////////////////////////////////////////////////////////////////////////////

func BondYield(rng *rand.Rand) *Actor {
	state := features.NewBondYieldState()
	a := NewActor("BondYield", rng)

	a.Append(features.NewBondYieldSetupActor(state, rng))
	a.Append(core.NewChurnActor(rng))
	a.Append(features.NewBondYieldPrimeRewardsActor(state, rng))
	a.Append(features.NewBondYieldSelectTargetActor(state, rng))
	a.Append(features.NewBondYieldManualActiveActor(state, rng))
	a.Append(features.NewBondYieldTargetAutoClaimActor(state, rng))
	a.Append(features.NewBondYieldChurnOutActor(state, rng))
	a.Append(features.NewBondYieldManualChurnedOutActor(state, rng))
	a.Append(features.NewBondYieldThresholdChurnInActor(state, rng))

	return a
}
