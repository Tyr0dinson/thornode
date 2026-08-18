package keeperv1

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// GetMimirWithRef is a helper function to more readably insert references (such as Asset MimirString or Chain) into Mimir key templates.
func (k KVStore) GetMimirWithRef(ctx cosmos.Context, template string, ref ...any) (int64, error) {
	// 'template' should be something like "Halt%sChain" (to halt an arbitrary specified chain)
	// or "Ragnarok-%s" (to halt the pool of an arbitrary specified Asset (MimirString used for Assets to join Chain and Symbol with a hyphen).
	key := fmt.Sprintf(template, ref...)
	return k.GetMimir(ctx, key)
}

// SetMimir save a mimir value to key value store
func (k KVStore) SetMimir(ctx cosmos.Context, key string, value int64) {
	keeper.MimirCacheFromContext(ctx).Disable()
	keeper.AnchorsCacheFromContext(ctx).Disable()
	k.setInt64(ctx, k.GetKey(prefixMimir, key), value)
}

// GetNodeMimirs get node mimirs value from key value store
func (k KVStore) GetNodeMimirs(ctx cosmos.Context, key string) (NodeMimirs, error) {
	key = strings.ToUpper(key)
	record := NodeMimirs{}
	store := k.kvStore(ctx)
	if !store.Has(k.GetKey(prefixNodeMimir, key)) {
		return record, nil
	}
	bz := store.Get(k.GetKey(prefixNodeMimir, key))
	if err := k.cdc.Unmarshal(bz, &record); err != nil {
		return NodeMimirs{}, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return record, nil
}

// SetNodeMimir save a mimir value to key value store for a specific node
func (k KVStore) SetNodeMimir(ctx cosmos.Context, key string, value int64, acc cosmos.AccAddress) error {
	key = strings.ToUpper(key)
	kvkey := k.GetKey(prefixNodeMimir, key)
	record, err := k.GetNodeMimirs(ctx, key)
	if err != nil {
		return err
	}
	record.Set(key, value, acc)

	// delete the node mimir if value is negative, except the abstain sentinel which is a
	// real vote and must persist to satisfy the ADR required-voting gate
	if value < 0 && value != constants.MimirAbstainValue {
		record.Delete(key, acc)
	}

	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil || len(record.Mimirs) == 0 {
		store.Delete(kvkey)
	} else {
		store.Set(kvkey, buf)
	}
	return err
}

// DeleteNodeMimirs deletes all node mimir votes for a given key
func (k KVStore) DeleteNodeMimirs(ctx cosmos.Context, key string) {
	k.del(ctx, k.GetKey(prefixNodeMimir, key))
}

func (k KVStore) PurgeOperationalNodeMimirs(ctx cosmos.Context) {
	iterNode := k.GetNodeMimirIterator(ctx)
	defer iterNode.Close()
	for ; iterNode.Valid(); iterNode.Next() {
		key := strings.TrimPrefix(string(iterNode.Key()), string(prefixNodeMimir)+"/")
		if constants.IsOperationalMimir(key) {
			k.DeleteNodeMimirs(ctx, key)
		}
	}
}

// GetMimirIterator iterate gas units
func (k KVStore) GetMimirIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixMimir)
}

// GetNodeMimirIterator iterate gas units
func (k KVStore) GetNodeMimirIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixNodeMimir)
}

func (k KVStore) DeleteMimir(ctx cosmos.Context, key string) error {
	keeper.MimirCacheFromContext(ctx).Disable()
	keeper.AnchorsCacheFromContext(ctx).Disable()
	k.del(ctx, k.GetKey(prefixMimir, key))
	return nil
}

func (k KVStore) GetNodePauseChain(ctx cosmos.Context, acc cosmos.AccAddress) int64 {
	record := int64(-1)
	_, _ = k.getInt64(ctx, k.GetKey(prefixNodePauseChain, acc.String()), &record)
	return record
}

func (k KVStore) SetNodePauseChain(ctx cosmos.Context, acc cosmos.AccAddress) {
	k.setInt64(ctx, k.GetKey(prefixNodePauseChain, acc.String()), ctx.BlockHeight())
}
