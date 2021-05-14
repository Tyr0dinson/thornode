package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func (k KVStore) setTHORName(ctx cosmos.Context, key string, record THORName) {
	store := ctx.KVStore(k.storeKey)
	buf := k.cdc.MustMarshalBinaryBare(&record)
	if buf == nil {
		store.Delete([]byte(key))
	} else {
		store.Set([]byte(key), buf)
	}
}

func (k KVStore) getTHORName(ctx cosmos.Context, key string, record *THORName) (bool, error) {
	store := ctx.KVStore(k.storeKey)
	if !store.Has([]byte(key)) {
		return false, nil
	}

	bz := store.Get([]byte(key))
	if err := k.cdc.UnmarshalBinaryBare(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return true, nil
}

// GetTHORNameIterator only iterate THORNames
func (k KVStore) GetTHORNameIterator(ctx cosmos.Context, name string) cosmos.Iterator {
	if len(name) == 0 {
		return k.getIterator(ctx, prefixTHORName)
	}
	store := ctx.KVStore(k.storeKey)
	return cosmos.KVStorePrefixIterator(store, []byte(k.GetKey(ctx, prefixTHORName, name+"/")))
}

// SetTHORName save the THORName object to store
func (k KVStore) SetTHORName(ctx cosmos.Context, name THORName) {
	k.setTHORName(ctx, k.GetKey(ctx, prefixTHORName, name.Key()), name)
}

// THORNameExists check whether the given name exists
func (k KVStore) THORNameExists(ctx cosmos.Context, name string, chain common.Chain) bool {
	record := THORName{
		Name:  name,
		Chain: chain,
	}
	if k.has(ctx, k.GetKey(ctx, prefixTHORName, record.Key())) {
		record, _ = k.GetTHORName(ctx, name, chain)
		return record.ExpireBlockHeight >= common.BlockHeight(ctx)
	}
	return false
}

// GetTHORName get THORName with the given pubkey from data store
func (k KVStore) GetTHORName(ctx cosmos.Context, name string, chain common.Chain) (THORName, error) {
	record := THORName{
		Name:  name,
		Chain: chain,
	}
	ok, err := k.getTHORName(ctx, k.GetKey(ctx, prefixTHORName, record.Key()), &record)
	if !ok {
		return record, fmt.Errorf("THORName doesn't exist: %s", record.Key())
	}
	if record.ExpireBlockHeight < common.BlockHeight(ctx) {
		return THORName{Name: name, Chain: chain}, nil
	}
	return record, err
}

// DeleteTHORName remove the given THORName from data store
func (k KVStore) DeleteTHORName(ctx cosmos.Context, name string) error {
	iterator := k.GetTHORNameIterator(ctx, name)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var n THORName
		k.Cdc().MustUnmarshalBinaryBare(iterator.Value(), &n)
		k.del(ctx, k.GetKey(ctx, prefixTHORName, n.Key()))
	}
	return nil
}
