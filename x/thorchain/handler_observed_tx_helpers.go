package thorchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/cosmos/cosmos-sdk/telemetry"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/hashicorp/go-metrics"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
)

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

// processTxInAttestation applies a single attestation for an observed tx and saves
// the voter. MsgObservedTxIn (single attester) uses it; MsgObservedTxQuorum applies
// its attestations with applyTxInAttestation and saves the voter once.
func processTxInAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	nas NodeAccounts,
	tx ObservedTx,
	signer cosmos.AccAddress,
	shouldSlashForDuplicate bool,
) (ObservedTxVoter, bool) {
	voter, ok, added := applyTxInAttestation(ctx, mgr, voter, nas, tx, signer, shouldSlashForDuplicate)
	if added {
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	}
	return voter, ok
}

// applyTxInAttestation records a single attestation on the voter without saving
// it, so a quorum message can save the voter once after all its attestations.
// It returns the voter, whether this attestation brought it to consensus, and
// whether it was recorded: false for a duplicate, which leaves the voter unchanged.
func applyTxInAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	nas NodeAccounts,
	tx ObservedTx,
	signer cosmos.AccAddress,
	shouldSlashForDuplicate bool,
) (ObservedTxVoter, bool, bool) {
	k := mgr.Keeper()
	slasher := mgr.Slasher()

	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_txin"),
		telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
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

	// A committee member's pre-broadcast "instant" observation and its later
	// scanner observation of the same tx differ only in gas (EVM) or block height
	// (all chains, from signing-tip vs inclusion) and split into separate voter
	// entries. Detect that the signer already recorded a matching variant BEFORE
	// Add records this one, so the redundant observe point can be refunded exactly
	// once below without depending on the post-consensus flex branch.
	// Refund a redundant re-observation only for the signer's FIRST prior variant
	// (exactly one earlier matching observation) — the honest "observed it, lost the
	// gas/height race, re-observed once" case. Further variants (count >= 2) are
	// self-inflicted spam and keep their observe slash point, so a node cannot file
	// unlimited gas/height variants at zero cost to erase the per-observation charge.
	redundantReObservation := voter.ShouldRefundRedundantInboundObservation(tx, signer)
	// Whether this is the signer's first observation in its finality tier, for
	// the cross-tier credit after final consensus below.
	firstInTier := !voter.HasObservedInTier(tx, signer)

	obsChain := telemetry.NewLabel("chain", telemChain(tx.Tx.Chain))
	obsDir := telemetry.NewLabel("direction", "in")

	if !voter.Add(tx, signer) {
		if !shouldSlashForDuplicate {
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
		}
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observe", "attestation"}, 1,
			[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("result", "duplicate")},
		)
		// A duplicate message, so do nothing further.
		return voter, ok, false
	}

	// Single mutually-exclusive attestation result per call; late wins over redundant.
	attResult := "new"

	// The signer already observed this tx via a different gas/height variant, so
	// this observation is redundant honest work rather than a missed observation:
	// refund its observe point. The observation stays recorded above so it still
	// counts toward consensus and gas correction.
	if redundantReObservation {
		attResult = "redundant"
		slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
	}
	// A node charged the lack penalty at non-final consensus that observes the
	// tx as final within that consensus's flex window, before final consensus
	// or as the observation that forms it, observed the tx in time: refund the
	// penalty and excuse it, as if its final observation had come first. Its
	// observe point is still settled by final consensus.
	if lateFinalObservationExcused(voter, tx, signer, ctx.BlockHeight(), observeFlex) &&
		inboundConsensusMatches(&voter, nas, tx) {
		voter.ExcusedObservers = append(voter.ExcusedObservers, signer.String())
		slasher.DecSlashPoints(slashCtx, lackOfObservationPenalty, signer)
	}
	if voter.HasFinalised(nas) {
		switch {
		case voter.FinalisedHeight == 0:
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "observe", "consensus"}, 1,
				[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("finalised", "true")},
			)
			ok = true
			nonFinalCredited := isNonFinalConsensus(voter.Tx)
			voter.Height = ctx.BlockHeight() // Always record the consensus height of the finalised Tx
			if voter.UnfinalizedHeight == 0 {
				voter.UnfinalizedHeight = voter.Height // Preserve first consensus height
			}
			voter.FinalisedHeight = ctx.BlockHeight()
			voter.Tx = *voter.GetTx(nas)

			// This signer brings the voter to consensus;
			// decrement all the signers' slash points and increment the non-signers' slash points.
			signers := voter.GetInboundObserveSigners()
			nonSigners := getNonSigners(nas, signers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
			if !nonFinalCredited {
				// Non-final consensus never formed, so nothing else will credit
				// the non-final observations already recorded.
				slasher.DecSlashPoints(slashCtx, observeSlashPoints, voter.GetInboundOtherTierObserveSigners()...)
			}
			// Persist the credited signer set, including legacy XMR observers,
			// so later observations cannot collect the same consensus credit again.
			voter.RecordObserveSigners(signers)
			voter.ExcusedObservers = nil
			queueObservationTally(ctx, k, observationTallyKindTxIn, tx.Tx.Chain, tx.Tx.ID.String())
		case ctx.BlockHeight() <= (voter.FinalisedHeight+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			inboundConsensusMatches(&voter, nas, tx) &&
			!voter.Tx.HasSigned(signer):
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so still take away their slash points
			// but only when the tx signer are voting is the tx that already reached consensus
			slasher.DecSlashPoints(slashCtx, observeSlashPoints+lackOfObservationPenalty, signer)
			attResult = "late"
		case ctx.BlockHeight() <= (voter.FinalisedHeight+observeFlex) &&
			!tx.IsFinal() && firstInTier &&
			voter.CreditsAcrossFinality(tx) &&
			inboundConsensusMatches(&voter, nas, tx):
			// The signer's first non-final observation landed after final
			// consensus, e.g. a sub-quorum attestation batch flushed after the
			// final quorum. Refund its observe point only: the lack penalty
			// belongs to the final tier.
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
			attResult = "late"
		}
	}
	if !ok && voter.HasConsensus(nas) && !tx.IsFinal() && voter.FinalisedHeight == 0 {
		if voter.Height == 0 {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "observe", "consensus"}, 1,
				[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("finalised", "false")},
			)
			ok = true
			voter.Height = ctx.BlockHeight()
			if voter.UnfinalizedHeight == 0 {
				voter.UnfinalizedHeight = voter.Height // Preserve first consensus height
			}
			// this is the tx that has consensus
			voter.Tx = *voter.GetTx(nas)

			// This signer brings the voter to consensus;
			// decrement all the signers' slash points and increment the non-signers' slash points.
			signers := voter.GetInboundObserveSigners()
			observers := excuseOtherTierObservers(&voter, signers, voter.GetInboundOtherTierObserveSigners())
			nonSigners := getNonSigners(nas, observers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
			// Persist the credited signer set, including legacy XMR observers,
			// so later observations cannot collect the same consensus credit again.
			voter.RecordObserveSigners(signers)
		} else if ctx.BlockHeight() <= (voter.Height+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			inboundConsensusMatches(&voter, nas, tx) &&
			!voter.Tx.HasSigned(signer) {
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so still take away their slash points
			// but only when the tx signer are voting is the tx that already reached consensus
			slasher.DecSlashPoints(slashCtx, lateObserveRefund(voter, signer, observeSlashPoints, lackOfObservationPenalty), signer)
			attResult = "late"
		}
	}

	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "observe", "attestation"}, 1,
		[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("result", attResult)},
	)

	return voter, ok, true
}

// ensureVaultAndGetTxInVoter will make sure the vault exists, then get the ObservedTxInVoter from the store.
// if it doesn't exist, it will create a new one.
func ensureVaultAndGetTxInVoter(ctx cosmos.Context, vaultPubKey common.PubKey, txID common.TxID, k keeper.Keeper) (ObservedTxVoter, error) {
	// check we are sending to a valid vault
	if !k.VaultExists(ctx, vaultPubKey) {
		ctx.Logger().Info("Not valid Observed Pubkey", "observed pub key", vaultPubKey)
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observation", "dropped"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "txin_vault_not_found")},
		)
		return ObservedTxVoter{}, fmt.Errorf("vault not found for observed tx in pubkey: %s", vaultPubKey)
	}

	voter, err := k.GetObservedTxInVoter(ctx, txID)
	if err != nil {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observation", "dropped"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "txin_voter_read_fail")},
		)
		return ObservedTxVoter{}, fmt.Errorf("fail to get tx in voter: %w", err)
	}

	return voter, nil
}

const (
	compromisedVaultMimirKeyPrefix  = "CompromisedVault-"
	compromisedVaultNoOpSecurityMsg = "inbound to compromised vault no-oped"
)

// isVaultCompromised reports whether CompromisedVault-<pubkey> is set.
// The compromised-vault Mimir is keyed by vault pubkey rather than chain or
// address so it covers every inbound address exposed by the same vault.
// On KV read failure it returns true so callers can fail closed.
func isVaultCompromised(ctx cosmos.Context, k keeper.Keeper, vaultPubKey common.PubKey) (bool, error) {
	flagged, err := k.GetMimir(ctx, compromisedVaultMimirKeyPrefix+vaultPubKey.String())
	if err != nil {
		return true, err
	}
	return flagged > 0, nil
}

