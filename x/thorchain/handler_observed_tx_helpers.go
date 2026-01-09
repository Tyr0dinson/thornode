package thorchain

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// CreateMempoolRevertTxID creates a deterministic reverse TxID from original TxID + action index.
// The reverse TxID is created by reversing the hex string and XORing the last byte with the action index.
// This allows the original TxID to be recovered without needing persistent storage.
func CreateMempoolRevertTxID(originalTxID common.TxID, actionIndex int) common.TxID {
	s := originalTxID.String()
	if len(s) != 64 {
		return originalTxID // fallback for invalid TxID
	}
	// Reverse the string
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	// XOR last byte with action index to ensure uniqueness per action
	lastByte, _ := strconv.ParseUint(string(runes[62:64]), 16, 8)
	lastByte ^= uint64(actionIndex)
	return common.TxID(string(runes[:62]) + fmt.Sprintf("%02X", lastByte))
}

// GetOriginalFromMempoolRevertTxID recovers the original TxID from a mempool revert TxID.
// It tries action indices 0-15 and returns the first match found in the keeper.
func GetOriginalFromMempoolRevertTxID(ctx cosmos.Context, k keeper.Keeper, reverseTxID common.TxID) (common.TxID, bool) {
	s := reverseTxID.String()
	if len(s) != 64 {
		return common.TxID(""), false
	}
	lastByte, err := strconv.ParseUint(s[62:64], 16, 8)
	if err != nil {
		return common.TxID(""), false
	}

	for actionIndex := 0; actionIndex < 16; actionIndex++ {
		// XOR to undo the action index encoding
		originalLastByte := lastByte ^ uint64(actionIndex)
		candidate := s[:62] + fmt.Sprintf("%02X", originalLastByte)

		// Reverse the string
		runes := []rune(candidate)
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		originalTxID := common.TxID(string(runes))

		// Check if voter exists
		if _, err := k.GetObservedTxInVoter(ctx, originalTxID); err == nil {
			return originalTxID, true
		}
	}
	return common.TxID(""), false
}

