package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (k KVStore) setTxOutFailVoter(ctx cosmos.Context, key []byte, record types.TxOutFailVoter) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getTxOutFailVoter(ctx cosmos.Context, key []byte, record *types.TxOutFailVoter) (bool, error) {
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

// SetTxOutFailVoter stores the supplied voter instance.
func (k KVStore) SetTxOutFailVoter(ctx cosmos.Context, voter types.TxOutFailVoter) {
	k.setTxOutFailVoter(ctx, k.GetKey(prefixTxOutFail, voter.String()), voter)
}

// GetTxOutFailVoterIterator returns an iterator over all TxOut fail voters.
func (k KVStore) GetTxOutFailVoterIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixTxOutFail)
}

// GetTxOutFailVoter retrieves the voter for the given identifier.
func (k KVStore) GetTxOutFailVoter(ctx cosmos.Context, id string) (types.TxOutFailVoter, error) {
	record := types.TxOutFailVoter{ID: id}
	_, err := k.getTxOutFailVoter(ctx, k.GetKey(prefixTxOutFail, id), &record)
	return record, err
}