// emitCompromisedVaultNoOpSecurityEvent emits the operator-facing alert for an
// inbound that reached quorum but was intentionally skipped because its vault is
// flagged by CompromisedVault-<pubkey>.
func emitCompromisedVaultNoOpSecurityEvent(ctx cosmos.Context, mgr Manager, tx common.ObservedTx, vaultPubKey common.PubKey) {
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "inbound", "compromised_vault_noop"},
		1,
		[]metrics.Label{
			telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
			telemetry.NewLabel("pubkey", vaultPubKey.String()),
		},
	)

	// EventSecurity includes the external tx attributes, but not the observed
	// vault pubkey, so keep the vault key in the message for incident review.
	msg := fmt.Sprintf("%s: vault=%s", compromisedVaultNoOpSecurityMsg, vaultPubKey.String())
	if err := mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(tx.Tx, msg)); err != nil {
		ctx.Logger().Error("fail to emit compromised vault no-op security event", "pub_key", vaultPubKey.String(), "tx", tx.Tx.ID.String(), "error", err)
	}
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
	k := mgr.Keeper()

	// An XMR errata can reach consensus while the original inbound has only
	// partial observations. In that ordering the errata voter is committed,
	// but the empty inbound voter cannot yet be marked Reverted and later
	// errata votes short-circuit on BlockHeight. Re-apply that monotonic outcome
	// at the inbound's first quorum, before any vault, pool, memo, or swap side
	// effect can run.
	if err := applyCommittedXMRErrataToInboundConsensus(ctx, k, &voter); err != nil {
		return err
	}

	if voter.Reverted {
		ctx.Logger().Info("tx had been reverted", "Tx", tx.String())
		return nil
	}

	vault, err := k.GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
		return nil
	}

	voter.Tx.Tx.Memo = tx.Tx.Memo

	hasFinalised := voter.HasFinalised(activeNodeAccounts)

	compromised, compromisedErr := isVaultCompromised(ctx, k, vault.PubKey)
	if compromisedErr != nil {
		ctx.Logger().Error("failed to read CompromisedVault mimir, treating inbound as no-op", "pub_key", vault.PubKey.String(), "error", compromisedErr)
	}
	if compromised {
		// The tx has already reached observation quorum. Record observation
		// metadata, but deliberately skip every value-changing action below:
		// vault accounting, memo resolution, refunds, swap queueing, and
		// internal handler dispatch. Funds sent to a compromised vault should
		// not affect THORChain state.
		if err = k.SetLastChainHeight(ctx, tx.Tx.Chain, tx.BlockHeight); err != nil {
			ctx.Logger().Error("fail to set last chain height", "error", err)
		}

		mgr.ObMgr().AppendObserver(tx.Tx.Chain, voter.Tx.GetSigners())
		if hasFinalised {
			// Mark finalised no-op observations done so they do not remain as
			// pending inbound work waiting for an outbound that will never be
			// scheduled.
			emitCompromisedVaultNoOpSecurityEvent(ctx, mgr, tx, vault.PubKey)
			voter.SetDone()
			ctx.Logger().Info("observed tx in to compromised vault no-oped", "pub_key", vault.PubKey.String(), "tx", tx.Tx.ID.String())
		} else {
			ctx.Logger().Info("observed tx in to compromised vault pending confirmation counting", "pub_key", vault.PubKey.String(), "tx", tx.Tx.ID.String())
		}
		k.SetObservedTxInVoter(ctx, voter)
		return nil
	}

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

	// Update vault balances from inbounds with Migrate memos immediately,
	// to minimise any gap between outbound and inbound observations.
	// TODO: In future somehow update both balances in a single action,
	// so the ActiveVault balance increase is guaranteed to never be early nor late?
	if hasFinalised || memo.IsType(TxMigrate) {
		if vault.IsAsgard() && !voter.UpdatedVault {
			if tx.Tx.FromAddress.Equals(tx.Tx.ToAddress) {
				// Don't add to or subtract from vault balances when the sender and recipient are the same
				// (particularly avoid Consolidate SafeSub zeroing of vault balances).
				voter.UpdatedVault = true
			} else if shouldCreditObservedInboundVault(ctx, mgr, tx, memo.IsType(TxMigrate), memo.GetBlockHeight()) {
				vault.AddFunds(tx.Tx.Coins)
				vault.InboundTxCount++
				voter.UpdatedVault = true
			}
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
		if err = rollbackAndRefundInactiveVaultMempoolSwap(ctx, mgr, tx, voter); err != nil {
			ctx.Logger().Error("fail to clean up mempool swap for inactive vault", "error", err, "hash", voter.TxID)
		}
		return nil
	}

	// When a mempool swap finalizes, move staged outbounds to the real queue
	// and return early - the swap was already executed from mempool, don't process it again.
	// The error is intentionally not returned: propagating it would roll back the handler's
	// cache context, rolling back voter state, chain height, and vault updates made above.
	// finalizeOutboundsAtomic is internally transactional, so logging the error here does not leak
	// partial staging/cap/queue side effects while preserving the voter, chain-height, and
	// vault updates already made above. Any still-staged mempool outbounds will be
	// retried later by LackSigning's timeout reconciler instead of needing an
	// immediate hard failure here.
	if hasFinalised && len(voter.Actions) > 0 {
		if err = finalizeOutboundsAtomic(ctx, mgr, voter); err != nil {
			ctx.Logger().Error("fail to finalize outbounds", "error", err, "hash", voter.TxID)
		}
		return nil
	}

	// Resolve reference memo before any finalized refund path. In particular,
	// an inactive-vault XMR refund needs the registered ADD asset address rather
	// than the unavailable observed sender encoded by the raw r:<ref> memo.
	// Keep the parsed memo aligned as well so post-handler completion logic uses
	// the actual transaction type rather than TxReferenceReadMemo.
	// Amount-encoded memoless inbounds have their synthesized r:<ref> memo on
	// the voter, while the observed transaction memo remains empty. Preserve
	// that synthesized memo so successful processing consumes the reference.
	referenceMemo := voter.Tx.Tx.Memo
	referenceAsset := common.EmptyAsset
	if len(tx.Tx.Coins) > 0 {
		referenceAsset = tx.Tx.Coins[0].Asset
	}
	referenceResolved := false
	if resolvedMemo, ok := resolveReferenceMemo(ctx, mgr, k, &tx, &voter); ok {
		memo = resolvedMemo
		referenceResolved = true
	}

	ctx.Logger().Debug(
		"tx in finalized and has consensus",
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
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "inbound", "refund"}, 1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
				telemetry.NewLabel("reason", "invalid_memo"),
			},
		)
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
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "inbound", "refund"}, 1,
				[]metrics.Label{
					telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
					telemetry.NewLabel("reason", "trading_halted"),
				},
			)
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
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "inbound", "refund"}, 1,
				[]metrics.Label{
					telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
					telemetry.NewLabel("reason", "swap_queue_failed"),
				},
			)
			if refundErr := refundTx(ctx, tx, mgr, CodeSwapFail, err.Error(), ""); refundErr != nil {
				ctx.Logger().Error("fail to refund swap", "error", refundErr)
				return fmt.Errorf("swap failed and refund also failed: %w, refund error: %v", err, refundErr)
			}
			return nil
		}
		if referenceResolved {
			trackReferenceMemoUsage(ctx, mgr, referenceAsset, referenceMemo, tx.Tx.ID)
		}
		return nil
	}

	mCtx := ctx

	// Check and block switch assets
	// Check is independent of the mimir to enable the handler in order to support
	// bifrost & switch whitelisting prior to switching commencing
	_, isSwitch := m.(*MsgSwitch)
	if !isSwitch && len(tx.Tx.Coins) > 0 && mgr.SwitchManager().IsSwitch(ctx, tx.Tx.Coins[0].Asset) {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "inbound", "refund"}, 1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
				telemetry.NewLabel("reason", "switch_asset"),
			},
		)
		if err = refundTx(ctx, tx, mgr, CodeTxFail, "asset is a switch asset", ""); err != nil {
			ctx.Logger().Error("fail to refund", "error", err)
		}

		return nil
	}

	_, err = handler(mCtx, m)
	if err != nil {
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "inbound", "refund"}, 1,
			[]metrics.Label{
				telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
				telemetry.NewLabel("reason", "handler_failed"),
			},
		)
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
	if referenceResolved {
		trackReferenceMemoUsage(ctx, mgr, referenceAsset, referenceMemo, tx.Tx.ID)
	}

	ctx.Logger().Info("tx in processed", "chain", tx.Tx.Chain, "id", tx.Tx.ID, "finalized", tx.IsFinal())

	return nil
}

// applyCommittedXMRErrataToInboundConsensus closes the ordering gap between a
// committed errata voter and the original XMR inbound reaching quorum. It must
// not run the normal errata accounting reversal: at this point the inbound has
// not executed any value-changing path, so marking it Reverted is sufficient
// and avoids subtracting state which was never credited.
func applyCommittedXMRErrataToInboundConsensus(ctx cosmos.Context, k keeper.Keeper, voter *ObservedTxVoter) error {
	if voter == nil || voter.Reverted || voter.Tx.IsEmpty() || !voter.Tx.Tx.Chain.Equals(common.XMRChain) {
		return nil
	}
	errataVoter, err := k.GetErrataTxVoter(ctx, voter.TxID, voter.Tx.Tx.Chain)
	if err != nil {
		return fmt.Errorf("fail to get errata voter for inbound %s: %w", voter.TxID, err)
	}
	if errataVoter.BlockHeight <= 0 {
		return nil
	}
	voter.SetReverted()
	k.SetObservedTxInVoter(ctx, *voter)
	return nil
}

func shouldCreditObservedInboundVault(ctx cosmos.Context, mgr Manager, tx common.ObservedTx, isMigrate bool, migrateHeight int64) bool {
	if !tx.Tx.Chain.Equals(common.XMRChain) || !isMigrate {
		return true
	}
	if common.IsXMRUnknownSenderAddress(tx.Tx.FromAddress) {
		ctx.Logger().Info("skip xmr migrate inbound vault credit: source vault is unknown", "tx", tx.Tx.ID.String(), "to", tx.Tx.ToAddress.String())
		return false
	}

	txOut, err := mgr.Keeper().GetTxOut(ctx, migrateHeight)
	if err != nil {
		ctx.Logger().Error("skip xmr migrate inbound vault credit: fail to get txout", "height", migrateHeight, "tx", tx.Tx.ID.String(), "error", err)
		return false
	}
	for _, item := range txOut.TxArray {
		if !xmrMigrateInboundMatchesTxOut(tx.Tx, item) {
			continue
		}
		return true
	}

	ctx.Logger().Info(
		"skip xmr migrate inbound vault credit: no matching scheduled txout",
		"tx", tx.Tx.ID.String(),
		"height", migrateHeight,
		"from", tx.Tx.FromAddress.String(),
		"to", tx.Tx.ToAddress.String(),
		"coins", tx.Tx.Coins.String(),
	)
	return false
}

