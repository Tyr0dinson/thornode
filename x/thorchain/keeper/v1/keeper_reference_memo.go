package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setReferenceMemo(ctx cosmos.Context, key string, record ReferenceMemo) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete([]byte(key))
	} else {
		store.Set([]byte(key), buf)
	}
}

func (k KVStore) getReferenceMemo(ctx cosmos.Context, key string, record *ReferenceMemo) (bool, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if !store.Has([]byte(key)) {
		return false, nil
	}

	bz := store.Get([]byte(key))
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}

	return true, nil
}

// GetReferenceMemoIterator only iterate ReferenceMemos
func (k KVStore) GetReferenceMemoIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixReferenceMemo)
}

// SetReferenceMemo save the ReferenceMemo object to store
func (k KVStore) SetReferenceMemo(ctx cosmos.Context, record ReferenceMemo) {
	k.setReferenceMemo(ctx, string(k.GetKey(prefixReferenceMemo, record.Key())), record)
	k.setHashAlias(ctx, record.RegistrationHash, record.Key())
}

// ReferenceMemoExists check whether the given record exists
func (k KVStore) ReferenceMemoExists(ctx cosmos.Context, asset common.Asset, ref string) bool {
	record := ReferenceMemo{
		Asset:     asset,
		Reference: ref,
	}
	return k.has(ctx, k.GetKey(prefixReferenceMemo, record.Key()))
}

// GetReferenceMemo get ReferenceMemo with the given asset and ref from data store
func (k KVStore) GetReferenceMemo(ctx cosmos.Context, asset common.Asset, ref string) (ReferenceMemo, error) {
	record := ReferenceMemo{
		Asset:     asset,
		Reference: ref,
	}
	_, err := k.getReferenceMemo(ctx, string(k.GetKey(prefixReferenceMemo, record.Key())), &record)
	return record, err
}

// GetReferenceMemoByTxnHash get ReferenceMemo with the given txn hash from data store
func (k KVStore) GetReferenceMemoByTxnHash(ctx cosmos.Context, hash common.TxID) (ReferenceMemo, error) {
	record := ReferenceMemo{}
	key := k.getHashAlias(ctx, hash)
	_, err := k.getReferenceMemo(ctx, string(k.GetKey(prefixReferenceMemo, key)), &record)
	return record, err
}

// DeleteReferenceMemo remove the given ReferenceMemo from data store
func (k KVStore) DeleteReferenceMemo(ctx cosmos.Context, asset common.Asset, ref string) error {
	n := ReferenceMemo{
		Asset:     asset,
		Reference: ref,
	}
	k.del(ctx, k.GetKey(prefixReferenceMemo, n.Key()))
	return nil
}

func (k KVStore) GetLastReferenceNumber(ctx cosmos.Context, asset common.Asset) string {
	var record string
	_, _ = k.getString(ctx, string(k.GetKey(prefixReferenceMemoIndex, asset.String())), &record)
	return record
}

func (k KVStore) SetLastReferenceNumber(ctx cosmos.Context, asset common.Asset, ref string) {
	k.setString(ctx, string(k.GetKey(prefixReferenceMemoIndex, asset.String())), ref)
}

func (k KVStore) getHashAlias(ctx cosmos.Context, hash common.TxID) string {
	var record string
	_, _ = k.getString(ctx, string(k.GetKey(prefixReferenceMemoHash, hash.String())), &record)
	return record
}

func (k KVStore) setHashAlias(ctx cosmos.Context, hash common.TxID, key string) {
	hashKey := string(k.GetKey(prefixReferenceMemoHash, hash.String()))
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if store.Has([]byte(hashKey)) {
		// this hash already exist, never override it as it would allow an
		// attacker to overwrite the memo
		return
	}
	if hash.IsEmpty() {
		return
	}
	k.setString(ctx, hashKey, key)
}
