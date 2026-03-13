package thorchain

import (
	"fmt"
	"sort"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// IntentMgr handles intent matching and expiration in EndBlock
type IntentMgr struct {
	mgr Manager
}

// NewIntentManager creates a new instance of IntentMgr
func NewIntentManager(mgr Manager) IntentManager {
	return &IntentMgr{
		mgr: mgr,
	}
}

// EndBlock processes intents at the end of each block
func (im *IntentMgr) EndBlock(ctx cosmos.Context) error {
	// Clean up stale entries from the active intent and auction indices
	im.mgr.Keeper().CleanupStaleActiveIntents(ctx)
	im.mgr.Keeper().CleanupStaleActiveAuctions(ctx)

	intentsEnabled := im.mgr.Keeper().GetConfigInt64(ctx, constants.IntentsEnabled) > 0

	// Only process new matches when intents are enabled; when disabled as an
	// emergency measure, skip executing new solves to prevent further damage.
	if intentsEnabled {
		if err := im.processMatches(ctx); err != nil {
			ctx.Logger().Error("fail to process intent matches", "error", err)
			return err
		}
	}

	// Always process expirations and auctions for cleanup, even when disabled,
	// so existing intents can expire/complete and users can recover funds.
	if err := im.processExpirations(ctx); err != nil {
		ctx.Logger().Error("fail to process intent expirations", "error", err)
		return err
	}

	if err := im.processAuctions(ctx); err != nil {
		ctx.Logger().Error("fail to process intent auctions", "error", err)
		return err
	}

	return nil
}

// selectBestSolve selects the best solve from competing bids using deterministic rules:
// 1. A solve must exceed others by at least 1x gas fee to be considered "better"
// 2. Solves within 1x gas of each other are grouped as equals
// 3. Among equals: earliest SubmitHeight wins
// 4. Final tie-breaker: lowest SolveTxID lexicographically
func (im *IntentMgr) selectBestSolve(ctx cosmos.Context, intent types.Intent, solves []types.IntentSolve) types.IntentSolve {
	if len(solves) == 0 {
		return types.IntentSolve{}
	}
	if len(solves) == 1 {
		return solves[0]
	}

	// Use the target asset's chain gas fee for the comparison threshold.
	// Since solve amounts are denominated in the target asset, we must use the
	// same denomination for the gas fee tolerance to avoid unit mismatches.
	// For RUNE, GetChain() returns THORChain with near-zero gas (exact matching).
	// For secured/trade assets, GetChain() returns the underlying L1 chain,
	// providing a reasonable tolerance in the same denomination (e.g. satoshis).
	targetAsset := intent.TargetAsset
	gasFeeChain := targetAsset.GetChain()
	gasFee, err := calculateGasFee(ctx, im.mgr, gasFeeChain)
	if err != nil {
		ctx.Logger().Error("fail to calculate gas fee for solve selection, using first solve", "chain", gasFeeChain, "error", err)
		return solves[0] // Return first solve as deterministic fallback
	}

	// Sort by amount descending to process highest first
	sorted := make([]types.IntentSolve, len(solves))
	copy(sorted, solves)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TargetCoin.Amount.GT(sorted[j].TargetCoin.Amount)
	})

	// Find the highest amount
	highestAmount := sorted[0].TargetCoin.Amount

	// Collect all solves within 1x gas of the highest (they're considered equal)
	var candidates []types.IntentSolve
	for _, solve := range sorted {
		// If this solve is within 1x gas of highest, it's a candidate
		// i.e., highest - solve.Amount <= gasFee
		diff := common.SafeSub(highestAmount, solve.TargetCoin.Amount)
		if diff.LTE(gasFee) {
			candidates = append(candidates, solve)
		}
	}

	// Deterministic tiebreaker among equals: earliest SubmitHeight, then lowest SolveTxID
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SubmitHeight != candidates[j].SubmitHeight {
			return candidates[i].SubmitHeight < candidates[j].SubmitHeight
		}
		return candidates[i].SolveTxID.String() < candidates[j].SolveTxID.String()
	})

	return candidates[0]
}

// refundLosingSolves refunds all solve bids except the winning one
func (im *IntentMgr) refundLosingSolves(ctx cosmos.Context, intent types.Intent, winningSolveTxID common.TxID, solves []types.IntentSolve) {
	for _, solve := range solves {
		if solve.SolveTxID.Equals(winningSolveTxID) {
			continue // Skip winner
		}

		// Set ModuleName for RUNE solves (RUNE is escrowed in Asgard module)
		moduleName := ""
		if solve.TargetCoin.Asset.IsRune() {
			moduleName = AsgardName
		}

		toi := TxOutItem{
			Chain:      solve.TargetCoin.Asset.GetChain(),
			ToAddress:  solve.SolverAddress,
			Coin:       solve.TargetCoin,
			Memo:       NewRefundMemo(solve.SolveTxID).String(),
			InHash:     solve.SolveTxID,
			ModuleName: moduleName,
		}

		ok, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, toi, cosmos.ZeroUint())
		switch {
		case err != nil:
			ctx.Logger().Error("fail to refund losing solve", "solve_txid", solve.SolveTxID, "error", err)
			unrefundableCoinCleanup(ctx, im.mgr, toi, "failed_refund")
		case !ok:
			ctx.Logger().Error("fail to schedule losing solve refund", "solve_txid", solve.SolveTxID)
			unrefundableCoinCleanup(ctx, im.mgr, toi, "failed_refund")
		default:
			ctx.Logger().Info("refunded losing intent solve",
				"intent_txid", intent.TxID,
				"solve_txid", solve.SolveTxID,
				"amount", solve.TargetCoin.Amount,
			)
		}
		// Clean up the solve record
		im.mgr.Keeper().RemoveIntentSolve(ctx, solve.SolveTxID)
	}
}

// refundUserSource refunds the user's escrowed source funds when intent execution fails
func (im *IntentMgr) refundUserSource(ctx cosmos.Context, intent types.Intent) {
	sourceAsset := intent.Source.Asset

	moduleName := ""
	if sourceAsset.IsRune() {
		moduleName = AsgardName
	}

	toi := TxOutItem{
		Chain:      sourceAsset.GetChain(),
		ToAddress:  intent.SourceAddress,
		Coin:       intent.Source,
		Memo:       NewRefundMemo(intent.TxID).String(),
		InHash:     intent.TxID,
		ModuleName: moduleName,
	}

	ok, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, toi, cosmos.ZeroUint())
	if err != nil {
		ctx.Logger().Error("fail to refund user source on intent execution failure", "intent_txid", intent.TxID, "error", err)
		unrefundableCoinCleanup(ctx, im.mgr, toi, "failed_intent_execution_refund")
		return
	}
	if !ok {
		ctx.Logger().Error("fail to schedule user source refund on intent execution failure", "intent_txid", intent.TxID)
		unrefundableCoinCleanup(ctx, im.mgr, toi, "failed_intent_execution_refund")
		return
	}

	ctx.Logger().Info("refunded user source due to intent execution failure",
		"intent_txid", intent.TxID,
		"amount", intent.Source.Amount,
		"to", intent.SourceAddress,
	)
}