func xmrMigrateInboundMatchesTxOut(tx common.Tx, item TxOutItem) bool {
	if !item.Chain.Equals(common.XMRChain) {
		return false
	}
	if !item.InHash.Equals(common.BlankTxID) {
		return false
	}
	if !common.AddressEquals(item.Chain, item.ToAddress, tx.ToAddress) {
		return false
	}

	sourcePubKey, err := txOutSourcePubKey(item)
	if err != nil {
		return false
	}
	fromAddress, err := sourcePubKey.GetAddress(item.Chain)
	if err != nil {
		return false
	}
	if !common.AddressEquals(item.Chain, fromAddress, tx.FromAddress) {
		return false
	}

	matchCoin := tx.Coins.Contains(item.Coin)
	if matchCoin {
		return true
	}

	asset := item.Chain.GetGasAsset()
	if !item.Coin.Asset.Equals(asset) {
		return false
	}
	// Inbound XMR gas is observer-reported and may not equal the actual signer fee.
	received := tx.Coins.GetCoin(asset).Amount
	minReceived := item.Coin.Amount
	maxReceived := item.Coin.Amount.Add(item.MaxGas.ToCoins().GetCoin(asset).Amount)
	return !received.LT(minReceived) && !received.GT(maxReceived)
}

// rollbackAndRefundInactiveVaultMempoolSwap centralizes the inactive-vault
// recovery sequence used in both the quorum handler and the timeout
// reconciler. The observed-tx path intentionally keeps its existing
// log-and-continue behavior, so refund scheduling failures are logged here
// while rollback/inspection failures are returned to the caller for context.
func rollbackAndRefundInactiveVaultMempoolSwap(ctx cosmos.Context, mgr Manager, tx common.ObservedTx, voter ObservedTxVoter) error {
	k := mgr.Keeper()

	hasCleanupState, cleanupStateErr := needsMempoolSwapRollback(ctx, k, voter)
	if cleanupStateErr != nil {
		return fmt.Errorf("fail to inspect mempool swap rollback state: %w", cleanupStateErr)
	}
	if hasCleanupState {
		if err := rollbackMempoolSwapAtomic(ctx, k, mgr, voter); err != nil {
			return fmt.Errorf("fail to roll back mempool swap for inactive vault: %w", err)
		}
		incrMempoolSwapCounter("rolled_back_total", []metrics.Label{telemetry.NewLabel("reason", "inactive_vault")})
	}

	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "inbound", "refund"}, 1,
		[]metrics.Label{
			telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
			telemetry.NewLabel("reason", "inactive_vault"),
		},
	)

	// Keep refund scheduling best-effort here, matching the wider refundTx
	// contract used throughout the handler paths. If we ever want refund
	// scheduling failure to become fatal, that change should be made in the
	// shared refund machinery rather than only in this inactive-vault branch.
	if newErr := refundTx(ctx, tx, mgr, CodeInvalidVault, "observed inbound tx to an inactive vault", ""); newErr != nil {
		ctx.Logger().Error("fail to refund", "error", newErr)
	}

	return nil
}

// processTxOutAttestation applies a single attestation for an observed tx and saves
// the voter. MsgObservedTxOut (single attester) uses it; MsgObservedTxQuorum applies
// its attestations with applyTxOutAttestation and saves the voter once.
func processTxOutAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	nas NodeAccounts,
	tx ObservedTx,
	signer cosmos.AccAddress,
	shouldSlashForDuplicate bool,
) (ObservedTxVoter, bool) {
	voter, ok, added := applyTxOutAttestation(ctx, mgr, voter, nas, tx, signer, shouldSlashForDuplicate)
	if added {
		mgr.Keeper().SetObservedTxOutVoter(ctx, voter)
	}
	return voter, ok
}

// applyTxOutAttestation records a single attestation on the voter without saving
// it, so a quorum message can save the voter once after all its attestations.
// It returns the voter, whether this attestation brought it to consensus, and
// whether it was recorded: false for a duplicate, which leaves the voter unchanged.
func applyTxOutAttestation(
	ctx cosmos.Context,
	mgr Manager,
	voter ObservedTxVoter,
	nas NodeAccounts,
	tx ObservedTx,
	signer cosmos.AccAddress,
	shouldSlashForDuplicate bool,
) (ObservedTxVoter, bool, bool) {
	k := mgr.Keeper()
	slasher := mgr.Slasher()

	observeSlashPoints := mgr.GetConstants().GetInt64Value(constants.ObserveSlashPoints)
	lackOfObservationPenalty := mgr.GetConstants().GetInt64Value(constants.LackOfObservationPenalty)
	observeFlex := k.GetConfigInt64(ctx, constants.ObservationDelayFlexibility)
	ok := false

	slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
		telemetry.NewLabel("reason", "failed_observe_txout"),
		telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
	}))
	slashCtx = ctx.WithContext(context.WithValue(slashCtx.Context(), constants.CtxObservedTx, tx.Tx.ID.String()))

	if err := k.SetLastObserveHeight(ctx, tx.Tx.Chain, signer, tx.BlockHeight); err != nil {
		ctx.Logger().Error("fail to save last observe height", "error", err, "signer", signer, "chain", tx.Tx.Chain)
	}

	// As an observation requires processing by all nodes no matter what,
	// any observation should increment ObserveSlashPoints,
	// to be decremented only if contributing to or within ObservationDelayFlexibility of consensus.
	slasher.IncSlashPoints(slashCtx, observeSlashPoints, signer)

	// A committee member's pre-broadcast "instant" observation and its later
	// scanner observation of the same tx differ only in gas (EVM) or block height
	// (all chains, from signing-tip vs inclusion) and split into separate voter
	// entries. Detect that the signer already recorded a matching variant BEFORE
	// Add records this one, so the redundant observe point can be refunded exactly
	// once below without depending on the post-consensus flex branch.
	// Refund a redundant re-observation only for the signer's FIRST prior variant
	// (exactly one earlier matching observation) — the honest "observed it, lost the
	// gas/height race, re-observed once" case. Further variants (count >= 2) are
	// self-inflicted spam and keep their observe slash point, so a node cannot file
	// unlimited gas/height variants at zero cost to erase the per-observation charge.
	redundantReObservation := voter.ShouldRefundRedundantOutboundObservation(tx, signer)
	// Whether this is the signer's first observation in its finality tier, for
	// the cross-tier credit after final consensus below.
	firstInTier := !voter.HasObservedInTier(tx, signer)

	obsChain := telemetry.NewLabel("chain", telemChain(tx.Tx.Chain))
	obsDir := telemetry.NewLabel("direction", "out")

	if !voter.Add(tx, signer) {
		if !shouldSlashForDuplicate {
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
		}
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observe", "attestation"}, 1,
			[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("result", "duplicate")},
		)
		// A duplicate message, so do nothing further.
		return voter, ok, false
	}

	// Single mutually-exclusive attestation result per call; late wins over redundant.
	attResult := "new"

	// The signer already observed this tx via a different gas/height variant, so
	// this observation is redundant honest work rather than a missed observation:
	// refund its observe point. The observation stays recorded above so it still
	// counts toward consensus and gas correction.
	if redundantReObservation {
		attResult = "redundant"
		slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
	}

	// Outbound memos can have | data passthrough,
	// so linked TxID extracted with memo parsing and GetTxID
	// rather than strings.Split .
	if memo, err := ParseMemoWithTHORNames(ctx, k, tx.Tx.Memo); err != nil {
		ctx.Logger().Error("fail to parse outbound memo", "error", err, "memo", tx.Tx.Memo)
	} else if inhash := memo.GetTxID(); !inhash.IsEmpty() {
		k.SetObservedLink(ctx, inhash, tx.Tx.ID)
	}

	// A late final observation of non-final consensus, as on the inbound path.
	if lateFinalObservationExcused(voter, tx, signer, ctx.BlockHeight(), observeFlex) &&
		outboundConsensusMatches(&voter, nas, tx) {
		voter.ExcusedObservers = append(voter.ExcusedObservers, signer.String())
		slasher.DecSlashPoints(slashCtx, lackOfObservationPenalty, signer)
	}

	if voter.HasFinalised(nas) {
		switch {
		case voter.FinalisedHeight == 0:
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "observe", "consensus"}, 1,
				[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("finalised", "true")},
			)
			nonFinalCredited := isNonFinalConsensus(voter.Tx)
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

			ctx.Logger().Debug(
				"tx out finalized and has consensus",
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
			signers := voter.GetOutboundObserveSigners()
			nonSigners := getNonSigners(nas, signers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
			if !nonFinalCredited {
				// Non-final consensus never formed, so nothing else will credit
				// the non-final observations already recorded.
				slasher.DecSlashPoints(slashCtx, observeSlashPoints, voter.GetOutboundOtherTierObserveSigners()...)
			}
			// Record the credited gas-agnostic observers on the consensus Tx so the
			// ObservationDelayFlexibility branch below (keyed off Tx.HasSigned) does
			// not refund the same node a second time when its own gas-used
			// observation arrives after consensus has already formed.
			voter.RecordObserveSigners(signers)
			voter.ExcusedObservers = nil
			queueObservationTally(ctx, k, observationTallyKindTxOut, tx.Tx.Chain, tx.Tx.ID.String())
		case ctx.BlockHeight() <= (voter.FinalisedHeight+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			outboundConsensusMatches(&voter, nas, tx) &&
			!voter.Tx.HasSigned(signer):
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so we still take away their slash points
			slasher.DecSlashPoints(slashCtx, observeSlashPoints+lackOfObservationPenalty, signer)
			attResult = "late"
		case ctx.BlockHeight() <= (voter.FinalisedHeight+observeFlex) &&
			!tx.IsFinal() && firstInTier &&
			voter.CreditsAcrossFinality(tx) &&
			outboundConsensusMatches(&voter, nas, tx):
			// The signer's first non-final observation landed after final
			// consensus, e.g. a sub-quorum attestation batch flushed after the
			// final quorum. Refund its observe point only: the lack penalty
			// belongs to the final tier.
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signer)
			attResult = "late"
		}

		// Gas correction for re-org re-observations.
		// When a voter is finalized but a re-observation has the same tx except gas
		// (likely due to a reorg changing the effective gas price), check if enough
		// nodes agree on the corrected gas to update vault accounting.
		if voter.FinalisedHeight > 0 &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			!voter.Tx.Tx.Gas.Equals(tx.Tx.Gas) &&
			voter.Tx.EqualsConsensusIgnoreGas(tx) {
			gasCorrectionSigners := countMatchingSigners(voter, tx, nas)
			if HasSuperMajority(gasCorrectionSigners, len(nas)) {
				correctOutboundGas(ctx, mgr, voter.Tx, tx)
				voter.Tx.Tx.Gas = tx.Tx.Gas
			}
		}
	}
	if !ok && voter.HasConsensus(nas) && !tx.IsFinal() && voter.FinalisedHeight == 0 {
		if voter.Height == 0 {
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "observe", "consensus"}, 1,
				[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("finalised", "false")},
			)
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
			signers := voter.GetOutboundObserveSigners()
			observers := excuseOtherTierObservers(&voter, signers, voter.GetOutboundOtherTierObserveSigners())
			nonSigners := getNonSigners(nas, observers)
			slasher.DecSlashPoints(slashCtx, observeSlashPoints, signers...)
			slasher.IncSlashPoints(slashCtx, lackOfObservationPenalty, nonSigners...)
			// Record the credited gas-agnostic observers on the consensus Tx so the
			// ObservationDelayFlexibility branch below (keyed off Tx.HasSigned) does
			// not refund the same node a second time when its own gas-used
			// observation arrives after consensus has already formed.
			voter.RecordObserveSigners(signers)
		} else if ctx.BlockHeight() <= (voter.Height+observeFlex) &&
			voter.Tx.IsFinal() == tx.IsFinal() &&
			voter.Tx.EqualsOutboundObservationCredit(tx) &&
			!voter.Tx.HasSigned(signer) {
			// Track already-decremented slash points with the consensus Tx's Signers list.
			voter.Tx.Signers = append(voter.Tx.Signers, signer.String())
			// event the tx had been processed , given the signer just a bit late , so still take away their slash points
			// but only when the tx signer are voting is the tx that already reached consensus
			slasher.DecSlashPoints(slashCtx, lateObserveRefund(voter, signer, observeSlashPoints, lackOfObservationPenalty), signer)
			attResult = "late"
		}

		// Gas correction for re-org re-observations (non-finalized consensus path).
		if voter.Height > 0 &&
			!voter.Tx.Tx.Gas.Equals(tx.Tx.Gas) &&
			voter.Tx.EqualsConsensusIgnoreGas(tx) {
			gasCorrectionSigners := countMatchingSigners(voter, tx, nas)
			if HasSuperMajority(gasCorrectionSigners, len(nas)) {
				correctOutboundGas(ctx, mgr, voter.Tx, tx)
				voter.Tx.Tx.Gas = tx.Tx.Gas
			}
		}
	}

	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "observe", "attestation"}, 1,
		[]metrics.Label{obsChain, obsDir, telemetry.NewLabel("result", attResult)},
	)

	return voter, ok, true
}

