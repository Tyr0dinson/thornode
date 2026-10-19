package keeperv1

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setPool(ctx cosmos.Context, key []byte, record Pool) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getPool(ctx cosmos.Context, key []byte, record *Pool) (bool, error) {
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

// GetPoolIterator iterate pools
func (k KVStore) GetPoolIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixPool)
}

// GetPools return all pool in key value store regardless state
func (k KVStore) GetPools(ctx cosmos.Context) (Pools, error) {
	var pools Pools
	iterator := k.GetPoolIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		// Full scans (GetAnchors per stable-swap check, EndBlock managers) see
		// mostly unchanged pools, so their decodes are memoized. Point reads in
		// getPool often follow a SetPool of the same pool and would only miss.
		if !k.memoDecodePool(iterator.Key(), iterator.Value(), &pool) {
			if err := k.Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
				return nil, dbError(ctx, "Unmarsahl: pool", err)
			}
		}
		pools = append(pools, pool)
	}
	return pools, nil
}

// GetPool get the entire Pool metadata struct based on given asset.
// Note: returns a default-initialized Pool (with empty Asset) when the pool
// does not exist, with err==nil. Callers should check pool.IsEmpty() or use
// PoolExist() to distinguish between a nonexistent pool and an existing one.
func (k KVStore) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	record := NewPool()
	_, err := k.getPool(ctx, k.GetKey(prefixPool, asset.String()), &record)

	return record, err
}

// SetPool save the entire Pool metadata struct to key value store
func (k KVStore) SetPool(ctx cosmos.Context, pool Pool) error {
	if pool.Asset.IsEmpty() {
		return errors.New("cannot save a pool with an empty asset")
	}
	k.setPool(ctx, k.GetKey(prefixPool, pool.Asset.String()), pool)
	return nil
}

// PoolExist check whether the given pool exist in the data store
func (k KVStore) PoolExist(ctx cosmos.Context, asset common.Asset) bool {
	return k.has(ctx, k.GetKey(prefixPool, asset.String()))
}

func (k KVStore) RemovePool(ctx cosmos.Context, asset common.Asset) {
	k.del(ctx, k.GetKey(prefixPool, asset.String()))
}

func (k KVStore) SetPoolLUVI(ctx cosmos.Context, asset common.Asset, luvi cosmos.Uint) {
	key := k.GetKey(prefixPoolLUVI, asset.String())
	k.setUint(ctx, key, luvi)
}

func (k KVStore) GetPoolLUVI(ctx cosmos.Context, asset common.Asset) (cosmos.Uint, error) {
	key := k.GetKey(prefixPoolLUVI, asset.String())
	record := cosmos.ZeroUint()
	_, err := k.getUint(ctx, key, &record)
	return record, err
}