// processMatches executes intents that have solve bids matching at the current height
func (im *IntentMgr) processMatches(ctx cosmos.Context) error {
	currentHeight := ctx.BlockHeight()

	// Get all solve bids that match at this height
	solves, err := im.mgr.Keeper().GetIntentSolvesByMatchHeight(ctx, currentHeight)
	if err != nil {
		return fmt.Errorf("fail to get intent solves by match height: %w", err)
	}

	// Group solves by intent ID
	solvesByIntent := make(map[string][]types.IntentSolve)
	for _, solve := range solves {
		intentID := solve.IntentTxID.String()
		solvesByIntent[intentID] = append(solvesByIntent[intentID], solve)
	}

	// Get sorted intent IDs for deterministic processing
	intentIDs := make([]string, 0, len(solvesByIntent))
	// analyze-ignore(map-iteration)
	for intentID := range solvesByIntent {
		intentIDs = append(intentIDs, intentID)
	}
	sort.Strings(intentIDs)

	// Process each intent's competing solves
	for _, intentID := range intentIDs {
		intentSolves := solvesByIntent[intentID]
		if len(intentSolves) == 0 {
			continue
		}

		intentTxID := intentSolves[0].IntentTxID

		// Get the intent
		intent, err := im.mgr.Keeper().GetIntent(ctx, intentTxID)
		if err != nil {
			ctx.Logger().Error("fail to get intent", "txid", intentTxID, "error", err)
			continue
		}

		// Skip if intent is not active
		if intent.Status != IntentActive {
			ctx.Logger().Info("intent is not active, skipping", "txid", intentTxID, "status", intent.Status.String())
			continue
		}

		// Skip if intent has reached its expiry height - let processExpirations handle it.
		// When a solve's match_height was capped to ExpiryHeight (because its calculated delay
		// exceeded the TTL), the intent should expire rather than execute the solve.
		if currentHeight >= intent.ExpiryHeight {
			ctx.Logger().Info("intent has expired, skipping execution", "txid", intentTxID, "expiry_height", intent.ExpiryHeight)
			continue
		}

		// Select best solve deterministically (must exceed by 1x gas to be "better", then earliest submit, then lowest txid)
		winningSolve := im.selectBestSolve(ctx, intent, intentSolves)

		// Defensive assertion: MatchHeight must not exceed ExpiryHeight.
		// The handler caps MatchHeight at ExpiryHeight, but this guards against regressions.
		if winningSolve.MatchHeight > intent.ExpiryHeight {
			ctx.Logger().Error("solve MatchHeight exceeds intent ExpiryHeight, skipping",
				"intent_txid", intentTxID,
				"solve_txid", winningSolve.SolveTxID,
				"match_height", winningSolve.MatchHeight,
				"expiry_height", intent.ExpiryHeight,
			)
			continue
		}

		// Execute the intent with winning solve using CacheContext for atomicity.
		// This ensures that if any step fails (fee processing, pool changes, TxOut scheduling),
		// all state changes are rolled back, preventing partial execution where the solver
		// gets both assets while the user gets nothing.
		cacheCtx, commit := ctx.CacheContext()
		if err := im.executeIntent(cacheCtx, intent, winningSolve); err != nil {
			ctx.Logger().Error("fail to execute intent", "intent_txid", intentTxID, "solve_txid", winningSolve.SolveTxID, "error", err)
			// CacheContext discarded - no pool/fee changes persisted
			// Refund ALL solvers (including winner) since execution failed
			im.refundLosingSolves(ctx, intent, common.BlankTxID, intentSolves)
			// Refund the user's escrowed source funds
			im.refundUserSource(ctx, intent)
			// Mark intent as expired to prevent double-refund in processExpirations
			intent.Status = IntentExpired
			im.mgr.Keeper().SetIntent(ctx, intent)
			// Refund any remaining solves at other match heights to prevent orphaned funds
			if err := im.refundIntentSolves(ctx, intent); err != nil {
				ctx.Logger().Error("fail to refund remaining solves after execution failure", "intent_txid", intentTxID, "error", err)
			}
			continue
		}
		commit()
		ctx.EventManager().EmitEvents(cacheCtx.EventManager().Events())

		// Refund losing solves at this height (winner excluded since execution succeeded)
		im.refundLosingSolves(ctx, intent, winningSolve.SolveTxID, intentSolves)

		// Clean up the winning solve record
		im.mgr.Keeper().RemoveIntentSolve(ctx, winningSolve.SolveTxID)

		// Refund any remaining solves at other match heights to prevent orphaned funds
		if err := im.refundIntentSolves(ctx, intent); err != nil {
			ctx.Logger().Error("fail to refund remaining solves after intent completion", "intent_txid", intentTxID, "error", err)
		}
	}

	return nil
}

// processExpirations handles intents that have expired
func (im *IntentMgr) processExpirations(ctx cosmos.Context) error {
	currentHeight := ctx.BlockHeight()

	// Get all active intents from the active status index
	activeIntents, err := im.mgr.Keeper().GetActiveIntents(ctx)
	if err != nil {
		return fmt.Errorf("fail to get active intents: %w", err)
	}

	// Cap the number of expirations per block to prevent timeout
	maxProcessing := im.mgr.Keeper().GetConfigInt64(ctx, constants.IntentMaxEndBlockProcessing)
	if maxProcessing <= 0 {
		maxProcessing = 100
	}
	var processed int64

	for _, intent := range activeIntents {
		if processed >= maxProcessing {
			ctx.Logger().Warn("reached per-block processing cap for intent expirations", "cap", maxProcessing)
			break
		}
		// Check if expired
		if currentHeight >= intent.ExpiryHeight {
			if err := im.expireIntent(ctx, intent); err != nil {
				ctx.Logger().Error("fail to expire intent", "txid", intent.TxID, "error", err)
				continue
			}
			processed++
		}
	}

	return nil
}