// inboundConsensusMatches reports whether a late inbound observation matches
// the consensus tx for late credit. After consensus, handleObservedTxInQuorum
// rewrites a memoless inbound's voter.Tx memo (r:<ref>, then the resolved
// memo), so a late observation of the same tx, still memoless, no longer
// equals voter.Tx. It is also checked against the consensus entry as observed.
func inboundConsensusMatches(voter *ObservedTxVoter, nas NodeAccounts, tx common.ObservedTx) bool {
	if voter.Tx.EqualsInboundObservationCredit(tx) {
		return true
	}
	observed := voter.ObservedConsensusTx(nas)
	return !observed.IsEmpty() && observed.EqualsInboundObservationCredit(tx)
}

// outboundConsensusMatches is the outbound counterpart. At final consensus,
// handleObservedTxOutQuorum gives a memoless outbound's voter.Tx the memo of
// its scheduled TxOutItem, so a late observation of the same tx, still
// memoless, no longer equals voter.Tx. Non-final consensus leaves voter.Tx
// unchanged, so only the late arms after final consensus need this.
func outboundConsensusMatches(voter *ObservedTxVoter, nas NodeAccounts, tx common.ObservedTx) bool {
	if voter.Tx.EqualsOutboundObservationCredit(tx) {
		return true
	}
	observed := voter.ObservedConsensusTx(nas)
	return !observed.IsEmpty() && observed.EqualsOutboundObservationCredit(tx)
}

// isNonFinalConsensus reports whether consensusTx is a non-final consensus tx,
// i.e. non-final consensus has already credited the non-final tier.
func isNonFinalConsensus(consensusTx common.ObservedTx) bool {
	return !consensusTx.IsEmpty() && !consensusTx.IsFinal()
}

// excuseOtherTierObservers records on the voter, at non-final consensus, the
// nodes that observed the tx only in the final tier, and returns them together
// with the credited signers: every node that observed the tx in either tier and
// so must not be charged the lack-of-observation penalty. A final-tier
// observation keeps its own observe point until final consensus credits it.
func excuseOtherTierObservers(voter *ObservedTxVoter, signers, otherTier []cosmos.AccAddress) []cosmos.AccAddress {
	observers := slices.Clone(signers)
	voter.ExcusedObservers = nil
	for _, observer := range otherTier {
		if slices.ContainsFunc(signers, func(signer cosmos.AccAddress) bool { return signer.Equals(observer) }) {
			continue
		}
		voter.ExcusedObservers = append(voter.ExcusedObservers, observer.String())
		observers = append(observers, observer)
	}
	return observers
}

// lateObserveRefund is the refund owed to a signer whose observation matches
// the non-final consensus tx within the flex window: its observe point plus the
// lack penalty charged at consensus, or only the observe point if it was
// excused from that penalty.
func lateObserveRefund(voter ObservedTxVoter, signer cosmos.AccAddress, observeSlashPoints, lackOfObservationPenalty int64) int64 {
	if voter.IsExcusedObserver(signer) {
		return observeSlashPoints
	}
	return observeSlashPoints + lackOfObservationPenalty
}

// lateFinalObservationExcused reports whether a final observation lands within
// the flex window of a non-final consensus that final consensus has not yet
// followed, from a node charged the lack penalty there: one that neither signed
// the consensus tx nor was already excused. The caller checks that tx matches
// the consensus tx.
func lateFinalObservationExcused(voter ObservedTxVoter, tx common.ObservedTx, signer cosmos.AccAddress, height, observeFlex int64) bool {
	return tx.IsFinal() && isNonFinalConsensus(voter.Tx) &&
		height <= voter.Height+observeFlex &&
		voter.CreditsAcrossFinality(tx) &&
		!voter.Tx.HasSigned(signer) && !voter.IsExcusedObserver(signer)
}

