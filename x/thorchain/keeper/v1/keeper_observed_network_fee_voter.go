package keeperv1

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setObservedNetworkFeeVoter(ctx cosmos.Context, key []byte, record ObservedNetworkFeeVoter) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getObservedNetworkFeeVoter(ctx cosmos.Context, key []byte, record *ObservedNetworkFeeVoter) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return true, nil
}

// SetObservedNetworkFeeVoter - save a observed network fee voter object
func (k KVStore) SetObservedNetworkFeeVoter(ctx cosmos.Context, networkFeeVoter ObservedNetworkFeeVoter) {
	key := networkFeeVoter.ID()
	k.setObservedNetworkFeeVoter(ctx, k.GetKey(prefixNetworkFeeVoter, key), networkFeeVoter)
}

// GetObservedNetworkFeeVoterIteratorForChain iterates only the voters for one chain.
//
// ObservedNetworkFeeVoter.ID() formats as "CHAIN-height-rate-size", so the chain leads
// the key and a prefix scan is exact. The trailing "-" is what makes it safe: without
// it a chain ID that is a prefix of another would pull in the wrong records.
//
// The unscoped iterator walks every voter the network has ever recorded, for every
// chain, and that store is never pruned - so anything on a per-block path must use this.
func (k KVStore) GetObservedNetworkFeeVoterIteratorForChain(ctx cosmos.Context, chain common.Chain) cosmos.Iterator {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	prefix := fmt.Sprintf("%s/%s-", prefixNetworkFeeVoter, strings.ToUpper(chain.String()))
	return cosmos.KVStorePrefixIterator(store, []byte(prefix))
}

// SetLatestObservedNetworkFeeVoterID records which voter last reached consensus for the
// chain, so chain activation can read that one voter instead of scanning the chain's
// whole, never-pruned voter history. An empty id removes the record.
func (k KVStore) SetLatestObservedNetworkFeeVoterID(ctx cosmos.Context, chain common.Chain, id string) {
	store := k.kvStore(ctx)
	key := k.GetKey(prefixLatestNetworkFeeVoter, chain.String())
	if id == "" {
		store.Delete(key)
		return
	}
	store.Set(key, []byte(id))
}

// GetLatestObservedNetworkFeeVoter returns the voter recorded by
// SetLatestObservedNetworkFeeVoterID, and false when none is recorded.
func (k KVStore) GetLatestObservedNetworkFeeVoter(ctx cosmos.Context, chain common.Chain) (ObservedNetworkFeeVoter, bool, error) {
	store := k.kvStore(ctx)
	id := store.Get(k.GetKey(prefixLatestNetworkFeeVoter, chain.String()))
	if len(id) == 0 {
		return ObservedNetworkFeeVoter{}, false, nil
	}
	var voter ObservedNetworkFeeVoter
	found, err := k.getObservedNetworkFeeVoter(ctx, k.GetKey(prefixNetworkFeeVoter, string(id)), &voter)
	return voter, found, err
}

// GetObservedNetworkFeeVoterIterator iterate tx in voters
func (k KVStore) GetObservedNetworkFeeVoterIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixNetworkFeeVoter)
}

// GetObservedNetworkFeeVoter - gets information of an observed network fee voter
func (k KVStore) GetObservedNetworkFeeVoter(ctx cosmos.Context, height int64, chain common.Chain, rate, size int64) (ObservedNetworkFeeVoter, error) {
	record := NewObservedNetworkFeeVoter(height, chain)
	if rate > 0 {
		record.FeeRate = rate
	}
	if size > 0 {
		record.TransactionSize = size
	}
	key := record.ID()
	_, err := k.getObservedNetworkFeeVoter(ctx, k.GetKey(prefixNetworkFeeVoter, key), &record)
	return record, err
}