// executeIntent performs the actual swap and completes the intent
func (im *IntentMgr) executeIntent(ctx cosmos.Context, intent Intent, solve IntentSolve) error {
	// Determine which asset to charge fees on (must be L1 pool or RUNE)
	// Note: Secured/trade assets cannot be charged fees because:
	// 1. They can't be swapped to RUNE for reserve
	// 2. Deducting from them causes pool/vault accounting imbalance
	sourcePool, err := im.mgr.Keeper().GetPool(ctx, intent.Source.Asset.GetLayer1Asset())
	if err != nil {
		return fmt.Errorf("fail to get source pool: %w", err)
	}
	targetPool, err := im.mgr.Keeper().GetPool(ctx, solve.TargetCoin.Asset.GetLayer1Asset())
	if err != nil {
		return fmt.Errorf("fail to get target pool: %w", err)
	}

	canChargeSource := isIntentFeeChargeableAsset(intent.Source.Asset, !sourcePool.IsEmpty() && sourcePool.Status == PoolAvailable)
	canChargeTarget := isIntentFeeChargeableAsset(solve.TargetCoin.Asset, !targetPool.IsEmpty() && targetPool.Status == PoolAvailable)

	// Log a warning if no fees can be charged (pool may have changed status since intent was opened)
	if !canChargeSource && !canChargeTarget {
		ctx.Logger().Warn("intent executing with zero fees - no fee-chargeable asset available",
			"intent_txid", intent.TxID,
			"source_asset", intent.Source.Asset,
			"target_asset", solve.TargetCoin.Asset,
		)
	}

	// Calculate protocol fee
	protocolFeeBps := intent.MinFeeBasisPoints
	protocolFee := common.NoCoin
	affiliateFee := common.NoCoin
	var feePool Pool

	// Prepare outbound items
	// Use DestinationAddress for the solver's outbound - this allows cross-chain solving
	// where the solver sends from one chain and receives on another
	solverToi := TxOutItem{
		Chain:     intent.Source.Asset.GetChain(),
		ToAddress: solve.DestinationAddress,
		Coin:      intent.Source,
		Memo:      fmt.Sprintf("OUT:%s", solve.SolveTxID),
		InHash:    solve.SolveTxID,
	}

	userToi := TxOutItem{
		Chain:     solve.TargetCoin.Asset.GetChain(),
		ToAddress: intent.TargetAddress,
		Coin:      solve.TargetCoin,
		Memo:      fmt.Sprintf("OUT:%s", intent.TxID),
		InHash:    intent.TxID,
	}

	// Determine which asset to charge fees on (prefer source, then target)
	if canChargeSource {
		// Charge fees on source asset
		protocolFeeAmount := common.GetSafeShare(cosmos.NewUint(uint64(protocolFeeBps)), cosmos.NewUint(10000), intent.Source.Amount)
		protocolFee = common.NewCoin(intent.Source.Asset, protocolFeeAmount)
		feePool = sourcePool

		// Subtract protocol fee from source amount going to solver
		if protocolFeeAmount.GT(cosmos.ZeroUint()) {
			solverToi.Coin.Amount = common.SafeSub(solverToi.Coin.Amount, protocolFeeAmount)
		}

		// Calculate and subtract affiliate fee from source
		if intent.AffiliateBasisPoints > 0 && !intent.AffiliateAddress.IsEmpty() {
			affiliateFeeAmount := common.GetSafeShare(cosmos.NewUint(uint64(intent.AffiliateBasisPoints)), cosmos.NewUint(10000), intent.Source.Amount)
			affiliateFee = common.NewCoin(intent.Source.Asset, affiliateFeeAmount)
			if affiliateFeeAmount.GT(cosmos.ZeroUint()) {
				solverToi.Coin.Amount = common.SafeSub(solverToi.Coin.Amount, affiliateFeeAmount)
			}
		}
	} else if canChargeTarget {
		// Charge fees on target asset
		protocolFeeAmount := common.GetSafeShare(cosmos.NewUint(uint64(protocolFeeBps)), cosmos.NewUint(10000), solve.TargetCoin.Amount)
		protocolFee = common.NewCoin(solve.TargetCoin.Asset, protocolFeeAmount)
		feePool = targetPool

		// Subtract protocol fee from target amount going to user
		if protocolFeeAmount.GT(cosmos.ZeroUint()) {
			userToi.Coin.Amount = common.SafeSub(userToi.Coin.Amount, protocolFeeAmount)
		}

		// Calculate and subtract affiliate fee from target
		if intent.AffiliateBasisPoints > 0 && !intent.AffiliateAddress.IsEmpty() {
			affiliateFeeAmount := common.GetSafeShare(cosmos.NewUint(uint64(intent.AffiliateBasisPoints)), cosmos.NewUint(10000), solve.TargetCoin.Amount)
			affiliateFee = common.NewCoin(solve.TargetCoin.Asset, affiliateFeeAmount)
			if affiliateFeeAmount.GT(cosmos.ZeroUint()) {
				userToi.Coin.Amount = common.SafeSub(userToi.Coin.Amount, affiliateFeeAmount)
			}
		}
	}

	// Validate outbound amounts are non-zero after fee deductions
	if solverToi.Coin.Amount.IsZero() {
		return fmt.Errorf("solver outbound amount is zero after fee deduction (source: %s, fees too large)", intent.Source.Amount)
	}
	if userToi.Coin.Amount.IsZero() {
		return fmt.Errorf("user outbound amount is zero after fee deduction (target: %s, fees too large)", solve.TargetCoin.Amount)
	}

	// Process protocol fee - deposit into reserve
	if !protocolFee.IsEmpty() && protocolFee.Amount.GT(cosmos.ZeroUint()) {
		err = im.processProtocolFee(ctx, solve.SolveTxID, protocolFee, feePool)
		if err != nil {
			return fmt.Errorf("fail to process protocol fee: %w", err)
		}
		// Re-fetch pool after protocol fee swap to get updated balances
		// (processProtocolFee modifies and saves the pool, but feePool is passed by value)
		if !feePool.IsEmpty() && !protocolFee.Asset.IsRune() && !protocolFee.Asset.IsSecuredAsset() && !protocolFee.Asset.IsTradeAsset() {
			feePool, err = im.mgr.Keeper().GetPool(ctx, feePool.Asset)
			if err != nil {
				return fmt.Errorf("fail to re-fetch pool after protocol fee: %w", err)
			}
		}
	}

	// Process affiliate fee - send to affiliate
	if !affiliateFee.IsEmpty() && affiliateFee.Amount.GT(cosmos.ZeroUint()) {
		err = im.processAffiliateFee(ctx, intent, solve, affiliateFee, feePool)
		if err != nil {
			return fmt.Errorf("fail to process affiliate fee: %w", err)
		}
	}

	// Schedule outbound for source asset to solver
	solverOk, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, solverToi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to add solver outbound tx: %w", err)
	}

	// Schedule outbound for target asset to user
	userOk, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, userToi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to add user outbound tx: %w", err)
	}

	// Only mark completed if both TxOuts succeeded
	if !solverOk || !userOk {
		return fmt.Errorf("fail to schedule txouts: solver=%v, user=%v", solverOk, userOk)
	}

	// Update intent status to completed
	intent.Status = IntentCompleted
	im.mgr.Keeper().SetIntent(ctx, intent)

	// Emit complete event
	evt := NewEventIntentComplete(
		intent.TxID,
		solve.SolveTxID,
		solverToi.Coin,
		userToi.Coin,
		protocolFee,
		affiliateFee,
	)
	if err := im.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit intent complete event", "error", err)
	}

	ctx.Logger().Info("intent executed",
		"intent_txid", intent.TxID,
		"solve_txid", solve.SolveTxID,
		"source_amount", intent.Source.Amount,
		"target_amount", solve.TargetCoin.Amount,
		"protocol_fee", protocolFee.Amount,
		"affiliate_fee", affiliateFee.Amount,
		"fee_asset", protocolFee.Asset,
	)

	return nil
}

