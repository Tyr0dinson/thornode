package keeperv1

import (
    "github.com/cosmos/cosmos-sdk/runtime"
    "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// SetRefundRecord records that a refund has been scheduled/emitted for a given
// inbound txid. Subsequent attempts can consult HasRefundRecord to ensure
// idempotency.
func (k KVStore) SetRefundRecord(ctx cosmos.Context, txidIn string, reason string) error {
    store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
    key := k.GetKey(prefixMemoRefundRecord, txidIn)
    // store reason as value (optional; may be empty)
    store.Set(key, []byte(reason))
    return nil
}

// HasRefundRecord returns true if a refund record exists for the given inbound
// txid. This is used to guarantee idempotent refunds across retries.
func (k KVStore) HasRefundRecord(ctx cosmos.Context, txidIn string) bool {
    store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
    key := k.GetKey(prefixMemoRefundRecord, txidIn)
    return store.Has(key)
}

