package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
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

func normalizeTssKeysignFailVoter(record TssKeysignFailVoter, found bool) (TssKeysignFailVoter, error) {
	if !found {
		record.SchemaVersion = types.CurrentTssKeysignFailVoterSchemaVersion
		return record, nil
	}

	switch record.SchemaVersion {
	case 0:
		// Version zero is every record written before schema versioning. Its
		// Completed default is ambiguous, so the scheduled upgrade deliberately
		// seals all such voters instead of guessing from Height or signer state.
		record.SchemaVersion = types.CurrentTssKeysignFailVoterSchemaVersion
		record.Completed = true
		return record, nil
	case types.CurrentTssKeysignFailVoterSchemaVersion:
		return record, nil
	default:
		return record, fmt.Errorf("unsupported TSS keysign fail voter schema version: %d", record.SchemaVersion)
	}
}

// SetTssKeysignFailVoter - save a tss keysign fail voter object
func (k KVStore) SetTssKeysignFailVoter(ctx cosmos.Context, tss TssKeysignFailVoter) {
	switch tss.SchemaVersion {
	case 0:
		// Every post-upgrade writer emits the explicit schema. A zero-valued
		// struct is a new version-1 voter, never a newly written legacy record.
		tss.SchemaVersion = types.CurrentTssKeysignFailVoterSchemaVersion
	case types.CurrentTssKeysignFailVoterSchemaVersion:
	default:
		panic(fmt.Sprintf("refusing to persist unsupported TSS keysign fail voter schema version: %d", tss.SchemaVersion))
	}
	k.setTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, tss.String()), tss)
}

// GetTssKeysignFailVoter - gets information of a tss keysign failure voter object
func (k KVStore) GetTssKeysignFailVoter(ctx cosmos.Context, id string) (TssKeysignFailVoter, error) {
	record := TssKeysignFailVoter{ID: id}
	found, err := k.getTssKeysignFailVoter(ctx, k.GetKey(prefixTssKeysignFailure, id), &record)
	if err != nil {
		return record, err
	}
	return normalizeTssKeysignFailVoter(record, found)
}

// WalkTssKeysignFailVoters visits normalized voters in store order. Returning
// true from visit stops the walk. Callers never receive raw schema-zero bytes.
func (k KVStore) WalkTssKeysignFailVoters(ctx cosmos.Context, visit func(TssKeysignFailVoter) bool) error {
	iterator := k.getIterator(ctx, prefixTssKeysignFailure)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var record TssKeysignFailVoter
		if err := k.cdc.Unmarshal(iterator.Value(), &record); err != nil {
			return dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, iterator.Key()), err)
		}
		normalized, err := normalizeTssKeysignFailVoter(record, true)
		if err != nil {
			return err
		}
		if visit(normalized) {
			return nil
		}
	}
	return nil
}