// processProtocolFee handles the protocol fee - swaps to RUNE if needed and adds to reserve
func (im *IntentMgr) processProtocolFee(ctx cosmos.Context, solveTxID common.TxID, protocolFee common.Coin, feePool Pool) error {
	switch {
	case protocolFee.Asset.IsRune():
		// Add RUNE directly to reserve
		if err := im.mgr.Keeper().AddPoolFeeToReserve(ctx, protocolFee.Amount); err != nil {
			return fmt.Errorf("fail to add fee to reserve: %w", err)
		}
		ctx.Logger().Info("added intent protocol fee to reserve", "amount", protocolFee.Amount)
	case protocolFee.Asset.IsSecuredAsset() || protocolFee.Asset.IsTradeAsset():
		// For secured/trade assets, the fee is already deducted from what the solver receives.
		// The fee portion was escrowed (burned) and won't be given to anyone.
		// We don't add anything to reserve since no RUNE is actually being generated -
		// the fee represents reduced payout to the solver, not a transfer to reserve.
		ctx.Logger().Info("intent protocol fee deducted from secured/trade asset (no reserve addition)",
			"fee_asset", protocolFee.Asset,
			"fee_amount", protocolFee.Amount,
		)
	case !feePool.IsEmpty():
		// Perform a direct swap of L1 fee to RUNE and add to reserve.
		// We do this directly instead of queuing a swap to avoid timing issues
		// with invariant checks. The swap queue approach can cause the pool to
		// have more coins than the vault during the window between solve completion
		// and swap execution.
		// Note: This bypasses the swap queue's volume/LUVI tracking, but swap events
		// are still emitted for indexer consumption. The fee amounts are small
		// (basis-point-level) so the impact on pool metrics is minimal.

		// Get the swapper for swap calculations
		swapper, err := GetSwapper(im.mgr.Keeper().GetVersion())
		if err != nil {
			return fmt.Errorf("fail to get swapper: %w", err)
		}

		// Calculate swap parameters: L1 asset -> RUNE
		// X = pool.BalanceAsset (source depth), Y = pool.BalanceRune (target depth)
		X := feePool.BalanceAsset
		x := protocolFee.Amount
		Y := feePool.BalanceRune

		// Calculate slip and emit amount
		slipBps := swapper.CalcSwapSlip(X, x)
		minSlipBps := cosmos.ZeroUint() // No min slip for internal swaps
		emitRune, liquidityFee, _ := swapper.GetSwapCalc(X, x, Y, slipBps, minSlipBps)

		// Ensure we have enough RUNE in the pool
		if emitRune.GTE(Y) {
			return fmt.Errorf("not enough RUNE in pool for protocol fee swap")
		}

		// Update pool balances directly
		feePool.BalanceAsset = feePool.BalanceAsset.Add(x)
		feePool.BalanceRune = common.SafeSub(feePool.BalanceRune, emitRune)

		// Save the updated pool
		if err := im.mgr.Keeper().SetPool(ctx, feePool); err != nil {
			return fmt.Errorf("fail to save pool after protocol fee swap: %w", err)
		}

		// Transfer emitted RUNE from Asgard to Reserve
		if err := im.mgr.Keeper().AddPoolFeeToReserve(ctx, emitRune); err != nil {
			return fmt.Errorf("fail to add swapped fee to reserve: %w", err)
		}

		// Emit swap event for the direct swap
		swapEvt := NewEventSwap(
			feePool.Asset,
			cosmos.ZeroUint(), // no trade target
			slipBps,
			liquidityFee,
			cosmos.ZeroUint(), // liquidity fee in RUNE (same as liquidity fee for L1->RUNE)
			common.Tx{
				ID:    solveTxID,
				Chain: common.THORChain,
				Coins: common.NewCoins(protocolFee),
			},
			common.NewCoin(common.RuneAsset(), emitRune),
			cosmos.ZeroUint(), // synth units
		)
		if err := im.mgr.EventMgr().EmitEvent(ctx, swapEvt); err != nil {
			ctx.Logger().Error("fail to emit protocol fee swap event", "error", err)
		}

		ctx.Logger().Info("executed direct intent protocol fee swap to RUNE",
			"fee_asset", protocolFee.Asset,
			"fee_amount", protocolFee.Amount,
			"emit_rune", emitRune,
			"liquidity_fee", liquidityFee,
		)
	}

	// Emit fee event
	feeEvent := NewEventFee(solveTxID, common.NewFee(common.NewCoins(protocolFee), cosmos.ZeroUint()), cosmos.ZeroUint())
	if err := im.mgr.EventMgr().EmitEvent(ctx, feeEvent); err != nil {
		ctx.Logger().Error("fail to emit fee event", "error", err)
	}

	return nil
}

// processAffiliateFee handles the affiliate fee - sends RUNE directly or swaps to RUNE first
func (im *IntentMgr) processAffiliateFee(ctx cosmos.Context, intent Intent, solve IntentSolve, affiliateFee common.Coin, feePool Pool) error {
	affiliateAddr := intent.AffiliateAddress

	// Convert affiliate address to AccAddress for RUNE transfers
	affiliateAccAddr, err := affiliateAddr.AccAddress()
	if err != nil {
		return fmt.Errorf("fail to convert affiliate address: %w", err)
	}

	switch {
	case affiliateFee.Asset.IsRune():
		// Send RUNE directly to affiliate from Asgard (where intent RUNE is held)
		if err := im.mgr.Keeper().SendFromModuleToAccount(ctx, AsgardName, affiliateAccAddr, common.NewCoins(affiliateFee)); err != nil {
			return fmt.Errorf("fail to send RUNE to affiliate: %w", err)
		}
		ctx.Logger().Info("sent intent affiliate fee",
			"affiliate", affiliateAddr,
			"amount", affiliateFee.Amount,
		)
	case affiliateFee.Asset.IsSecuredAsset() || affiliateFee.Asset.IsTradeAsset():
		// For secured/trade assets, we cannot pay the affiliate since the fee is in
		// secured/trade asset form which was already escrowed (burned).
		// The affiliate fee is effectively not collected for these asset types.
		// Future improvement: could send the secured/trade asset directly to affiliate's THORChain address
		ctx.Logger().Info("affiliate fee not paid for secured/trade asset intent",
			"affiliate", affiliateAddr,
			"fee_asset", affiliateFee.Asset,
			"fee_amount", affiliateFee.Amount,
		)
	case !feePool.IsEmpty():
		// Perform a direct swap of L1 fee to RUNE and send to affiliate.
		// We do this directly instead of queuing a swap to avoid timing issues
		// with invariant checks.

		// Get the swapper for swap calculations
		swapper, err := GetSwapper(im.mgr.Keeper().GetVersion())
		if err != nil {
			return fmt.Errorf("fail to get swapper: %w", err)
		}

		// Calculate swap parameters: L1 asset -> RUNE
		// X = pool.BalanceAsset (source depth), Y = pool.BalanceRune (target depth)
		X := feePool.BalanceAsset
		x := affiliateFee.Amount
		Y := feePool.BalanceRune

		// Calculate slip and emit amount
		slipBps := swapper.CalcSwapSlip(X, x)
		minSlipBps := cosmos.ZeroUint() // No min slip for internal swaps
		emitRune, liquidityFee, _ := swapper.GetSwapCalc(X, x, Y, slipBps, minSlipBps)

		// Ensure we have enough RUNE in the pool
		if emitRune.GTE(Y) {
			return fmt.Errorf("not enough RUNE in pool for affiliate fee swap")
		}

		// Update pool balances directly
		feePool.BalanceAsset = feePool.BalanceAsset.Add(x)
		feePool.BalanceRune = common.SafeSub(feePool.BalanceRune, emitRune)

		// Save the updated pool
		if err := im.mgr.Keeper().SetPool(ctx, feePool); err != nil {
			return fmt.Errorf("fail to save pool after affiliate fee swap: %w", err)
		}

		// Send emitted RUNE to affiliate from Asgard
		runeCoin := common.NewCoin(common.RuneNative, emitRune)
		if err := im.mgr.Keeper().SendFromModuleToAccount(ctx, AsgardName, affiliateAccAddr, common.NewCoins(runeCoin)); err != nil {
			return fmt.Errorf("fail to send swapped RUNE to affiliate: %w", err)
		}

		// Emit swap event for the direct swap
		swapEvt := NewEventSwap(
			feePool.Asset,
			cosmos.ZeroUint(), // no trade target
			slipBps,
			liquidityFee,
			cosmos.ZeroUint(), // liquidity fee in RUNE
			common.Tx{
				ID:    solve.SolveTxID,
				Chain: common.THORChain,
				Coins: common.NewCoins(affiliateFee),
			},
			common.NewCoin(common.RuneAsset(), emitRune),
			cosmos.ZeroUint(), // synth units
		)
		if err := im.mgr.EventMgr().EmitEvent(ctx, swapEvt); err != nil {
			ctx.Logger().Error("fail to emit affiliate fee swap event", "error", err)
		}

		ctx.Logger().Info("executed direct intent affiliate fee swap to RUNE",
			"affiliate", affiliateAddr,
			"fee_asset", affiliateFee.Asset,
			"fee_amount", affiliateFee.Amount,
			"emit_rune", emitRune,
		)
	}

	// Emit affiliate fee event - use the correct base amount depending on which side fees are charged
	var feeBaseAmount cosmos.Uint
	if affiliateFee.Asset.Equals(intent.Source.Asset) {
		feeBaseAmount = intent.Source.Amount
	} else {
		feeBaseAmount = solve.TargetCoin.Amount
	}
	affFeeEvent := NewEventAffiliateFee(
		intent.TxID,
		"", // memo not needed for intents
		"", // thorname not tracked for intents
		affiliateAddr,
		affiliateFee.Asset,
		uint64(intent.AffiliateBasisPoints),
		feeBaseAmount,
		affiliateFee.Amount,
	)
	if err := im.mgr.EventMgr().EmitEvent(ctx, affFeeEvent); err != nil {
		ctx.Logger().Error("fail to emit affiliate fee event", "error", err)
	}

	return nil
}

