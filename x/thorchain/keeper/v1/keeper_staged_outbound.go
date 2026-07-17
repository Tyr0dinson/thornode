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

	// Get existing list or create new one
	list, err := k.getStagedOutboundList(ctx, inHash)
	if err != nil {
		return fmt.Errorf("failed to get staged outbound list: %w", err)
	}

	// Add the new item
	staged := StagedOutbound{
		TxOutItem:       item,
		ScheduledHeight: scheduledHeight,
		StagedHeight:    ctx.BlockHeight(),
	}
	list.Items = append(list.Items, staged)

	// Save the updated list
	return k.setStagedOutboundList(ctx, inHash, list)
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
	key := k.GetKey(prefixStagedOutbound, inHash.String())
	k.del(ctx, key)
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
	items := make([]TxOutItem, 0)
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
			items = append(items, staged.TxOutItem)
			if staged.StagedHeight <= 0 || staged.StagedHeight > ctx.BlockHeight() {
				continue
			}
			age := ctx.BlockHeight() - staged.StagedHeight
			if age > summary.OldestAge {
				summary.OldestAge = age
			}
		}
	}

	// Value all staged items together so GetTOIsValue can reuse one pool cache
	// across the entire staged-outbound store.
	summary.RuneValue, summary.CloutValue = k.GetTOIsValue(ctx, items...)

	return summary, nil
}

// GetStagedOutboundValue returns the total RUNE value and clout of all staged outbounds.
// This is used by CalcTxOutHeight to include staged values in delay calculations.
func (k KVStore) GetStagedOutboundValue(ctx cosmos.Context) (cosmos.Uint, cosmos.Uint, error) {
	summary, err := k.GetStagedOutboundSummary(ctx)
	return summary.RuneValue, summary.CloutValue, err
}

// RestoreStagedOutboundList writes an entire StagedOutboundList to the store
// without modifying StagedHeight. This is used by InitGenesis to preserve the
// original staged_height from the exported state.
func (k KVStore) RestoreStagedOutboundList(ctx cosmos.Context, list StagedOutboundList) error {
	if list.InHash.IsEmpty() {
		return fmt.Errorf("cannot restore staged outbound list with empty InHash")
	}
	return k.setStagedOutboundList(ctx, list.InHash, list)
}

// Internal helper functions

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
