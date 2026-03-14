package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// -------------------- Prepaid Gas Methods --------------------
// Prepaid gas tracks user-funded gas coins attached to intent cancel transactions.
// When a non-pool intent is cancelled, the user must supply gas tokens for the
// refund outbound. This store records that prepaid amount so the gas accounting
// path can skip reserve reimbursement and settle any leftover.

// SetIntentPrepaidGas saves the prepaid gas coin for an intent cancel refund.
func (k KVStore) SetIntentPrepaidGas(ctx cosmos.Context, txID common.TxID, coin common.Coin) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixIntentPrepaidGas, txID.String())
	buf := k.cdc.MustMarshal(&coin)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

// GetIntentPrepaidGas retrieves the prepaid gas coin for an intent.
// Returns an empty coin and no error if no prepaid gas exists.
func (k KVStore) GetIntentPrepaidGas(ctx cosmos.Context, txID common.TxID) (common.Coin, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixIntentPrepaidGas, txID.String())
	if !store.Has(key) {
		return common.NoCoin, nil
	}

	bz := store.Get(key)
	var record common.Coin
	if err := k.cdc.Unmarshal(bz, &record); err != nil {
		return common.NoCoin, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (Coin) %s", key), err)
	}
	return record, nil
}

// DeleteIntentPrepaidGas removes the prepaid gas record for an intent.
func (k KVStore) DeleteIntentPrepaidGas(ctx cosmos.Context, txID common.TxID) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Delete(k.GetKey(prefixIntentPrepaidGas, txID.String()))
}
