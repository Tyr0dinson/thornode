package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// finalityPoolView removes active mempool-swap balance mutations from local
// pool copies used by irreversible consensus decisions. The keeper maintains
// one aggregate overlay key per pool as lifecycle records change, so this view
// performs no lifecycle scan and memoizes each pool lookup within a pass.
type finalityPoolView struct {
	k              keeper.Keeper
	overlays       map[common.Asset]types.MempoolSwapPoolBalanceOverlay
	supplyOverlays map[string]types.MempoolSwapNativeSupplyOverlay
}

func newFinalityPoolView(k keeper.Keeper) *finalityPoolView {
	return &finalityPoolView{
		k:              k,
		overlays:       make(map[common.Asset]types.MempoolSwapPoolBalanceOverlay),
		supplyOverlays: make(map[string]types.MempoolSwapNativeSupplyOverlay),
	}
}

func mempoolSwapPoolBalanceOverlayIsZero(overlay types.MempoolSwapPoolBalanceOverlay) bool {
	return overlay.BalanceRuneIncrease.IsZero() &&
		overlay.BalanceRuneDecrease.IsZero() &&
		overlay.BalanceAssetIncrease.IsZero() &&
		overlay.BalanceAssetDecrease.IsZero()
}

func (v *finalityPoolView) overlay(ctx cosmos.Context, asset common.Asset) (types.MempoolSwapPoolBalanceOverlay, error) {
	// A saver/synth pool is a separate THORChain accounting record, not an
	// alternate key for its backing L1 pool. Stop here before layer-1
	// normalization so every caller—not only adjust/apply—gets the correct
	// empty balance overlay for the saver pool.
	if asset.IsSyntheticAsset() {
		return types.MempoolSwapPoolBalanceOverlay{
			Asset:                asset,
			BalanceRuneIncrease:  cosmos.ZeroUint(),
			BalanceRuneDecrease:  cosmos.ZeroUint(),
			BalanceAssetIncrease: cosmos.ZeroUint(),
			BalanceAssetDecrease: cosmos.ZeroUint(),
		}, nil
	}
	asset = asset.GetLayer1Asset()
	if overlay, ok := v.overlays[asset]; ok {
		return overlay, nil
	}
	overlay, err := v.k.GetMempoolSwapPoolBalanceOverlay(ctx, asset)
	if err != nil {
		return overlay, err
	}
	v.overlays[asset] = overlay
	return overlay, nil
}

func removeMempoolSwapPoolBalanceOverlay(
	actual, increase, decrease cosmos.Uint,
	asset common.Asset,
	field string,
) (cosmos.Uint, error) {
	withDecrease := actual.Add(decrease)
	if increase.GT(withDecrease) {
		return cosmos.ZeroUint(), fmt.Errorf(
			"mempool swap %s overlay exceeds live %s balance for %s: increase %s, live+decrease %s",
			field, field, asset, increase, withDecrease,
		)
	}
	return withDecrease.Sub(increase), nil
}

func applyMempoolSwapPoolBalanceOverlay(
	finalized, increase, decrease cosmos.Uint,
	asset common.Asset,
	field string,
) (cosmos.Uint, error) {
	withIncrease := finalized.Add(increase)
	if decrease.GT(withIncrease) {
		return cosmos.ZeroUint(), fmt.Errorf(
			"mempool swap %s overlay exceeds regenerated %s balance for %s: decrease %s, finalized+increase %s",
			field, field, asset, decrease, withIncrease,
		)
	}
	return withIncrease.Sub(decrease), nil
}

// adjust returns a pool copy containing only finalized balance mutations. The
// affected result remains true for a gross overlay even when opposing active
// mutations happen to net to zero; mutation handlers such as POL must not
// entangle their state with any still-reversible swap.
func (v *finalityPoolView) adjust(ctx cosmos.Context, pool Pool) (adjusted Pool, affected bool, err error) {
	// Saver pools are independent THORChain-native accounting records. Their
	// Asset.GetLayer1Asset() points at the backing L1 pool, but speculative swap
	// balance effects are never written to the saver pool itself. Looking up the
	// L1 overlay here would therefore subtract somebody else's balances from the
	// saver record (and commonly underflow its intentionally-zero RUNE side).
	if pool.Asset.IsSyntheticAsset() {
		return pool, false, nil
	}
	overlay, err := v.overlay(ctx, pool.Asset)
	if err != nil {
		return Pool{}, false, err
	}
	if mempoolSwapPoolBalanceOverlayIsZero(overlay) {
		return pool, false, nil
	}
	pool.BalanceRune, err = removeMempoolSwapPoolBalanceOverlay(
		pool.BalanceRune,
		overlay.BalanceRuneIncrease,
		overlay.BalanceRuneDecrease,
		pool.Asset,
		"rune",
	)
	if err != nil {
		return Pool{}, true, err
	}
	pool.BalanceAsset, err = removeMempoolSwapPoolBalanceOverlay(
		pool.BalanceAsset,
		overlay.BalanceAssetIncrease,
		overlay.BalanceAssetDecrease,
		pool.Asset,
		"asset",
	)
	if err != nil {
		return Pool{}, true, err
	}
	return pool, true, nil
}

