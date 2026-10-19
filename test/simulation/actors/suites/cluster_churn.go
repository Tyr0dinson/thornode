package suites

import (
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/features"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// ClusterChurn
////////////////////////////////////////////////////////////////////////////////////////

// ClusterChurn activates the cluster, checks Bond Yield, and replaces a validator.
func ClusterChurn(rng *rand.Rand) *Actor {
	return clusterChurn(rng, false)
}

// ClusterChurnWithXMRPostChurn checks the same vault replacement requested by the
// Bond Yield scenario instead of waiting for a later independent rotation.
func ClusterChurnWithXMRPostChurn(rng *rand.Rand) *Actor {
	return ClusterChurnWithXMRPostChurnActors(rng)
}

// ClusterChurnWithPostChurnActors runs checks with the full active validator set
// before Bond Yield selects a target and requests its leave.
func ClusterChurnWithPostChurnActors(rng *rand.Rand, actors ...*Actor) *Actor {
	return clusterChurn(rng, false, actors...)
}

// ClusterChurnWithXMRPostChurnActors also checks the same vault replacement
// requested by Bond Yield after the additional activation checks.
func ClusterChurnWithXMRPostChurnActors(rng *rand.Rand, actors ...*Actor) *Actor {
	return clusterChurn(rng, XMRPostChurnEnabled(), actors...)
}

func clusterChurn(rng *rand.Rand, verifyXMR bool, actors ...*Actor) *Actor {
	state := features.NewBondYieldState()
	a := NewActor("ClusterChurn", rng)

	a.Append(features.NewBondYieldSetupActor(state, rng))
	a.Append(core.NewChurnActorWithSkip(rng, state.SkipReason))
	for _, actor := range actors {
		a.Append(actor)
	}
	a.Append(features.NewBondYieldSelectTargetActor(state, rng))
	autoClaim := features.NewBondYieldTargetAutoClaimActor(state, rng)
	a.Append(autoClaim)
	// SPL tokens must follow the Solana vault rotation of the churn-out. The SPL
	// snapshot runs beside the auto-claim setup, so the next step (the XMR capture or
	// the leave) still waits on both directly.
	splCapture, splVerify := core.NewSPLChurnChecks(rng, state.SkipReason)
	alongside(autoClaim, splCapture)
	var verify *Actor
	if verifyXMR {
		capture, after := core.NewXMRChurnOutChecks(rng)
		a.Append(capture)
		verify = after
	}
	a.Append(features.NewBondYieldChurnOutActor(state, rng))
	cleanup := features.NewBondYieldCleanupActor(rng)
	a.Append(cleanup)
	// The SPL migration check waits for the rotation itself, so it runs beside the
	// cleanup and finishes before the cleanup's last Mimir restore: the next stage
	// still waits on that restore alone.
	alongside(cleanup, splVerify)
	cleanup.WalkDepthFirst(func(actor *Actor) bool {
		if len(actor.Children) == 0 {
			splVerify.Children[actor] = true
		}
		return true
	})
	if verify != nil {
		a.Append(verify)
	}

	return a
}

// alongside schedules b beside sibling, after the same parents. Append must already
// have placed sibling.
func alongside(sibling, b *Actor) {
	for parent := range sibling.Parents() {
		parent.Children[b] = true
	}
}
