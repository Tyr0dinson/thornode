package thorchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// maxMempoolRevertActions is the maximum number of staged outbound actions that can
// be reverted via reverse swaps. GetOriginalFromMempoolRevertTxID brute-forces
// action indices during recovery, so this value caps the search space.
const maxMempoolRevertActions = 16

// CreateMempoolRollbackTxID creates a deterministic synthetic TxID for an
// inline rollback swap from the original inbound TxID and action index.
// The current inline design no longer needs to recover the original txid from
// the rollback txid, so a simple namespaced hash is enough.
func CreateMempoolRollbackTxID(originalTxID common.TxID, actionIndex int) common.TxID {
	// Namespace the hash input so rollback txids cannot accidentally collide
	// with other synthetic txid schemes that may reuse the same original txid.
	sum := sha256.Sum256([]byte(fmt.Sprintf("mempool-rollback|%s|%d", originalTxID, actionIndex)))
	return common.TxID(fmt.Sprintf("%X", sum))
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

	// Allow eligible swaps to execute at consensus (before finalization) for the
	// mempool swap optimization. Streaming swaps remain eligible whether
	// streaming was requested explicitly in the memo or derived implicitly by
	// the advanced queue. Outbounds still won't be signed until the inbound
	// finalizes.
	canProcessMempoolSwap := canUseMempoolSwap(ctx, mgr, voter.Tx.Tx, memo)
	shouldHandleMempoolSwap := !hasFinalised && canProcessMempoolSwap

	if !shouldHandleMempoolSwap && !hasFinalised {
		ctx.Logger().Info("transaction pending confirmation counting", "hash", voter.TxID)
		return nil
	}

	if shouldHandleMempoolSwap {
		if voter.UnfinalizedHeight == 0 {
			voter.UnfinalizedHeight = ctx.BlockHeight()
			k.SetObservedTxInVoter(ctx, voter)
		}

		// Runtime admission for the mempool path starts here. Vault eligibility
		// is still checked up front, but TOR capacity is reserved only once the
		// inbound has parsed as a real swap and passed the later reject checks.
		if vault.Status == InactiveVault {
			incrMempoolSwapCounter("blocked_total", []metrics.Label{telemetry.NewLabel("reason", "inactive_vault")})
			ctx.Logger().Info("inactive vault cannot process mempool swap before finalization", "hash", voter.TxID)
			return nil
		}
	}

	// If a mempool swap was executed before finalization but the inbound
	// finalized on an inactive vault, roll back the speculative mempool state
	// first and then refund the inbound instead of releasing staged outbounds.
	if hasFinalised && vault.Status == InactiveVault {
		ctx.Logger().Error("observed tx on inactive vault", "tx", tx.String())

		hasCleanupState, cleanupStateErr := needsMempoolSwapRollback(ctx, k, voter)
		if cleanupStateErr != nil {
			ctx.Logger().Error("fail to inspect mempool swap rollback state", "error", cleanupStateErr, "hash", voter.TxID)
			return nil
		}
		if hasCleanupState {
			if err = rollbackMempoolSwapAtomic(ctx, k, mgr, voter); err != nil {
				ctx.Logger().Error("fail to roll back mempool swap for inactive vault", "error", err, "hash", voter.TxID)
				return nil
			}
			incrMempoolSwapCounter("rolled_back_total", []metrics.Label{telemetry.NewLabel("reason", "inactive_vault")})

			// Refresh the local copy so any code added below sees the normalized
			// voter state that rollback already wrote to the store.
			refreshedVoter, getErr := k.GetObservedTxInVoter(ctx, voter.TxID)
			if getErr != nil {
				ctx.Logger().Error("fail to refresh voter after mempool rollback", "error", getErr, "hash", voter.TxID)
			} else {
				voter = refreshedVoter
			}
		}

		if newErr := refundTx(ctx, tx, mgr, CodeInvalidVault, "observed inbound tx to an inactive vault", ""); newErr != nil {
			ctx.Logger().Error("fail to refund", "error", newErr)
		}
		return nil
	}

	// When a mempool swap finalizes, move staged outbounds to the real queue
	// and return early - the swap was already executed from mempool, don't process it again.
	// The error is intentionally not returned: propagating it would roll back the handler's
	// cache context, rolling back voter state, chain height, and vault updates made above.
	// finalizeOutboundsAtomic is internally transactional, so logging the error here does not leak
	// partial staging/cap/queue side effects while preserving the voter, chain-height, and
	// vault updates already made above.
	if hasFinalised && len(voter.Actions) > 0 {
		if err = finalizeOutboundsAtomic(ctx, mgr, voter); err != nil {
			ctx.Logger().Error("fail to finalize outbounds", "error", err, "hash", voter.TxID)
		}
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
		if shouldHandleMempoolSwap {
			ctx.Logger().Info("deferring mempool refund decision until inbound finalization",
				"hash", voter.TxID,
				"reason", txErr.Error())
			return nil
		}
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
			if shouldHandleMempoolSwap {
				ctx.Logger().Info("deferring halted-trading refund until inbound finalization", "hash", voter.TxID)
				return nil
			}
			if newErr := refundTx(ctx, tx, mgr, se.ErrUnauthorized.ABCICode(), "trading halted", ""); nil != newErr {
				ctx.Logger().Error("fail to refund for halted trading", "error", newErr)
			}
			return nil
		}
	}

	// if its a swap, send it to our queue for processing later
	if isSwap {
		reservedMempoolSwapCapacity := false
		if shouldHandleMempoolSwap && !voter.HasReservedMempoolSwapTOR() {
			sourceAsset := voter.Tx.Tx.Coins[0].Asset
			sourceAmount := voter.Tx.Tx.Coins[0].Amount
			mempoolSwapCap := cosmos.NewUint(uint64(k.GetConfigInt64(ctx, constants.MempoolSwapCapTOR)))
			torValue, hasCapacity, capacityErr := checkMempoolSwapCapacity(ctx, k, sourceAsset, sourceAmount)
			switch {
			case capacityErr != nil:
				incrMempoolSwapCounter("blocked_total", []metrics.Label{telemetry.NewLabel("reason", "capacity_error")})
				ctx.Logger().Info("fail to check mempool swap capacity, waiting for finalization",
					"error", capacityErr, "hash", voter.TxID)
				return nil
			case !hasCapacity:
				incrMempoolSwapCounter("blocked_total", []metrics.Label{telemetry.NewLabel("reason", "capacity")})
				ctx.Logger().Info("mempool swap cannot start before finalization",
					"current_usage", k.GetMempoolSwapUsageTOR(ctx),
					"swap_tor", torValue,
					"cap", mempoolSwapCap,
					"hash", voter.TxID)
				return nil
			}

			newUsage := k.AddMempoolSwapUsageTOR(ctx, torValue)
			voter.MempoolSwapTOR = torValue
			k.SetObservedTxInVoter(ctx, voter)
			ctx.Logger().Info("reserved mempool swap capacity at admission",
				"hash", voter.TxID,
				"tor_value", voter.MempoolSwapTOR,
				"new_usage", newUsage,
				"cap", mempoolSwapCap,
				"memo", voter.Tx.Tx.Memo)
			incrMempoolSwapCounter("admitted_total", nil)
			reservedMempoolSwapCapacity = true
		}

		if err = addSwap(ctx, mgr, *swapMsg); err != nil {
			if shouldHandleMempoolSwap {
				if reservedMempoolSwapCapacity {
					voter = releaseMempoolSwapCapacityAndClear(ctx, k, voter, "released mempool swap capacity after queue admission failure")
				}
				ctx.Logger().Info("deferring swap queue rejection until inbound finalization",
					"hash", voter.TxID,
					"reason", err.Error())
				return nil
			}
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

// finalizeOutboundsAtomic moves staged outbounds to the real TxOut queue when the
// inbound transaction finalizes. For native (THORChain) outbounds, this function
// executes them immediately. It runs in its own CacheContext and only commits if
// the full finalize flow succeeds, so partial staging/cap/queue mutations are
// discarded on error. This is part of the staged queue approach for mempool swaps.
func finalizeOutboundsAtomic(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter) error {
	cacheCtx, commit := ctx.CacheContext()
	if err := finalizeOutboundsCached(cacheCtx, mgr, voter); err != nil {
		return err
	}
	commit()
	return nil
}

// needsMempoolSwapRollback reports whether this inbound still has
// mempool-swap state that must be rolled back before finalized handling can
// continue. This includes staged outbounds, queued swap work, emitted actions,
// and reserved mempool capacity.
func needsMempoolSwapRollback(ctx cosmos.Context, k keeper.Keeper, voter ObservedTxVoter) (bool, error) {
	if len(voter.Actions) > 0 {
		return true, nil
	}
	if voter.HasReservedMempoolSwapTOR() {
		return true, nil
	}
	if k.HasSwapQueueItem(ctx, voter.TxID, 0) || k.HasAdvSwapQueueItem(ctx, voter.TxID, 0) {
		return true, nil
	}
	stagedItems, err := k.GetStagedOutbounds(ctx, voter.TxID)
	if err != nil {
		return false, fmt.Errorf("fail to get staged outbounds: %w", err)
	}
	return len(stagedItems) > 0, nil
}

// restoreFinalNativeMempoolTxOutItem maps a staged native mempool outbound
// back to the final native action in voter.Actions. Secured targets are
// restored from the escrowed underlying asset while other native targets must
// still match the original staged asset.
func restoreFinalNativeMempoolTxOutItem(voter ObservedTxVoter, index int, staged keeper.StagedOutbound) (TxOutItem, error) {
	if index >= len(voter.Actions) {
		return TxOutItem{}, fmt.Errorf("staged outbound index %d has no matching voter action", index)
	}
	action := voter.Actions[index]
	if !action.Chain.IsTHORChain() {
		return TxOutItem{}, fmt.Errorf("staged THOR outbound index %d maps to non-THOR action %s", index, action.Chain)
	}
	finalItem := staged.TxOutItem
	if !action.Coin.Asset.IsSecuredAsset() {
		if !staged.TxOutItem.Coin.Asset.Equals(action.Coin.Asset) {
			return TxOutItem{}, fmt.Errorf("staged THOR outbound index %d asset mismatch: staged %s action %s", index, staged.TxOutItem.Coin.Asset, action.Coin.Asset)
		}
		return finalItem, nil
	}
	if !staged.TxOutItem.Coin.Asset.Equals(action.Coin.Asset.GetLayer1Asset()) {
		return TxOutItem{}, fmt.Errorf("staged escrow asset mismatch at index %d: staged %s action %s", index, staged.TxOutItem.Coin.Asset, action.Coin.Asset)
	}

	// Keep the staged amount and metadata, but switch back to the final
	// native asset before running the outbound.
	finalItem.Coin.Asset = action.Coin.Asset
	return finalItem, nil
}

// buildPendingMempoolRollbackAction rebuilds an output we can unwind from
// advanced-swap queue state when part of the swap already ran but nothing
// has been staged yet.
func buildPendingMempoolRollbackAction(ctx cosmos.Context, k keeper.Keeper, voter ObservedTxVoter) (*TxOutItem, error) {
	if !k.HasAdvSwapQueueItem(ctx, voter.TxID, 0) {
		ctx.Logger().Debug("no partial advanced-queue output available for mempool rollback", "hash", voter.TxID)
		return nil, nil
	}

	msg, err := k.GetAdvSwapQueueItem(ctx, voter.TxID, 0)
	if err != nil {
		return nil, fmt.Errorf("fail to get pending adv swap item: %w", err)
	}
	if msg.State.Out.IsZero() {
		ctx.Logger().Debug("advanced-queue mempool rollback has zero emitted output", "hash", voter.TxID)
		return nil, nil
	}

	asset := msg.TargetAsset
	// For native secured targets, rollback uses the escrowed underlying
	// asset, not the final secured asset.
	if asset.IsSecuredAsset() {
		asset = asset.GetLayer1Asset()
	}

	action := TxOutItem{
		Chain:  asset.GetChain(),
		InHash: voter.TxID,
		Coin:   common.NewCoin(asset, msg.State.Out),
	}
	return &action, nil
}

// clearPendingMempoolSwapQueueItems removes the primary queued swap work for a
// mempool inbound after rollback.
//
// With the current production flow, the only queue entry that can still belong
// to a mempool swap here is the main swap item at index 0:
// - the swap may have been queued before the queue manager got a chance to run
// - an advanced swap may have partially executed and still be stored at index 0
//
// Affiliate sidecars are deferred until finalization, so they should not exist
// during this rollback cleanup path. Preferred-asset follow-up swaps use their
// own synthetic txid, not the original inbound txid.
//
// Because of that current invariant, clearing index 0 is enough here. If the
// mempool flow ever starts creating extra same-txid queue entries before
// finalization, this helper must be widened again.
func clearPendingMempoolSwapQueueItems(ctx cosmos.Context, k keeper.Keeper, txID common.TxID) error {
	// Legacy/basic queue mempool swaps only use index 0 for the main swap item.
	if k.HasSwapQueueItem(ctx, txID, 0) {
		k.RemoveSwapQueueItem(ctx, txID, 0)
	}

	// Advanced queue mempool swaps also keep the main in-flight swap at index 0.
	if k.HasAdvSwapQueueItem(ctx, txID, 0) {
		if err := k.RemoveAdvSwapQueueItem(ctx, txID, 0); err != nil {
			return fmt.Errorf("fail to remove adv swap queue item: %w", err)
		}
	}
	return nil
}

// selectActiveVaultForChain finds an active Asgard vault that can actually
// derive an address for the requested source chain.
func selectActiveVaultForChain(ctx cosmos.Context, k keeper.Keeper, chain common.Chain) (Vault, error) {
	activeVaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		return Vault{}, fmt.Errorf("fail to get active vaults: %w", err)
	}
	if len(activeVaults) == 0 {
		return Vault{}, fmt.Errorf("no active vaults available for source chain %s", chain)
	}

	for _, vault := range activeVaults {
		// The reverse swap needs both chain support metadata and a usable
		// address derivation for the source chain.
		if !chain.IsTHORChain() && !vault.GetChains().Has(chain) {
			ctx.Logger().Debug("skipping active vault without source-chain support",
				"source_chain", chain,
				"vault", vault.PubKey)
			continue
		}
		if _, err := vault.GetAddress(chain); err != nil {
			ctx.Logger().Debug("skipping active vault without usable source-chain address",
				"source_chain", chain,
				"vault", vault.PubKey,
				"error", err)
			continue
		}
		return vault, nil
	}

	return Vault{}, fmt.Errorf("no active vault supports source chain %s", chain)
}

type mempoolRollbackAction struct {
	item        TxOutItem
	actionIndex int
}

type finalizedMempoolOutbound struct {
	item            TxOutItem
	scheduledHeight int64
}

// aggregateMempoolRollbackActions coalesces split external staged outbounds into
// one reverse-swap input per asset. Native THOR outbounds stay separate because
// they unwind through native accounting or native balance logic instead.
func aggregateMempoolRollbackActions(actions []mempoolRollbackAction) []mempoolRollbackAction {
	aggregated := make([]mempoolRollbackAction, 0, len(actions))
	externalByAsset := make(map[string]int, len(actions))

	for _, action := range actions {
		if !action.item.Chain.IsTHORChain() {
			key := action.item.Coin.Asset.String()
			if idx, ok := externalByAsset[key]; ok {
				aggregated[idx].item.Coin.Amount = aggregated[idx].item.Coin.Amount.Add(action.item.Coin.Amount)
				continue
			}
			externalByAsset[key] = len(aggregated)
		}
		aggregated = append(aggregated, action)
	}

	return aggregated
}

// buildMempoolRollbackSwapMsg builds one unwind swap for a mempool rollback.
// The reverse swap starts from the staged or escrowed output asset and donates
// the recovered source asset back to the original source pool.
func buildMempoolRollbackSwapMsg(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	sourceAsset common.Asset,
	action TxOutItem,
	actionIndex int,
	vault Vault,
) (MsgSwap, error) {
	txID := CreateMempoolRollbackTxID(voter.TxID, actionIndex)

	maxGasCoin, _, err := mgr.GasMgr().GetGasDetails(ctx, action.Chain)
	if err != nil {
		ctx.Logger().Error("failed to get gas details for mempool rollback swap", "chain", action.Chain, "error", err)
	}
	if err != nil || maxGasCoin.IsEmpty() {
		maxGasCoin = common.NewCoin(action.Chain.GetGasAsset(), cosmos.OneUint())
	}

	sourceChain := sourceAsset.GetChain()
	destAddress, err := vault.GetAddress(sourceChain)
	if err != nil {
		return MsgSwap{}, fmt.Errorf("fail to get vault address for source chain %s: %w", sourceChain, err)
	}

	vaultAddress, err := vault.GetAddress(common.THORChain)
	if err != nil {
		return MsgSwap{}, fmt.Errorf("fail to get vault address for THORChain: %w", err)
	}

	swapMemo := fmt.Sprintf("=:%s:%s", sourceAsset, destAddress)
	postSwapMemo := fmt.Sprintf("DONATE:%s", sourceAsset.GetLayer1Asset())
	rollbackFromAddress := vaultAddress
	rollbackToAddress := vaultAddress
	if action.Coin.IsNative() {
		// Native rollback inputs already live in module balances,
		// so route the synthetic internal tx through the Asgard module address
		asgardAddress, addrErr := mgr.Keeper().GetModuleAddress(AsgardName)
		if addrErr != nil {
			return MsgSwap{}, fmt.Errorf("fail to get Asgard module address for native mempool rollback: %w", addrErr)
		}
		rollbackFromAddress = asgardAddress
		rollbackToAddress = asgardAddress
	}

	msg := MsgSwap{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.THORChain,
			FromAddress: rollbackFromAddress,
			ToAddress:   rollbackToAddress,
			Coins:       common.Coins{action.Coin},
			Gas:         common.Gas{maxGasCoin},
			Memo:        swapMemo,
		},
		TargetAsset:             sourceAsset,
		Destination:             destAddress,
		AffiliateBasisPoints:    cosmos.ZeroUint(),
		PostSwapMemo:            postSwapMemo,
		TradeTarget:             cosmos.ZeroUint(),
		AggregatorTargetAddress: "",
		AggregatorTargetLimit:   nil,
		Signer:                  mgr.Keeper().GetModuleAccAddress(AsgardName),
	}

	ctx.Logger().Info("built inline rollback swap for mempool rollback",
		"txid", txID,
		"from_asset", action.Coin.Asset,
		"from_amount", action.Coin.Amount,
		"to_asset", sourceAsset,
		"memo", swapMemo,
		"post_swap_memo", postSwapMemo)

	return msg, nil
}