// processTxInAttestation processes a single attestation for an observed tx.
// This is used by both MsgObservedTxIn (single attester) and MsgObservedTxInQuorum (multiple attesters).
func processTxInAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	nas NodeAccounts,
	tx ObservedTx,
	signer cosmos.AccAddress,
	shouldSlashForDuplicate bool,
) (ObservedTxVoter, bool) {
	k := mgr.Keeper()
	slasher := mgr.Slasher()

	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_txin"),
		telemetry.NewLabel("chain", string(tx.Tx.Chain)),
	}))
	slashCtx = ctx.WithContext(context.WithValue(slashCtx.Context(), constants.CtxObservedTx, tx.Tx.ID.String()))

	ok := false
	if err := k.SetLastObserveHeight(ctx, tx.Tx.Chain, signer, tx.BlockHeight); err != nil {
		ctx.Logger().Error("fail to save last observe height", "error", err, "signer", signer, "chain", tx.Tx.Chain)
	}

	// As an observation requires processing by all nodes no matter what,
	// any observation should increment ObserveSlashPoints,
	// to be decremented only if contributing to or within ObservationDelayFlexibility of consensus.
	slasher.IncSlashPoints(slashCtx, observeSlashPoints, signer)

	if !voter.Add(tx, signer) {
		if !shouldSlashForDuplicate {
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
		}
		// A duplicate message, so do nothing further.
		return voter, ok
	}
	if voter.HasFinalised(nas) {
		if voter.FinalisedHeight == 0 {
			ok = true
			voter.Height = ctx.BlockHeight() // Always record the consensus height of the finalised Tx
			if voter.UnfinalizedHeight == 0 {
				voter.UnfinalizedHeight = voter.Height // Preserve first consensus height
			}
			voter.FinalisedHeight = ctx.BlockHeight()
			voter.Tx = *voter.GetTx(nas)

			// This signer brings the voter to consensus;
			// decrement all the signers' slash points and increment the non-signers' slash points.
			signers := voter.GetConsensusSigners()
			nonSigners := getNonSigners(nas, signers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
		} else if ctx.BlockHeight() <= (voter.FinalisedHeight+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			voter.Tx.Tx.EqualsEx(tx.Tx) &&
			!voter.Tx.HasSigned(signer) {
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so still take away their slash points
			// but only when the tx signer are voting is the tx that already reached consensus
			slasher.DecSlashPoints(slashCtx, observeSlashPoints+lackOfObservationPenalty, signer)
		}
	}
	if !ok && voter.HasConsensus(nas) && !tx.IsFinal() && voter.FinalisedHeight == 0 {
		if voter.Height == 0 {
			ok = true
			voter.Height = ctx.BlockHeight()
			if voter.UnfinalizedHeight == 0 {
				voter.UnfinalizedHeight = voter.Height // Preserve first consensus height
			}
			// this is the tx that has consensus
			voter.Tx = *voter.GetTx(nas)

			// This signer brings the voter to consensus;
			// decrement all the signers' slash points and increment the non-signers' slash points.
			signers := voter.GetConsensusSigners()
			nonSigners := getNonSigners(nas, signers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
		} else if ctx.BlockHeight() <= (voter.Height+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			voter.Tx.Tx.EqualsEx(tx.Tx) &&
			!voter.Tx.HasSigned(signer) {
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so still take away their slash points
			// but only when the tx signer are voting is the tx that already reached consensus
			slasher.DecSlashPoints(slashCtx, observeSlashPoints+lackOfObservationPenalty, signer)
		}
	}

	k.SetObservedTxInVoter(ctx, voter)

	// Check to see if we have enough identical observations to process the transaction
	return voter, ok
}

// ensureVaultAndGetTxInVoter will make sure the vault exists, then get the ObservedTxInVoter from the store.
// if it doesn't exist, it will create a new one.
func ensureVaultAndGetTxInVoter(ctx cosmos.Context, vaultPubKey common.PubKey, txID common.TxID, k keeper.Keeper) (ObservedTxVoter, error) {
	// check we are sending to a valid vault
	if !k.VaultExists(ctx, vaultPubKey) {
		ctx.Logger().Info("Not valid Observed Pubkey", "observed pub key", vaultPubKey)
		return ObservedTxVoter{}, fmt.Errorf("vault not found for observed tx in pubkey: %s", vaultPubKey)
	}

	voter, err := k.GetObservedTxInVoter(ctx, txID)
	if err != nil {
		return ObservedTxVoter{}, fmt.Errorf("fail to get tx in voter: %w", err)
	}

	return voter, nil
}

// handleObservedTxInQuorum - will process the observed tx in quorum.
// used by both MsgObservedTxIn and MsgObservedTxInQuorum after processing
// attestation(s).
func handleObservedTxInQuorum(
	ctx cosmos.Context,
	mgr Manager,
	signer cosmos.AccAddress,
	activeNodeAccounts NodeAccounts,
	handler cosmos.Handler,
	tx common.ObservedTx,
	voter ObservedTxVoter,
	observers []cosmos.AccAddress,
	isQuorum bool,
) error {
	if !isQuorum {
		if voter.Height == ctx.BlockHeight() || voter.FinalisedHeight == ctx.BlockHeight() {
			// we've already process the transaction, but we should still
			// update the observing addresses
			mgr.ObMgr().AppendObserver(tx.Tx.Chain, observers)
		}
		return nil
	}

	// all logic after this is upon consensus

	if voter.Reverted {
		ctx.Logger().Info("tx had been reverted", "Tx", tx.String())
		return nil
	}

	k := mgr.Keeper()

	vault, err := k.GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
		return nil
	}

	voter.Tx.Tx.Memo = tx.Tx.Memo

	// add memo for memoless transactions (after consensus is reached)
	if len(tx.Tx.Coins) > 0 {
		// Use the asset from the first coin for reference memo lookup
		refAsset := tx.Tx.Coins[0].Asset

		// Generate reference memo for memoless transactions
		if strings.TrimSpace(voter.Tx.Tx.Memo) == "" {
			var referenceID string
			referenceID, err = generateReferenceMemoID(ctx, mgr, refAsset, tx)
			if err != nil {
				ctx.Logger().Error("failed to generate reference memo", "error", err, "txid", tx.Tx.ID.String())
				// Continue without reference memo - will likely be refunded later due to empty memo
			} else {
				// Use ReferenceReadMemo's CreateMemo function
				// Note: Reference memo expiry and duplicate usage validation is handled later in fetchMemoFromReference
				refMemo := NewReferenceReadMemo(referenceID)
				voter.Tx.Tx.Memo = refMemo.CreateMemo()
			}
		}

	}

	// memo errors are ignored here and will be caught later in processing,
	// after vault update, voter setup, etc and the coin will be refunded
	memo, _ := ParseMemoWithTHORNames(ctx, k, voter.Tx.Tx.Memo)

	hasFinalised := voter.HasFinalised(activeNodeAccounts)

	// Allow swap execution at consensus (before finalization) for mempool swap optimization.
	// Only streaming swaps (StreamQuantity == 0) can be executed from mempool.
	// Outbounds won't be signed until the inbound finalizes.
	mempoolSwapCap := cosmos.NewUint(uint64(k.GetConfigInt64(ctx, constants.MempoolSwapCapTOR)))
	isStreamingSwap := false
	if swapMemo, ok := memo.(SwapMemo); ok {
		isStreamingSwap = swapMemo.GetStreamQuantity() == 0
	}
	canProcessMempoolSwap := !mempoolSwapCap.IsZero() && memo.IsType(TxSwap) && isStreamingSwap

	// Update vault balances from inbounds with Migrate memos immediately,
	// to minimise any gap between outbound and inbound observations.
	// TODO: In future somehow update both balances in a single action,
	// so the ActiveVault balance increase is guaranteed to never be early nor late?
	if hasFinalised || memo.IsType(TxMigrate) {
		if vault.IsAsgard() && !voter.UpdatedVault {
			if !tx.Tx.FromAddress.Equals(tx.Tx.ToAddress) {
				// Don't add to or subtract from vault balances when the sender and recipient are the same
				// (particularly avoid Consolidate SafeSub zeroing of vault balances).
				vault.AddFunds(tx.Tx.Coins)
				vault.InboundTxCount++
			}
			voter.UpdatedVault = true
		}
	}
	// Only update chain height when transaction is finalized, not from mempool observations.
	// Mempool transactions can arrive out of order relative to their external chain block heights,
	// and SetLastChainHeight rejects attempts to set a lower height than already stored.
	if hasFinalised {
		if err = k.SetLastChainHeight(ctx, tx.Tx.Chain, tx.BlockHeight); err != nil {
			ctx.Logger().Error("fail to set last chain height", "error", err)
		}
	}

	// save the changes in Tx Voter to key value store
	k.SetObservedTxInVoter(ctx, voter)
	if err = k.SetVault(ctx, vault); err != nil {
		ctx.Logger().Error("fail to set vault", "error", err)
		return nil
	}

	if !vault.IsAsgard() {
		ctx.Logger().Info("Vault is not an Asgard vault, transaction ignored.")
		return nil
	}

	if memo.IsOutbound() || memo.IsInternal() {
		// do not process outbound handlers here, or internal handlers
		return nil
	}

	// add addresses to observing addresses. This is used to detect
	// active/inactive observing node accounts

	mgr.ObMgr().AppendObserver(tx.Tx.Chain, voter.Tx.GetSigners())

	if !hasFinalised && !canProcessMempoolSwap {
		ctx.Logger().Info("transaction pending confirmation counting", "hash", voter.TxID)
		return nil
	}

	if !hasFinalised && canProcessMempoolSwap {
		// Check if there's capacity for this mempool swap
		sourceAsset := voter.Tx.Tx.Coins[0].Asset
		sourceAmount := voter.Tx.Tx.Coins[0].Amount
		var torValue cosmos.Uint
		var hasCapacity bool
		torValue, hasCapacity, err = checkMempoolSwapCapacity(ctx, k, sourceAsset, sourceAmount)
		switch {
		case err != nil:
			ctx.Logger().Info("fail to check mempool swap capacity, waiting for finalization",
				"error", err, "hash", voter.TxID)
			canProcessMempoolSwap = false
		case !hasCapacity:
			ctx.Logger().Info("mempool swap cap exceeded, waiting for finalization",
				"current_usage", k.GetMempoolSwapUsageTOR(ctx),
				"swap_tor", torValue,
				"cap", mempoolSwapCap,
				"hash", voter.TxID)
			canProcessMempoolSwap = false
		default:
			// Reserve capacity: add to usage and store TOR value on voter
			newUsage := k.AddMempoolSwapUsageTOR(ctx, torValue)
			voter.MempoolSwapTOR = torValue
			k.SetObservedTxInVoter(ctx, voter)
			ctx.Logger().Info("processing mempool swap before finalization",
				"hash", voter.TxID,
				"tor_value", torValue,
				"new_usage", newUsage,
				"cap", mempoolSwapCap,
				"memo", tx.Tx.Memo)
		}

		// If we can't process as mempool swap, return and wait for finalization
		if !canProcessMempoolSwap {
			return nil
		}
	}

	// When a mempool swap finalizes, update all associated outbound TxOutItems to Finalized=true
	// and return early - the swap was already executed from mempool, don't process it again
	if hasFinalised && len(voter.Actions) > 0 {
		if err = finalizeOutbounds(ctx, mgr, voter); err != nil {
			ctx.Logger().Error("fail to finalize outbounds", "error", err, "hash", voter.TxID)
		}
		ctx.Logger().Info("mempool swap already processed, finalizing only", "hash", voter.TxID)
		return nil
	}

	ctx.Logger().Debug("tx in finalized and has consensus",
		"id", tx.Tx.ID.String(),
		"chain", tx.Tx.Chain.String(),
		"height", tx.BlockHeight,
		"from", tx.Tx.FromAddress.String(),
		"to", tx.Tx.ToAddress.String(),
		"memo", tx.Tx.Memo,
		"coins", tx.Tx.Coins.String(),
		"gas", common.Coins(tx.Tx.Gas).String(),
		"observed_vault_pubkey", tx.ObservedPubKey.String(),
	)

	if vault.Status == InactiveVault {
		ctx.Logger().Error("observed tx on inactive vault", "tx", tx.String())
		if newErr := refundTx(ctx, tx, mgr, CodeInvalidVault, "observed inbound tx to an inactive vault", ""); newErr != nil {
			ctx.Logger().Error("fail to refund", "error", newErr)
		}
		return nil
	}

	// Resolve reference memo if needed before processing
	if len(tx.Tx.Coins) > 0 {
		asset := tx.Tx.Coins[0].Asset
		resolvedMemo := fetchMemoFromReference(ctx, mgr, asset, voter.Tx.Tx, voter.UnfinalizedHeight)
		preMemo := voter.Tx.Tx.Memo
		voter.Tx.Tx.Memo = resolvedMemo
		ctx.Logger().Info("reference memo conversion", "pre", preMemo, "post", voter.Tx.Tx.Memo, "asset", asset)
		k.SetObservedTxInVoter(ctx, voter)
	}

	// construct msg from memo
	m, txErr := processOneTxIn(ctx, k, voter.Tx, signer)
	if txErr != nil {
		ctx.Logger().Error("fail to process inbound tx", "error", txErr.Error(), "tx hash", tx.Tx.ID.String())
		if newErr := refundTx(ctx, tx, mgr, CodeInvalidMemo, txErr.Error(), ""); nil != newErr {
			ctx.Logger().Error("fail to refund", "error", newErr)
		}
		return nil
	}

	// check if we've halted trading
	swapMsg, isSwap := m.(*MsgSwap)
	_, isAddLiquidity := m.(*MsgAddLiquidity)

	if isSwap || isAddLiquidity {
		if k.IsTradingHalt(ctx, m) || k.RagnarokInProgress(ctx) {
			if newErr := refundTx(ctx, tx, mgr, se.ErrUnauthorized.ABCICode(), "trading halted", ""); nil != newErr {
				ctx.Logger().Error("fail to refund for halted trading", "error", newErr)
			}
			return nil
		}
	}

	// if its a swap, send it to our queue for processing later
	if isSwap {
		if err = addSwap(ctx, mgr, *swapMsg); err != nil {
			if refundErr := refundTx(ctx, tx, mgr, CodeSwapFail, err.Error(), ""); refundErr != nil {
				ctx.Logger().Error("fail to refund swap", "error", refundErr)
				return fmt.Errorf("swap failed and refund also failed: %w, refund error: %v", err, refundErr)
			}
		}
		return nil
	}

	mCtx := ctx

	// Check and block switch assets
	// Check is independent of the mimir to enable the handler in order to support
	// bifrost & switch whitelisting prior to switching commencing
	_, isSwitch := m.(*MsgSwitch)
	if !isSwitch && len(tx.Tx.Coins) > 0 && mgr.SwitchManager().IsSwitch(ctx, tx.Tx.Coins[0].Asset) {
		if err = refundTx(ctx, tx, mgr, CodeTxFail, "asset is a switch asset", ""); err != nil {
			ctx.Logger().Error("fail to refund", "error", err)
		}

		return nil
	}

	_, err = handler(mCtx, m)
	if err != nil {
		if err = refundTx(ctx, tx, mgr, CodeTxFail, err.Error(), ""); err != nil {
			return fmt.Errorf("fail to refund: %w", err)
		}
		return nil
	}

	// if an outbound is not expected, mark the voter as done
	if !memo.GetType().HasOutbound() {
		// retrieve the voter from store in case the handler caused a change
		voter, err = k.GetObservedTxInVoter(ctx, tx.Tx.ID)
		if err != nil {
			return fmt.Errorf("fail to get voter")
		}
		voter.SetDone()
		k.SetObservedTxInVoter(ctx, voter)
	}

	ctx.Logger().Info("tx in processed", "chain", tx.Tx.Chain, "id", tx.Tx.ID, "finalized", tx.IsFinal())

	return nil
}

// processTxOutAttestation processes a single attestation for an observed tx.
// This is used by both MsgObservedTxOut (single attester) and MsgObservedTxOutQuorum (multiple attesters).
func processTxOutAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	nas NodeAccounts,
	tx ObservedTx,
	signer cosmos.AccAddress,
	shouldSlashForDuplicate bool,
) (ObservedTxVoter, bool) {
	k := mgr.Keeper()
	slasher := mgr.Slasher()

	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	ok := false

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_txout"),
		telemetry.NewLabel("chain", string(tx.Tx.Chain)),
	}))
	slashCtx = ctx.WithContext(context.WithValue(slashCtx.Context(), constants.CtxObservedTx, tx.Tx.ID.String()))

	if err := k.SetLastObserveHeight(ctx, tx.Tx.Chain, signer, tx.BlockHeight); err != nil {
		ctx.Logger().Error("fail to save last observe height", "error", err, "signer", signer, "chain", tx.Tx.Chain)
	}

	// As an observation requires processing by all nodes no matter what,
	// any observation should increment ObserveSlashPoints,
	// to be decremented only if contributing to or within ObservationDelayFlexibility of consensus.
	slasher.IncSlashPoints(slashCtx, observeSlashPoints, signer)

	if !voter.Add(tx, signer) {
		if !shouldSlashForDuplicate {
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
		}
		// A duplicate message, so do nothing further.
		return voter, ok
	}

	// Outbound memos can have | data passthrough,
	// so linked TxID extracted with memo parsing and GetTxID
	// rather than strings.Split .
	if memo, err := ParseMemoWithTHORNames(ctx, k, tx.Tx.Memo); err != nil {
		ctx.Logger().Error("fail to parse outbound memo", "error", err, "memo", tx.Tx.Memo)
	} else if inhash := memo.GetTxID(); !inhash.IsEmpty() {
		k.SetObservedLink(ctx, inhash, tx.Tx.ID)
	}

	if voter.HasFinalised(nas) {
		if voter.FinalisedHeight == 0 {
			if voter.Height == 0 {
				ok = true
				// Record the consensus height at which outbound consensus actions are taken.
				voter.Height = ctx.BlockHeight()
				if voter.UnfinalizedHeight == 0 {
					voter.UnfinalizedHeight = voter.Height // Preserve first consensus height
				}
			}
			voter.FinalisedHeight = ctx.BlockHeight()
			voter.Tx = *voter.GetTx(nas)

			ctx.Logger().Debug("tx out finalized and has consensus",
				"id", tx.Tx.ID.String(),
				"chain", tx.Tx.Chain.String(),
				"height", tx.BlockHeight,
				"from", tx.Tx.FromAddress.String(),
				"to", tx.Tx.ToAddress.String(),
				"memo", tx.Tx.Memo,
				"coins", tx.Tx.Coins.String(),
				"gas", common.Coins(tx.Tx.Gas).String(),
				"observed_vault_pubkey", tx.ObservedPubKey.String(),
			)

			// This signer brings the voter to consensus;
			// decrement all the signers' slash points and increment the non-signers' slash points.
			signers := voter.GetConsensusSigners()
			nonSigners := getNonSigners(nas, signers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
		} else if ctx.BlockHeight() <= (voter.FinalisedHeight+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			voter.Tx.Tx.EqualsEx(tx.Tx) &&
			!voter.Tx.HasSigned(signer) {
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so we still take away their slash points
			slasher.DecSlashPoints(slashCtx, observeSlashPoints+lackOfObservationPenalty, signer)
		}

		// Gas correction for re-org re-observations.
		// When a voter is finalized but a re-observation has the same tx except gas
		// (likely due to a reorg changing the effective gas price), check if enough
		// nodes agree on the corrected gas to update vault accounting.
		if voter.FinalisedHeight > 0 &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			!voter.Tx.Tx.Gas.Equals(tx.Tx.Gas) &&
			voter.Tx.Tx.EqualsExIgnoreGas(tx.Tx) {
			gasCorrectionSigners := countMatchingSigners(voter, tx, nas)
			if HasSuperMajority(gasCorrectionSigners, len(nas)) {
				correctOutboundGas(ctx, mgr, voter.Tx, tx)
				voter.Tx.Tx.Gas = tx.Tx.Gas
			}
		}
	}
	if !ok && voter.HasConsensus(nas) && !tx.IsFinal() && voter.FinalisedHeight == 0 {
		if voter.Height == 0 {
			ok = true
			// Record the consensus height at which outbound consensus actions are taken,
			// even if not yet Finalised.
			voter.Height = ctx.BlockHeight()
			if voter.UnfinalizedHeight == 0 {
				voter.UnfinalizedHeight = voter.Height // Preserve first consensus height
			}
			// this is the tx that has consensus
			voter.Tx = *voter.GetTx(nas)

			// This signer brings the voter to consensus;
			// decrement all the signers' slash points and increment the non-signers' slash points.
			signers := voter.GetConsensusSigners()
			nonSigners := getNonSigners(nas, signers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
		} else if ctx.BlockHeight() <= (voter.Height+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			voter.Tx.Tx.EqualsEx(tx.Tx) &&
			!voter.Tx.HasSigned(signer) {
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so still take away their slash points
			// but only when the tx signer are voting is the tx that already reached consensus
			slasher.DecSlashPoints(slashCtx, observeSlashPoints+lackOfObservationPenalty, signer)
		}

		// Gas correction for re-org re-observations (non-finalized consensus path).
		if voter.Height > 0 &&
			!voter.Tx.Tx.Gas.Equals(tx.Tx.Gas) &&
			voter.Tx.Tx.EqualsExIgnoreGas(tx.Tx) {
			gasCorrectionSigners := countMatchingSigners(voter, tx, nas)
			if HasSuperMajority(gasCorrectionSigners, len(nas)) {
				correctOutboundGas(ctx, mgr, voter.Tx, tx)
				voter.Tx.Tx.Gas = tx.Tx.Gas
			}
		}
	}

	k.SetObservedTxOutVoter(ctx, voter)

	// Check to see if we have enough identical observations to process the transaction
	return voter, ok
}

// countMatchingSigners counts the number of unique active node signers that have observed
// the exact same tx (including gas) as the given observation.
func countMatchingSigners(voter ObservedTxVoter, tx ObservedTx, nas NodeAccounts) int {
	signers := make(map[string]struct{})
	for _, otherTx := range voter.Txs {
		if otherTx.IsFinal() != tx.IsFinal() {
			continue
		}
		if !otherTx.Tx.EqualsEx(tx.Tx) {
			continue
		}
		for _, s := range otherTx.GetSigners() {
			if !nas.IsNodeKeys(s) {
				continue
			}
			signers[s.String()] = struct{}{}
		}
	}
	return len(signers)
}

// correctOutboundGas adjusts a vault's gas accounting when a reorg results in
// a different gas amount for the same outbound transaction. It computes the delta
// between the original consensus gas and the corrected gas, then adjusts the vault
// balance and gas manager accordingly.
func correctOutboundGas(ctx cosmos.Context, mgr Manager, consensusTx common.ObservedTx, correctedTx ObservedTx) {
	oldGas := consensusTx.Tx.Gas
	newGas := correctedTx.Tx.Gas

	vault, err := mgr.Keeper().GetVault(ctx, correctedTx.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault for gas correction", "error", err)
		return
	}

	// Calculate gas delta per asset and adjust vault balance.
	var additionalGas common.Gas
	for _, newGasCoin := range newGas {
		oldAmount := oldGas.ToCoins().GetCoin(newGasCoin.Asset).Amount
		if newGasCoin.Amount.GT(oldAmount) {
			// More gas was actually spent - deduct additional from vault.
			delta := common.NewCoin(newGasCoin.Asset, newGasCoin.Amount.Sub(oldAmount))
			vault.SubFunds(common.NewCoins(delta))
			additionalGas = additionalGas.Add(delta)
		} else if oldAmount.GT(newGasCoin.Amount) {
			// Less gas was actually spent - credit back to vault.
			delta := common.NewCoin(newGasCoin.Asset, oldAmount.Sub(newGasCoin.Amount))
			vault.AddFunds(common.NewCoins(delta))
		}
	}

	if err := mgr.Keeper().SetVault(ctx, vault); err != nil {
		ctx.Logger().Error("fail to save vault after gas correction", "error", err)
		return
	}

	// If more gas was spent, add the delta to the gas manager for reserve reimbursement,
	// skipping reimbursement for inactive vaults and during ragnarok (matching addGasFees behavior).
	if !additionalGas.IsEmpty() {
		if vault.Status == InactiveVault {
			return
		}
		if mgr.Keeper().RagnarokInProgress(ctx) {
			gasAsset := correctedTx.Tx.Chain.GetGasAsset()
			if !correctedTx.Tx.Coins.GetCoin(gasAsset).IsEmpty() {
				return
			}
		}
		outAsset := common.EmptyAsset
		if len(correctedTx.Tx.Coins) != 0 {
			outAsset = correctedTx.Tx.Coins[0].Asset
		}
		mgr.GasMgr().AddGasAsset(outAsset, additionalGas, false)
	}

	ctx.Logger().Info("corrected outbound gas after reorg",
		"txid", correctedTx.Tx.ID,
		"old_gas", common.Coins(oldGas).String(),
		"new_gas", common.Coins(newGas).String(),
	)
}

// ensureVaultAndGetTxOutVoter will make sure the vault exists, then get the ObservedTxOutVoter from the store.
// if it doesn't exist, it will create a new one.
func ensureVaultAndGetTxOutVoter(ctx cosmos.Context, k keeper.Keeper, vaultPubKey common.PubKey, txID common.TxID, observers []cosmos.AccAddress, keysignMs int64) (ObservedTxVoter, error) {
	// check we are sending from a valid vault
	if !k.VaultExists(ctx, vaultPubKey) {
		ctx.Logger().Info("Not valid Observed Pubkey", "observed pub key", vaultPubKey)
		return ObservedTxVoter{}, fmt.Errorf("vault not found for observed tx out pubkey: %s", vaultPubKey)
	}

	if keysignMs > 0 {
		keysignMetric, err := k.GetTssKeysignMetric(ctx, txID)
		if err != nil {
			ctx.Logger().Error("fail to get keysign metric", "error", err)
		} else {
			for _, o := range observers {
				keysignMetric.AddNodeTssTime(o, keysignMs)
			}
			k.SetTssKeysignMetric(ctx, keysignMetric)
		}
	}

	voter, err := k.GetObservedTxOutVoter(ctx, txID)
	if err != nil {
		return ObservedTxVoter{}, fmt.Errorf("fail to get tx out voter: %w", err)
	}

	return voter, nil
}

// handleObservedTxOutQuorum - will process the observed tx out quorum.
// used by both MsgObservedTxOut and MsgObservedTxOutQuorum after processing
// attestation(s).
func handleObservedTxOutQuorum(
	ctx cosmos.Context,
	mgr Manager,
	signer cosmos.AccAddress,
	activeNodeAccounts NodeAccounts,
	handler cosmos.Handler,
	tx common.ObservedTx,
	voter ObservedTxVoter,
	observers []cosmos.AccAddress,
	isQuorum bool,
) error {
	// check whether the tx has consensus
	if !isQuorum {
		if voter.Height == ctx.BlockHeight() || voter.FinalisedHeight == ctx.BlockHeight() {
			// we've already process the transaction, but we should still
			// update the observing addresses
			mgr.ObMgr().AppendObserver(tx.Tx.Chain, observers)
		}
		return nil
	}

	k := mgr.Keeper()

	if isCancelOrApprovalTx(tx) {
		ctx.Logger().Info("skipping slash for cancel tx with empty memo", "txid", tx.Tx.ID)
		// Credit gas to gas manager and deduct from vault (legit operational spend, no penalty)
		// This also adds to fee_spent_rune for dynamic outbound fee calculation
		if err := addGasFees(ctx, mgr, tx); err != nil {
			ctx.Logger().Error("fail to add gas fees for cancel tx", "error", err)
		}
		return nil
	}

	// if memo isn't valid or its an inbound memo, slash the vault
	memo, _ := ParseMemoWithTHORNames(ctx, k, tx.Tx.Memo)
	if memo.IsInbound() {
		vault, err := k.GetVault(ctx, tx.ObservedPubKey)
		if err != nil {
			ctx.Logger().Error("fail to get vault", "error", err)
			return nil
		}
		toSlash := make(common.Coins, len(tx.Tx.Coins))
		copy(toSlash, tx.Tx.Coins)
		toSlash = toSlash.Add(tx.Tx.Gas.ToCoins()...)

		slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
			telemetry.NewLabel("reason", "sent_extra_funds"),
			telemetry.NewLabel("chain", string(tx.Tx.Chain)),
		}))

		if err := mgr.Slasher().SlashVault(slashCtx, tx.ObservedPubKey, toSlash, mgr); err != nil {
			ctx.Logger().Error("fail to slash account for sending extra fund", "error", err)
		}
		vault.SubFunds(toSlash)
		if err := k.SetVault(ctx, vault); err != nil {
			ctx.Logger().Error("fail to save vault", "error", err)
		}

		return nil
	}

	// Process the handler if we can get the consensus tx.
	// Even if the handler fails, vault accounting must still happen because
	// the outbound transaction is irrevocable (funds already left the vault).
	vaultSlash := false
	txOut := voter.GetTx(activeNodeAccounts) // get consensus tx, in case our for loop is incorrect
	if txOut == nil || txOut.IsEmpty() {
		ctx.Logger().Error("fail to get consensus tx from voter", "txid", tx.Tx.ID)
		// Continue with vault accounting below - outbound is irrevocable
	} else {
		txOut.Tx.Memo = tx.Tx.Memo

		// For memoless outbounds, look up the original memo from the scheduled TxOutItem
		if txOut.Tx.Memo == "" {
			if originalMemo := findOriginalMemoForOutbound(ctx, mgr, tx); originalMemo != "" {
				txOut.Tx.Memo = originalMemo
				ctx.Logger().Info("resolved memoless outbound", "original_memo", originalMemo, "txid", tx.Tx.ID)
			}
		}

		if tx.Tx.Chain.IsEmpty() {
			ctx.Logger().Error("fail to process txOut: chain is empty", "tx", tx.Tx.String())
		} else if m, mErr := processOneTxIn(ctx, k, *txOut, signer); mErr != nil {
			ctx.Logger().Error("fail to process txOut",
				"error", mErr,
				"tx", tx.Tx.String())
		} else {
			// add addresses to observing addresses. This is used to detect
			// active/inactive observing node accounts
			mgr.ObMgr().AppendObserver(tx.Tx.Chain, txOut.GetSigners())

			// emit tss keysign metrics
			if tx.KeysignMs > 0 {
				keysignMetric, kmErr := k.GetTssKeysignMetric(ctx, tx.Tx.ID)
				if kmErr != nil {
					ctx.Logger().Error("fail to get tss keysign metric", "error", kmErr, "hash", tx.Tx.ID)
				} else {
					evt := NewEventTssKeysignMetric(keysignMetric.TxID, keysignMetric.GetMedianTime())
					if emitErr := mgr.EventMgr().EmitEvent(ctx, evt); emitErr != nil {
						ctx.Logger().Error("fail to emit tss metric event", "error", emitErr)
					}
				}
			}

			res, hErr := handler(ctx, m)
			if hErr != nil {
				ctx.Logger().Error("handler failed:", "error", hErr)
				// Continue with vault accounting - outbound is irrevocable
			} else if res != nil && res.Events != nil {
				for _, ev := range res.Events {
					if ev.Type == "vault-slash" {
						vaultSlash = true
						break
					}
				}
			}
		}
	}

	// Vault accounting - always runs because outbound is irrevocable.
	// Gas and coin deductions must happen regardless of handler success.

	// only deduct gas fee via the manager if there was not a vault slash that covered it
	if !vaultSlash {
		if err := addGasFees(ctx, mgr, tx); err != nil {
			ctx.Logger().Error("fail to add gas fee", "error", err)
		}
	}

	// If sending from one of our vaults, decrement coins
	vault, err := k.GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
	} else {
		// if the vault was slashed we skipped the gas manager above and deduct gas directly
		if vaultSlash {
			vault.SubFunds(tx.Tx.Gas.ToCoins())
		}

		// Don't add to or subtract from vault balances when the sender and recipient are the same
		// (particularly avoid Consolidate SafeSub zeroing of vault balances).
		if !tx.Tx.FromAddress.Equals(tx.Tx.ToAddress) {
			// skip deducting funds for outbound fake gas
			if !isOutboundFakeGasTx(tx) {
				vault.SubFunds(tx.Tx.Coins)
			}
			vault.OutboundTxCount++
		}

		if vault.IsAsgard() && memo.IsType(TxMigrate) {
			// only remove the block height that had been specified in the memo
			vault.RemovePendingTxBlockHeights(memo.GetBlockHeight())
		}

		if !vault.HasFunds() && vault.Status == RetiringVault {
			// we have successfully removed all funds from a retiring vault,
			// mark it as inactive
			vault.UpdateStatus(InactiveVault, ctx.BlockHeight())
		}
		// if the vault is frozen, then unfreeze it. Since we saw that a
		// transaction was signed
		for _, coin := range tx.Tx.Coins {
			for i := range vault.Frozen {
				if strings.EqualFold(coin.Asset.GetChain().String(), vault.Frozen[i]) {
					vault.Frozen = append(vault.Frozen[:i], vault.Frozen[i+1:]...)
					break
				}
			}
		}
		if err := k.SetVault(ctx, vault); err != nil {
			ctx.Logger().Error("fail to save vault", "error", err)
		}
	}

	// Mark voter as done AFTER all vault operations to prevent
	// inconsistency if vault ops fail.
	voter.SetDone()
	k.SetObservedTxOutVoter(ctx, voter)

	ctx.Logger().Info("tx out processed", "chain", tx.Tx.Chain, "id", tx.Tx.ID, "finalized", tx.IsFinal())

	return nil
}