// countMatchingSigners counts the number of unique active node signers that have observed
// the exact same tx (including gas) as the given observation.
func countMatchingSigners(voter ObservedTxVoter, tx ObservedTx, nas NodeAccounts) int {
	signers := make(map[string]struct{})
	for _, otherTx := range voter.Txs {
		if otherTx.IsFinal() != tx.IsFinal() {
			continue
		}
		if !otherTx.ObservedPubKey.Equals(tx.ObservedPubKey) {
			continue
		}
		if !otherTx.EqualsConsensus(tx) {
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
			debitVault(ctx, mgr, &vault, consensusTx, common.NewCoins(delta), vaultDeficitReasonOutboundGasCorrection)
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

	ctx.Logger().Info(
		"corrected outbound gas after reorg",
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
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observation", "dropped"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "txout_vault_not_found")},
		)
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
		telemetry.IncrCounterWithLabels(
			[]string{"thornode", "observation", "dropped"},
			1,
			[]metrics.Label{telemetry.NewLabel("reason", "txout_voter_read_fail")},
		)
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
		if len(tx.SpentOutputRefs) > 0 && (voter.Height > 0 || voter.FinalisedHeight > 0) {
			saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, tx, activeNodeAccounts)
		}
		// A late final vote can complete the durable-nonce supermajority after the
		// outbound itself reached consensus. If that commit is a nonce-account creation
		// whose processing was deferred at quorum (below), book it now, exactly once:
		// the commit fires once per value.
		// Gated on the chain, not on this vote: the tally reads every recorded vote,
		// so a vote that omits SolNonce can still be the one that completes it.
		if tx.Tx.Chain.Equals(common.SOLChain) && saveSolVaultNonceWithQuorum(ctx, mgr, voter, activeNodeAccounts) &&
			voter.Height > 0 && isSolNonceCreateTx(voter.Tx) && solNonceCommittedBy(ctx, mgr, voter.Tx) {
			bookSolNonceCreateAsGas(ctx, mgr, voter.Tx)
		}
		return nil
	}

	k := mgr.Keeper()

	// Commit the vault's durable nonce BEFORE any early return below: every branch
	// (cancel/approval, inbound-memo slash, normal outbound) is a vault spend that
	// can advance the nonce. Gated on the chain, not on this vote (see above).
	if tx.Tx.Chain.Equals(common.SOLChain) {
		saveSolVaultNonceWithQuorum(ctx, mgr, voter, activeNodeAccounts)
	}

	// A Solana vault's durable-nonce account creation: the rent it moved into the nonce
	// account (plus the fee) is booked as vault gas, so the vault's recorded balance
	// keeps matching its main account (migrations never overshoot, solvency needs no
	// adjustment). It is an operational spend: no outbound to match, no slash. Checked
	// before the memo-based branches below, which would treat it as unmatched.
	if isSolNonceCreateTx(tx) {
		if solNonceCommittedBy(ctx, mgr, tx) {
			bookSolNonceCreateAsGas(ctx, mgr, tx)
			return nil
		}
		// Outbound consensus ignores SolNonce, so it can form before the nonce
		// supermajority does (one vote omitting or changing the nonce is enough). If
		// any final consensus vote attests this as the vault's nonce-account creation,
		// defer rather than slash it as an unmatched outflow: the late vote that
		// commits the nonce books it (above). Only a real creation carries a SolNonce
		// for this shape; bifrost never sends a durable payment to the nonce address.
		//
		// Residual: a single byzantine vote can attach a SolNonce to a genuine memoless
		// transfer of this shape and so defer it instead of slashing it. Every such
		// transfer pays the vault's OWN seed-derived nonce account, so the funds stay
		// under vault control and the attacker gains nothing; if no nonce ever commits,
		// the rent + fee (<= solNonceCreateMaxRent) stays unbooked, a bounded
		// accounting drift, not a loss.
		if voterAttestsSolNonceCreate(voter, tx) {
			ctx.Logger().Info("deferring durable-nonce account creation until its nonce commits", "txid", tx.Tx.ID)
			return nil
		}
	}

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
			telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
		}))

		if err := mgr.Slasher().SlashVault(slashCtx, tx.ObservedPubKey, toSlash, mgr); err != nil {
			ctx.Logger().Error("fail to slash account for sending extra fund", "error", err)
		}
		// The inbound-memo slash debit has the same silent-drop risk as a normal
		// outbound debit: bifrost reports the vault sent toSlash, but THORChain's
		// tracked balance may be below that. Record any underflow as deficit and
		// surface it instead of losing the accounting gap.
		debitVault(ctx, mgr, &vault, tx, toSlash, vaultDeficitReasonVaultSlash)
		if err := k.SetVault(ctx, vault); err != nil {
			ctx.Logger().Error("fail to save vault", "error", err)
		} else if tx.Tx.Chain.RequiresTerminalErrataOutcome() {
			// Record the principal debit only once it is stored, so errata
			// restores exactly what this observation removed from the vault.
			voter.UpdatedVault = true
			k.SetObservedTxOutVoter(ctx, voter)
		}

		return nil
	}

	// Process the handler if we can get the consensus tx.
	// Even if the handler fails, vault accounting must still happen because
	// the outbound transaction is irrevocable (funds already left the vault).
	vaultSlash := false
	slashObservedOutbound := func(reason string, err error) {
		ctx.Logger().Error("slash observed outbound", "reason", reason, "error", err, "tx", tx.Tx.String())
		toSlash := make(common.Coins, len(tx.Tx.Coins))
		copy(toSlash, tx.Tx.Coins)
		toSlash = toSlash.Add(tx.Tx.Gas.ToCoins()...)

		slashCtx := ctx.WithContext(context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{
			telemetry.NewLabel("reason", reason),
			telemetry.NewLabel("chain", telemChain(tx.Tx.Chain)),
		}))

		if slashErr := mgr.Slasher().SlashVault(slashCtx, tx.ObservedPubKey, toSlash, mgr); slashErr != nil {
			ctx.Logger().Error("fail to slash account for observed outbound", "error", slashErr)
		} else {
			vaultSlash = true
		}
	}
	txOut := voter.GetTx(activeNodeAccounts) // get consensus tx, in case our for loop is incorrect
	if txOut == nil || txOut.IsEmpty() {
		ctx.Logger().Error("fail to get consensus tx from voter", "txid", tx.Tx.ID)
		slashObservedOutbound("missing_consensus_tx", nil)
	} else {
		txOut.Tx.Memo = tx.Tx.Memo

		// For memoless outbounds, look up the original memo from the scheduled TxOutItem
		if txOut.Tx.Memo == "" {
			if originalMemo := findOriginalMemoForOutbound(ctx, mgr, tx); originalMemo != "" {
				txOut.Tx.Memo = originalMemo
				ctx.Logger().Info("resolved memoless outbound", "original_memo", originalMemo, "txid", tx.Tx.ID)
				// The per-vote link above parsed the empty raw memo, so a memoless
				// outbound has no observed link. The SOL durable-nonce landed-copy hold
				// finds copies only through that link: record it from the recovered memo.
				if tx.Tx.Chain.Equals(common.SOLChain) {
					if recovered, mErr := ParseMemoWithTHORNames(ctx, k, originalMemo); mErr == nil {
						if inhash := recovered.GetTxID(); !inhash.IsEmpty() {
							k.SetObservedLink(ctx, inhash, tx.Tx.ID)
						}
					}
				}
			}
		}

		if tx.Tx.Chain.IsEmpty() {
			ctx.Logger().Error("fail to process txOut: chain is empty", "tx", tx.Tx.String())
			slashObservedOutbound("empty_chain", nil)
		} else if m, mErr := processOneTxIn(ctx, k, *txOut, signer); mErr != nil {
			ctx.Logger().Error("fail to process txOut",
				"error", mErr,
				"tx", tx.Tx.String())
			slashObservedOutbound("invalid_outbound", mErr)
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
					medianKeysignMs := keysignMetric.GetMedianTime()
					evt := NewEventTssKeysignMetric(keysignMetric.TxID, medianKeysignMs)
					if emitErr := mgr.EventMgr().EmitEvent(ctx, evt); emitErr != nil {
						ctx.Logger().Error("fail to emit tss metric event", "error", emitErr)
					}
					// median keysign time climbs well before a degrading signing
					// party starts producing outright MsgTssKeysignFail
					telemetry.SetGaugeWithLabels(
						[]string{"thornode", "tss", "keysign", "time_ms"},
						float32(medianKeysignMs),
						[]metrics.Label{telemetry.NewLabel("chain", telemChain(tx.Tx.Chain))},
					)
				}
			}

			res, hErr := handler(ctx, m)
			if hErr != nil {
				ctx.Logger().Error("handler failed:", "error", hErr)
				slashObservedOutbound("failed_outbound", hErr)
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
	principalDebited := false
	vault, err := k.GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault", "error", err)
	} else {
		// if the vault was slashed we skipped the gas manager above and deduct gas directly
		if vaultSlash {
			// The slashed-vault gas debit can underflow the same way as the main
			// coin debit when the vault has insufficient gas reserve.
			debitVault(ctx, mgr, &vault, tx, tx.Tx.Gas.ToCoins(), vaultDeficitReasonVaultSlashGas)
		}

		// Don't add to or subtract from vault balances when the sender and recipient are the same
		// (particularly avoid Consolidate SafeSub zeroing of vault balances).
		if !tx.Tx.FromAddress.Equals(tx.Tx.ToAddress) {
			// skip deducting funds for outbound fake gas
			if !isOutboundFakeGasTx(tx) {
				// An outbound observation should normally be covered by the vault's
				// tracked balance. If it is not, the L1 already sent the funds, so
				// THORChain must record the deficit and halt/signal rather than keep
				// phantom credit on the vault.
				debitVault(ctx, mgr, &vault, tx, tx.Tx.Coins, vaultDeficitReasonOutbound)
				principalDebited = true
			}
			vault.OutboundTxCount++
		}

		if vault.IsAsgard() && memo.IsType(TxMigrate) {
			// only remove the block height that had been specified in the memo
			vault.RemovePendingTxBlockHeights(memo.GetBlockHeight())
		}

		// RetiringVault status should clear once all real coins are drained,
		// even if deficit accounting still remains on the vault.
		if !vault.HasFunds() && vault.Status == RetiringVault {
			// We have successfully removed all funds from a retiring vault;
			// mark it inactive so it no longer blocks churn or unbonding.
			if vault.HasDeficit() {
				ctx.Logger().Info(
					"Retiring vault has no funds but still has deficit; marking inactive",
					"vault_pubkey", vault.PubKey,
					"deficit", vault.Deficit.String(),
				)
			}
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
		} else if principalDebited && tx.Tx.Chain.RequiresTerminalErrataOutcome() {
			// Record the principal debit only once it is stored, so errata
			// restores exactly what this observation removed from the vault.
			voter.UpdatedVault = true
		}
	}
	saveXMRSpentOutputRefsWithQuorum(ctx, mgr, voter, tx, activeNodeAccounts)

	// Mark voter as done AFTER all vault operations to prevent
	// inconsistency if vault ops fail.
	voter.SetDone()
	k.SetObservedTxOutVoter(ctx, voter)

	ctx.Logger().Info("tx out processed", "chain", tx.Tx.Chain, "id", tx.Tx.ID, "finalized", tx.IsFinal())

	return nil
}

// saveSolVaultNonceWithQuorum commits a Solana vault's durable nonce from the
// observations of the vault transaction that set it: the nonce account's creation,
// or a durable outbound that advanced it. It writes only when
//   - the transaction itself reached FINAL consensus (voter.Tx), so a nonce advance
//     never outruns the consensus record LackSigning uses to detect a landed copy,
//   - a supermajority of active validators attested the IDENTICAL SolNonce on FINAL
//     observations of that consensus tx (no "absent matches anything" rule),
//   - the nonce account is the vault's own seed-derived account, and
//   - the attested slot is newer than the stored one (idempotent and monotonic).
//
// Signers build every durable outbound from this committed value, so the value a
// vault's durable txs use changes only here, when the tx that consumed the previous
// value is committed. It is called on every observation of the transaction, so a
// late final vote that completes the supermajority still commits. It never affects
// outbound processing: SolNonce is deliberately not part of EqualsConsensus.
// It reports whether this call committed a new value.
func saveSolVaultNonceWithQuorum(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter, nas NodeAccounts) bool {
	consensus := voter.Tx
	if voter.FinalisedHeight <= 0 || consensus.IsEmpty() || !consensus.IsFinal() {
		return false
	}
	hasCandidate := false
	for _, v := range voter.Txs {
		if v.SolNonce != nil {
			hasCandidate = true
			break
		}
	}
	if !hasCandidate {
		return false
	}
	// Late votes after the value is committed skip the tally (commit is idempotent).
	var committedSlot int64
	if vault, err := mgr.Keeper().GetVault(ctx, consensus.ObservedPubKey); err == nil {
		if existing, found, err := mgr.Keeper().GetSolVaultNonce(ctx, vault.PubKey); err == nil && found {
			committedSlot = existing.Slot
		}
	}
	for _, candidate := range voter.Txs {
		if candidate.SolNonce == nil || !candidate.Tx.Chain.Equals(common.SOLChain) || !candidate.IsFinal() ||
			candidate.SolNonce.Slot <= committedSlot {
			continue
		}
		// Outbound processing rewrites a memoless outbound's consensus memo to the
		// recovered one (voter.Tx aliases the stored consensus), so raw votes would no
		// longer match it and a late vote could never complete the tally. Compare on
		// the candidate's raw memo instead: counted votes still agree with each other
		// on the memo and with the consensus on every other field.
		ref := consensus
		ref.Tx.Memo = candidate.Tx.Memo
		voters := make(map[string]struct{})
		for _, tx := range voter.Txs {
			if tx.SolNonce == nil || !tx.IsFinal() || !tx.EqualsConsensus(ref) {
				continue
			}
			if !tx.Tx.ID.Equals(candidate.Tx.ID) ||
				!tx.Tx.FromAddress.Equals(candidate.Tx.FromAddress) ||
				!tx.ObservedPubKey.Equals(candidate.ObservedPubKey) {
				continue
			}
			if !common.SolNonceUpdatesEqual(tx.SolNonce, candidate.SolNonce) {
				continue
			}
			for _, signer := range tx.GetSigners() {
				if !nas.IsNodeKeys(signer) {
					continue
				}
				voters[signer.String()] = struct{}{}
			}
		}
		if !HasSuperMajority(len(voters), len(nas)) {
			continue
		}
		return commitSolVaultNonce(ctx, mgr, candidate)
	}
	return false
}

