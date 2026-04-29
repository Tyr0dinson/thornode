package keeperv1

import (
	"fmt"
	"strings"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

// SetObservedTxInVoter - save a txin voter object
func (k KVStore) SetObservedTxInVoter(ctx cosmos.Context, tx ObservedTxVoter) {
	k.setObservedTxVoter(ctx, prefixObservedTxIn, tx)
	if tx.UpdatedVault {
		k.clearInFlightMigrate(ctx, tx.TxID)
	}
}

// SetObservedTxOutVoter - save a txout voter object
func (k KVStore) SetObservedTxOutVoter(ctx cosmos.Context, tx ObservedTxVoter) {
	k.setObservedTxVoter(ctx, prefixObservedTxOut, tx)
	k.trackInFlightMigrate(ctx, tx)
}

func (k KVStore) setObservedTxVoter(ctx cosmos.Context, prefix types.DbPrefix, tx ObservedTxVoter) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefix, tx.String())
	buf := k.cdc.MustMarshal(&tx)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

// GetObservedTxInVoterIterator iterate tx in voters
func (k KVStore) GetObservedTxInVoterIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getObservedTxVoterIterator(ctx, prefixObservedTxIn)
}

// GetObservedTxOutVoterIterator iterate tx out voters
func (k KVStore) GetObservedTxOutVoterIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getObservedTxVoterIterator(ctx, prefixObservedTxOut)
}

func (k KVStore) GetInFlightMigrateIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixInFlightMigrate)
}

func (k KVStore) getObservedTxVoterIterator(ctx cosmos.Context, prefix types.DbPrefix) cosmos.Iterator {
	return k.getIterator(ctx, prefix)
}

// GetObservedTxInVoter - gets information of an observed inbound tx based on the txid
func (k KVStore) GetObservedTxInVoter(ctx cosmos.Context, hash common.TxID) (ObservedTxVoter, error) {
	return k.getObservedTxVoter(ctx, prefixObservedTxIn, hash)
}

// GetObservedTxOutVoter - gets information of an observed outbound tx based on the txid
func (k KVStore) GetObservedTxOutVoter(ctx cosmos.Context, hash common.TxID) (ObservedTxVoter, error) {
	return k.getObservedTxVoter(ctx, prefixObservedTxOut, hash)
}

func (k KVStore) getObservedTxVoter(ctx cosmos.Context, prefix types.DbPrefix, hash common.TxID) (ObservedTxVoter, error) {
	record := ObservedTxVoter{TxID: hash}
	key := k.GetKey(prefix, hash.String())
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if !store.Has(key) {
		return record, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, &record); err != nil {
		return record, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return record, nil
}

func (k KVStore) SetObservedLink(ctx cosmos.Context, inhash, outhash common.TxID) {
	k.setObservedLink(ctx, inhash, outhash)
}

func (k KVStore) setObservedLink(ctx cosmos.Context, inhash, outhash common.TxID) {
	key := k.GetKey(prefixObservedLink, inhash.String())
	record := make([]string, 0)
	if _, err := k.getStrings(ctx, key, &record); err != nil {
		ctx.Logger().Error("fail to get observed link", "inhash", inhash, "error", err)
	}
	for _, s := range record {
		if s == outhash.String() {
			return
		}
	}

	record = append(record, outhash.String())
	k.setStrings(ctx, key, record)
}

func (k KVStore) GetObservedLink(ctx cosmos.Context, inhash common.TxID) []common.TxID {
	hashes := make([]common.TxID, 0)
	strs := make([]string, 0)

	key := k.GetKey(prefixObservedLink, inhash.String())
	if _, err := k.getStrings(ctx, key, &strs); err != nil {
		ctx.Logger().Error("fail to get observed link", "inhash", inhash, "error", err)
	}

	for _, s := range strs {
		hash, err := common.NewTxID(s)
		if err != nil {
			ctx.Logger().Error("failed to parse TXID", "txid", s, "error", err)
			continue
		}
		hashes = append(hashes, hash)
	}
	return hashes
}

func (k KVStore) trackInFlightMigrate(ctx cosmos.Context, tx ObservedTxVoter) {
	key := string(k.GetKey(prefixInFlightMigrate, tx.TxID.String()))
	if tx.Tx.IsEmpty() || tx.Height == 0 || tx.Tx.ObservedPubKey.IsEmpty() {
		k.del(ctx, []byte(key))
		return
	}

	memoType, _, _ := strings.Cut(tx.Tx.Tx.Memo, ":")
	if !strings.EqualFold(memoType, "migrate") {
		k.del(ctx, []byte(key))
		return
	}

	k.setString(ctx, key, tx.Tx.ObservedPubKey.String())
}

func (k KVStore) clearInFlightMigrate(ctx cosmos.Context, txID common.TxID) {
	k.del(ctx, k.GetKey(prefixInFlightMigrate, txID.String()))
}