// deferredAffiliateGroupKey identifies mempool-staged outbounds that belong to
// the same logical outbound. Mempool swaps defer affiliate side effects until
// finalization, so the staged pieces must be regrouped here before those
// deferred affiliate adjustments are applied once.
func deferredAffiliateGroupKey(item TxOutItem) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s",
		item.InHash,
		item.Chain,
		item.ToAddress,
		item.Coin.Asset,
		item.Memo,
		item.ModuleName,
	)
}

// applyDeferredAffiliateFees runs the affiliate-fee adjustments that mempool
// swaps intentionally deferred while their outbounds were only staged. It
// applies that logic once per logical outbound at finalization time, then
// spreads any reduced net amount back across that outbound's split pieces.
func applyDeferredAffiliateFees(ctx cosmos.Context, mgr Manager, outbounds []finalizedMempoolOutbound) ([]finalizedMempoolOutbound, error) {
	tos, ok := mgr.TxOutStore().(*TxOutStorage)
	if !ok {
		return nil, fmt.Errorf("fail to cast TxOutStore to TxOutStorage")
	}

	grouped := make(map[string][]int, len(outbounds))
	groupOrder := make([]string, 0, len(outbounds))
	adjusted := make([]finalizedMempoolOutbound, len(outbounds))
	copy(adjusted, outbounds)

	for i, outbound := range adjusted {
		key := deferredAffiliateGroupKey(outbound.item)
		if _, ok := grouped[key]; !ok {
			groupOrder = append(groupOrder, key)
		}
		grouped[key] = append(grouped[key], i)
	}

	for _, key := range groupOrder {
		indices := grouped[key]
		groupItem := adjusted[indices[0]].item
		groupTotal := cosmos.ZeroUint()
		for _, idx := range indices {
			groupTotal = groupTotal.Add(adjusted[idx].item.Coin.Amount)
		}

		// Rebuild the full outbound amount first so the affiliate fee is
		// calculated once on the total amount the swap produced.
		groupItem.Coin.Amount = groupTotal

		adjustedGroupItem, feeErr := tos.getAffiliateFeeAdjustedTxOutItem(ctx, mgr, groupItem)
		if feeErr != nil {
			ctx.Logger().Error("fail to take deferred affiliate fee", "error", feeErr, "inhash", groupItem.InHash)
			continue
		}

		// For Trade and Secured Assets, keep the staged outbound amounts as-is.
		// Their affiliate fee is taken later from the user's balance, so there
		// is no reduced outbound amount to spread back here.
		asset := groupItem.Coin.Asset
		if asset.IsTradeAsset() || asset.IsSecuredAsset() {
			continue
		}

		// Spread any reduced net amount back over the split pieces in order.
		remaining := adjustedGroupItem.Coin.Amount
		for _, idx := range indices {
			amount := adjusted[idx].item.Coin.Amount
			if remaining.GTE(amount) {
				remaining = remaining.Sub(amount)
				continue
			}
			adjusted[idx].item.Coin.Amount = remaining
			remaining = cosmos.ZeroUint()
		}
	}

	return adjusted, nil
}

