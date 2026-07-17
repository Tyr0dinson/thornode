package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// SetStagedOutbound adds a staged outbound item for a mempool swap.
// Multiple outbounds can be staged for the same inbound transaction.
// The inbound transaction hash is extracted from item.InHash.
func (k KVStore) SetStagedOutbound(ctx cosmos.Context, item TxOutItem, scheduledHeight int64) error {
	inHash := item.InHash
	if inHash.IsEmpty() {
		return fmt.Errorf("cannot stage outbound with empty InHash")
	}
	cacheCtx, commit := ctx.CacheContext()

	// Get existing list or create new one
	list, err := k.getStagedOutboundList(cacheCtx, inHash)
	if err != nil {
		return fmt.Errorf("failed to get staged outbound list: %w", err)
	}

	// Add the new item
	staged := StagedOutbound{
		TxOutItem:       item,
		ScheduledHeight: scheduledHeight,
		StagedHeight:    cacheCtx.BlockHeight(),
	}
	list.Items = append(list.Items, staged)

	if err := k.adjustStagedOutboundTotals(cacheCtx, []StagedOutbound{staged}, true); err != nil {
		return fmt.Errorf("failed to update staged outbound totals: %w", err)
	}
	if err := k.setStagedOutboundList(cacheCtx, inHash, list); err != nil {
		return err
	}
	commit()
	return nil
}

// HasStagedOutbound returns whether any staged outbounds exist for an inbound transaction.
func (k KVStore) HasStagedOutbound(ctx cosmos.Context, inHash common.TxID) bool {
	key := k.GetKey(prefixStagedOutbound, inHash.String())
	return k.has(ctx, key)
}

// GetStagedOutbounds returns all staged outbounds for an inbound transaction.
func (k KVStore) GetStagedOutbounds(ctx cosmos.Context, inHash common.TxID) ([]StagedOutbound, error) {
	list, err := k.getStagedOutboundList(ctx, inHash)
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// DeleteStagedOutbounds removes all staged outbounds for an inbound transaction.
func (k KVStore) DeleteStagedOutbounds(ctx cosmos.Context, inHash common.TxID) {
	cacheCtx, commit := ctx.CacheContext()
	list, err := k.getStagedOutboundList(cacheCtx, inHash)
	if err != nil {
		ctx.Logger().Error("failed to get staged outbounds for deletion", "error", err, "in_hash", inHash)
		return
	}
	if len(list.Items) == 0 {
		return
	}
	if err := k.adjustStagedOutboundTotals(cacheCtx, list.Items, false); err != nil {
		ctx.Logger().Error("failed to update staged outbound totals during deletion", "error", err, "in_hash", inHash)
		return
	}
	key := k.GetKey(prefixStagedOutbound, inHash.String())
	k.del(cacheCtx, key)
	commit()
}

// GetStagedOutboundIterator returns an iterator over all staged outbound lists.
func (k KVStore) GetStagedOutboundIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixStagedOutbound)
}

// GetStagedOutboundSummary returns the total value, count, and age metadata for
// all staged outbounds in a single store scan.
func (k KVStore) GetStagedOutboundSummary(ctx cosmos.Context) (keeper.StagedOutboundSummary, error) {
	summary := keeper.StagedOutboundSummary{
		RuneValue:  cosmos.ZeroUint(),
		CloutValue: cosmos.ZeroUint(),
	}
	iter := k.GetStagedOutboundIterator(ctx)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var list StagedOutboundList
		if err := k.cdc.Unmarshal(iter.Value(), &list); err != nil {
			ctx.Logger().Error("failed to unmarshal staged outbound list", "error", err)
			continue
		}

		summary.InboundCount++
		summary.OutboundCount += int64(len(list.Items))

		for _, staged := range list.Items {
			if staged.StagedHeight <= 0 || staged.StagedHeight > ctx.BlockHeight() {
				continue
			}
			age := ctx.BlockHeight() - staged.StagedHeight
			if age > summary.OldestAge {
				summary.OldestAge = age
			}
		}
	}

	var err error
	summary.RuneValue, summary.CloutValue, err = k.GetStagedOutboundValue(ctx)
	if err != nil {
		return summary, err
	}

	return summary, nil
}

// GetStagedOutboundValue returns the total RUNE value and clout of all staged outbounds.
// This is used by CalcTxOutHeight to include staged values in delay calculations.
func (k KVStore) GetStagedOutboundValue(ctx cosmos.Context) (cosmos.Uint, cosmos.Uint, error) {
	items := make([]TxOutItem, 0)
	iter := k.getIterator(ctx, prefixStagedOutboundCoinTotal)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var coin common.Coin
		if err := k.cdc.Unmarshal(iter.Value(), &coin); err != nil {
			return cosmos.ZeroUint(), cosmos.ZeroUint(), dbError(ctx, "failed to unmarshal staged outbound coin total", err)
		}
		items = append(items, TxOutItem{Coin: coin})
	}

	runeValue, _ := k.GetTOIsValue(ctx, items...)
	cloutValue := cosmos.ZeroUint()
	_, err := k.getUint(ctx, k.GetKey(prefixStagedOutboundCloutTotal, "total"), &cloutValue)
	if err != nil {
		return cosmos.ZeroUint(), cosmos.ZeroUint(), err
	}
	return runeValue, cloutValue, nil
}

