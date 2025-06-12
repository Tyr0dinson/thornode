package keeperv1

import (
	"encoding/hex"
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setPriceFeed(ctx cosmos.Context, key string, record PriceFeed) {
	store := ctx.KVStore(k.storeKey)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete([]byte(key))
	} else {
		store.Set([]byte(key), buf)
	}
}

func (k KVStore) getPriceFeed(ctx cosmos.Context, key string, record *PriceFeed) (bool, error) {
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

// GetPriceFeed gets the oracle price feed for the node from kv store, returns error, if it doesn't exists
func (k KVStore) GetPriceFeed(ctx cosmos.Context, node string) (PriceFeed, error) {
	var record PriceFeed
	_, err := k.getPriceFeed(ctx, k.GetKey(prefixPriceFeed, node), &record)
	return record, err
}

// SetPriceFeed saves the price feed to kv store
func (k KVStore) SetPriceFeed(ctx cosmos.Context, priceFeed PriceFeed) error {
	if err := priceFeed.Valid(); err != nil {
		return err
	}
	k.setPriceFeed(ctx, k.GetKey(prefixPriceFeed, hex.EncodeToString(priceFeed.Node)), priceFeed)
	return nil
}

// GetPriceIterator
func (k KVStore) GetPriceFeedIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixPriceFeed)
}