// expireIntent handles an expired intent by starting an auction
func (im *IntentMgr) expireIntent(ctx cosmos.Context, intent Intent) error {
	// Check if we should refund directly instead of auctioning
	// Refund if: source asset is native OR has an available L1 pool
	sourcePool, err := im.mgr.Keeper().GetPool(ctx, intent.Source.Asset.GetLayer1Asset())
	if err != nil {
		return fmt.Errorf("fail to get source pool for intent expiry: %w", err)
	}
	isPoolAsset := !sourcePool.IsEmpty() && sourcePool.Status == PoolAvailable

	if isPoolAsset || intent.Source.Asset.IsNative() {
		// Direct refund - no auction needed
		return im.refundExpiredIntent(ctx, intent)
	}

	// For non-refundable assets, give the owner a cancel window before auctioning.
	// During this window the intent stays Active so the owner can still cancel
	// and recover their funds without being exposed to auction sniping.
	cancelWindow := im.mgr.Keeper().GetConfigInt64(ctx, constants.IntentPostExpiryCancelBlocks)
	if cancelWindow > 0 && ctx.BlockHeight() < intent.ExpiryHeight+cancelWindow {
		ctx.Logger().Info("intent in post-expiry cancel window, deferring auction",
			"txid", intent.TxID,
			"expiry_height", intent.ExpiryHeight,
			"cancel_deadline", intent.ExpiryHeight+cancelWindow,
		)
		return nil // still in cancel window, skip auction for now
	}

	return im.StartIntentAuction(ctx, intent)
}

// StartIntentAuction creates an auction for the intent's source asset and
// transitions the intent to auctioning status. This is used both by the expiry
// path (for non-refundable assets) and by the cancel path (for non-pool,
// non-native assets that cannot be directly refunded).
func (im *IntentMgr) StartIntentAuction(ctx cosmos.Context, intent Intent) error {
	// Get auction parameters
	auctionBps := im.mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionBasisPoints)
	if auctionBps <= 0 {
		auctionBps = 1000 // Default 10%
	}

	auctionDuration := im.mgr.Keeper().GetConfigInt64(ctx, constants.IntentAuctionDurationBlocks)
	if auctionDuration <= 0 {
		auctionDuration = 14400 // Default ~1 day
	}

	// Calculate auction amount (1k bps = 10% of source)
	maxBps := cosmos.NewUint(constants.MaxBasisPts)
	auctionAmount := common.GetSafeShare(cosmos.NewUint(uint64(auctionBps)), maxBps, intent.Source.Amount)
	remainingAmount := common.SafeSub(intent.Source.Amount, auctionAmount)

	// Determine bid asset (gas asset of source chain)
	sourceChain := intent.Source.Asset.GetChain()
	bidAsset := sourceChain.GetGasAsset()

	// Calculate minimum bid (3x network fee)
	minBid, err := calculateMinBid(ctx, im.mgr, sourceChain)
	if err != nil {
		return fmt.Errorf("fail to calculate minimum bid for auction: %w", err)
	}

	// Create the auction
	auction := IntentAuction{
		IntentTxID:      intent.TxID,
		AuctionAmount:   common.NewCoin(intent.Source.Asset, auctionAmount),
		RemainingAmount: common.NewCoin(intent.Source.Asset, remainingAmount),
		BidAsset:        bidAsset,
		MinBid:          minBid,
		StartHeight:     ctx.BlockHeight(),
		ExpiryHeight:    ctx.BlockHeight() + auctionDuration,
		Status:          IntentAuctionStatus_AuctionActive,
		OwnerAddress:    intent.SourceAddress,
	}

	// Save the auction
	im.mgr.Keeper().SetIntentAuction(ctx, auction)

	// Refund any existing solves for this intent.
	// Even if some individual refunds fail (handled by unrefundableCoinCleanup),
	// we still transition to auctioning. The solve records are always cleaned up
	// regardless of refund success. Only abort if we can't read solves at all.
	if err := im.refundIntentSolves(ctx, intent); err != nil {
		ctx.Logger().Error("fail to refund intent solves", "txid", intent.TxID, "error", err)
	}

	// Update intent status to auctioning
	intent.Status = IntentAuctioning
	im.mgr.Keeper().SetIntent(ctx, intent)

	// Emit auction start event
	evt := NewEventIntentAuctionStart(
		intent.TxID,
		auction.AuctionAmount,
		auction.RemainingAmount,
		bidAsset,
		minBid,
		auction.ExpiryHeight,
	)
	if err := im.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit intent auction start event", "error", err)
	}

	ctx.Logger().Info("started intent auction",
		"intent_txid", intent.TxID,
		"auction_amount", auctionAmount,
		"remaining_amount", remainingAmount,
		"bid_asset", bidAsset,
		"min_bid", minBid,
		"expiry_height", auction.ExpiryHeight,
	)

	return nil
}

