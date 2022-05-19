package keeperv1

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

func (k KVStore) SetTxnObservation(ctx cosmos.Context, txnOb TxnObservation) {
	key := k.GetKey(ctx, prefixObservation, fmt.Sprintf("/tx/%s", txnOb.Hash()))
	store := ctx.KVStore(k.storeKey)
	if store.Has([]byte(key)) {
		// already set, cannot overwrite
		return
	}
	buf := k.cdc.MustMarshal(&txnOb)
	if buf == nil {
		store.Delete([]byte(key))
	} else {
		store.Set([]byte(key), buf)
	}
}

func (k KVStore) GetTxnObservation(ctx cosmos.Context, hash string) (TxnObservation, error) {
	record := TxnObservation{}
	key := k.GetKey(ctx, prefixObservation, fmt.Sprintf("/tx/%s", hash))
	store := ctx.KVStore(k.storeKey)
	if !store.Has([]byte(key)) {
		return record, nil
	}
	bz := store.Get([]byte(key))
	if err := k.cdc.Unmarshal(bz, &record); err != nil {
		return record, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return record, nil
}

func (k KVStore) GetTxn(ctx cosmos.Context, hash common.TxID) (TxnObservation, bool, error) {
	nas, err := k.ListActiveValidators(ctx)
	if err != nil {
		return TxnObservation{}, false, err
	}
	return k.GetTxnWithNodeAccounts(ctx, hash, nas)
}

func (k KVStore) GetTxnWithNodeAccounts(ctx cosmos.Context, txID common.TxID, nas NodeAccounts) (TxnObservation, bool, error) {
	topScore := 0
	winner := ""
	for _, hash := range k.GetTxHashToChecksum(ctx, txID) {
		// if hash has an underscore prefix, that means it has already reached
		// consensus, select this and exit
		if strings.HasPrefix("_", hash) {
			newHash := strings.TrimPrefix(hash, "_")
			record, err := k.GetTxnObservation(ctx, newHash)
			return record, true, err
		}
		count := 0
		for _, na := range nas {
			if k.HasObservationVote(ctx, hash, na.NodeAddress) {
				count++
			}
		}
		if count > topScore || winner == "" {
			winner = hash
			topScore = count
		}
	}

	if winner == "" {
		return TxnObservation{}, false, fmt.Errorf("txn not found")
	}

	hasConsensus := HasSuperMajority(topScore, len(nas))
	if hasConsensus {
		newHash := fmt.Sprintf("_%s", winner)
		k.SetTxHashToChecksum(ctx, txID, newHash)
	}
	record, err := k.GetTxnObservation(ctx, winner)
	return record, hasConsensus, err
}

func (k KVStore) SetObservationVote(ctx cosmos.Context, hash string, signer cosmos.AccAddress) {
	key := k.GetKey(ctx, prefixObservationVote, fmt.Sprintf("%s/%s", signer.String(), hash))
	k.setInt64(ctx, key, 0)
}

func (k KVStore) HasObservationVote(ctx cosmos.Context, hash string, signer cosmos.AccAddress) bool {
	key := k.GetKey(ctx, prefixObservationVote, fmt.Sprintf("%s/%s", signer.String(), hash))
	store := ctx.KVStore(k.storeKey)
	return store.Has([]byte(key))
}

func (k KVStore) SetTxHashToChecksum(ctx cosmos.Context, hash common.TxID, checksum string) {
	key := k.GetKey(ctx, prefixObservationLink, hash.String())
	if len(checksum) == 0 {
		return
	}
	record := make([]string, 0)
	_, _ = k.getStrings(ctx, key, &record)
	for i, s := range record {
		if strings.EqualFold(s, checksum) {
			return
		}
		// check if checksum already exists, but is being committed with an
		// underscore prefix (aka consensus reached).
		if strings.EqualFold(fmt.Sprintf("_%s", s), checksum) {
			record[i] = checksum
			k.setStrings(ctx, key, record)
			return
		}
	}

	record = append(record, checksum)
	k.setStrings(ctx, key, record)
}

func (k KVStore) GetTxHashToChecksum(ctx cosmos.Context, inhash common.TxID) []string {
	strs := make([]string, 0)
	key := k.GetKey(ctx, prefixObservationLink, inhash.String())
	_, _ = k.getStrings(ctx, key, &strs)
	return strs
}
