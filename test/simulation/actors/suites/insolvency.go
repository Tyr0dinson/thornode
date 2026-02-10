package suites

import (
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/common"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Insolvency
////////////////////////////////////////////////////////////////////////////////////////

func Insolvency(rng *rand.Rand) *Actor {
	a := NewActor("Insolvency", rng)
	a.Ops = append(a.Ops, func(config *OpConfig) OpResult {
		config.SetSolvencyHaltLogOnly(true)
		a.Log().Info().Msg("set solvency halt watcher to log-only mode")
		return OpResult{Continue: true}
	})

	// Set price/config mimirs before running insolvency checks.
	// Values are in 1e8 notation:
	// - DollarsPerRune = 0.5
	// - PermittedSolvencyGapUSD = 10
	a.Children[core.NewMimirActor("DollarsPerRune", 50_000_000, rng)] = true
	a.Append(core.NewMimirActor("PermittedSolvencyGapUSD", 1_000_000_000, rng))

	tests := NewActor("Insolvency-Tests", rng)

	// check every chain
	for _, chain := range acommon.SimChains {
		// skip chains where we can't "steal"
		if !chain.Equals(common.BTCChain) {
			continue
		}
		tests.Children[core.NewInsolvencyActor(chain, rng)] = true
	}
	a.Append(tests)

	restoreWatcherMode := NewActor("Insolvency-Watcher-Reset", rng)
	restoreWatcherMode.Ops = append(restoreWatcherMode.Ops, func(config *OpConfig) OpResult {
		config.SetSolvencyHaltLogOnly(false)
		restoreWatcherMode.Log().Info().Msg("set solvency halt watcher to fatal mode")
		return OpResult{Continue: true}
	})
	a.Append(restoreWatcherMode)

	return a
}