// commitSolVaultNonce writes an agreed durable-nonce update for the observing vault,
// after checking it names the vault's own seed-derived nonce account and is newer
// than what is stored.
func commitSolVaultNonce(ctx cosmos.Context, mgr Manager, obs common.ObservedTx) bool {
	k := mgr.Keeper()
	vault, err := k.GetVault(ctx, obs.ObservedPubKey)
	if err != nil {
		ctx.Logger().Error("fail to get vault for sol durable nonce", "pubkey", obs.ObservedPubKey, "error", err)
		return false
	}
	vaultAddr, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
	if err != nil || !vaultAddr.Equals(obs.Tx.FromAddress) {
		ctx.Logger().Error("sol durable nonce observation is not from the vault's SOL address",
			"vault", vault.PubKey, "from", obs.Tx.FromAddress, "error", err)
		return false
	}
	expected, err := common.SolNonceAccountAddress(obs.Tx.FromAddress.String())
	if err != nil || expected != obs.SolNonce.NonceAccount {
		ctx.Logger().Error("sol durable nonce account is not the vault's seed-derived account",
			"vault", vault.PubKey, "from", obs.Tx.FromAddress, "attested", obs.SolNonce.NonceAccount, "expected", expected, "error", err)
		return false
	}
	existing, found, err := k.GetSolVaultNonce(ctx, vault.PubKey)
	if err != nil {
		ctx.Logger().Error("fail to get sol vault nonce", "vault", vault.PubKey, "error", err)
		return false
	}
	if found && existing.Slot >= obs.SolNonce.Slot {
		return false // already committed (idempotent), or an older update
	}
	// The previous ACTIVE commit (none on a first commit or after a break-glass
	// clear) bounds which pending outbounds were signed durably with a now-consumed
	// nonce and can be re-issued straight away (reissueSupersededSolDurable).
	prev, prevActive, err := activeSolVaultNonce(ctx, k, vault.PubKey)
	if err != nil {
		ctx.Logger().Error("fail to get active sol vault nonce", "vault", vault.PubKey, "error", err)
		prevActive = false
	}
	k.SetSolVaultNonce(ctx, SolVaultNonce{
		VaultPubKey:   vault.PubKey,
		NonceAccount:  obs.SolNonce.NonceAccount,
		Nonce:         obs.SolNonce.Nonce,
		Slot:          obs.SolNonce.Slot,
		UpdatedHeight: ctx.BlockHeight(),
		SetByTx:       obs.Tx.ID,
	})
	ctx.Logger().Info("committed sol durable nonce", "vault", vault.PubKey, "nonce", obs.SolNonce.Nonce,
		"slot", obs.SolNonce.Slot, "set_by_tx", obs.Tx.ID)
	if prevActive {
		reissueSupersededSolDurable(ctx, mgr, vault, prev.UpdatedHeight, &obs)
	}
	return true
}

// bookSolNonceCreateAsGas books a nonce-account creation's rent plus fee as vault gas.
func bookSolNonceCreateAsGas(ctx cosmos.Context, mgr Manager, tx common.ObservedTx) {
	gasTx := tx
	gasTx.Tx.Gas = common.Gas(tx.Tx.Gas.ToCoins().Add(tx.Tx.Coins...)) // Add returns a new slice
	if err := addGasFees(ctx, mgr, gasTx); err != nil {
		ctx.Logger().Error("fail to book durable-nonce account rent as gas", "error", err)
	}
}

// voterAttestsSolNonceCreate reports whether any final vote matching tx's consensus
// fields attests a durable nonce for tx's destination (the vault's nonce account).
func voterAttestsSolNonceCreate(voter ObservedTxVoter, tx common.ObservedTx) bool {
	for _, v := range voter.Txs {
		if v.SolNonce != nil && v.IsFinal() && v.EqualsConsensus(tx) && v.SolNonce.NonceAccount == tx.Tx.ToAddress.String() {
			return true
		}
	}
	return false
}

// solNonceCommittedBy reports whether tx is the transaction that set the observing
// vault's committed durable nonce, i.e. a supermajority attested the nonce it
// created (saveSolVaultNonceWithQuorum runs just before this check). This is the
// agreed evidence that a memoless transfer to the nonce account is the account's
// creation and not a scheduled (memoless) payment to that address.
func solNonceCommittedBy(ctx cosmos.Context, mgr Manager, tx common.ObservedTx) bool {
	vault, err := mgr.Keeper().GetVault(ctx, tx.ObservedPubKey)
	if err != nil {
		return false
	}
	record, found, err := mgr.Keeper().GetSolVaultNonce(ctx, vault.PubKey)
	return err == nil && found && record.SetByTx.Equals(tx.Tx.ID)
}

// saveXMRSpentOutputRefsWithQuorum persists XMR spent-output refs for the
// voter's consensus outbound once a supermajority of active nodes has
// reported matching refs. Safe to call repeatedly: it skips the write when
// the refs are already stored.
func saveXMRSpentOutputRefsWithQuorum(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter, tx common.ObservedTx, activeNodeAccounts NodeAccounts) {
	if !tx.Tx.Chain.Equals(common.XMRChain) {
		return
	}
	if len(tx.SpentOutputRefs) == 0 {
		return
	}
	if voter.Height <= 0 && voter.FinalisedHeight <= 0 {
		return
	}

	// Spent refs are side metadata for the outbound that already reached
	// consensus. Do not let a separate ref-bearing observation select its own
	// outbound; it must attach to the voter's consensus tx.
	consensusTx := voter.GetTx(activeNodeAccounts)
	if consensusTx == nil || consensusTx.IsEmpty() {
		return
	}

	refTx := findSpentOutputRefs(*consensusTx, voter, activeNodeAccounts)
	if len(refTx.SpentOutputRefs) == 0 {
		return
	}

	existing, err := mgr.Keeper().GetXMRSpentOutputRefs(ctx, refTx.ObservedPubKey)
	existingSet := make(map[string]struct{}, len(existing))
	if err != nil {
		ctx.Logger().Error("fail to get xmr spent output refs", "error", err)
	} else {
		for _, ref := range existing {
			existingSet[common.SpentOutputRefIdentity(ref)] = struct{}{}
		}
	}

	allExisting := len(existingSet) > 0
	for _, ref := range refTx.SpentOutputRefs {
		if _, ok := existingSet[common.SpentOutputRefIdentity(ref)]; !ok {
			allExisting = false
			break
		}
	}
	if allExisting {
		return
	}

	if err := mgr.Keeper().SetXMRSpentOutputRefs(ctx, refTx.ObservedPubKey, refTx.Tx.ID, refTx.SpentOutputRefs, refTx.BlockHeight, ctx.BlockHeight()); err != nil {
		ctx.Logger().Error("fail to save xmr spent output refs", "error", err)
	}
}

// findSpentOutputRefs returns the observed tx whose spent-output refs reached
// supermajority for this outbound voter's consensus tx.
func findSpentOutputRefs(consensusTx common.ObservedTx, voter ObservedTxVoter, nas NodeAccounts) common.ObservedTx {
	for _, candidate := range voter.Txs {
		// Candidate ref sets are only eligible when their outbound payload is the
		// same payload that already reached consensus.
		if !matchesConsensusOutboundIgnoringSpentRefs(candidate, consensusTx) || len(candidate.SpentOutputRefs) == 0 {
			continue
		}

		voters := make(map[string]struct{})
		for _, tx := range voter.Txs {
			if tx.IsFinal() != candidate.IsFinal() {
				continue
			}
			if !tx.ObservedPubKey.Equals(candidate.ObservedPubKey) {
				continue
			}
			if !tx.Tx.EqualsEx(candidate.Tx) {
				continue
			}
			if !tx.HasEquivalentXMRTxKey(candidate) {
				continue
			}
			// Count only observations that both agree with the candidate ref set
			// and still bind back to the consensus outbound.
			if !matchesConsensusOutboundIgnoringSpentRefs(tx, consensusTx) {
				continue
			}
			if !common.SpentOutputRefsEqual(tx.SpentOutputRefs, candidate.SpentOutputRefs) {
				continue
			}
			for _, signer := range tx.GetSigners() {
				if !nas.IsNodeKeys(signer) {
					continue
				}
				voters[signer.String()] = struct{}{}
			}
		}

		if HasSuperMajority(len(voters), len(nas)) {
			return candidate
		}
	}

	return common.ObservedTx{}
}

// matchesConsensusOutboundIgnoringSpentRefs binds spent-ref metadata to the
// already-consensed outbound payload. XMR observers can report the same
// outbound at different external heights, so height fields are not part of
// this metadata identity check.
func matchesConsensusOutboundIgnoringSpentRefs(tx, consensusTx common.ObservedTx) bool {
	if !tx.Tx.Chain.Equals(common.XMRChain) || !consensusTx.Tx.Chain.Equals(common.XMRChain) {
		return false
	}
	if tx.IsFinal() != consensusTx.IsFinal() {
		return false
	}
	if !tx.Tx.EqualsEx(consensusTx.Tx) {
		return false
	}
	if !tx.ObservedPubKey.Equals(consensusTx.ObservedPubKey) {
		return false
	}
	if !strings.EqualFold(tx.Aggregator, consensusTx.Aggregator) {
		return false
	}
	if !strings.EqualFold(tx.AggregatorTarget, consensusTx.AggregatorTarget) {
		return false
	}

	emptyAmt := cosmos.ZeroUint()
	if tx.AggregatorTargetLimit == nil {
		tx.AggregatorTargetLimit = &emptyAmt
	}
	if consensusTx.AggregatorTargetLimit == nil {
		consensusTx.AggregatorTargetLimit = &emptyAmt
	}
	if !tx.AggregatorTargetLimit.Equal(*consensusTx.AggregatorTargetLimit) {
		return false
	}

	return tx.HasEquivalentXMRTxKey(consensusTx)
}

type vaultDeficitReason string

const (
	vaultDeficitReasonOutbound              vaultDeficitReason = "outbound"
	vaultDeficitReasonOutboundGas           vaultDeficitReason = "outbound_gas"
	vaultDeficitReasonOutboundGasCorrection vaultDeficitReason = "outbound_gas_correction"
	vaultDeficitReasonVaultSlash            vaultDeficitReason = "vault_slash"
	vaultDeficitReasonVaultSlashGas         vaultDeficitReason = "vault_slash_gas"
	vaultDeficitReasonInboundErrataReversal vaultDeficitReason = "inbound_errata_reversal"
	vaultDeficitReasonNoVault               vaultDeficitReason = "novault"
)

