package keeperv1

import cosmos "gitlab.com/thorchain/thornode/common/cosmos"

func (k KVStore) SetLedgerVoter(ctx cosmos.Context, ledger LedgerVoter) {
	store := ctx.KVStore(k.storeKey)
	key := k.GetKey(ctx, prefixLedger, ledger.String())
	store.Set([]byte(key), k.cdc.MustMarshalBinaryBare(ledger))
}

func (k KVStore) GetLedgerVoterIterator(ctx cosmos.Context) cosmos.Iterator {
	store := ctx.KVStore(k.storeKey)
	return cosmos.KVStorePrefixIterator(store, []byte(prefixLedger))
}

func (k KVStore) GetLedgerVoter(ctx cosmos.Context, id string) (LedgerVoter, error) {
	key := k.GetKey(ctx, prefixLedger, id)

	store := ctx.KVStore(k.storeKey)
	if !store.Has([]byte(key)) {
		return LedgerVoter{ID: id}, nil
	}

	bz := store.Get([]byte(key))
	var record LedgerVoter
	if err := k.cdc.UnmarshalBinaryBare(bz, &record); err != nil {
		return LedgerVoter{}, dbError(ctx, "Unmarshal: ledger voter", err)
	}
	return record, nil
}
