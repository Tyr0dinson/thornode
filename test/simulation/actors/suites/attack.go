package suites

import (
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/core"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Attack - TSS Signature Ordering Vulnerability PoC
////////////////////////////////////////////////////////////////////////////////////////

// Attack demonstrates the TSS signature ordering vulnerability by:
// 1. Running Bootstrap to setup pools
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

	// Stage 1: Bootstrap pools (required for swaps)
	suite.Append(Bootstrap())

	// Stage 2: Execute attack PoC
	suite.Append(core.NewAttackActor())

	return suite
}
