package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setYieldAccount(ctx cosmos.Context, key string, record YieldAccount) {
	store := ctx.KVStore(k.storeKey)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete([]byte(key))
	} else {
		store.Set([]byte(key), buf)
	}
}

func (k KVStore) getYieldAccount(ctx cosmos.Context, key string, record *YieldAccount) (bool, error) {
	store := ctx.KVStore(k.storeKey)
	if !store.Has([]byte(key)) {
		return false, nil
	}

	bz := store.Get([]byte(key))
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return true, nil
}

func (k KVStore) GetYieldAccount(ctx cosmos.Context, addr common.Address, asset common.Asset) (YieldAccount, error) {
	record := NewYieldAccount(addr, asset)
	_, err := k.getYieldAccount(ctx, k.GetKey(prefixYieldAccount, record.Key()), &record)
	return record, err
}

func (k KVStore) SetYieldAccount(ctx cosmos.Context, ya YieldAccount) {
	key := k.GetKey(prefixYieldAccount, ya.Key())
	k.setYieldAccount(ctx, key, ya)
}