// debitVault subtracts funds and handles any newly-created deficit.
// It mutates the vault in memory; callers are still responsible for SetVault.
// The requested coins are explicit so gas and main coin debits share the same path.
func debitVault(ctx cosmos.Context, mgr Manager, vault *Vault, tx common.ObservedTx, requested common.Coins, reason vaultDeficitReason) {
	if missed := vault.SubFundsWithDeficit(requested); !missed.IsEmpty() {
		handleVaultDeficitCreated(ctx, mgr, mgr.Keeper(), *vault, tx, requested, missed, reason)
	}
}

// handleVaultDeficitCreated emits security signals for newly-created vault deficit.
func handleVaultDeficitCreated(ctx cosmos.Context, mgr Manager, k keeper.Keeper, vault Vault, tx common.ObservedTx, requested common.Coins, missed common.Coins, reason vaultDeficitReason) {
	requestedByAsset := common.Coins{}.Add(requested...).NoneEmpty()
	missedByAsset := common.Coins{}.Add(missed...).NoneEmpty()
	for _, coin := range missedByAsset {
		requestedAmount := requestedByAsset.GetCoin(coin.Asset).Amount
		accountedFromCoins := common.SafeSub(requestedAmount, coin.Amount)
		deficitTotal := vault.GetDeficit(coin.Asset).Amount
		msg := fmt.Sprintf(
			"vault deficit created: vault=%s tx=%s reason=%s asset=%s requested=%s accounted_from_coins=%s deficit_delta=%s deficit_total=%s",
			vault.PubKey, tx.Tx.ID, reason, coin.Asset, requestedAmount, accountedFromCoins, coin.Amount, deficitTotal,
		)
		ctx.Logger().Error(msg)

		// The ledger just discovered missing funds. Today this produces a log line
		// and an on-chain security event and nothing scrapeable; the reason label
		// discriminates benign gas rounding from funds-missing.
		deficitLabels := []metrics.Label{
			telemetry.NewLabel("reason", string(reason)),
			telemetry.NewLabel("asset", coin.Asset.String()),
		}
		telemetry.IncrCounterWithLabels([]string{"thornode", "vault", "deficit_created"}, 1, deficitLabels)
		telemetry.IncrCounterWithLabels([]string{"thornode", "vault", "deficit_created_amount"}, telem(coin.Amount), deficitLabels)

		if err := mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(tx.Tx, msg)); err != nil {
			ctx.Logger().Error("fail to emit vault deficit security event", "error", err)
		}

		if shouldHaltForVaultDeficit(ctx, mgr, k, reason, common.NewCoin(coin.Asset, deficitTotal)) {
			// Halt signing and trading, not the chain-scanner solvency halt. Bifrost
			// should keep observing while THORChain stops new outbounds and trades
			// against the affected chain until operators investigate and unhalt.
			chain := coin.Asset.GetChain()
			telemetry.IncrCounterWithLabels(
				[]string{"thornode", "vault", "deficit_halt"},
				1,
				[]metrics.Label{
					telemetry.NewLabel("chain", chain.String()),
					telemetry.NewLabel("reason", string(reason)),
				},
			)
			setHaltMimirIfNotPreserved(ctx, k, mgr, fmt.Sprintf(constants.MimirTemplateHaltSigning, chain))
			setHaltMimirIfNotPreserved(ctx, k, mgr, fmt.Sprintf(constants.MimirTemplateHaltTrading, chain))
		}
	}
}

// shouldHaltForVaultDeficit applies reason-specific halt policy for new deficits.
func shouldHaltForVaultDeficit(ctx cosmos.Context, mgr Manager, k keeper.Keeper, reason vaultDeficitReason, deficit common.Coin) bool {
	switch reason {
	case vaultDeficitReasonOutbound, vaultDeficitReasonVaultSlash, vaultDeficitReasonVaultSlashGas, vaultDeficitReasonNoVault:
		return true
	case vaultDeficitReasonOutboundGas, vaultDeficitReasonOutboundGasCorrection:
		return gasDeficitCrossesHaltThreshold(ctx, mgr, k, deficit)
	case vaultDeficitReasonInboundErrataReversal:
		return false
	default:
		return true
	}
}

// gasDeficitCrossesHaltThreshold checks the total per-asset deficit against current max gas.
// It is only called for gas-related deficit reasons.
func gasDeficitCrossesHaltThreshold(ctx cosmos.Context, mgr Manager, k keeper.Keeper, deficit common.Coin) bool {
	multiplier := k.GetConfigInt64(ctx, constants.VaultDeficitGasHaltMaxGasMultiplier)
	if multiplier <= 0 {
		return false
	}
	chain := deficit.Asset.GetChain()
	maxGasCoin, err := mgr.GasMgr().GetMaxGas(ctx, chain)
	if err != nil {
		ctx.Logger().Error("fail to get max gas for vault deficit halt threshold", "chain", chain, "error", err)
		return true
	}
	if maxGasCoin.Amount.IsZero() {
		ctx.Logger().Error("max gas is zero for vault deficit halt threshold", "chain", chain)
		return false
	}
	threshold := maxGasCoin.Amount.MulUint64(uint64(multiplier))
	return deficit.Amount.GTE(threshold)
}

