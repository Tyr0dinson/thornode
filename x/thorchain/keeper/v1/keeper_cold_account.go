package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func (k KVStore) setColdAccount(ctx cosmos.Context, key string, record ColdAccount) {
	store := ctx.KVStore(k.storeKey)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete([]byte(key))
	} else {
		store.Set([]byte(key), buf)
	}
}

func (k KVStore) getColdAccount(ctx cosmos.Context, key string, record *ColdAccount) (bool, error) {
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

// ListColdAccountsByStatus - get a list of cold accounts with the given status
func (k KVStore) ListColdAccountsByStatus(ctx cosmos.Context, status ColdStatus) (ColdAccounts, error) {
	accts := make(ColdAccounts, 0)
	iter := k.GetColdAccountIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var acct ColdAccount
		if err := k.cdc.Unmarshal(iter.Value(), &acct); err != nil {
			return accts, dbError(ctx, "Unmarshal: cold account", err)
		}
		if acct.Status == status {
			accts = append(accts, acct)
		}
	}
	return accts, nil
}

// ListActiveColdAccounts - get a list of active cold accounts
func (k KVStore) ListActiveColdAccounts(ctx cosmos.Context) (ColdAccounts, error) {
	return k.ListColdAccountsByStatus(ctx, ColdActive)
}

// GetColdAccount try to get cold account with the given address from db
func (k KVStore) GetColdAccount(ctx cosmos.Context, addr cosmos.AccAddress, asset common.Asset) (ColdAccount, error) {
	record := NewColdAccount(addr, asset, common.PubKey(""))
	_, err := k.getColdAccount(ctx, k.GetKey(ctx, prefixColdAccount, record.Key()), &record)
	return record, err
}

// GetColdAccountByAddress try to get cold account with the given address from db
func (k KVStore) GetColdAccountByPubKey(ctx cosmos.Context, pk common.PubKey, asset common.Asset) (ColdAccount, error) {
	acc := cosmos.AccAddress{}
	k.getAccAddress(ctx, k.GetKey(ctx, prefixColdAccountPubKey, pk.String()), &acc)
	return k.GetColdAccount(ctx, acc, asset)
}

// SetColdAccount save the given cold account into data store
func (k KVStore) SetColdAccount(ctx cosmos.Context, acct ColdAccount) error {
	if acct.IsEmpty() {
		return nil
	}
	// ensure we're NOT overwriting the index
	acc := cosmos.AccAddress{}
	k.getAccAddress(ctx, k.GetKey(ctx, prefixColdAccountPubKey, acct.PubKey.String()), &acc)
	if !acc.Empty() && !acct.Operator.Equals(acc) {
		return fmt.Errorf("cannot overwrite cold account address index")
	}
	k.setAccAddress(ctx, k.GetKey(ctx, prefixColdAccountPubKey, acct.PubKey.String()), acct.Operator)
	k.setColdAccount(ctx, k.GetKey(ctx, prefixColdAccount, acct.Key()), acct)
	return nil
}

// GetColdAccountIterator iterate cold account
func (k KVStore) GetColdAccountIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixColdAccount)
}

func (k KVStore) SetColdAccountRatio(ctx cosmos.Context, acct ColdAccount, amt cosmos.Uint) {
	if amt.IsZero() {
		return
	}
	ra := rewriteRatio(ratioLength, getRatio(acct.Bond, amt))
	key := k.GetKey(ctx, prefixColdAccountRatio, fmt.Sprintf("%s/%s-%s", acct.Asset, ra, acct.Operator))
	store := ctx.KVStore(k.storeKey)
	store.Set([]byte(key), nil)
}

func (k KVStore) DelColdAccountRatio(ctx cosmos.Context, acct ColdAccount, ratio cosmos.Uint) {
	key := k.GetKey(ctx, prefixColdAccountRatio, fmt.Sprintf("%s/%s-%s", acct.Asset, ratio, acct.Operator))
	k.del(ctx, key)
}

// GetColdAccountRatioIterator iterate cold account
func (k KVStore) GetColdAccountIteratorRatio(ctx cosmos.Context, asset common.Asset) cosmos.Iterator {
	store := ctx.KVStore(k.storeKey)
	prefix := k.GetKey(ctx, prefixColdAccountRatio, asset.String())
	return cosmos.KVStoreReversePrefixIterator(store, []byte(prefix))
}
