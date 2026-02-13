package suites

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	acommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
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
	// - PermittedSolvencyGapUSD = 1
	// Keep the permitted gap low so small mocknet vault drifts still trigger solvency halt.
	a.Children[core.NewMimirActor("DollarsPerRune", 50_000_000, rng)] = true
	a.Append(core.NewMimirActor("PermittedSolvencyGapUSD", 100_000_000, rng))

	tests := NewActor("Insolvency-Launcher", rng)

	chains := insolvencySimulationChains()

	// Run on chains where the current InsolvencyActor has a simulation-only steal+restore path.
	// In mocknet, insolvency actors all contend for the same node lock/account, so build a
	// sequential chain of actors instead of spawning them as parallel children.
	//
	// - EVM: steal with empty memo, restore via self-transfer reconcile
	// - UTXO: steal with CONSOLIDATE, restore with unobserved CONSOLIDATE inbound
	// - GAIA/TRON/SOL: steal with empty memo, restore with NOOP:novault
	for _, chain := range chains {
		tests.Append(core.NewInsolvencyActor(chain, rng))
	}
	a.Append(tests)

	restoreWatcherMode := NewActor("Insolvency-Watcher-Reset", rng)
	restoreWatcherMode.Timeout = 5 * time.Minute
	restoreWatcherMode.Ops = append(restoreWatcherMode.Ops, func(config *OpConfig) OpResult {
		active := make([]string, 0)
		for _, chain := range chains {
			key := fmt.Sprintf("SolvencyHalt%sChain", chain)
			mimir, err := thornode.GetMimir(key)
			if err != nil {
				return OpResult{
					Error:  fmt.Errorf("failed to get mimir %s: %w", key, err),
					Finish: true,
				}
			}
			if mimir > 0 {
				active = append(active, fmt.Sprintf("%s=%d", key, mimir))
			}
		}
		if len(active) > 0 {
			restoreWatcherMode.Log().Info().
				Strs("active_halts", active).
				Msg("waiting for insolvency mimirs to clear before restoring watcher fatal mode")
			return OpResult{Continue: false}
		}
		return OpResult{Continue: true}
	})
	restoreWatcherMode.Ops = append(restoreWatcherMode.Ops, func(config *OpConfig) OpResult {
		config.SetSolvencyHaltLogOnly(false)
		restoreWatcherMode.Log().Info().Msg("set solvency halt watcher to fatal mode")
		return OpResult{Continue: true}
	})
	a.Append(restoreWatcherMode)

	return a
}

func insolvencySimulationChains() []common.Chain {
	chains := make([]common.Chain, 0, len(acommon.SimChains))
	for _, chain := range acommon.SimChains {
		chains = append(chains, chain)
	}
	return chains
}
