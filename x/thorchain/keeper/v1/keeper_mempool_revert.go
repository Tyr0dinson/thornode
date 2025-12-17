package keeperv1

import (
	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// SetMempoolRevertTxID stores a mapping from a reverse swap txID to the original inbound txID.
// This is used during mempool swap reversion to link the reverse swap back to the original
// transaction so we can look up the source amount to deduct from the pool.
func (k KVStore) SetMempoolRevertTxID(ctx cosmos.Context, reverseTxID, originalTxID common.TxID) {
	key := k.GetKey(prefixMempoolRevert, reverseTxID.String())
	k.setString(ctx, string(key), originalTxID.String())
}

// GetMempoolRevertTxID looks up the original inbound txID for a given reverse swap txID.
// Returns the original txID and true if found, or empty txID and false if not found.
func (k KVStore) GetMempoolRevertTxID(ctx cosmos.Context, reverseTxID common.TxID) (common.TxID, bool) {
	key := k.GetKey(prefixMempoolRevert, reverseTxID.String())
	var record string
	found, err := k.getString(ctx, string(key), &record)
	if err != nil || !found {
		return common.TxID(""), false
	}
	txID, err := common.NewTxID(record)
	if err != nil {
		return common.TxID(""), false
	}
	return txID, true
}

// DeleteMempoolRevertTxID removes the mapping for a reverse swap txID.
// This should be called after the mapping has been used to clean up storage.
func (k KVStore) DeleteMempoolRevertTxID(ctx cosmos.Context, reverseTxID common.TxID) {
	key := k.GetKey(prefixMempoolRevert, reverseTxID.String())
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Delete(key)
}
