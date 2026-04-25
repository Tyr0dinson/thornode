package keeperv1

import (
	"fmt"
	"strconv"

	"github.com/cosmos/cosmos-sdk/runtime"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (k KVStore) setKeygenBlock(ctx cosmos.Context, key []byte, record KeygenBlock) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getKeygenBlock(ctx cosmos.Context, key []byte, record *KeygenBlock) (bool, error) {
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

// SetKeygenBlock save the KeygenBlock to kv store
func (k KVStore) SetKeygenBlock(ctx cosmos.Context, keygen KeygenBlock) {
	k.setKeygenBlock(ctx, k.GetKey(prefixKeygen, strconv.FormatInt(keygen.Height, 10)), keygen)
}

// GetKeygenBlockIterator return an iterator
func (k KVStore) GetKeygenBlockIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixKeygen)
}

// GetKeygenBlock from a given height
func (k KVStore) GetKeygenBlock(ctx cosmos.Context, height int64) (KeygenBlock, error) {
	record := NewKeygenBlock(height)
	_, err := k.getKeygenBlock(ctx, k.GetKey(prefixKeygen, strconv.FormatInt(height, 10)), &record)
	return record, err
}

// SetChurnKeygenExpected stores the canonical (sorted) memberships of all
// keygen groups planned for the current churn attempt. This is used as the
// completion counter for churn — separate from KeygenBlock, which is the
// per-height TSS kickoff signal — so that retries which preserve already
// completed groups do not desync from the completion check in handler_tss.
// memberships is overwritten in full on each call; passing an empty slice
// deletes the record.
func (k KVStore) SetChurnKeygenExpected(ctx cosmos.Context, memberships [][]string) {
	key := k.GetKey(prefixChurnKeygenExpected, "")
	if len(memberships) == 0 {
		k.del(ctx, key)
		return
	}
	record := NewKeygenBlock(ctx.BlockHeight())
	for _, m := range memberships {
		record.Keygens = append(record.Keygens, types.Keygen{Members: m})
	}
	k.setKeygenBlock(ctx, key, record)
}

// GetChurnKeygenExpected returns the expected churn keygen memberships, and
// a boolean indicating whether the record was present.
func (k KVStore) GetChurnKeygenExpected(ctx cosmos.Context) ([][]string, bool, error) {
	var record KeygenBlock
	key := k.GetKey(prefixChurnKeygenExpected, "")
	ok, err := k.getKeygenBlock(ctx, key, &record)
	if err != nil || !ok {
		return nil, ok, err
	}
	memberships := make([][]string, 0, len(record.Keygens))
	for _, kg := range record.Keygens {
		memberships = append(memberships, kg.Members)
	}
	return memberships, true, nil
}

// DeleteChurnKeygenExpected removes the expected churn keygen record.
func (k KVStore) DeleteChurnKeygenExpected(ctx cosmos.Context) {
	k.del(ctx, k.GetKey(prefixChurnKeygenExpected, ""))
}