// setHaltMimirIfNotPreserved writes ctx.BlockHeight() to a halt mimir unless
// the operator has set the indefinite-manual sentinel (1), in which case the
// halt is preserved. A halt scheduled for a future block is overwritten:
// the vault deficit signals a present-time divergence that warrants halting
// now rather than waiting for the scheduled height.
//
// Differs from processSolvencyAttestation's guard, which preserves future
// halts because it can also unhalt — overwriting there would let a future
// halt be unhalted by an earlier solvent attestation. This function only
// sets, never unhalts, so future halts are safe to overwrite.
func setHaltMimirIfNotPreserved(ctx cosmos.Context, k keeper.Keeper, mgr Manager, key string) {
	haltVal, err := k.GetMimir(ctx, key)
	if err != nil {
		ctx.Logger().Error("fail to get mimir", "key", key, "error", err)
	}
	// Preserve manual indefinite halt; same-block dedupe avoids duplicate events.
	if haltVal == 1 || haltVal == ctx.BlockHeight() {
		return
	}
	k.SetMimir(ctx, key, ctx.BlockHeight())
	ev := NewEventSetMimir(strings.ToUpper(key), strconv.FormatInt(ctx.BlockHeight(), 10))
	if err := mgr.EventMgr().EmitEvent(ctx, ev); err != nil {
		ctx.Logger().Error("fail to emit set_mimir event for vault deficit", "key", key, "error", err)
	}
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
	searchPeriod := signingTransPeriod
	switch {
	case tx.Tx.Chain.Equals(common.XMRChain):
		searchPeriod = signingTransPeriod * xmrSigningPeriodMultiplier
	case isObservedEVMAggregatorOutbound(tx):
		searchPeriod = signingTransPeriod * evmAggregatorSigningPeriodMultiplier
	case isObservedEVMTokenOutbound(tx):
		searchPeriod = signingTransPeriod * evmTokenSigningPeriodMultiplier
	}

	// Search recent TxOut blocks for matching TxOutItem
	// Use the same range logic as handler_common_outbound.go
	earliestHeight := ctx.BlockHeight() - searchPeriod
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
			// CommonOutboundTxHandler records the current observation's OutHash
			// before memo recovery runs, so that exact item remains eligible here.
			// Exclude every other completed item; duplicate delivery of the current
			// transaction is stopped earlier by the ObservedTxVoter done guard.
			if !item.OutHash.IsEmpty() && !item.OutHash.Equals(tx.Tx.ID) {
				continue
			}
			if !item.Chain.Equals(tx.Tx.Chain) {
				continue
			}
			if !common.EquivalentAddresses(item.Chain, item.ToAddress, tx.Tx.ToAddress) {
				continue
			}
			if !txOutPubKeyMatchesObserved(tx.Tx.Chain, tx.ObservedPubKey, item) {
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

// resolveReferenceMemo resolves and parses a memoless reference memo before
// processing. A successful resolution updates both the stored voter and the
// in-flight observed tx so processing, refund, and completion paths share the
// same memo. A failed resolution empties only the voter memo to prevent
// execution; the in-flight copy keeps the raw r:<ref> for refund events. When
// memoless transactions are halted, an otherwise-valid reference is resolved
// only into the in-flight copy so it can provide a safe refund address without
// becoming executable.
func resolveReferenceMemo(ctx cosmos.Context, mgr Manager, k keeper.Keeper, tx *common.ObservedTx, voter *ObservedTxVoter) (mem.Memo, bool) {
	if len(tx.Tx.Coins) == 0 {
		return mem.EmptyMemo, false
	}

	preMemo := voter.Tx.Tx.Memo
	referenceMemo, err := ParseMemoWithTHORNames(ctx, k, preMemo)
	if err != nil || !referenceMemo.IsType(TxReferenceReadMemo) {
		return mem.EmptyMemo, false
	}

	asset := tx.Tx.Coins[0].Asset
	lookup := lookupMemoFromReference(ctx, mgr, asset, voter.Tx.Tx, voter.UnfinalizedHeight)
	resolvedMemo := lookup.memo
	// The voter memo is the source used by processOneTxIn. Store the lookup
	// result even when it is empty so a missing, expired, or exhausted
	// reference cannot execute using the raw r:<ref> memo.
	voter.Tx.Tx.Memo = resolvedMemo
	ctx.Logger().Info("reference memo conversion", "pre", preMemo, "post", resolvedMemo, "asset", asset)

	if resolvedMemo == "" {
		// Do not also blank tx.Tx.Memo. This in-flight copy is used by refund
		// events, where preserving the observed r:<ref> is more informative than
		// replacing it with an empty memo and does not make the reference executable.
		k.SetObservedTxInVoter(ctx, *voter)
		return mem.EmptyMemo, false
	}

	parsedMemo, err := ParseMemoWithTHORNames(ctx, k, resolvedMemo)
	if err != nil {
		voter.Tx.Tx.Memo = ""
		k.SetObservedTxInVoter(ctx, *voter)
		return mem.EmptyMemo, false
	}
	if parsedMemo.IsOutbound() || parsedMemo.IsInternal() {
		ctx.Logger().Error("resolved reference memo expands to a forbidden type; blanking",
			"type", parsedMemo.GetType().String(), "tx", tx.Tx.ID.String())
		voter.Tx.Tx.Memo = ""
		k.SetObservedTxInVoter(ctx, *voter)
		return mem.EmptyMemo, false
	}

	tx.Tx.Memo = resolvedMemo
	if lookup.halted {
		// HaltMemoless must stop execution, but sender-opaque chains still need
		// the validated registered memo on the in-flight copy so refundTx can
		// recover its explicit refund address. Keep the stored voter empty and
		// do not report a successful resolution, which also prevents usage from
		// being tracked by the caller.
		voter.Tx.Tx.Memo = ""
		k.SetObservedTxInVoter(ctx, *voter)
		return mem.EmptyMemo, false
	}

	k.SetObservedTxInVoter(ctx, *voter)
	// The caller parsed its local memo before reference resolution. Return the
	// resolved parsed value so post-handler HasOutbound/SetDone decisions use
	// the registered transaction type rather than TxReferenceReadMemo.
	return parsedMemo, true
}

type referenceMemoLookupResult struct {
	memo   string
	halted bool
}

// fetchMemoFromReference fetches a memo without consuming the reference. Usage
// is recorded only after the resolved transaction has been accepted for execution.
func fetchMemoFromReference(ctx cosmos.Context, mgr Manager, asset common.Asset, tx common.Tx, txObservationHeight int64) string {
	lookup := lookupMemoFromReference(ctx, mgr, asset, tx, txObservationHeight)
	if lookup.halted {
		return ""
	}
	return lookup.memo
}

// lookupMemoFromReference validates a reference independently from the
// operational halt. Callers that process transactions must still reject a
// halted lookup, while refund handling can use an otherwise-valid memo solely
// to recover its explicit refund address.
func lookupMemoFromReference(ctx cosmos.Context, mgr Manager, asset common.Asset, tx common.Tx, txObservationHeight int64) referenceMemoLookupResult {
	mem, err := ParseMemoWithTHORNames(ctx, mgr.Keeper(), tx.Memo)
	if err != nil {
		ctx.Logger().Debug("failed to parse memo in fetchMemoFromReference", "error", err, "memo", tx.Memo, "txid", tx.ID)
	}
	if mem.GetType() == TxReferenceReadMemo {
		m, ok := mem.(ReferenceReadMemo)
		if !ok {
			return referenceMemoLookupResult{memo: tx.Memo}
		}

		result := referenceMemoLookupResult{}
		haltMemoless, err := mgr.Keeper().GetMimir(ctx, constants.HaltMemoless.String())
		if err == nil && haltMemoless > 0 {
			result.halted = true
		}

		// Check if the reference memo exists before proceeding
		if !mgr.Keeper().ReferenceMemoExists(ctx, asset, m.Reference) {
			ctx.Logger().Debug("reference memo not found", "reference", m.Reference, "asset", asset)
			return result
		}

		refMemo, err := mgr.Keeper().GetReferenceMemo(ctx, asset, m.Reference)
		if err != nil {
			ctx.Logger().Error("unable to fetch pool for ref memo lookup", "error", err, "asset", asset)
			return result
		}

		// Ensure transaction was observed after the reference memo was created
		if txObservationHeight <= refMemo.Height {
			ctx.Logger().Info("transaction observed before reference memo creation", "tx_height", txObservationHeight, "memo_height", refMemo.Height, "reference", m.Reference)
			// Do not track this attempt against the current record. Reference numbers
			// are recycled, so the record may belong to a later registrant whose
			// usage allowance must not be consumed by an older observation.
			return result
		}

		ttl := mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnTTL)
		if !refMemo.IsExpired(ctx.BlockHeight(), ttl) {
			// Check usage limit
			maxUse := mgr.Keeper().GetConfigInt64(ctx, constants.MemolessTxnMaxUse)
			if maxUse > 0 && refMemo.GetUsageCount() >= maxUse {
				ctx.Logger().Info("reference memo usage limit exceeded in fetchMemoFromReference", "reference", m.Reference, "usage_count", refMemo.GetUsageCount(), "max_use", maxUse)
				return result
			}

			result.memo = refMemo.Memo
			return result
		}

		return result
	}
	return referenceMemoLookupResult{memo: tx.Memo}
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
// still match the original staged asset. This relies on staged outbounds and
// voter.Actions preserving the same prepared-output order; if that ordering
// model changes in the future, this positional mapping must be revisited.
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
	if msg.State == nil {
		return nil, fmt.Errorf("pending adv swap item missing state")
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
			// Do not finalize the gross outbound if deferred affiliate handling
			// fails. Let the surrounding CacheContext abort so staged outbounds
			// and reserved mempool state remain intact for retry.
			return nil, fmt.Errorf("fail to take deferred affiliate fee: %w", feeErr)
		}

		// For Trade and Secured Assets, keep the staged outbound amounts as-is.
		// Their affiliate fee is taken later from the user's balance, so there
		// is no reduced outbound amount to spread back here.
		asset := groupItem.Coin.Asset
		if asset.IsTradeAsset() || asset.IsSecuredAsset() {
			continue
		}

		// Spread any reduced net amount back over the split pieces in order.
		// We intentionally keep this deterministic left-to-right remainder
		// assignment instead of rebalancing dust across pieces, because rollback
		// and outbound matching only care about the total net amount and stable
		// ordering is easier to reason about than trying to "smooth" the split.
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

	finalActions := make([]TxOutItem, 0, len(finalizedOutbounds))

	// Move each staged outbound to its final form.
	for _, finalized := range finalizedOutbounds {
		finalItem := finalized.item
		if finalItem.Coin.Amount.IsZero() {
			continue
		}

		// Materialize the staged outbound through the finalized txout path so
		// outbound fees and their accounting side effects are applied only now,
		// not while the swap was still speculative in the mempool.
		materializedOutputs, materializeErr := tos.finalizeStagedTxOutItem(ctx, mgr, finalItem, finalized.scheduledHeight)
		if materializeErr != nil {
			ctx.Logger().Error("fail to finalize staged outbound", "error", materializeErr, "inhash", finalItem.InHash)
			return fmt.Errorf("fail to finalize staged outbound: %w", materializeErr)
		}

		// Replace the speculative gross action(s) with the finalized outputs so
		// later outbound matching and slasher logic see the real net amounts.
		finalActions = append(finalActions, materializedOutputs...)
		if len(materializedOutputs) == 0 {
			// Some staged outputs can disappear after final fee deduction or dust
			// handling. In that case nothing remains to log or queue.
			continue
		}

		targetHeight := finalized.scheduledHeight
		if targetHeight < ctx.BlockHeight() {
			targetHeight = ctx.BlockHeight()
		}

		if finalItem.Chain.IsTHORChain() {
			ctx.Logger().Info("executed staged native outbound on finalization",
				"inhash", finalItem.InHash,
				"asset", materializedOutputs[0].Coin.Asset,
				"amount", sumPreparedTxOutAmount(materializedOutputs))
		} else {
			ctx.Logger().Info("moved staged outbound to queue",
				"inhash", finalItem.InHash,
				"chain", finalItem.Chain,
				"target_height", targetHeight,
				"original_scheduled", finalized.scheduledHeight)
		}
	}

	if len(voter.Actions) > 0 {
		voter.Actions = finalActions
		mgr.Keeper().SetObservedTxInVoter(ctx, voter)
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

	// Roll back any clout spent during outbound scheduling for the staged
	// mempool outbounds. Without this the swapper addresses would have
	// artificially inflated Spent counters, reducing their Available clout
	// for future swaps even though the outbound never actually went out.
	if err = reclaimStagedClout(ctx, keeper, voter, stagedItems); err != nil {
		ctx.Logger().Error("fail to reclaim staged clout on rollback", "error", err, "hash", voter.TxID)
		// Non-fatal: clout auto-restores after CloutReset blocks, so a
		// failure here should not abort the entire rollback.
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

	// Emit a security event so THORSec is notified of the rollback.
	securityEvent := NewEventSecurity(voter.Tx.Tx, "mempool swap rolled back")
	if err = mgr.EventMgr().EmitEvent(ctx, securityEvent); err != nil {
		ctx.Logger().Error("fail to emit security event for mempool rollback", "error", err)
	}

	return nil
}

// reclaimStagedClout reverses the swapper-clout Spent increments that were
// applied when staged mempool outbounds were originally scheduled. This mirrors
// the reclaim logic in handler_common_outbound for observed outbounds, but is
// triggered during rollback since the outbound never actually went out.
func reclaimStagedClout(ctx cosmos.Context, k keeper.Keeper, voter ObservedTxVoter, stagedItems []keeper.StagedOutbound) error {
	fromAddr := voter.Tx.Tx.FromAddress

	for _, staged := range stagedItems {
		spent := staged.TxOutItem.CloutSpent
		if spent == nil || spent.IsZero() {
			continue
		}

		cloutIn, err := k.GetSwapperClout(ctx, fromAddr)
		if err != nil {
			return fmt.Errorf("fail to get swapper clout for sender %s: %w", fromAddr, err)
		}
		cloutOut, err := k.GetSwapperClout(ctx, staged.TxOutItem.ToAddress)
		if err != nil {
			return fmt.Errorf("fail to get swapper clout for recipient %s: %w", staged.TxOutItem.ToAddress, err)
		}

		clout1, clout2 := calcReclaim(cloutIn.Claimable(), cloutOut.Claimable(), *spent)

		cloutIn.Reclaim(clout1)
		cloutIn.LastReclaimHeight = ctx.BlockHeight()
		if err = k.SetSwapperClout(ctx, cloutIn); err != nil {
			return fmt.Errorf("fail to save sender clout: %w", err)
		}

		if cloutIn.Address.Equals(cloutOut.Address) {
			// cloutOut is about to overwrite cloutIn, so reincrement with clout1.
			cloutOut.Reclaim(clout1)
		}
		cloutOut.Reclaim(clout2)
		cloutOut.LastReclaimHeight = ctx.BlockHeight()
		if err = k.SetSwapperClout(ctx, cloutOut); err != nil {
			return fmt.Errorf("fail to save recipient clout: %w", err)
		}

		ctx.Logger().Info("reclaimed staged clout on mempool rollback",
			"hash", voter.TxID,
			"spent", spent,
			"reclaim_in", clout1,
			"reclaim_out", clout2)
	}
	return nil
}