// refundExpiredIntent refunds an expired intent directly without auction
// This is used when the source asset is native or has an L1 pool.
// If refunding fails repeatedly (e.g., chain halted), a grace period of
// IntentTTLBlocks beyond expiry falls back to unrefundableCoinCleanup
// to prevent the intent from blocking processing indefinitely.
func (im *IntentMgr) refundExpiredIntent(ctx cosmos.Context, intent Intent) error {
	sourceAsset := intent.Source.Asset

	// Determine module name for RUNE
	moduleName := ""
	if sourceAsset.IsRune() {
		moduleName = AsgardName
	}

	// Create refund txout
	toi := TxOutItem{
		Chain:      sourceAsset.GetChain(),
		ToAddress:  intent.SourceAddress,
		Coin:       intent.Source,
		Memo:       NewRefundMemo(intent.TxID).String(),
		InHash:     intent.TxID,
		ModuleName: moduleName,
	}

	ok, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, toi, cosmos.ZeroUint())
	if err != nil || !ok {
		// Check if we've exceeded the grace period for retries
		ttlBlocks := im.mgr.Keeper().GetConfigInt64(ctx, constants.IntentTTLBlocks)
		if ttlBlocks <= 0 {
			ttlBlocks = 14400
		}
		graceDeadline := intent.ExpiryHeight + ttlBlocks
		if ctx.BlockHeight() > graceDeadline {
			// Grace period exceeded — fall back to unrefundableCoinCleanup to unblock processing
			ctx.Logger().Error("intent refund grace period exceeded, cleaning up unrefundable funds",
				"txid", intent.TxID,
				"expiry_height", intent.ExpiryHeight,
				"grace_deadline", graceDeadline,
			)
			unrefundableCoinCleanup(ctx, im.mgr, toi, "intent_refund_grace_exceeded")

			// Still proceed with cleanup below
		} else {
			// Within grace period — return error to retry next block
			if err != nil {
				return fmt.Errorf("fail to schedule refund for expired intent: %w", err)
			}
			return fmt.Errorf("fail to add refund txout for expired intent")
		}
	}

	// Refund any existing solves for this intent
	if err := im.refundIntentSolves(ctx, intent); err != nil {
		ctx.Logger().Error("fail to refund intent solves", "txid", intent.TxID, "error", err)
	}

	// Update intent status
	intent.Status = IntentExpired
	im.mgr.Keeper().SetIntent(ctx, intent)

	// Emit expiry event (abandoned=false since we are refunding)
	evt := NewEventIntentExpire(intent.TxID, intent.Source, intent.SourceAddress, false)
	if err := im.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit intent expire event", "error", err)
	}

	ctx.Logger().Info("refunded expired intent",
		"txid", intent.TxID,
		"amount", intent.Source.Amount,
		"to", intent.SourceAddress,
	)

	return nil
}

// RefundIntentSolves refunds all solve bids for an intent (public interface method)
func (im *IntentMgr) RefundIntentSolves(ctx cosmos.Context, intent types.Intent) error {
	return im.refundIntentSolves(ctx, intent)
}

// refundIntentSolves refunds all solve bids for an intent when auction starts
func (im *IntentMgr) refundIntentSolves(ctx cosmos.Context, intent Intent) error {
	solves, err := im.mgr.Keeper().GetIntentSolvesByIntentID(ctx, intent.TxID)
	if err != nil {
		return fmt.Errorf("fail to get intent solves: %w", err)
	}

	var failedRefunds []string
	for _, solve := range solves {
		// Set ModuleName for RUNE solves (RUNE is escrowed in Asgard module)
		moduleName := ""
		if solve.TargetCoin.Asset.IsRune() {
			moduleName = AsgardName
		}

		// Schedule refund for the solve
		toi := TxOutItem{
			Chain:      solve.TargetCoin.Asset.GetChain(),
			ToAddress:  solve.SolverAddress,
			Coin:       solve.TargetCoin,
			Memo:       NewRefundMemo(solve.SolveTxID).String(),
			InHash:     solve.SolveTxID,
			ModuleName: moduleName,
		}

		ok, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, toi, cosmos.ZeroUint())
		switch {
		case err != nil:
			ctx.Logger().Error("fail to refund solve", "solve_txid", solve.SolveTxID, "solver_address", solve.SolverAddress, "amount", solve.TargetCoin.Amount, "error", err)
			failedRefunds = append(failedRefunds, solve.SolveTxID.String())
			// Move funds to reserve
			unrefundableCoinCleanup(ctx, im.mgr, toi, "failed_refund")
		case !ok:
			ctx.Logger().Error("fail to schedule solve refund", "solve_txid", solve.SolveTxID, "solver_address", solve.SolverAddress, "amount", solve.TargetCoin.Amount)
			failedRefunds = append(failedRefunds, solve.SolveTxID.String())
			// Move funds to reserve
			unrefundableCoinCleanup(ctx, im.mgr, toi, "failed_refund")
		default:
			ctx.Logger().Info("refunded intent solve due to auction start",
				"intent_txid", intent.TxID,
				"solve_txid", solve.SolveTxID,
				"amount", solve.TargetCoin.Amount,
			)
		}
		// Clean up the solve record
		im.mgr.Keeper().RemoveIntentSolve(ctx, solve.SolveTxID)
	}

	if len(failedRefunds) > 0 {
		return fmt.Errorf("failed to refund %d solves: %v", len(failedRefunds), failedRefunds)
	}
	return nil
}

// processAuctions processes active auctions at the end of each block
func (im *IntentMgr) processAuctions(ctx cosmos.Context) error {
	currentHeight := ctx.BlockHeight()

	// Get all active auctions
	activeAuctions, err := im.mgr.Keeper().GetActiveAuctions(ctx)
	if err != nil {
		return fmt.Errorf("fail to get active auctions: %w", err)
	}

	// Cap the number of auctions processed per block to prevent timeout
	maxProcessing := im.mgr.Keeper().GetConfigInt64(ctx, constants.IntentMaxEndBlockProcessing)
	if maxProcessing <= 0 {
		maxProcessing = 100
	}
	var processed int64

	for _, auction := range activeAuctions {
		if processed >= maxProcessing {
			ctx.Logger().Warn("reached per-block processing cap for auction completions", "cap", maxProcessing)
			break
		}
		// Check if auction has expired
		if currentHeight >= auction.ExpiryHeight {
			if err := im.completeAuction(ctx, auction); err != nil {
				ctx.Logger().Error("fail to complete auction", "intent_txid", auction.IntentTxID, "error", err)
				continue
			}
			processed++
		}
	}

	return nil
}

