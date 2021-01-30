package keeperv1

import (
	"errors"
	"fmt"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

func (k KVStore) setComposite(ctx cosmos.Context, key string, record Composite) {
	store := ctx.KVStore(k.storeKey)
	buf := k.cdc.MustMarshalBinaryBare(&record)
	if buf == nil {
		store.Delete([]byte(key))
	} else {
		store.Set([]byte(key), buf)
	}
}

func (k KVStore) getComposite(ctx cosmos.Context, key string, record *Composite) (bool, error) {
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

// GetCompositeIterator iterate composite assets
func (k KVStore) GetCompositeIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixComposite)
}

// CompositeExist check whether the given composite exists in the data store
func (k KVStore) CompositeExists(ctx cosmos.Context, name string) bool {
	return k.has(ctx, k.GetKey(ctx, prefixComposite, name))
}

// GetComposite get Composite by given name
func (k KVStore) GetComposite(ctx cosmos.Context, name string) (Composite, error) {
	record := Composite{}
	_, err := k.getComposite(ctx, k.GetKey(ctx, prefixComposite, name), &record)

	return record, err
}

// SetComposite save the entire Composite metadata struct to key value store
func (k KVStore) SetComposite(ctx cosmos.Context, comp Composite) error {
	if comp.IsEmpty() {
		return errors.New("cannot save a composite asset with an empty name")
	}

	k.setComposite(ctx, k.GetKey(ctx, prefixComposite, comp.Name), comp)
	return nil
}
