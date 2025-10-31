package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// setOperatorBlameRecord internal helper to save operator blame record
func (k KVStore) setOperatorBlameRecord(ctx cosmos.Context, key []byte, record OperatorBlameRecord) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

// getOperatorBlameRecord internal helper to get operator blame record
func (k KVStore) getOperatorBlameRecord(ctx cosmos.Context, key []byte, record *OperatorBlameRecord) (bool, error) {
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

// GetOperatorBlameRecord retrieves blame record by operator address
func (k KVStore) GetOperatorBlameRecord(ctx cosmos.Context, addr cosmos.AccAddress) (OperatorBlameRecord, error) {
	key := k.GetKey(prefixOperatorBlameRecord, addr.String())
	var record OperatorBlameRecord
	ok, err := k.getOperatorBlameRecord(ctx, key, &record)
	if err != nil {
		return record, err
	}
	if !ok {
		// Return empty record if not found
		return NewOperatorBlameRecord(addr), nil
	}
	return record, nil
}

// SetOperatorBlameRecord stores blame record
func (k KVStore) SetOperatorBlameRecord(ctx cosmos.Context, record OperatorBlameRecord) error {
	if len(record.OperatorAddress) == 0 {
		return fmt.Errorf("operator address cannot be empty")
	}
	key := k.GetKey(prefixOperatorBlameRecord, record.OperatorAddress.String())
	k.setOperatorBlameRecord(ctx, key, record)
	return nil
}

// IncrementKeygenBlame increments keygen blame counters
func (k KVStore) IncrementKeygenBlame(ctx cosmos.Context, addr cosmos.AccAddress) error {
	record, err := k.GetOperatorBlameRecord(ctx, addr)
	if err != nil {
		return err
	}

	record.KeygenBlameCountTotal++
	record.KeygenBlameCountSinceChurn++

	return k.SetOperatorBlameRecord(ctx, record)
}

// IncrementKeysignBlame increments keysign blame counters
func (k KVStore) IncrementKeysignBlame(ctx cosmos.Context, addr cosmos.AccAddress) error {
	record, err := k.GetOperatorBlameRecord(ctx, addr)
	if err != nil {
		return err
	}

	record.KeysignBlameCountTotal++
	record.KeysignBlameCountSinceChurn++

	return k.SetOperatorBlameRecord(ctx, record)
}

// CalculateKeygenPenaltyMultiplier returns keygen_since_churn, capped by MaxKeygenBlameMultiplier if > 0
// If MaxKeygenBlameMultiplier = 0, no cap is applied (unlimited escalation)
func (k KVStore) CalculateKeygenPenaltyMultiplier(ctx cosmos.Context, addr cosmos.AccAddress) (int64, error) {
	record, err := k.GetOperatorBlameRecord(ctx, addr)
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

// GetOperatorBlameIterator returns iterator for all blame records
func (k KVStore) GetOperatorBlameIterator(ctx cosmos.Context) cosmos.Iterator {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return cosmos.KVStorePrefixIterator(store, []byte(prefixOperatorBlameRecord))
}

// ResetAllSinceChurnCounters resets all since_churn counters to zero (called on successful keygen)
func (k KVStore) ResetAllSinceChurnCounters(ctx cosmos.Context) error {
	iter := k.GetOperatorBlameIterator(ctx)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		var record OperatorBlameRecord
		if err := k.cdc.Unmarshal(iter.Value(), &record); err != nil {
			ctx.Logger().Error("fail to unmarshal operator blame record", "error", err)
			continue
		}

		// Only update if there's something to reset
		if record.KeygenBlameCountSinceChurn > 0 || record.KeysignBlameCountSinceChurn > 0 {
			record.ResetSinceChurnCounters()
			if err := k.SetOperatorBlameRecord(ctx, record); err != nil {
				ctx.Logger().Error("fail to reset blame counters", "address", record.OperatorAddress.String(), "error", err)
			}
		}
	}

	return nil
}