// completeAuction finalizes an expired auction.
// Uses CacheContext for atomicity to prevent partial state changes on failure,
// which could otherwise cause double-payments on retry.
func (im *IntentMgr) completeAuction(ctx cosmos.Context, auction IntentAuction) error {
	// Get bids for this auction
	bids, err := im.mgr.Keeper().GetIntentAuctionBidsByIntentID(ctx, auction.IntentTxID)
	if err != nil {
		return fmt.Errorf("fail to get auction bids: %w", err)
	}

	// Get the intent
	intent, err := im.mgr.Keeper().GetIntent(ctx, auction.IntentTxID)
	if err != nil {
		return fmt.Errorf("fail to get intent: %w", err)
	}

	// If no bids, cancel the auction and abandon funds
	if len(bids) == 0 {
		return im.cancelAuction(ctx, auction, intent)
	}

	// Find the highest bid
	auctionBids := IntentAuctionBids(bids)
	highestBidAmount := auctionBids.HighestBid()

	// Collect all bids with the highest amount (for deterministic tiebreaker)
	var candidates []IntentAuctionBid
	for i := range bids {
		if bids[i].BidAmount.Amount.Equal(highestBidAmount) {
			candidates = append(candidates, bids[i])
		}
	}

	if len(candidates) == 0 {
		return fmt.Errorf("no winning bid found")
	}

	// Deterministic tiebreaker: earliest SubmitHeight wins, then lowest BidTxID
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].SubmitHeight != candidates[j].SubmitHeight {
			return candidates[i].SubmitHeight < candidates[j].SubmitHeight
		}
		return candidates[i].BidTxID.String() < candidates[j].BidTxID.String()
	})
	winningBid := candidates[0]

	// Use CacheContext for atomicity: if any step fails, all state changes are rolled back.
	// Without this, partial completion (e.g., losing bids refunded but winner payout fails)
	// would leave the auction in an inconsistent state and cause double-payments on retry.
	cacheCtx, commit := ctx.CacheContext()

	// Process losing bids - refund them
	for i := range bids {
		if bids[i].BidTxID.Equals(winningBid.BidTxID) {
			continue // Skip winner
		}

		// Determine module name for RUNE bids (bid RUNE is escrowed in Asgard)
		bidModuleName := ""
		if bids[i].BidAmount.Asset.IsRune() {
			bidModuleName = AsgardName
		}

		// Refund the losing bid
		toi := TxOutItem{
			Chain:      bids[i].BidAmount.Asset.GetChain(),
			ToAddress:  bids[i].BidderAddress,
			Coin:       bids[i].BidAmount,
			Memo:       NewRefundMemo(bids[i].BidTxID).String(),
			InHash:     bids[i].BidTxID,
			ModuleName: bidModuleName,
		}

		ok, refundErr := im.mgr.TxOutStore().TryAddTxOutItem(cacheCtx, im.mgr, toi, cosmos.ZeroUint())
		switch {
		case refundErr != nil:
			// Return error to roll back the entire CacheContext and retry next block.
			// This prevents losing bidders' funds from being silently lost
			// (especially for non-native L1 assets where unrefundableCoinCleanup
			// only logs and leaves funds untracked in the vault).
			return fmt.Errorf("fail to refund losing bid %s: %w", bids[i].BidTxID, refundErr)
		case !ok:
			return fmt.Errorf("fail to schedule losing bid refund for %s", bids[i].BidTxID)
		default:
			cacheCtx.Logger().Info("refunded losing auction bid",
				"intent_txid", auction.IntentTxID,
				"bid_txid", bids[i].BidTxID,
				"amount", bids[i].BidAmount.Amount,
			)
		}
		// Clean up the losing bid record
		im.mgr.Keeper().RemoveIntentAuctionBid(cacheCtx, bids[i].BidTxID)
	}

	// Determine module name for RUNE outbounds (RUNE is escrowed in Asgard)
	moduleName := ""
	if auction.AuctionAmount.Asset.IsRune() {
		moduleName = AsgardName
	}

	// Send auction amount to winner
	winnerToi := TxOutItem{
		Chain:      auction.AuctionAmount.Asset.GetChain(),
		ToAddress:  winningBid.BidderAddress,
		Coin:       auction.AuctionAmount,
		Memo:       NewOutboundMemo(winningBid.BidTxID).String(),
		InHash:     winningBid.BidTxID,
		ModuleName: moduleName,
	}

	ok, err := im.mgr.TxOutStore().TryAddTxOutItem(cacheCtx, im.mgr, winnerToi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to send auction amount to winner: %w", err)
	}
	if !ok {
		return fmt.Errorf("fail to schedule winner txout")
	}

	// Send remaining amount to owner (skip if zero, e.g. when IntentAuctionBasisPoints = 10000)
	if !auction.RemainingAmount.Amount.IsZero() {
		ownerToi := TxOutItem{
			Chain:      auction.RemainingAmount.Asset.GetChain(),
			ToAddress:  auction.OwnerAddress,
			Coin:       auction.RemainingAmount,
			Memo:       NewRefundMemo(auction.IntentTxID).String(),
			InHash:     auction.IntentTxID,
			ModuleName: moduleName,
		}

		ok, err = im.mgr.TxOutStore().TryAddTxOutItem(cacheCtx, im.mgr, ownerToi, cosmos.ZeroUint())
		if err != nil {
			return fmt.Errorf("fail to send remaining amount to owner: %w", err)
		}
		if !ok {
			return fmt.Errorf("fail to schedule owner refund txout")
		}
	}

	// Process winning bid: swap to RUNE and add to reserve
	if err := im.processWinningBid(cacheCtx, winningBid); err != nil {
		cacheCtx.Logger().Error("fail to process winning bid", "error", err)
		return fmt.Errorf("fail to process winning bid revenue: %w", err)
	}

	// Clean up the winning bid record
	im.mgr.Keeper().RemoveIntentAuctionBid(cacheCtx, winningBid.BidTxID)

	// Update auction status and record winning bid
	auction.Status = IntentAuctionStatus_AuctionCompleted
	auction.WinningBidTxID = winningBid.BidTxID
	im.mgr.Keeper().SetIntentAuction(cacheCtx, auction)

	// Update intent status to expired (auction completed)
	intent.Status = IntentExpired
	im.mgr.Keeper().SetIntent(cacheCtx, intent)

	// All steps succeeded, commit all state changes atomically
	commit()
	ctx.EventManager().EmitEvents(cacheCtx.EventManager().Events())

	// Emit auction complete event
	evt := NewEventIntentAuctionComplete(
		auction.IntentTxID,
		winningBid.BidTxID,
		winningBid.BidderAddress,
		winningBid.BidAmount,
		auction.AuctionAmount,
		auction.RemainingAmount,
	)
	if err := im.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit auction complete event", "error", err)
	}

	ctx.Logger().Info("auction completed",
		"intent_txid", auction.IntentTxID,
		"winner", winningBid.BidderAddress,
		"winning_bid", winningBid.BidAmount.Amount,
		"auction_amount", auction.AuctionAmount.Amount,
		"owner_refund", auction.RemainingAmount.Amount,
	)

	return nil
}

