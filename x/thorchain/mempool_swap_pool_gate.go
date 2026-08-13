package thorchain

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
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
