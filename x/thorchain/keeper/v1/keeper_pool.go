package keeperv1

import (
	"errors"
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const availablePoolRuneDepthKey = "total"

func (k KVStore) setPool(ctx cosmos.Context, key []byte, record Pool) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getPool(ctx cosmos.Context, key []byte, record *Pool) (bool, error) {
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
		err := k.Cdc().Unmarshal(iterator.Value(), &pool)
		if err != nil {
			return nil, dbError(ctx, "Unmarsahl: pool", err)
		}
		pools = append(pools, pool)
	}
	return pools, nil
}

func availablePoolRuneDepthContribution(pool Pool) cosmos.Uint {
	if pool.Status == types.PoolStatus_Available && !pool.BalanceRune.IsZero() {
		return pool.BalanceRune
	}
	return cosmos.ZeroUint()
}

func (k KVStore) getAvailablePoolRuneDepth(ctx cosmos.Context) (cosmos.Uint, bool, error) {
	record := cosmos.ZeroUint()
	ok, err := k.getUint(ctx, k.GetKey(prefixPoolAvailableRuneDepth, availablePoolRuneDepthKey), &record)
	return record, ok, err
}

func (k KVStore) setAvailablePoolRuneDepth(ctx cosmos.Context, total cosmos.Uint) {
	k.setUint(ctx, k.GetKey(prefixPoolAvailableRuneDepth, availablePoolRuneDepthKey), total)
}

func (k KVStore) recomputeAvailablePoolRuneDepth(ctx cosmos.Context) (cosmos.Uint, error) {
	total := cosmos.ZeroUint()
	iterator := k.GetPoolIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var pool Pool
		if err := k.Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return cosmos.ZeroUint(), dbError(ctx, "Unmarshal: available pool rune depth", err)
		}
		total = total.Add(availablePoolRuneDepthContribution(pool))
	}

	return total, nil
}

// GetAvailablePoolRuneDepth returns the total RUNE depth across all available pools.
// If the aggregate has not been initialized yet, it is recomputed from pool state
// and backfilled so subsequent reads avoid a full pool scan.
func (k KVStore) GetAvailablePoolRuneDepth(ctx cosmos.Context) (cosmos.Uint, error) {
	total, ok, err := k.getAvailablePoolRuneDepth(ctx)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	if ok {
		return total, nil
	}

	total, err = k.recomputeAvailablePoolRuneDepth(ctx)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	k.setAvailablePoolRuneDepth(ctx, total)
	return total, nil
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

	existingPool, err := k.GetPool(ctx, pool.Asset)
	if err != nil {
		return err
	}
	totalRuneDepth, err := k.GetAvailablePoolRuneDepth(ctx)
	if err != nil {
		return err
	}
	oldContribution := availablePoolRuneDepthContribution(existingPool)
	newContribution := availablePoolRuneDepthContribution(pool)

	k.setPool(ctx, k.GetKey(prefixPool, pool.Asset.String()), pool)
	switch {
	case newContribution.GTE(oldContribution):
		totalRuneDepth = totalRuneDepth.Add(newContribution.Sub(oldContribution))
	default:
		totalRuneDepth = common.SafeSub(totalRuneDepth, oldContribution.Sub(newContribution))
	}
	k.setAvailablePoolRuneDepth(ctx, totalRuneDepth)
	return nil
}

// PoolExist check whether the given pool exist in the data store
func (k KVStore) PoolExist(ctx cosmos.Context, asset common.Asset) bool {
	return k.has(ctx, k.GetKey(prefixPool, asset.String()))
}

func (k KVStore) RemovePool(ctx cosmos.Context, asset common.Asset) {
	if pool, err := k.GetPool(ctx, asset); err == nil {
		if totalRuneDepth, totalErr := k.GetAvailablePoolRuneDepth(ctx); totalErr == nil {
			totalRuneDepth = common.SafeSub(totalRuneDepth, availablePoolRuneDepthContribution(pool))
			k.setAvailablePoolRuneDepth(ctx, totalRuneDepth)
		}
	}
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