// finalizeOutboundsCached moves staged outbounds into their final form when the
// inbound confirms. Native outbounds run here, and external ones go to TxOut.
func finalizeOutboundsCached(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter) error {
	ctx.Logger().Info("finalizing outbounds for inbound", "hash", voter.TxID)

	// Get staged outbounds for this inbound transaction
	// Idempotency: if no staged outbounds exist, either they were never staged or already finalized
	stagedItems, err := mgr.Keeper().GetStagedOutbounds(ctx, voter.TxID)
	if err != nil {
		return fmt.Errorf("fail to get staged outbounds: %w", err)
	}

	if len(stagedItems) == 0 {
		ctx.Logger().Debug("no staged outbounds to finalize, skipping", "hash", voter.TxID)
		return nil
	}

	// Release mempool swap capacity if this was a mempool swap
	voter = releaseMempoolSwapCapacityAndClear(ctx, mgr.Keeper(), voter, "released mempool swap capacity")

	ctx.Logger().Info("found staged outbounds to finalize",
		"hash", voter.TxID,
		"count", len(stagedItems))

	// Delete staged outbounds BEFORE processing to prevent double-processing on retry.
	// This helper runs inside a CacheContext, so any later error discards both the deletion
	// and any partial queue/native-outbound work.
	mgr.Keeper().DeleteStagedOutbounds(ctx, voter.TxID)

	finalizedOutbounds := make([]finalizedMempoolOutbound, 0, len(stagedItems))
	for i, staged := range stagedItems {
		finalItem := staged.TxOutItem
		if staged.TxOutItem.Chain.IsTHORChain() {
			finalItem, err = restoreFinalNativeMempoolTxOutItem(voter, i, staged)
			if err != nil {
				return fmt.Errorf("fail to restore staged native outbound: %w", err)
			}
		}
		finalizedOutbounds = append(finalizedOutbounds, finalizedMempoolOutbound{
			item:            finalItem,
			scheduledHeight: staged.ScheduledHeight,
		})
	}

	finalizedOutbounds, err = applyDeferredAffiliateFees(ctx, mgr, finalizedOutbounds)
	if err != nil {
		return err
	}

	tos, ok := mgr.TxOutStore().(*TxOutStorage)
	if !ok {
		return fmt.Errorf("fail to cast TxOutStore to TxOutStorage")
	}

	// Move each staged outbound to its final form.
	for _, finalized := range finalizedOutbounds {
		finalItem := finalized.item
		if finalItem.Coin.Amount.IsZero() {
			continue
		}

		// Floor the scheduled height at current block if the scheduled time has passed
		targetHeight := finalized.scheduledHeight
		if targetHeight < ctx.BlockHeight() {
			targetHeight = ctx.BlockHeight()
		}

		if finalItem.Chain.IsTHORChain() {
			if err = tos.nativeTxOut(ctx, mgr, finalItem); err != nil {
				ctx.Logger().Error("fail to execute native outbound", "error", err, "inhash", finalItem.InHash)
				return fmt.Errorf("fail to execute native outbound: %w", err)
			}
			ctx.Logger().Info("executed staged native outbound on finalization",
				"inhash", finalItem.InHash,
				"asset", finalItem.Coin.Asset,
				"amount", finalItem.Coin.Amount)
		} else {
			// Add external outbound to real TxOut queue at the target height
			if err = mgr.Keeper().AppendTxOut(ctx, targetHeight, finalItem); err != nil {
				return fmt.Errorf("fail to move staged outbound to queue: %w", err)
			}
			ctx.Logger().Info("moved staged outbound to queue",
				"inhash", finalItem.InHash,
				"chain", finalItem.Chain,
				"target_height", targetHeight,
				"original_scheduled", finalized.scheduledHeight)
		}
	}

	incrMempoolSwapCounter("completed_total", []metrics.Label{telemetry.NewLabel("path", "staged_finalize")})
	return nil
}

