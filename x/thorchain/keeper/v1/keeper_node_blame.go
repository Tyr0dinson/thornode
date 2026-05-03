package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// setNodeBlameRecord internal helper to save node blame record
func (k KVStore) setNodeBlameRecord(ctx cosmos.Context, key []byte, record NodeBlameRecord) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

// getNodeBlameRecord internal helper to get node blame record
func (k KVStore) getNodeBlameRecord(ctx cosmos.Context, key []byte, record *NodeBlameRecord) (bool, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if !store.Has(key) {
		return false, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return true, nil
}

// GetNodeBlameRecord retrieves blame record by node address
func (k KVStore) GetNodeBlameRecord(ctx cosmos.Context, addr cosmos.AccAddress) (NodeBlameRecord, error) {
	key := k.GetKey(prefixNodeBlameRecord, addr.String())
	var record NodeBlameRecord
	ok, err := k.getNodeBlameRecord(ctx, key, &record)
	if err != nil {
		return record, err
	}
	if !ok {
		// Return empty record if not found
		return NewNodeBlameRecord(addr), nil
	}
	return record, nil
}

// SetNodeBlameRecord stores blame record
func (k KVStore) SetNodeBlameRecord(ctx cosmos.Context, record NodeBlameRecord) error {
	if len(record.NodeAddress) == 0 {
		return fmt.Errorf("node address cannot be empty")
	}
	key := k.GetKey(prefixNodeBlameRecord, record.NodeAddress.String())
	k.setNodeBlameRecord(ctx, key, record)
	return nil
}

// IncrementKeygenBlame increments keygen blame counters
func (k KVStore) IncrementKeygenBlame(ctx cosmos.Context, addr cosmos.AccAddress) error {
	record, err := k.GetNodeBlameRecord(ctx, addr)
	if err != nil {
		return err
	}

	record.KeygenBlameCountTotal++
	record.KeygenBlameCountSinceChurn++

	return k.SetNodeBlameRecord(ctx, record)
}

// IncrementKeysignBlame increments keysign blame counters
func (k KVStore) IncrementKeysignBlame(ctx cosmos.Context, addr cosmos.AccAddress) error {
	record, err := k.GetNodeBlameRecord(ctx, addr)
	if err != nil {
		return err
	}

	record.KeysignBlameCountTotal++
	record.KeysignBlameCountSinceChurn++

	return k.SetNodeBlameRecord(ctx, record)
}

// CalculateKeygenPenaltyMultiplier returns keygen_since_churn, capped by MaxKeygenBlameMultiplier if > 0
// If MaxKeygenBlameMultiplier = 0, no cap is applied (unlimited escalation)
func (k KVStore) CalculateKeygenPenaltyMultiplier(ctx cosmos.Context, addr cosmos.AccAddress) (int64, error) {
	record, err := k.GetNodeBlameRecord(ctx, addr)
	if err != nil {
		return 1, err
	}

	count := record.KeygenBlameCountSinceChurn

	// If count is 0, use 1 as minimum multiplier
	if count == 0 {
		return 1, nil
	}

	// Apply cap if MaxKeygenBlameMultiplier > 0
	maxMultiplier := k.GetConfigInt64(ctx, constants.MaxKeygenBlameMultiplier)
	if maxMultiplier > 0 && count > maxMultiplier {
		return maxMultiplier, nil
	}

	return count, nil
}

// GetNodeBlameIterator returns iterator for all blame records
func (k KVStore) GetNodeBlameIterator(ctx cosmos.Context) cosmos.Iterator {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return cosmos.KVStorePrefixIterator(store, []byte(prefixNodeBlameRecord))
}

// ResetAllSinceChurnCounters resets all since_churn counters to zero (called on successful keygen)
func (k KVStore) ResetAllSinceChurnCounters(ctx cosmos.Context) error {
	// Collect records that need updating before mutating the store. Writing to
	// the same store being iterated has implementation-defined behavior across
	// Cosmos store backends.
	var toReset []NodeBlameRecord
	iter := k.GetNodeBlameIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		var record NodeBlameRecord
		if err := k.cdc.Unmarshal(iter.Value(), &record); err != nil {
			ctx.Logger().Error("fail to unmarshal node blame record", "error", err)
			continue
		}
		if record.KeygenBlameCountSinceChurn > 0 || record.KeysignBlameCountSinceChurn > 0 {
			toReset = append(toReset, record)
		}
	}
	iter.Close()

	for _, record := range toReset {
		record.ResetSinceChurnCounters()
		if err := k.SetNodeBlameRecord(ctx, record); err != nil {
			ctx.Logger().Error("fail to reset blame counters", "address", record.NodeAddress.String(), "error", err)
		}
	}

	return nil
}