// apply overlays active speculative mutations onto a finalized pool base.
// This is needed for virtual pools whose base depths are regenerated every
// block: overwriting the live pool with the bare base would erase mutations
// still owned by lifecycle records and make a later rollback incorrect.
func (v *finalityPoolView) apply(ctx cosmos.Context, pool Pool) (adjusted Pool, affected bool, err error) {
	// See adjust: saver-pool balances never own the backing L1 pool's overlay.
	if pool.Asset.IsSyntheticAsset() {
		return pool, false, nil
	}
	overlay, err := v.overlay(ctx, pool.Asset)
	if err != nil {
		return Pool{}, false, err
	}
	if mempoolSwapPoolBalanceOverlayIsZero(overlay) {
		return pool, false, nil
	}
	pool.BalanceRune, err = applyMempoolSwapPoolBalanceOverlay(
		pool.BalanceRune,
		overlay.BalanceRuneIncrease,
		overlay.BalanceRuneDecrease,
		pool.Asset,
		"rune",
	)
	if err != nil {
		return Pool{}, true, err
	}
	pool.BalanceAsset, err = applyMempoolSwapPoolBalanceOverlay(
		pool.BalanceAsset,
		overlay.BalanceAssetIncrease,
		overlay.BalanceAssetDecrease,
		pool.Asset,
		"asset",
	)
	if err != nil {
		return Pool{}, true, err
	}
	return pool, true, nil
}

func (v *finalityPoolView) getPool(ctx cosmos.Context, asset common.Asset) (Pool, bool, error) {
	pool, err := v.k.GetPool(ctx, asset)
	if err != nil {
		return pool, false, err
	}
	return v.adjust(ctx, pool)
}

func mempoolSwapNativeSupplyOverlayIsZero(overlay types.MempoolSwapNativeSupplyOverlay) bool {
	return overlay.SupplyIncrease.IsZero() && overlay.SupplyDecrease.IsZero()
}

func (v *finalityPoolView) supplyOverlay(ctx cosmos.Context, asset common.Asset) (types.MempoolSwapNativeSupplyOverlay, error) {
	key := asset.Native()
	if overlay, ok := v.supplyOverlays[key]; ok {
		return overlay, nil
	}
	overlay, err := v.k.GetMempoolSwapNativeSupplyOverlay(ctx, asset)
	if err != nil {
		return overlay, err
	}
	v.supplyOverlays[key] = overlay
	return overlay, nil
}

// getTotalSupply removes every active lifecycle's exact gross native supply
// mutation from the live bank supply. Gross increase and decrease fields are
// intentionally kept separate: two opposing speculative swaps must not hide
// that an irreversible consumer is observing reversible state.
func (v *finalityPoolView) getTotalSupply(ctx cosmos.Context, asset common.Asset) (adjusted cosmos.Uint, affected bool, err error) {
	live := v.k.GetTotalSupply(ctx, asset)
	overlay, err := v.supplyOverlay(ctx, asset)
	if err != nil {
		return cosmos.ZeroUint(), false, err
	}
	if mempoolSwapNativeSupplyOverlayIsZero(overlay) {
		return live, false, nil
	}
	adjusted, err = removeMempoolSwapPoolBalanceOverlay(
		live,
		overlay.SupplyIncrease,
		overlay.SupplyDecrease,
		asset,
		"native supply",
	)
	return adjusted, true, err
}

func getAvailablePoolsRuneWithFinalityView(
	ctx cosmos.Context,
	k keeper.Keeper,
	poolView *finalityPoolView,
) (Pools, cosmos.Uint, error) {
	availablePoolsRune := cosmos.ZeroUint()
	var availablePools Pools
	iterator := k.GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := k.Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return nil, cosmos.ZeroUint(), fmt.Errorf("fail to unmarshal pool: %w", err)
		}
		pool, _, err := poolView.adjust(ctx, pool)
		if err != nil {
			return nil, cosmos.ZeroUint(), err
		}
		if !pool.IsAvailable() || pool.Asset.IsNative() || pool.BalanceRune.IsZero() {
			continue
		}
		availablePoolsRune = availablePoolsRune.Add(pool.BalanceRune)
		availablePools = append(availablePools, pool)
	}
	return availablePools, availablePoolsRune, nil
}

func getVaultsLiquidityRuneWithFinalityView(
	ctx cosmos.Context,
	k keeper.Keeper,
	poolView *finalityPoolView,
) (cosmos.Uint, error) {
	vaultsLiquidityRune := cosmos.ZeroUint()
	poolCache := make(map[common.Asset]Pool)
	vaults, err := k.GetAsgardVaults(ctx)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to get vaults: %w", err)
	}
	for i := range vaults {
		if vaults[i].Status == InactiveVault {
			continue
		}
		for _, coin := range vaults[i].Coins {
			if coin.IsRune() {
				vaultsLiquidityRune = vaultsLiquidityRune.Add(coin.Amount)
				continue
			}
			asset := coin.Asset.GetLayer1Asset()
			pool, ok := poolCache[asset]
			if !ok {
				pool, _, err = poolView.getPool(ctx, asset)
				if err != nil {
					return cosmos.ZeroUint(), fmt.Errorf("fail to get finality-adjusted pool for asset %s: %w", asset, err)
				}
				poolCache[asset] = pool
			}
			vaultsLiquidityRune = vaultsLiquidityRune.Add(pool.AssetValueInRune(coin.Amount))
		}
	}
	return vaultsLiquidityRune, nil
}
