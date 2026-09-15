package thorchain

import (
	"fmt"
	"math"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// addMempoolSwapRecoveryHeight adds positive block delays without allowing a
// user-supplied interval or an extreme Mimir value to wrap an indexed recovery
// height into the past. Saturation is preferable to overflow: normal policy
// bounds keep production values far below MaxInt64, while corrupt configuration
// remains deterministic and visible to invariants/operators.
func addMempoolSwapRecoveryHeight(height int64, delays ...int64) int64 {
	for _, delay := range delays {
		if delay <= 0 {
			continue
		}
		if height > math.MaxInt64-delay {
			return math.MaxInt64
		}
		height += delay
	}
	return height
}

func multiplyMempoolSwapRecoveryDelay(value, multiplier int64) int64 {
	if value <= 0 || multiplier <= 0 {
		return 0
	}
	if value > math.MaxInt64/multiplier {
		return math.MaxInt64
	}
	return value * multiplier
}

// mempoolSwapRecoveryStreamInterval bounds the finality lock grace granted to
// a market swap which still has slices left to run. Advanced-queue
// normalization reduces a request whose interval exceeds the maximum stream
// lifetime to one slice, but deliberately preserves the requested interval in
// queue state. Using that raw uint64 as a recovery delay would let an
// in-flight swap own a pool for an attacker-chosen duration (and int64
// conversion could wrap). The queue's configured maximum lifetime is the
// largest grace that can be justified; once the swap settles its caller drops
// the grace entirely.
func mempoolSwapRecoveryStreamInterval(ctx cosmos.Context, k keeper.Keeper, msg MsgSwap) int64 {
	if msg.State == nil || msg.State.Interval == 0 || !msg.IsMarketSwap() {
		return 0
	}
	maxLengthName := constants.StreamingSwapMaxLength
	if len(msg.Tx.Coins) == 1 && msg.Tx.Coins[0].Asset.IsNative() && msg.TargetAsset.IsNative() {
		maxLengthName = constants.StreamingSwapMaxLengthNative
	}
	maxLength := k.GetConfigInt64(ctx, maxLengthName)
	if maxLength <= 0 {
		return 0
	}
	if msg.State.Interval > uint64(maxLength) {
		return maxLength
	}
	return int64(msg.State.Interval)
}

// mempoolSwapRecoveryScheduleDone reports whether a lifecycle status means the
// swap will never execute another advanced-queue slice. Settlement marks the
// record STAGED or SETTLED as it drops the queue item, so from that point the
// stream/TTL grace below would only wait on work that has already finished.
func mempoolSwapRecoveryScheduleDone(status types.MempoolSwapStatus) bool {
	return status == types.MempoolSwapStatus_STAGED || status == types.MempoolSwapStatus_SETTLED
}

// mempoolSwapRecoveryHeight returns the next height at which recovery should
// inspect an active speculative swap. The normal window matches LackSigning's
// historical retry policy, while streams and limit orders which still have
// slices to run are never expired before their next scheduled execution or
// configured TTL.
func mempoolSwapRecoveryHeight(ctx cosmos.Context, k keeper.Keeper, msg MsgSwap, status types.MempoolSwapStatus) int64 {
	signingPeriod := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	if signingPeriod <= 0 {
		signingPeriod = k.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	}
	attempts := k.GetConfigInt64(ctx, constants.MaxOutboundAttempts)
	if attempts <= 0 {
		attempts = defaultMempoolSwapTimeoutAttempts
	}

	next := addMempoolSwapRecoveryHeight(
		ctx.BlockHeight(),
		multiplyMempoolSwapRecoveryDelay(signingPeriod, attempts),
	)
	// A finished swap only waits on inbound finality, not on its own schedule.
	// Normalization can reduce a market swap whose interval exceeds the maximum
	// stream lifetime to a single slice while preserving the requested interval
	// in queue state, so granting the grace here would hold the inbound's pools
	// for StreamingSwapMaxLength after that one slice had already settled.
	if mempoolSwapRecoveryScheduleDone(status) {
		return next
	}
	if msg.State != nil {
		// A healthy stream may intentionally wait longer than the generic
		// recovery window between slices. Give it one signing period of grace.
		streamNext := addMempoolSwapRecoveryHeight(
			ctx.BlockHeight(),
			mempoolSwapRecoveryStreamInterval(ctx, k, msg),
			signingPeriod,
		)
		if streamNext > next {
			next = streamNext
		}
	}
	if msg.IsLimitSwap() && msg.InitialBlockHeight > 0 && msg.State != nil {
		maxAge := k.GetConfigInt64(ctx, constants.StreamingLimitSwapMaxAge)
		expiry := addMempoolSwapRecoveryHeight(
			msg.InitialBlockHeight,
			getLimitSwapTTL(msg, maxAge),
			signingPeriod,
		)
		if expiry > next {
			next = expiry
		}
	}
	return next
}

func newMempoolSwapLifecycle(
	ctx cosmos.Context,
	k keeper.Keeper,
	voter ObservedTxVoter,
	msg MsgSwap,
	reservedTOR cosmos.Uint,
) types.MempoolSwap {
	return types.MempoolSwap{
		TxId:               msg.Tx.ID,
		Index:              msg.Index,
		VaultPubKey:        voter.Tx.ObservedPubKey,
		ReservedTor:        reservedTOR,
		AdmittedHeight:     ctx.BlockHeight(),
		UpdatedHeight:      ctx.BlockHeight(),
		NextRecoveryHeight: mempoolSwapRecoveryHeight(ctx, k, msg, types.MempoolSwapStatus_RESERVED),
		Status:             types.MempoolSwapStatus_RESERVED,
		LatestSwap:         msg,
		PendingEffects:     types.MempoolSwapPendingEffects{},
	}
}

// updateMempoolSwapProgressCached keeps the lifecycle rollback snapshot and
// its timeout index in the same cache transaction as queue/pool progress.
func updateMempoolSwapProgressCached(
	ctx cosmos.Context,
	k keeper.Keeper,
	msg MsgSwap,
	status types.MempoolSwapStatus,
) error {
	record, found, err := k.GetMempoolSwap(ctx, msg.Tx.ID, msg.Index)
	if err != nil {
		return fmt.Errorf("fail to get mempool swap lifecycle: %w", err)
	}
	if !found {
		return nil
	}
	if record.Status == types.MempoolSwapStatus_ROLLBACK_PENDING {
		return fmt.Errorf("mempool swap %s is pending rollback", msg.Tx.ID)
	}
	record.LatestSwap = msg
	// After finality the record is a zero-reservation reorg tombstone while
	// ordinary advanced-queue execution finishes. Preserve that state while
	// still refreshing the exact rollback snapshot.
	if record.Status != types.MempoolSwapStatus_FINALIZED {
		record.Status = status
	}
	record.UpdatedHeight = ctx.BlockHeight()
	// Schedule from the reported execution progress rather than record.Status:
	// a FINALIZED tombstone which has just settled has no queue work left, even
	// though its status is deliberately preserved above.
	record.NextRecoveryHeight = mempoolSwapRecoveryHeight(ctx, k, msg, status)
	if err = k.SetMempoolSwap(ctx, record); err != nil {
		return fmt.Errorf("fail to update mempool swap lifecycle: %w", err)
	}
	return nil
}

// releaseMempoolSwapLifecycleCached releases the canonical reservation and
// clears the voter's compatibility mirror. The lifecycle record is the owner;
// the voter value is deliberately not used to decide the amount to release.
func releaseMempoolSwapLifecycleCached(
	ctx cosmos.Context,
	k keeper.Keeper,
	record types.MempoolSwap,
	voter ObservedTxVoter,
	reason string,
) (ObservedTxVoter, error) {
	if record.ReservedTor.IsZero() {
		if voter.HasReservedMempoolSwapTOR() {
			return voter, fmt.Errorf("finalized mempool swap %s retains a voter TOR reservation", record.Cursor())
		}
		return voter, nil
	}
	currentUsage := k.GetMempoolSwapUsageTOR(ctx)
	if record.ReservedTor.GT(currentUsage) {
		return voter, fmt.Errorf(
			"mempool swap lifecycle reservation %s exceeds aggregate usage %s for %s",
			record.ReservedTor, currentUsage, record.Cursor(),
		)
	}
	newUsage := k.SubMempoolSwapUsageTOR(ctx, record.ReservedTor)
	ctx.Logger().Info(reason,
		"hash", record.TxId,
		"tor_value", record.ReservedTor,
		"new_usage", newUsage)

	if voter.HasReservedMempoolSwapTOR() && !voter.MempoolSwapTOR.Equal(record.ReservedTor) {
		ctx.Logger().Error("mempool swap voter reservation differs from lifecycle record",
			"hash", record.TxId,
			"voter_tor", voter.MempoolSwapTOR,
			"record_tor", record.ReservedTor)
	}
	voter.MempoolSwapTOR = cosmos.ZeroUint()
	k.SetObservedTxInVoter(ctx, voter)
	return voter, nil
}

// completeMempoolSwapCached transitions speculative ownership at inbound
// finality. A terminal swap applies every effect and materializes staged work
// immediately. A stream with queue work remaining releases TOR but retains its
// full effects journal and latest rollback snapshot until terminal settlement.
func completeMempoolSwapCached(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter) error {
	k := mgr.Keeper()
	record, found, err := k.GetMempoolSwap(ctx, voter.TxID, 0)
	if err != nil {
		return fmt.Errorf("fail to get finalized mempool swap lifecycle: %w", err)
	}
	if !found {
		if k.HasStagedOutbound(ctx, voter.TxID) {
			return finalizeOutboundsCached(ctx, mgr, voter)
		}
		return nil
	}
	voter, err = k.GetObservedTxInVoter(ctx, voter.TxID)
	if err != nil || voter.IsEmpty() {
		if err == nil {
			err = fmt.Errorf("observed tx voter is empty")
		}
		return fmt.Errorf("fail to reload finalized mempool voter: %w", err)
	}
	if voter.FinalisedHeight == 0 {
		return fmt.Errorf("cannot complete unfinalized mempool swap %s", voter.TxID)
	}
	if record.Status == types.MempoolSwapStatus_ROLLBACK_PENDING {
		return fmt.Errorf("cannot complete mempool swap %s while rollback is pending", voter.TxID)
	}

	hadReservation := !record.ReservedTor.IsZero()
	if k.HasAdvSwapQueueItem(ctx, voter.TxID, int(record.Index)) {
		if k.HasStagedOutbound(ctx, voter.TxID) {
			return fmt.Errorf("mempool swap %s has both queued and staged finality state", voter.TxID)
		}
		voter, err = releaseMempoolSwapLifecycleCached(ctx, k, record, voter, "released finalized mempool swap lifecycle reservation")
		if err != nil {
			return err
		}
		record.ReservedTor = cosmos.ZeroUint()
		record.Status = types.MempoolSwapStatus_FINALIZED
		record.UpdatedHeight = ctx.BlockHeight()
		record.NextRecoveryHeight = mempoolSwapRecoveryHeight(ctx, k, record.LatestSwap, record.Status)
		if err = k.SetMempoolSwap(ctx, record); err != nil {
			return fmt.Errorf("fail to retain finalized mempool swap tombstone: %w", err)
		}
		if hadReservation {
			incrMempoolSwapCounter("completed_total", nil)
		}
		return nil
	}

	if err = applyPendingMempoolSwapEffectsCached(ctx, mgr, record); err != nil {
		return fmt.Errorf("fail to apply pending mempool swap effects: %w", err)
	}

	// Post-swap actions have no staged outbound of their own. Settlement defers
	// them while the inbound is unfinalized and the lifecycle snapshot carries
	// the exact output needed to execute them here once.
	if (record.Status == types.MempoolSwapStatus_STAGED || record.Status == types.MempoolSwapStatus_SETTLED) &&
		record.LatestSwap.PostSwapMemo != "" {
		if err = executePostSwapMemoCached(ctx, mgr, record.LatestSwap); err != nil {
			return fmt.Errorf("fail to execute finalized post-swap memo: %w", err)
		}
	}

	if k.HasStagedOutbound(ctx, voter.TxID) {
		if err = finalizeOutboundsCached(ctx, mgr, voter); err != nil {
			return err
		}
		voter, err = k.GetObservedTxInVoter(ctx, voter.TxID)
		if err != nil {
			return fmt.Errorf("fail to reload finalized mempool voter: %w", err)
		}
	}

	voter, err = releaseMempoolSwapLifecycleCached(ctx, k, record, voter, "released finalized mempool swap lifecycle reservation")
	if err != nil {
		return err
	}
	if err = k.DeleteMempoolSwap(ctx, record.TxId, record.Index); err != nil {
		return fmt.Errorf("fail to delete completed mempool swap lifecycle: %w", err)
	}

	if hadReservation {
		incrMempoolSwapCounter("completed_total", nil)
	}
	return nil
}

func completeMempoolSwapAtomic(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter) error {
	cacheCtx, commit := ctx.CacheContext()
	if err := completeMempoolSwapCached(cacheCtx, mgr, voter); err != nil {
		return err
	}
	commit()
	return nil
}

// finishFinalizedMempoolSwapSettlementCached applies the complete effects
// journal and removes the zero-reservation reorg tombstone only after the
// remaining advanced-queue work has settled in the same cache transaction.
func finishFinalizedMempoolSwapSettlementCached(ctx cosmos.Context, mgr Manager, txID common.TxID, index uint32) error {
	k := mgr.Keeper()
	record, found, err := k.GetMempoolSwap(ctx, txID, index)
	if err != nil {
		return fmt.Errorf("fail to load finalized mempool swap tombstone: %w", err)
	}
	if !found {
		return nil
	}
	if record.Status != types.MempoolSwapStatus_FINALIZED {
		return nil
	}
	if k.HasAdvSwapQueueItem(ctx, txID, int(index)) {
		return fmt.Errorf("cannot delete finalized mempool swap tombstone while queue work remains: %s", record.Cursor())
	}
	if !record.ReservedTor.IsZero() {
		return fmt.Errorf("finalized mempool swap tombstone still reserves TOR: %s", record.Cursor())
	}
	if err = applyPendingMempoolSwapEffectsCached(ctx, mgr, record); err != nil {
		return fmt.Errorf("fail to apply terminal mempool swap effects: %w", err)
	}
	if err = k.DeleteMempoolSwap(ctx, txID, index); err != nil {
		return fmt.Errorf("fail to delete finalized mempool swap tombstone: %w", err)
	}
	return nil
}

func finishFinalizedMempoolSwapSettlementAtomic(ctx cosmos.Context, mgr Manager, txID common.TxID, index uint32) error {
	cacheCtx, commit := ctx.CacheContext()
	if err := finishFinalizedMempoolSwapSettlementCached(cacheCtx, mgr, txID, index); err != nil {
		return err
	}
	commit()
	return nil
}

// deferMempoolPostSwapMemo reports whether settlement must retain a post-swap
// action in the lifecycle record until inbound finality.
func deferMempoolPostSwapMemo(ctx cosmos.Context, k keeper.Keeper, msg MsgSwap) bool {
	if msg.PostSwapMemo == "" || !k.HasMempoolSwap(ctx, msg.Tx.ID, msg.Index) {
		return false
	}
	voter, err := k.GetObservedTxInVoter(ctx, msg.Tx.ID)
	return err == nil && voter.FinalisedHeight == 0
}

// getLifecycleRollbackSwap returns the durable latest queue snapshot when the
// queue entry itself has already been removed by settlement.
func getLifecycleRollbackSwap(ctx cosmos.Context, k keeper.Keeper, txID common.TxID, index uint32) (MsgSwap, bool, error) {
	record, found, err := k.GetMempoolSwap(ctx, txID, index)
	if err != nil {
		return MsgSwap{}, found, err
	}
	if !found {
		return MsgSwap{}, false, nil
	}
	return record.LatestSwap, true, nil
}
