package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setPrice(ctx cosmos.Context, key string, record OraclePrice) {
	store := ctx.KVStore(k.storeKey)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete([]byte(key))
	} else {
		store.Set([]byte(key), buf)
	}
}

func (k KVStore) getPrice(ctx cosmos.Context, key string, record *OraclePrice) (bool, error) {
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

// GetPrice get the oracle price the asset from kv store, returns error, if it doesn't exists
func (k KVStore) GetPrice(ctx cosmos.Context, symbol string) (OraclePrice, error) {
	var record OraclePrice
	_, err := k.getPrice(ctx, k.GetKey(prefixOraclePrice, symbol), &record)
	return record, err
}

// SetPrice save the oracle price to kv store
func (k KVStore) SetPrice(ctx cosmos.Context, oraclePrice OraclePrice) error {
	if err := oraclePrice.Valid(); err != nil {
		return err
	}
	k.setPrice(ctx, k.GetKey(prefixOraclePrice, oraclePrice.Symbol), oraclePrice)
	return nil
}

// GetPriceIterator
func (k KVStore) GetPriceIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixOraclePrice)
}
