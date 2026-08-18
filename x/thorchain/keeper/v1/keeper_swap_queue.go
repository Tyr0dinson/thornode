package keeperv1

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setMsgSwap(ctx cosmos.Context, key []byte, record MsgSwap) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getMsgSwap(ctx cosmos.Context, key []byte, record *MsgSwap) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return true, nil
}

// SetSwapQueueItem - writes a swap item to the kv store
func (k KVStore) SetSwapQueueItem(ctx cosmos.Context, msg MsgSwap, i int) error {
	key := k.GetKey(prefixSwapQueueItem, formatSwapQueueItemKey(msg.Tx.ID, i))
	k.setMsgSwap(ctx, key, msg)
	return nil
}

// GetSwapQueueIterator iterate swap queue
func (k KVStore) GetSwapQueueIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixSwapQueueItem)
}

// GetSwapQueueItem - write the given swap queue item information to key values tore
func (k KVStore) GetSwapQueueItem(ctx cosmos.Context, txID common.TxID, i int) (MsgSwap, error) {
	record := MsgSwap{}
	ok, err := k.getMsgSwap(ctx, k.GetKey(prefixSwapQueueItem, formatSwapQueueItemKey(txID, i)), &record)
	if !ok {
		return record, errors.New("not found")
	}
	return record, err
}

// HasSwapQueueItem - checks if swap item already exists
func (k KVStore) HasSwapQueueItem(ctx cosmos.Context, txID common.TxID, i int) bool {
	record := MsgSwap{}
	ok, _ := k.getMsgSwap(ctx, k.GetKey(prefixSwapQueueItem, formatSwapQueueItemKey(txID, i)), &record)
	return ok
}

// RemoveSwapQueueItem - removes a swap item from the kv store
func (k KVStore) RemoveSwapQueueItem(ctx cosmos.Context, txID common.TxID, i int) {
	k.del(ctx, k.GetKey(prefixSwapQueueItem, formatSwapQueueItemKey(txID, i)))
}

// AddMalformedSwapPurge records that malformed swap queue records were
// deleted, so the circuit breaker can surface the purge at its next check —
// the records themselves are gone before it runs. The counter is an
// operational signal and is deliberately not exported in genesis.
func (k KVStore) AddMalformedSwapPurge(ctx cosmos.Context, count int64) {
	if count <= 0 {
		return
	}
	key := k.GetKey(prefixMalformedSwapPurge, "count")
	var current int64
	_, _ = k.getInt64(ctx, key, &current)
	k.setInt64(ctx, key, current+count)
}

// ConsumeMalformedSwapPurge returns the number of malformed swap queue
// records deleted since the last call and clears the counter.
func (k KVStore) ConsumeMalformedSwapPurge(ctx cosmos.Context) int64 {
	key := k.GetKey(prefixMalformedSwapPurge, "count")
	var count int64
	_, _ = k.getInt64(ctx, key, &count)
	k.del(ctx, key)
	return count
}
