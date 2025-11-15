package suites

import (
	"fmt"

	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Attack - TSS Signature Ordering Vulnerability PoC
////////////////////////////////////////////////////////////////////////////////////////

// Attack demonstrates the TSS signature ordering vulnerability by:
// 1. Running minimal BTC-only bootstrap (avoids TRON error, faster setup)
// 2. Running AttackActor to execute the full exploit demonstration
//
// Prerequisites:
// - bifrost-pig must have ATTACK_MODE=true environment variable
// - Other bifrost instances must NOT have ATTACK_MODE set (defaults to false)
//
// Expected Results:
// - BTC vault receives 3 donations (creating 3 UTXOs)
// - RUNE->BTC swap triggers multi-UTXO outbound transaction
// - Attacker node (bifrost-pig) broadcasts valid transaction first
// - Honest nodes fail with NULLFAIL error
// - Keysign retry is triggered
// - Double-spend attempt is detected
// - Attack report is generated with evidence
func Attack() *Actor {
	suite := NewActor("Attack-Suite")

	// Stage 1: Bootstrap BTC pool only (minimal setup for attack)
	// suite.Append(BootstrapBTCOnly())

	// Stage 2: Execute attack PoC
	suite.Append(core.NewAttackActor())

	return suite
}

////////////////////////////////////////////////////////////////////////////////////////
// BTC-Only Bootstrap
////////////////////////////////////////////////////////////////////////////////////////

// BootstrapBTCOnly creates minimal bootstrap with only BTC pool for attack PoC.
// This avoids the TRON pool decimal mismatch error and reduces setup time
// from 5-10 minutes (18 pools) to 1-2 minutes (1 pool).
func BootstrapBTCOnly() *Actor {
	a := NewActor("Bootstrap-BTC-Only")

	// Create BTC pool using existing DualLPActor
	// This handles: user acquisition, RUNE deposit, BTC deposit, LP verification
	a.Children[core.NewDualLPActor(common.BTCAsset)] = true

	// Verify BTC pool is available before proceeding to attack
	verify := NewActor("Verify-BTC-Pool")
	verify.Ops = append(verify.Ops, verifyBTCPoolAvailable)
	a.Append(verify)

	return a
}

// verifyBTCPoolAvailable checks that BTC.BTC pool exists and is in Available status.
// Retries until pool is available or returns error if pool not found.
func verifyBTCPoolAvailable(config *OpConfig) OpResult {
	pools, err := thornode.GetPools()
	if err != nil {
		log.Error().Err(err).Msg("failed to get pools")
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("failed to query pools: %w", err),
		}
	}

	// Find BTC.BTC pool
	for _, pool := range pools {
		if pool.Asset == "BTC.BTC" {
			if pool.Status != "Available" {
				err := fmt.Errorf("BTC pool status: %s (expected Available)", pool.Status)
				log.Warn().Err(err).Msg("BTC pool not ready, retrying...")
				return OpResult{Continue: false} // Retry until available
			}

			log.Info().
				Str("asset", pool.Asset).
				Str("status", pool.Status).
				Msg("BTC pool verified and available")
			return OpResult{Finish: true}
		}
	}

	err = fmt.Errorf("BTC.BTC pool not found in %d total pools", len(pools))
	log.Error().Err(err).Msg("BTC pool missing")
	return OpResult{
		Finish: true,
		Error:  err,
	}
}