// matchedOutbound holds a matching TxOutItem with its height and hash for sorting
type matchedOutbound struct {
	height int64
	item   TxOutItem
	hash   string
}

// findOriginalMemoForOutbound searches the TxOut queue for a matching scheduled outbound
// and returns its OriginalMemo. This is needed for memoless outbounds where the on-chain
// memo is empty but we need the original memo to properly link the outbound to its inbound.
//
// When multiple TxOutItems match (e.g., two outbounds with identical on-chain fields but
// different memos), this function uses deterministic ordering matching Bifrost's signer
// (see bifrost/signer/storage.go:200-204) to ensure THORNode picks the same one Bifrost signed.
func findOriginalMemoForOutbound(ctx cosmos.Context, mgr Manager, tx common.ObservedTx) string {
	// Check coins early to avoid unnecessary iteration
	if len(tx.Tx.Coins) == 0 {
		return ""
	}

	k := mgr.Keeper()
	signingTransPeriod := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod)

	// Search recent TxOut blocks for matching TxOutItem
	// Use the same range logic as handler_common_outbound.go
	earliestHeight := ctx.BlockHeight() - signingTransPeriod
	if earliestHeight < 1 {
		earliestHeight = 1
	}

	// A TxOutItem might be rescheduled (by LackSigning) rounded up to nearest multiple of RescheduleCoalesceBlocks,
	// so check backwards from that future nearest multiple.
	latestHeight := ctx.BlockHeight()
	rescheduleCoalesceBlocks := k.GetConfigInt64(ctx, constants.RescheduleCoalesceBlocks)
	if rescheduleCoalesceBlocks > 1 {
		overBlocks := latestHeight % rescheduleCoalesceBlocks
		if overBlocks != 0 {
			latestHeight += rescheduleCoalesceBlocks - overBlocks
		}
	}

	// Collect all matching TxOutItems
	var matches []matchedOutbound

	for height := earliestHeight; height <= latestHeight; height++ {
		txOut, err := k.GetTxOut(ctx, height)
		if err != nil {
			continue
		}
		for _, item := range txOut.TxArray {
			// Skip if already has an OutHash (already processed)
			if !item.OutHash.IsEmpty() {
				continue
			}
			if !item.Chain.Equals(tx.Tx.Chain) {
				continue
			}
			if !item.ToAddress.Equals(tx.Tx.ToAddress) {
				continue
			}
			// Check both regular and EdDSA vault pubkeys (same as handler_common_outbound.go)
			if !item.VaultPubKey.Equals(tx.ObservedPubKey) && !item.VaultPubKeyEddsa.Equals(tx.ObservedPubKey) {
				continue
			}
			if !item.Coin.Asset.Equals(tx.Tx.Coins[0].Asset) {
				continue
			}
			// Check coin amount matches (with some flexibility for gas)
			// Similar to handler_common_outbound.go logic
			matchCoin := tx.Tx.Coins.EqualsEx(common.Coins{item.Coin})
			if !matchCoin && item.Coin.Asset.Equals(item.Chain.GetGasAsset()) {
				asset := item.Chain.GetGasAsset()
				intendToSpend := item.Coin.Amount.Add(item.MaxGas.ToCoins().GetCoin(asset).Amount)
				actualSpend := tx.Tx.Coins.GetCoin(asset).Amount.Add(tx.Tx.Gas.ToCoins().GetCoin(asset).Amount)
				if intendToSpend.Equal(actualSpend) {
					matchCoin = true
				}
			}
			if !matchCoin {
				continue
			}
			// Found a match - add to list for deterministic sorting
			matches = append(matches, matchedOutbound{
				height: height,
				item:   item,
				hash:   item.Hash(),
			})
		}
	}

	if len(matches) == 0 {
		return ""
	}

	// Sort by height (ascending), then by hash (ascending)
	// This matches Bifrost's deterministic ordering in signer/storage.go:200-204
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].hash < matches[j].hash
	})
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].height < matches[j].height
	})

	// Return memo from first match (what Bifrost would sign first)
	if matches[0].item.OriginalMemo != "" {
		return matches[0].item.OriginalMemo
	}
	return matches[0].item.Memo
}