// RestoreStagedOutboundList writes an entire StagedOutboundList to the store
// without modifying StagedHeight. This is used by InitGenesis to preserve the
// original staged_height from the exported state.
func (k KVStore) RestoreStagedOutboundList(ctx cosmos.Context, list StagedOutboundList) error {
	if list.InHash.IsEmpty() {
		return fmt.Errorf("cannot restore staged outbound list with empty InHash")
	}
	cacheCtx, commit := ctx.CacheContext()
	previous, err := k.getStagedOutboundList(cacheCtx, list.InHash)
	if err != nil {
		return fmt.Errorf("failed to get existing staged outbound list: %w", err)
	}
	if err := k.adjustStagedOutboundTotals(cacheCtx, previous.Items, false); err != nil {
		return fmt.Errorf("failed to remove existing staged outbound totals: %w", err)
	}
	if err := k.adjustStagedOutboundTotals(cacheCtx, list.Items, true); err != nil {
		return fmt.Errorf("failed to restore staged outbound totals: %w", err)
	}
	if err := k.setStagedOutboundList(cacheCtx, list.InHash, list); err != nil {
		return err
	}
	commit()
	return nil
}

// Internal helper functions

func stagedOutboundTotalDelta(items []StagedOutbound) (common.Coins, cosmos.Uint) {
	coins := common.Coins{}
	clout := cosmos.ZeroUint()
	for _, staged := range items {
		itemCoins := append(common.Coins{staged.TxOutItem.Coin}, staged.TxOutItem.MaxGas...)
		for _, coin := range itemCoins {
			if coin.IsEmpty() {
				continue
			}
			coins = coins.Add(coin)
		}
		if staged.TxOutItem.CloutSpent != nil {
			clout = clout.Add(*staged.TxOutItem.CloutSpent)
		}
	}
	return coins, clout
}

// adjustStagedOutboundTotals maintains raw per-asset amounts rather than a
// cached RUNE valuation. Reads therefore avoid scanning every staged outbound
// while still reflecting pool-price changes made later in the same block.
func (k KVStore) adjustStagedOutboundTotals(ctx cosmos.Context, items []StagedOutbound, add bool) error {
	deltaCoins, deltaClout := stagedOutboundTotalDelta(items)
	if len(deltaCoins) == 0 && deltaClout.IsZero() {
		return nil
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	type coinUpdate struct {
		key  []byte
		coin common.Coin
	}
	updates := make([]coinUpdate, 0, len(deltaCoins))
	for _, delta := range deltaCoins {
		key := k.GetKey(prefixStagedOutboundCoinTotal, delta.Asset.String())
		current := common.NewCoin(delta.Asset, cosmos.ZeroUint())
		if store.Has(key) {
			if err := k.cdc.Unmarshal(store.Get(key), &current); err != nil {
				return dbError(ctx, "failed to unmarshal staged outbound coin total", err)
			}
			if !current.Asset.Equals(delta.Asset) {
				return fmt.Errorf("staged outbound coin total asset mismatch: expected %s, got %s", delta.Asset, current.Asset)
			}
		}

		if add {
			current.Amount = current.Amount.Add(delta.Amount)
		} else {
			if current.Amount.LT(delta.Amount) {
				return fmt.Errorf("staged outbound coin total underflow for %s: %s < %s", delta.Asset, current.Amount, delta.Amount)
			}
			current.Amount = current.Amount.Sub(delta.Amount)
		}
		updates = append(updates, coinUpdate{key: key, coin: current})
	}

	cloutKey := k.GetKey(prefixStagedOutboundCloutTotal, "total")
	currentClout := cosmos.ZeroUint()
	if _, err := k.getUint(ctx, cloutKey, &currentClout); err != nil {
		return err
	}
	if add {
		currentClout = currentClout.Add(deltaClout)
	} else {
		if currentClout.LT(deltaClout) {
			return fmt.Errorf("staged outbound clout total underflow: %s < %s", currentClout, deltaClout)
		}
		currentClout = currentClout.Sub(deltaClout)
	}

	for _, update := range updates {
		if update.coin.Amount.IsZero() {
			store.Delete(update.key)
			continue
		}
		store.Set(update.key, k.cdc.MustMarshal(&update.coin))
	}
	k.setUint(ctx, cloutKey, currentClout)
	return nil
}

func (k KVStore) setStagedOutboundList(ctx cosmos.Context, inHash common.TxID, list StagedOutboundList) error {
	key := k.GetKey(prefixStagedOutbound, inHash.String())
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	if len(list.Items) == 0 {
		store.Delete(key)
		return nil
	}

	buf := k.cdc.MustMarshal(&list)
	store.Set(key, buf)
	return nil
}

func (k KVStore) getStagedOutboundList(ctx cosmos.Context, inHash common.TxID) (StagedOutboundList, error) {
	key := k.GetKey(prefixStagedOutbound, inHash.String())
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	list := StagedOutboundList{
		InHash: inHash,
		Items:  []StagedOutbound{},
	}

	if !store.Has(key) {
		return list, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, &list); err != nil {
		return list, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", list, key), err)
	}

	return list, nil
}
