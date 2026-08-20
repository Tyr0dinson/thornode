package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setErrataTxVoter(ctx cosmos.Context, key []byte, record ErrataTxVoter) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getErrataTxVoter(ctx cosmos.Context, key []byte, record *ErrataTxVoter) (bool, error) {
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

// SetErrataTxVoter - save a errata voter object
func (k KVStore) SetErrataTxVoter(ctx cosmos.Context, errata ErrataTxVoter) {
	k.setErrataTxVoter(ctx, k.GetKey(prefixErrataTx, errata.String()), errata)
}

// GetErrataTxVoterIterator iterate errata tx voter
func (k KVStore) GetErrataTxVoterIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixErrataTx)
}

// GetErrataTxVoter - gets information of errata tx voter
func (k KVStore) GetErrataTxVoter(ctx cosmos.Context, txID common.TxID, chain common.Chain) (ErrataTxVoter, error) {
	return k.GetErrataTxVoterAtHeight(ctx, txID, chain, 0)
}

// GetErrataTxVoterAtHeight gets the voter for one external-chain errata
// generation. Height zero addresses the legacy TxID-only voter.
func (k KVStore) GetErrataTxVoterAtHeight(
	ctx cosmos.Context,
	txID common.TxID,
	chain common.Chain,
	externalHeight int64,
) (ErrataTxVoter, error) {
	record := NewErrataTxVoterAtHeight(txID, chain, externalHeight)
	_, err := k.getErrataTxVoter(ctx, k.GetKey(prefixErrataTx, record.String()), &record)
	return record, err
}