// fetchMemoFromReference fetches memo from reference if it's a memoless transaction
func fetchMemoFromReference(ctx cosmos.Context, mgr Manager, asset common.Asset, tx common.Tx, txObservationHeight int64) string {
	mem, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), tx.Memo)
	if err != nil {
		ctx.Logger().Debug("failed to parse memo in fetchMemoFromReference", "error", err, "memo", tx.Memo, "txid", tx.ID)
	}
	if mem.GetType() == TxReferenceReadMemo {
		m, ok := mem.(ReferenceReadMemo)
		if !ok {
			return tx.Memo
		}
		// Check if memoless transactions are halted
		haltMemoless, err := mgr.Keeper().GetMimir(ctx, constants.HaltMemoless.String())
		if err == nil && haltMemoless > 0 {
			return ""
		}

		// Check if the reference memo exists before proceeding
		if !mgr.Keeper().ReferenceMemoExists(ctx, asset, m.Reference) {
			ctx.Logger().Debug("reference memo not found", "reference", m.Reference, "asset", asset)
			return ""
		}

		refMemo, err := mgr.Keeper().GetReferenceMemo(ctx, asset, m.Reference)
		if err != nil {
			ctx.Logger().Error("unable to fetch pool for ref memo lookup", "error", err, "asset", asset)
			return ""
		}

		// Ensure transaction was observed after the reference memo was created
		if txObservationHeight <= refMemo.Height {
			ctx.Logger().Info("transaction observed before reference memo creation", "tx_height", txObservationHeight, "memo_height", refMemo.Height, "reference", m.Reference)
			// Track usage for audit purposes - we intentionally track all usage attempts (including failures)
			// to maintain a record of who tried to use the reference memo, even if the attempt was invalid.
			trackReferenceMemoUsage(ctx, mgr, asset, tx.Memo, tx.ID)
			return ""
		}

		ttl := mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnTTL)
		if !refMemo.IsExpired(ctx.BlockHeight(), ttl) {
			// Check usage limit
			maxUse := mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnMaxUse)
			if maxUse > 0 && refMemo.GetUsageCount() >= maxUse {
				ctx.Logger().Info("reference memo usage limit exceeded in fetchMemoFromReference", "reference", m.Reference, "usage_count", refMemo.GetUsageCount(), "max_use", maxUse)
				// Track usage for audit purposes - we intentionally track all usage attempts (including failures)
				// to maintain a record of who tried to use the reference memo, even if the attempt was invalid.
				trackReferenceMemoUsage(ctx, mgr, asset, tx.Memo, tx.ID)
				return ""
			}

			// Track successful usage
			trackReferenceMemoUsage(ctx, mgr, asset, tx.Memo, tx.ID)
			return refMemo.Memo
		}

		// Track usage for audit purposes - we intentionally track all usage attempts (including failures)
		// to maintain a record of who tried to use the reference memo, even if the attempt was invalid (expired).
		trackReferenceMemoUsage(ctx, mgr, asset, tx.Memo, tx.ID)
		return ""
	}
	return tx.Memo
}