// deductMempoolRollbackSourceAsset removes the source asset that was credited
// speculatively when the mempool swap started but never actually arrived on
// chain. Callers must do this exactly once per rolled-back inbound.
func deductMempoolRollbackSourceAsset(ctx cosmos.Context, mgr Manager, sourceAsset common.Asset, sourceAmount cosmos.Uint) error {
	pool, err := mgr.Keeper().GetPool(ctx, sourceAsset.GetLayer1Asset())
	if err != nil {
		return fmt.Errorf("fail to get pool for mempool rollback deduction: %w", err)
	}

	pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, sourceAmount)
	if err = mgr.Keeper().SetPool(ctx, pool); err != nil {
		return fmt.Errorf("fail to set pool after mempool rollback deduction: %w", err)
	}

	emitPoolBalanceChangedEvent(ctx,
		NewPoolMod(sourceAsset.GetLayer1Asset(), cosmos.ZeroUint(), false, sourceAmount, false),
		"mempool swap rollback",
		mgr)

	return nil
}

// rollbackMempoolSwapAtomic rolls back mempool-swap execution by
// executing the reverse swap inline against live pool state and donating the
// recovered source asset back into the original source pool inside the same
// cache context.
func rollbackMempoolSwapAtomic(ctx cosmos.Context, keeper keeper.Keeper, mgr Manager, voter ObservedTxVoter) error {
	cacheCtx, commit := ctx.CacheContext()
	if err := rollbackMempoolSwapCached(cacheCtx, keeper, mgr, voter); err != nil {
		return err
	}
	commit()
	return nil
}