// processWinningBid handles the winning bid - direct swap to RUNE and add to reserve.
// Uses a direct pool swap (not the swap queue) to prevent the bid from being refunded
// to the bidder on swap failure, which would let them win the auction for free.
func (im *IntentMgr) processWinningBid(ctx cosmos.Context, bid IntentAuctionBid) error {
	// If the bid is already in RUNE, transfer it from Asgard to Reserve
	// (bids are deposited to Asgard via handler_deposit.go)
	if bid.BidAmount.Asset.IsRune() {
		runeCoins := common.NewCoins(common.NewCoin(common.RuneAsset(), bid.BidAmount.Amount))
		if err := im.mgr.Keeper().SendFromModuleToModule(ctx, AsgardName, ReserveName, runeCoins); err != nil {
			return fmt.Errorf("fail to transfer winning RUNE bid from asgard to reserve: %w", err)
		}
		ctx.Logger().Info("transferred winning RUNE bid to reserve", "amount", bid.BidAmount.Amount)
		return nil
	}

	// For non-RUNE assets, perform a direct swap to RUNE and add to reserve.
	// This is the same approach used by processProtocolFee — directly updating
	// pool balances rather than queuing a swap. This prevents the failure mode
	// where a queued swap fails and refunds the bid back to the bidder,
	// letting them win the auction for free.
	bidPool, err := im.mgr.Keeper().GetPool(ctx, bid.BidAmount.Asset.GetLayer1Asset())
	if err != nil {
		return fmt.Errorf("fail to get pool for winning bid swap: %w", err)
	}
	if bidPool.IsEmpty() {
		return fmt.Errorf("no pool found for winning bid asset %s", bid.BidAmount.Asset)
	}

	swapper, err := GetSwapper(im.mgr.Keeper().GetVersion())
	if err != nil {
		return fmt.Errorf("fail to get swapper: %w", err)
	}

	// Calculate swap: bid asset -> RUNE
	X := bidPool.BalanceAsset
	x := bid.BidAmount.Amount
	Y := bidPool.BalanceRune

	slipBps := swapper.CalcSwapSlip(X, x)
	minSlipBps := cosmos.ZeroUint()
	emitRune, liquidityFee, _ := swapper.GetSwapCalc(X, x, Y, slipBps, minSlipBps)

	if emitRune.GTE(Y) {
		return fmt.Errorf("not enough RUNE in pool for winning bid swap")
	}

	// Update pool balances directly
	bidPool.BalanceAsset = bidPool.BalanceAsset.Add(x)
	bidPool.BalanceRune = common.SafeSub(bidPool.BalanceRune, emitRune)

	if err := im.mgr.Keeper().SetPool(ctx, bidPool); err != nil {
		return fmt.Errorf("fail to save pool after winning bid swap: %w", err)
	}

	// Transfer emitted RUNE from Asgard to Reserve
	if err := im.mgr.Keeper().AddPoolFeeToReserve(ctx, emitRune); err != nil {
		return fmt.Errorf("fail to add winning bid RUNE to reserve: %w", err)
	}

	// Emit swap event for the direct swap
	swapEvt := NewEventSwap(
		bidPool.Asset,
		cosmos.ZeroUint(),
		slipBps,
		liquidityFee,
		cosmos.ZeroUint(),
		common.Tx{
			ID:    bid.BidTxID,
			Chain: common.THORChain,
			Coins: common.NewCoins(bid.BidAmount),
		},
		common.NewCoin(common.RuneAsset(), emitRune),
		cosmos.ZeroUint(),
	)
	if err := im.mgr.EventMgr().EmitEvent(ctx, swapEvt); err != nil {
		ctx.Logger().Error("fail to emit winning bid swap event", "error", err)
	}

	ctx.Logger().Info("executed direct winning bid swap to RUNE for reserve",
		"bid_asset", bid.BidAmount.Asset,
		"bid_amount", bid.BidAmount.Amount,
		"emit_rune", emitRune,
	)

	return nil
}

// cancelAuction cancels an auction with no bids
// For native assets (secured, trade, RUNE), refund to owner
// For external chain assets, abandon funds from vault
func (im *IntentMgr) cancelAuction(ctx cosmos.Context, auction IntentAuction, intent Intent) error {
	sourceAsset := auction.AuctionAmount.Asset
	totalAmount := auction.AuctionAmount.Amount.Add(auction.RemainingAmount.Amount)
	totalCoin := common.NewCoin(sourceAsset, totalAmount)

	// Check if this is a native asset (RUNE, secured, or trade asset)
	// These don't live in vaults - they're in user bank accounts as cosmos coins
	isNativeAsset := sourceAsset.IsRune() || sourceAsset.IsSecuredAsset() || sourceAsset.IsTradeAsset()

	abandoned := false

	if isNativeAsset {
		// Refund native assets to the owner via txout
		// For RUNE, use Asgard module (where intent RUNE is escrowed)
		// For secured/trade assets, default to Asgard (they don't actually use module)
		moduleName := ""
		if sourceAsset.IsRune() {
			moduleName = AsgardName
		}

		toi := TxOutItem{
			Chain:      sourceAsset.GetChain(),
			ToAddress:  auction.OwnerAddress,
			Coin:       totalCoin,
			Memo:       fmt.Sprintf("REFUND:%s", auction.IntentTxID),
			InHash:     auction.IntentTxID,
			ModuleName: moduleName,
		}

		ok, err := im.mgr.TxOutStore().TryAddTxOutItem(ctx, im.mgr, toi, cosmos.ZeroUint())
		if err != nil {
			return fmt.Errorf("fail to schedule refund for cancelled auction: %w", err)
		}
		if !ok {
			return fmt.Errorf("fail to add refund txout for cancelled auction")
		}

		ctx.Logger().Info("scheduled refund for cancelled auction",
			"intent_txid", auction.IntentTxID,
			"amount", totalAmount,
			"to", auction.OwnerAddress,
		)
	} else {
		// Abandon external chain assets from vault
		vaults, err := im.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
		if err != nil {
			return fmt.Errorf("fail to get active vaults: %w", err)
		}
		if len(vaults) == 0 {
			return fmt.Errorf("no active vaults available")
		}

		// Find vault with sufficient balance
		var selectedVault *Vault
		var closestDiff cosmos.Uint
		for i := range vaults {
			vaultBalance := vaults[i].GetCoin(sourceAsset).Amount

			if vaultBalance.GTE(totalAmount) {
				diff := common.SafeSub(vaultBalance, totalAmount)

				// Select this vault if it's the first valid one, has a closer balance,
				// or has the same balance but lower PubKey (deterministic tiebreaker)
				if selectedVault == nil || diff.LT(closestDiff) ||
					(diff.Equal(closestDiff) && vaults[i].PubKey.String() < selectedVault.PubKey.String()) {
					selectedVault = &vaults[i]
					closestDiff = diff
				}
			}
		}

		if selectedVault == nil {
			return fmt.Errorf("no vault found with sufficient balance for asset %s (need %s)", sourceAsset, totalAmount)
		}

		// Subtract the abandoned amount from the vault's tracked balance.
		// NOTE: The actual on-chain L1 UTXOs/outputs remain in the vault's addresses.
		// This creates a known surplus (on-chain > tracked) which the solvency checker
		// will observe. During vault migrations (churn), these "orphaned" UTXOs are swept
		// along with legitimate funds. This is the standard THORChain approach for
		// non-refundable, non-pool assets and is consistent with how other abandon
		// scenarios work in the codebase.
		selectedVault.SubFunds(common.NewCoins(totalCoin))

		// Save the updated vault
		if err := im.mgr.Keeper().SetVault(ctx, *selectedVault); err != nil {
			return fmt.Errorf("fail to save vault: %w", err)
		}

		abandoned = true

		ctx.Logger().Info("cancelled auction with no bids, funds abandoned",
			"intent_txid", auction.IntentTxID,
			"amount", totalAmount,
			"vault", selectedVault.PubKey,
		)
	}

	// Update auction status
	auction.Status = IntentAuctionStatus_AuctionCancelled
	im.mgr.Keeper().SetIntentAuction(ctx, auction)

	// Update intent status
	intent.Status = IntentExpired
	im.mgr.Keeper().SetIntent(ctx, intent)

	// Emit cancel event
	evt := NewEventIntentAuctionCancel(auction.IntentTxID, abandoned)
	if err := im.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit auction cancel event", "error", err)
	}

	return nil
}