// trackReferenceMemoUsage tracks that a transaction has used a reference memo
func trackReferenceMemoUsage(ctx cosmos.Context, mgr Manager, asset common.Asset, memo string, txID common.TxID) {
	mem, _ := ParseMemo(mgr.GetVersion(), memo) // ignore err
	if mem.GetType() == TxReferenceReadMemo {
		m, ok := mem.(ReferenceReadMemo)
		if !ok {
			return
		}

		// Only track usage for reference memos that actually exist in state
		if !mgr.Keeper().ReferenceMemoExists(ctx, asset, m.Reference) {
			ctx.Logger().Debug("reference memo not found for usage tracking, skipping", "reference", m.Reference, "asset", asset)
			return
		}

		refMemo, err := mgr.Keeper().GetReferenceMemo(ctx, asset, m.Reference)
		if err != nil {
			ctx.Logger().Error("fail to get reference memo for usage tracking", "error", err)
			return
		}

		// Check if already used by this transaction
		if refMemo.HasBeenUsedBy(txID) {
			// Already tracked, nothing to do
			return
		}

		// Add this transaction to the usage list (with duplicate protection)
		if refMemo.AddUsage(txID) {
			mgr.Keeper().SetReferenceMemo(ctx, refMemo)
			maxUse := mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnMaxUse)
			ctx.Logger().Info("reference memo usage tracked", "reference", m.Reference, "usage_count", refMemo.GetUsageCount(), "max_use", maxUse, "txid", txID, "asset", asset, "memo", refMemo.Memo)
		}
	}
}

