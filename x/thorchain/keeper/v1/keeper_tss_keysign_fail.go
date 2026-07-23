package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func (k KVStore) setTssKeysignFailVoter(ctx cosmos.Context, key []byte, record TssKeysignFailVoter) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getTssKeysignFailVoter(ctx cosmos.Context, key []byte, record *TssKeysignFailVoter) (bool, error) {
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

// SetTssKeysignFailVoter - save a tss keysign fail voter object
func (k KVStore) SetTssKeysignFailVoter(ctx cosmos.Context, tss TssKeysignFailVoter) {
	k.setTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, tss.String()), tss)
}

// GetTssKeysignFailVoterIterator iterate tx in voters
func (k KVStore) GetTssKeysignFailVoterIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixTssKeysignFailure)
}

// GetTssKeysignFailVoter - gets information of a tss keysign failure voter object
func (k KVStore) GetTssKeysignFailVoter(ctx cosmos.Context, id string) (TssKeysignFailVoter, error) {
	record := TssKeysignFailVoter{ID: id}
	found, err := k.getTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, id), &record)
	if err != nil {
		return record, err
	}
	// Before the explicit Completed field existed, the earliest handler marked
	// consensus by setting Height and retained Signers. Later handlers stopped
	// setting Height and cleared both Signers and LastRoundCount. Current
	// production code never sets Height on an in-progress voter and never
	// persists a genuinely new voter before its first signer is recorded.
	// Therefore either historical terminal shape is unambiguous:
	//   1. positive Height, with or without retained Signers; or
	//   2. a stored empty consensus state.
	//
	// A missing voter is initialized identically in memory, which is why found
	// is required here.
	// Normalize legacy terminal voters on read; the handler persists the
	// explicit marker if a replay reaches it.
	if found && !record.Completed &&
		(record.Height > 0 || (len(record.Signers) == 0 && record.LastRoundCount == 0)) {
		record.Completed = true
	}
	return record, err
}
