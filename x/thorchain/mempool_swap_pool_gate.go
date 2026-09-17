package thorchain

import (
	"errors"
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var errMempoolSwapPoolFinalityPending = errors.New("pool has active unfinalized mempool swap effects")

// mempoolSwapPoolFinalityAffected reports whether an irreversible pool
// operation would observe either speculative L1 pool balances or speculative
// synth supply. Both overlays are gross rather than net: opposing active swaps
// must not make an affected pool appear safe to mutate.
func mempoolSwapPoolFinalityAffected(
	ctx cosmos.Context,
	k keeper.Keeper,
	asset common.Asset,
) (balanceAffected, synthSupplyAffected bool, err error) {
	l1Asset := asset.GetLayer1Asset()

	poolOverlay, err := k.GetMempoolSwapPoolBalanceOverlay(ctx, l1Asset)
	if err != nil {
		return false, false, fmt.Errorf("fail to get mempool swap pool overlay for %s: %w", l1Asset, err)
	}
	balanceAffected = !mempoolSwapPoolBalanceOverlayIsZero(poolOverlay)

	synthAsset := l1Asset.GetSyntheticAsset()
	supplyOverlay, err := k.GetMempoolSwapNativeSupplyOverlay(ctx, synthAsset)
	if err != nil {
		return false, false, fmt.Errorf("fail to get mempool swap synth supply overlay for %s: %w", synthAsset, err)
	}
	synthSupplyAffected = !mempoolSwapNativeSupplyOverlayIsZero(supplyOverlay)

	return balanceAffected, synthSupplyAffected, nil
}

// ensureMempoolSwapPoolFinalized rejects irreversible LP/pool mutations while
// a lifecycle still owns reversible state for the relevant L1 pool.
func ensureMempoolSwapPoolFinalized(ctx cosmos.Context, k keeper.Keeper, asset common.Asset) error {
	balanceAffected, synthSupplyAffected, err := mempoolSwapPoolFinalityAffected(ctx, k, asset)
	if err != nil {
		return err
	}
	if !balanceAffected && !synthSupplyAffected {
		return nil
	}

	l1Asset := asset.GetLayer1Asset()
	switch {
	case balanceAffected && synthSupplyAffected:
		return fmt.Errorf("%w for %s: pool balance and synth supply overlays are active", errMempoolSwapPoolFinalityPending, l1Asset)
	case balanceAffected:
		return fmt.Errorf("%w for %s: pool balance overlay is active", errMempoolSwapPoolFinalityPending, l1Asset)
	default:
		return fmt.Errorf("%w for %s: synth supply overlay is active", errMempoolSwapPoolFinalityPending, l1Asset)
	}
}

// preemptMempoolSwapPoolOwnerCached gives an irreversible pool mutation
// priority over reversible speculative state. Unfinalized owners are rolled
// back and will follow the ordinary finalized path if their inbound later
// confirms. A finalized owner is completed instead; rolling it back without a
// refund would strand an inbound which is already in the vault.
//
// Mutation handlers should run this together with their subsequent write in
// one cache transaction, making preemption conditional on that write. A caller
// may instead commit resolution independently when rollback or completion is
// itself an acceptable terminal outcome, as with best-effort gas settlement.
func preemptMempoolSwapPoolOwnerCached(
	ctx cosmos.Context,
	mgr Manager,
	asset common.Asset,
) error {
	k := mgr.Keeper()
	if err := ensureMempoolSwapPoolFinalized(ctx, k, asset); err == nil {
		return nil
	} else if !errors.Is(err, errMempoolSwapPoolFinalityPending) {
		return err
	}

	owner, found, err := k.GetMempoolSwapPoolOwner(ctx, asset)
	if err != nil {
		return fmt.Errorf("fail to resolve mempool swap owner for mutation on %s: %w", asset.GetLayer1Asset(), err)
	}
	if !found {
		return fmt.Errorf("pool %s has a mempool swap overlay without an owner", asset.GetLayer1Asset())
	}
	voter, err := k.GetObservedTxInVoter(ctx, owner.TxId)
	if err != nil {
		return fmt.Errorf("fail to get mempool swap owner voter %s: %w", owner.Cursor(), err)
	}
	if voter.IsEmpty() || !voter.TxID.Equals(owner.TxId) {
		return fmt.Errorf("mempool swap pool owner %s has no matching voter", owner.Cursor())
	}

	switch {
	case voter.Reverted:
		if err = rollbackMempoolSwapCached(ctx, k, mgr, voter); err != nil {
			return fmt.Errorf("fail to roll back reverted mempool swap owner %s: %w", owner.Cursor(), err)
		}
	case voter.FinalisedHeight > 0:
		if err = completeMempoolSwapCached(ctx, mgr, voter); err != nil {
			return fmt.Errorf("fail to complete finalized mempool swap owner %s: %w", owner.Cursor(), err)
		}
	default:
		if err = rollbackMempoolSwapCached(ctx, k, mgr, voter); err != nil {
			return fmt.Errorf("fail to roll back unfinalized mempool swap owner %s: %w", owner.Cursor(), err)
		}
	}

	if err = ensureMempoolSwapPoolFinalized(ctx, k, asset); err != nil {
		return fmt.Errorf("mempool swap pool owner preemption did not clear %s: %w", asset.GetLayer1Asset(), err)
	}
	return nil
}

// preemptMempoolSwapExecutionOwnersAtomic lets a swap whose own inbound is
// already final take the pools it needs from a speculative lifecycle whose
// inbound is still unconfirmed.
//
// Per-pool serialization exists so a rollback can reverse its exact recorded
// deltas without a foreign mutation in between; it is not a priority claim.
// Making a final swap wait for an unconfirmed one turns that bookkeeping
// requirement into an availability lever. Bitcoin Core has relayed full-RBF
// replacements by default since v28, so nSequence signalling does not make an
// inbound irreplaceable and a fee floor only sets the entry price: a sender can
// buy a hold on a pool with a well-priced inbound, wait for the speculative
// swap, then replace the inbound so it never confirms. Without preemption every
// other swap on those pools waits out the lifecycle recovery timeout.
//
// Resolving the owner first restores the invariant rollback needs and caps the
// hold at "until someone else needs the pool". The preempted lifecycle is not
// lost: an unfinalized owner is rolled back and follows the ordinary finalized
// path if its inbound does confirm, exactly as it would for an add-liquidity or
// withdraw preemption.
//
// Rollback or completion of the owner is a valid terminal outcome on its own,
// so it commits independently of whether the caller's swap then succeeds.
func preemptMempoolSwapExecutionOwnersAtomic(ctx cosmos.Context, mgr Manager, msg MsgSwap) error {
	pools, err := mempoolSwapExecutionPools(msg)
	if err != nil {
		return err
	}
	cacheCtx, commit := ctx.CacheContext()
	for _, asset := range pools {
		if err = preemptMempoolSwapPoolOwnerCached(cacheCtx, mgr, asset); err != nil {
			return err
		}
	}
	// Preserve the caller's lifecycle identity when checking the result. A
	// finalized stream may still own effects that another lifecycle cannot
	// interleave, even though a swap without its own journal could proceed.
	record, found, err := mgr.Keeper().GetMempoolSwap(cacheCtx, msg.Tx.ID, msg.Index)
	if err != nil {
		return fmt.Errorf("fail to load preempting swap lifecycle: %w", err)
	}
	var owner *types.MempoolSwap
	if found {
		owner = &record
	}
	if err = ensureMempoolSwapExecutionPoolsAvailable(cacheCtx, mgr.Keeper(), msg, owner); err != nil {
		return err
	}
	commit()
	return nil
}

// mempoolSwapPreemptionAttempts remembers the pools whose owner could not be
// resolved during one EndBlock pass.
//
// Unlike the availability checks, preemption performs real rollback work before
// it can fail, and it only fails on a durable inconsistency or a rollback that
// recovery must retry. Neither clears within the pass, so re-attempting it for
// every later queued swap on the same pool would repeat that work for as many
// items as the queue holds while the condition persists. Recording the failure
// once keeps a stuck owner costing one attempt per pool per block.
type mempoolSwapPreemptionAttempts map[string]bool

func newMempoolSwapPreemptionAttempts() mempoolSwapPreemptionAttempts {
	return make(mempoolSwapPreemptionAttempts)
}

func (a mempoolSwapPreemptionAttempts) failedKeys(msg MsgSwap) ([]string, bool) {
	pools, err := mempoolSwapExecutionPools(msg)
	if err != nil {
		// A swap whose pools cannot be resolved never reaches preemption.
		return nil, false
	}
	keys := make([]string, 0, len(pools))
	failed := false
	for _, asset := range pools {
		key := strings.ToUpper(asset.GetLayer1Asset().String())
		keys = append(keys, key)
		if a[key] {
			failed = true
		}
	}
	return keys, failed
}

// preempt resolves the speculative owners standing in a final swap's way,
// skipping pools already known to be unresolvable this pass.
func (a mempoolSwapPreemptionAttempts) preempt(ctx cosmos.Context, mgr Manager, msg MsgSwap) error {
	keys, failed := a.failedKeys(msg)
	if failed {
		return errMempoolSwapPoolExecutionPending
	}
	err := preemptMempoolSwapExecutionOwnersAtomic(ctx, mgr, msg)
	if err != nil {
		for _, key := range keys {
			a[key] = true
		}
	}
	return err
}