func generateReferenceMemoID(ctx cosmos.Context, mgr Manager, asset common.Asset, tx common.ObservedTx) (string, error) {
	if len(tx.Tx.Coins) == 0 {
		return "", fmt.Errorf("no coins in transaction for reference generation")
	}

	coinAmount := tx.Tx.Coins[0].Amount
	if coinAmount.GT(cosmos.NewUint(math.MaxUint64)) {
		return "", fmt.Errorf("coin amount exceeds uint64 max")
	}
	amount := coinAmount.Uint64()
	return ExtractReferenceFromAmount(ctx, mgr, asset, amount)
}

// ExtractReferenceFromAmount extracts the reference ID from a transaction amount.
// This is the core logic shared by both the handler (generateReferenceMemoID)
// and the querier (queryReferenceMemoPreflight).
func ExtractReferenceFromAmount(ctx cosmos.Context, mgr Manager, asset common.Asset, amount uint64) (string, error) {
	if asset.IsEmpty() {
		return "", fmt.Errorf("asset is empty for reference generation")
	}

	if amount == 0 {
		return "", fmt.Errorf("zero amount in transaction for reference generation")
	}

	var decimals int64
	if asset.IsGasAsset() {
		decimals = asset.Chain.GetGasAssetDecimal()
	} else {
		pool, err := mgr.Keeper().GetPool(ctx, asset)
		if err != nil {
			ctx.Logger().Error("unable to fetch pool for ref memo", "error", err)
			return "", err
		}
		decimals = pool.Decimals
		if decimals == 0 {
			// For non-gas assets, a pool decimal value of zero is used as a sentinel
			// for THORChain default precision (1e8), not literal 0-decimal precision.
			decimals = int64(common.THORChainDecimals)
		}
	}

	baseEnd := mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnRefCount)
	txnRefLength := len(fmt.Sprintf("%d", baseEnd))

	// Prevent overflow: uint64 max is ~1.8e19, so max safe length is 19 digits
	const maxRefLength = 19
	if txnRefLength > maxRefLength {
		return "", fmt.Errorf("reference length %d exceeds maximum %d to prevent overflow", txnRefLength, maxRefLength)
	}

	// Calculate modulus based on reference length (10^txnRefLength)
	var modulus uint64 = 1
	if baseEnd > 0 {
		for i := 0; i < txnRefLength; i++ {
			modulus *= 10
		}
	}

	// Normalize amount to THORChain decimals.
	// Note: Only decimals < THORChainDecimals (8) need handling here. For chains with
	// decimals >= THORChainDecimals (e.g. SOL at 9), Bifrost already normalizes all
	// inbound amounts to 1e8 precision before they reach THORNode, so no scaling is
	// needed in those cases.
	if decimals < int64(common.THORChainDecimals) {
		divisor := int64(1)
		for i := decimals; i < int64(common.THORChainDecimals); i++ {
			divisor *= 10
		}
		amount /= uint64(divisor)
	}

	// Extract reference from the amount.
	refNum := amount % modulus

	// Zero references are not allowed as they would create ambiguous reference IDs.
	// This occurs when the amount (after decimal normalization) is exactly divisible
	// by the modulus (e.g., 50000000 sat with modulus 100 → ref 0).
	// Users should adjust their amount slightly to avoid this edge case.
	if refNum == 0 {
		return "", fmt.Errorf("zero reference from amount is invalid: amount %d is divisible by modulus %d", amount, modulus)
	}

	return leadingZeros(txnRefLength, fmt.Sprintf("%d", refNum)), nil
}