// clearMempoolRollbackState normalizes mempool-swap state after a
// successful inline rollback so a later finalized inbound is processed like a
// fresh swap.
func clearMempoolRollbackState(voter ObservedTxVoter) ObservedTxVoter {
	voter.Reverted = false
	voter.MempoolSwapTOR = cosmos.ZeroUint()
	voter.Actions = nil
	return voter
}

// rollbackMempoolSwapCached performs the inline mempool rollback itself.
// It gathers the staged or partially-executed outputs that need to be unwound,
// executes any required reverse swaps inline, then clears staged state and
// resets the voter only after the whole rollback succeeds in the current cache
// context.
func rollbackMempoolSwapCached(ctx cosmos.Context, keeper keeper.Keeper, mgr Manager, voter ObservedTxVoter) error {
	ctx.Logger().Info("rolling back mempool swap", "hash", voter.TxID)

	// Release mempool swap capacity if this was a mempool swap
	voter = releaseMempoolSwapCapacityAndClear(ctx, keeper, voter, "released mempool swap capacity on rollback")

	// Determine the original source asset from the inbound transaction
	if voter.Tx.Tx.Coins.IsEmpty() {
		return fmt.Errorf("no coins in inbound tx")
	}
	sourceAsset := voter.Tx.Tx.Coins[0].Asset

	// Get staged outbounds for this inbound transaction
	stagedItems, err := keeper.GetStagedOutbounds(ctx, voter.TxID)
	if err != nil {
		return fmt.Errorf("fail to get staged outbounds: %w", err)
	}

	var actionsToRollback []mempoolRollbackAction
	if len(stagedItems) == 0 {
		// Advanced swaps can build up executed output in queue state before
		// any staged outbound exists. Recover that output so rollback still
		// has something real to unwind.
		var pendingAction *TxOutItem
		pendingAction, err = buildPendingMempoolRollbackAction(ctx, keeper, voter)
		if err != nil {
			return err
		}
		if pendingAction != nil {
			actionsToRollback = append(actionsToRollback, mempoolRollbackAction{
				item:        *pendingAction,
				actionIndex: 0,
			})
		}
	} else {
		actionsToRollback = make([]mempoolRollbackAction, 0, len(stagedItems))
		for i, staged := range stagedItems {
			actionsToRollback = append(actionsToRollback, mempoolRollbackAction{
				item:        staged.TxOutItem,
				actionIndex: i,
			})
		}
	}
	actionsToRollback = aggregateMempoolRollbackActions(actionsToRollback)

	if len(actionsToRollback) == 0 {
		ctx.Logger().Debug("no staged outbounds to roll back", "hash", voter.TxID)
		// Even without staged outbounds, the primary swap item at queue index 0
		// can still be hanging around. Clear it so a later queue pass cannot
		// re-process this inbound.
		if err = clearPendingMempoolSwapQueueItems(ctx, keeper, voter.TxID); err != nil {
			return err
		}
		voter = clearMempoolRollbackState(voter)
		keeper.SetObservedTxInVoter(ctx, voter)
		return nil
	}

	ctx.Logger().Info("found staged outbounds to roll back",
		"hash", voter.TxID,
		"count", len(actionsToRollback))

	// Get the source amount from the voter's inbound transaction
	// This is the asset that was credited to the pool on mempool observation
	// but will never be received, so we need to remove it once (not per outbound)
	sourceAmount := voter.Tx.Tx.Coins[0].Amount
	assetRemovedFromPool := false
	aggregateDonatedAmount := cosmos.ZeroUint()
	var rollbackSwapVault Vault

	// Handle pool accounting for each staged outbound, coalescing split
	// external outbounds into one inline rollback swap per asset.
	for _, rollbackAction := range actionsToRollback {
		action := rollbackAction.item
		ctx.Logger().Info("rolling back staged action",
			"inhash", action.InHash,
			"from_asset", action.Coin.Asset,
			"from_amount", action.Coin.Amount,
			"to_asset", sourceAsset)

		// True native RUNE outputs can be unwound directly from pool accounting.
		// All other staged outputs, including synths, unwind through rollback swaps.
		if action.Coin.Asset.IsRune() {
			// Native RUNE output - directly adjust pool balances to reverse the swap.
			var pool Pool
			deductedSource := false
			pool, err = keeper.GetPool(ctx, sourceAsset.GetLayer1Asset())
			if err != nil {
				return fmt.Errorf("fail to get pool for native rollback: %w", err)
			}

			// Add the RUNE back to the pool (restore RUNE that was emitted)
			pool.BalanceRune = pool.BalanceRune.Add(action.Coin.Amount)

			// Subtract the source asset from the pool ONLY ONCE
			// (remove asset that was credited on mempool observation but never received)
			if !assetRemovedFromPool {
				pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, sourceAmount)
				assetRemovedFromPool = true
				deductedSource = true
				ctx.Logger().Info("rolled back native mempool swap pool balances",
					"pool", sourceAsset.GetLayer1Asset(),
					"rune_added", action.Coin.Amount,
					"asset_removed", sourceAmount)
			} else {
				ctx.Logger().Info("rolled back native mempool swap (rune only)",
					"pool", sourceAsset.GetLayer1Asset(),
					"rune_added", action.Coin.Amount)
			}

			if err = keeper.SetPool(ctx, pool); err != nil {
				return fmt.Errorf("fail to set pool after native rollback: %w", err)
			}
			if deductedSource {
				emitPoolBalanceChangedEvent(ctx,
					NewPoolMod(sourceAsset.GetLayer1Asset(), cosmos.ZeroUint(), false, sourceAmount, false),
					"mempool swap rollback",
					mgr)
			}
			continue
		}

		if action.Coin.Asset.IsSecuredAsset() {
			// Native secured outputs should already be changed to their
			// escrowed underlying asset before rollback sees them.
			return fmt.Errorf("unsupported native mempool rollback target: %s", action.Coin.Asset)
		}

		if rollbackSwapVault.IsEmpty() {
			rollbackSwapVault, err = selectActiveVaultForChain(ctx, keeper, sourceAsset.GetChain())
			if err != nil {
				return err
			}
		}

		rollbackSwap, buildErr := buildMempoolRollbackSwapMsg(ctx, mgr, voter, sourceAsset, action, rollbackAction.actionIndex, rollbackSwapVault)
		if buildErr != nil {
			return buildErr
		}
		if err = executeSwapWithoutOutboundsCached(ctx, mgr, &rollbackSwap); err != nil {
			return fmt.Errorf("fail to execute inline rollback swap: %w", err)
		}
		if !assetRemovedFromPool {
			// Deduct the phantom source asset only after the first rollback swap
			// succeeds so a failed rollback does not leave pool accounting half-updated.
			if err = deductMempoolRollbackSourceAsset(ctx, mgr, sourceAsset, sourceAmount); err != nil {
				return err
			}
			assetRemovedFromPool = true
		}
		if !rollbackSwap.State.Out.IsZero() {
			aggregateDonatedAmount = aggregateDonatedAmount.Add(rollbackSwap.State.Out)
			if err = executePostSwapMemoCached(ctx, mgr, rollbackSwap); err != nil {
				return fmt.Errorf("fail to execute inline rollback swap post-swap memo: %w", err)
			}
		}
	}

	// Delete all staged outbounds AFTER processing succeeds.
	// This helper runs inside a CacheContext, so returning an error discards all prior pool,
	// usage, and queue mutations and allows a clean retry.
	keeper.DeleteStagedOutbounds(ctx, voter.TxID)
	// Rollback may have started after the main queue item at index 0 was already
	// written for this inbound. Clear that leftover work now that the inline
	// rollback succeeded.
	if err = clearPendingMempoolSwapQueueItems(ctx, keeper, voter.TxID); err != nil {
		return err
	}

	// Reset the voter now that all mempool state has been undone.
	voter = clearMempoolRollbackState(voter)
	keeper.SetObservedTxInVoter(ctx, voter)
	// Keep the legacy event name for wire compatibility even though the
	// implementation now rolls back mempool execution inline.
	mempoolRollbackEvent := NewEventMempoolSwapReverted(
		voter.TxID,
		ctx.BlockHeight(),
		sourceAsset,
		sourceAmount,
		aggregateDonatedAmount,
	)
	if err = mgr.EventMgr().EmitEvent(ctx, mempoolRollbackEvent); err != nil {
		ctx.Logger().Error("fail to emit mempool rollback event", "error", err)
	}

	return nil
}