// finalizeOutbounds updates the Finalized flag on all outbound TxOutItems associated with an inbound
// transaction when that inbound transaction finalizes. This allows bifrost signers to proceed with
// signing outbounds that were created from mempool swaps. For native (THORChain) outbounds, this
// function also executes them since they were queued waiting for finalization.
func finalizeOutbounds(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter) error {
	ctx.Logger().Info("finalizing outbounds for inbound", "hash", voter.TxID, "actions_count", len(voter.Actions))

	// Release mempool swap capacity if this was a mempool swap
	if !voter.MempoolSwapTOR.IsNil() && !voter.MempoolSwapTOR.IsZero() {
		newUsage := mgr.Keeper().SubMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)
		ctx.Logger().Info("released mempool swap capacity",
			"hash", voter.TxID,
			"tor_value", voter.MempoolSwapTOR,
			"new_usage", newUsage)
	}

	// Get the maximum outbound delay to determine search range
	txOutDelayMax := mgr.Keeper().GetConfigInt64(ctx, constants.TxOutDelayMax)

	// Iterate through all actions (TxOutItems) for this inbound
	for i := range voter.Actions {
		action := &voter.Actions[i]

		// Skip if already finalized
		if action.InboundFinalized {
			continue
		}

		// Update the action's InboundFinalized flag
		action.InboundFinalized = true
		ctx.Logger().Info("marked action as finalized",
			"inhash", action.InHash,
			"chain", action.Chain,
			"to", action.ToAddress,
			"amount", action.Coin.Amount)

		// Now we need to find and update the queued TxOut in keeper storage
		// For mempool swaps, outbounds are created at consensus height (before finalization),
		// so we need to search from the earlier unfinalized height, not the finalized height.
		outboundHeight := voter.UnfinalizedHeight
		if outboundHeight == 0 {
			outboundHeight = voter.Height
		}
		if outboundHeight == 0 {
			outboundHeight = ctx.BlockHeight()
		}

		// Search through a reasonable range of future blocks (up to TxOutDelayMax)
		// to find the TxOut containing this action
		found := false
		maxSearchHeight := outboundHeight + txOutDelayMax
		for h := outboundHeight; h <= maxSearchHeight && !found; h++ {
			txOut, err := mgr.Keeper().GetTxOut(ctx, h)
			if err != nil {
				ctx.Logger().Error("fail to get txout", "error", err, "height", h)
				continue
			}

			// Search through all TxOutItems in this block's TxOut
			for j := range txOut.TxArray {
				item := &txOut.TxArray[j]
				// Match by InHash - that's the unique link to the inbound
				if item.InHash.Equals(action.InHash) {
					// Check if this is a native (THORChain) outbound that was queued
					if item.Chain.IsTHORChain() {
						// Execute the native outbound now that inbound is finalized
						tos, ok := mgr.TxOutStore().(*TxOutStorageVCUR)
						if !ok {
							return fmt.Errorf("fail to cast TxOutStore to TxOutStorageVCUR")
						}
						if err := tos.nativeTxOut(ctx, mgr, *item); err != nil {
							ctx.Logger().Error("fail to execute native outbound", "error", err, "inhash", item.InHash)
							return fmt.Errorf("fail to execute native outbound: %w", err)
						}
						// Mark as processed by setting OutHash
						item.OutHash = common.BlankTxID
						ctx.Logger().Info("executed queued native outbound",
							"height", h,
							"inhash", item.InHash,
							"asset", item.Coin.Asset,
							"amount", item.Coin.Amount)
					} else {
						// External outbound - just mark as finalized (existing behavior)
						item.InboundFinalized = true
						ctx.Logger().Info("updated queued txout item to finalized",
							"height", h,
							"inhash", item.InHash,
							"chain", item.Chain)
					}

					// Save the updated TxOut back to storage
					if err := mgr.Keeper().SetTxOut(ctx, txOut); err != nil {
						return fmt.Errorf("fail to save txout at height %d: %w", h, err)
					}
					found = true
					break
				}
			}
		}

		if !found {
			ctx.Logger().Warn("could not find queued txout item to finalize",
				"inhash", action.InHash,
				"chain", action.Chain,
				"search_range", fmt.Sprintf("%d-%d", outboundHeight, maxSearchHeight))
		}
	}

	// Save the updated voter with finalized actions
	mgr.Keeper().SetObservedTxInVoter(ctx, voter)

	return nil
}

// revertMempoolSwap reverts a mempool swap by creating a reverse swap back to the original asset,
// then donating the result to the original source pool using a chained memo.
// This is called when a mempool transaction is dropped or replaced before finalization.
func revertMempoolSwap(ctx cosmos.Context, keeper keeper.Keeper, mgr Manager, voter ObservedTxVoter) error {
	ctx.Logger().Info("reverting mempool swap", "hash", voter.TxID, "actions_count", len(voter.Actions))

	// Release mempool swap capacity if this was a mempool swap
	if !voter.MempoolSwapTOR.IsNil() && !voter.MempoolSwapTOR.IsZero() {
		newUsage := keeper.SubMempoolSwapUsageTOR(ctx, voter.MempoolSwapTOR)
		ctx.Logger().Info("released mempool swap capacity on revert",
			"hash", voter.TxID,
			"tor_value", voter.MempoolSwapTOR,
			"new_usage", newUsage)
	}

	// Get the maximum outbound delay to determine search range
	txOutDelayMax := keeper.GetConfigInt64(ctx, constants.TxOutDelayMax)

	// Determine the original source asset from the inbound transaction
	if voter.Tx.Tx.Coins.IsEmpty() {
		return fmt.Errorf("no coins in inbound tx")
	}
	sourceAsset := voter.Tx.Tx.Coins[0].Asset

	// Get an active vault to use as the destination
	activeVaults, err := keeper.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil || len(activeVaults) == 0 {
		return fmt.Errorf("fail to get active vaults: %w", err)
	}
	vault := activeVaults[0]

	// Iterate through all actions (TxOutItems) for this inbound
	for i := range voter.Actions {
		action := &voter.Actions[i]

		// Skip if already signed (OutHash is set)
		if !action.OutHash.IsEmpty() {
			ctx.Logger().Warn("action already signed, cannot revert",
				"inhash", action.InHash,
				"outhash", action.OutHash)
			continue
		}

		ctx.Logger().Info("reverting action",
			"inhash", action.InHash,
			"from_asset", action.Coin.Asset,
			"from_amount", action.Coin.Amount,
			"to_asset", sourceAsset)

		// Step 1: Remove the TxOutItem from the queued TxOut FIRST
		outboundHeight := voter.UnfinalizedHeight
		if outboundHeight == 0 {
			outboundHeight = ctx.BlockHeight()
		}

		removed := false
		maxSearchHeight := outboundHeight + txOutDelayMax
		for h := outboundHeight; h <= maxSearchHeight && !removed; h++ {
			txOut, err := keeper.GetTxOut(ctx, h)
			if err != nil {
				continue
			}

			// Search for the TxOutItem to remove
			for j := 0; j < len(txOut.TxArray); j++ {
				item := &txOut.TxArray[j]
				if item.InHash.Equals(action.InHash) {
					// Remove this item from the array
					txOut.TxArray = append(txOut.TxArray[:j], txOut.TxArray[j+1:]...)
					ctx.Logger().Info("removed queued txout item",
						"height", h,
						"inhash", item.InHash,
						"chain", item.Chain)

					// Save the updated TxOut
					if err := keeper.SetTxOut(ctx, txOut); err != nil {
						ctx.Logger().Error("fail to save txout after removal", "error", err, "height", h)
						return fmt.Errorf("fail to save txout after removal at height %d: %w", h, err)
					}
					removed = true
					break // IMPORTANT: must break after slice modification to avoid index issues
				}
			}
		}

		if !removed {
			ctx.Logger().Error("could not find and remove queued txout item",
				"inhash", action.InHash,
				"chain", action.Chain,
				"search_range", fmt.Sprintf("%d-%d", outboundHeight, maxSearchHeight))
			return fmt.Errorf("could not find queued txout item to remove: %s", action.InHash)
		}

		// Step 2: Handle pool accounting for the reverted swap
		// For native outputs (RUNE, synths, trade assets), directly adjust pool balances
		// For external chain outputs, create a reverse swap then donate
		if action.Chain.IsTHORChain() {
			// Native output - directly adjust pool balances to reverse the swap
			// The swap was: sourceAsset -> native (RUNE)
			// We need to:
			// 1. Add the RUNE back to the pool (reverse the RUNE that was emitted)
			// 2. Subtract the source asset from the pool (reverse the asset that was added)
			// This is because the inbound never finalized, so the source asset never arrived in the vault
			pool, err := keeper.GetPool(ctx, sourceAsset.GetLayer1Asset())
			if err != nil {
				ctx.Logger().Error("fail to get pool for native revert", "error", err, "asset", sourceAsset)
				continue
			}

			// Get the source amount from the voter's inbound transaction
			sourceAmount := voter.Tx.Tx.Coins[0].Amount

			// Add the RUNE back to the pool (restore RUNE that was emitted)
			pool.BalanceRune = pool.BalanceRune.Add(action.Coin.Amount)

			// Subtract the source asset from the pool (remove asset that was never received)
			// Use SafeSub to avoid underflow in case of any edge cases
			pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, sourceAmount)

			if err := keeper.SetPool(ctx, pool); err != nil {
				return fmt.Errorf("fail to set pool after native revert: %w", err)
			}

			ctx.Logger().Info("reverted native mempool swap pool balances",
				"pool", sourceAsset.GetLayer1Asset(),
				"rune_added", action.Coin.Amount,
				"asset_removed", sourceAmount)
			continue
		}

		// External chain output - create reverse swap with chained donate memo
		// The pool deduction will happen atomically when the donate executes in settleSwap
		// Generate a unique transaction ID for this reversal using deterministic reversal
		// The original TxID can be recovered by reversing the process without needing storage
		txID := CreateMempoolRevertTxID(voter.TxID, i)

		// Get max gas coin for the chain
		maxGasCoin, _, err := mgr.GasMgr().GetGasDetails(ctx, action.Chain)
		if err != nil {
			ctx.Logger().Error("failed to get gas details for mempool revert swap", "chain", action.Chain, "error", err)
		}
		if err != nil || maxGasCoin.IsEmpty() {
			maxGasCoin = common.NewCoin(action.Chain.GetGasAsset(), cosmos.OneUint())
		}

		// For the reverse swap, we need to get a vault address on the source asset's chain
		// because the output destination must match the target asset's chain
		sourceChain := sourceAsset.GetChain()
		destAddress, err := vault.PubKey.GetAddress(sourceChain)
		if err != nil {
			ctx.Logger().Error("fail to get vault address for source chain", "error", err, "chain", sourceChain)
			continue
		}

		// Get THORChain vault address for the swap message
		vaultAddress, err := vault.PubKey.GetAddress(common.THORChain)
		if err != nil {
			ctx.Logger().Error("fail to get vault address for THORChain", "error", err)
			continue
		}

		// Create a reverse swap with chained memo: swap back to source asset, then donate to pool
		// The "|" separator tells the system to process a second memo after the swap completes
		memo := fmt.Sprintf("=:%s:%s|DONATE:%s", sourceAsset, destAddress, sourceAsset.GetLayer1Asset())

		msg := MsgSwap{
			Tx: common.Tx{
				ID:          txID,
				Chain:       common.THORChain,
				FromAddress: vaultAddress,
				ToAddress:   vaultAddress,
				Coins:       common.Coins{action.Coin},
				Gas:         common.Gas{maxGasCoin},
				Memo:        memo,
			},
			TargetAsset:             sourceAsset,
			Destination:             destAddress,
			TradeTarget:             cosmos.ZeroUint(),
			AggregatorTargetAddress: "",
			AggregatorTargetLimit:   nil,
			Signer:                  mgr.Keeper().GetModuleAccAddress(AsgardName),
		}

		// Add the reverse swap to the swap queue
		if err := mgr.Keeper().SetSwapQueueItem(ctx, msg, 0); err != nil {
			return fmt.Errorf("fail to queue reverse swap: %w", err)
		}

		ctx.Logger().Info("queued reverse swap for mempool revert",
			"txid", txID,
			"from_asset", action.Coin.Asset,
			"from_amount", action.Coin.Amount,
			"to_asset", sourceAsset,
			"memo", memo)
	}

	// Mark the voter as reverted
	voter.Reverted = true
	keeper.SetObservedTxInVoter(ctx, voter)

	// Note: The MempoolSwapReverted event is emitted from settleSwap when the donate executes,
	// as that's when we know the donated amount

	return nil
}
