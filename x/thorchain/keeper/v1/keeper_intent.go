package keeperv1

import (
	"fmt"
	"strconv"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// -------------------- Intent Methods --------------------

func (k KVStore) setIntent(ctx cosmos.Context, key []byte, record types.Intent) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getIntent(ctx cosmos.Context, key []byte, record *types.Intent) (bool, error) {
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

// SetIntent saves an intent to the KV store and updates active status index
func (k KVStore) SetIntent(ctx cosmos.Context, intent types.Intent) {
	// Save the intent
	k.setIntent(ctx, k.GetKey(prefixIntent, intent.TxID.String()), intent)

	// Manage active status index using prefix keys (O(1) add/remove)
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	activeKey := k.GetKey(prefixIntentStatusIndex, intent.TxID.String())

	if intent.Status == types.IntentStatus_IntentActive {
		store.Set(activeKey, []byte{1})
	} else {
		store.Delete(activeKey)
	}
}

// GetIntent retrieves an intent from the KV store
func (k KVStore) GetIntent(ctx cosmos.Context, txID common.TxID) (types.Intent, error) {
	record := types.Intent{}
	_, err := k.getIntent(ctx, k.GetKey(prefixIntent, txID.String()), &record)
	return record, err
}

// IntentExists checks whether an intent exists
func (k KVStore) IntentExists(ctx cosmos.Context, txID common.TxID) bool {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return store.Has(k.GetKey(prefixIntent, txID.String()))
}

// RemoveIntent removes an intent and all associated child records (solves, auction, bids)
func (k KVStore) RemoveIntent(ctx cosmos.Context, txID common.TxID) {
	// Remove all associated solves
	solves, err := k.GetIntentSolvesByIntentID(ctx, txID)
	if err == nil {
		for _, solve := range solves {
			k.RemoveIntentSolve(ctx, solve.SolveTxID)
		}
	}

	// Remove associated auction and its bids
	if k.IntentAuctionExists(ctx, txID) {
		k.RemoveIntentAuction(ctx, txID)
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Delete(k.GetKey(prefixIntent, txID.String()))

	// Remove from active intent index (O(1) with prefix keys)
	store.Delete(k.GetKey(prefixIntentStatusIndex, txID.String()))
}

// GetIntentIterator returns an iterator for all intents
func (k KVStore) GetIntentIterator(ctx cosmos.Context) cosmos.Iterator {
	key := k.GetKey(prefixIntent, "")
	return k.getIterator(ctx, kvTypes.DbPrefix(key))
}

// GetActiveIntents retrieves all active intents using prefix key iteration
func (k KVStore) GetActiveIntents(ctx cosmos.Context) ([]types.Intent, error) {
	key := k.GetKey(prefixIntentStatusIndex, "")
	iter := k.getIterator(ctx, kvTypes.DbPrefix(key))
	defer iter.Close()

	var intents []types.Intent
	for ; iter.Valid(); iter.Next() {
		// Extract the TxID from the key
		keyStr := string(iter.Key())
		parts := splitKey(keyStr)
		if len(parts) == 0 {
			continue
		}
		txID := common.TxID(parts[len(parts)-1])

		intent, err := k.GetIntent(ctx, txID)
		if err != nil {
			ctx.Logger().Error("failed to get intent from active index", "txid", txID, "error", err)
			continue
		}
		intents = append(intents, intent)
	}

	return intents, nil
}

// CleanupStaleActiveIntents removes stale entries from the active intent index.
// This should be called from EndBlock to ensure deterministic cleanup.
func (k KVStore) CleanupStaleActiveIntents(ctx cosmos.Context) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixIntentStatusIndex, "")
	iter := k.getIterator(ctx, kvTypes.DbPrefix(key))
	defer iter.Close()

	var staleKeys [][]byte
	for ; iter.Valid(); iter.Next() {
		keyStr := string(iter.Key())
		parts := splitKey(keyStr)
		if len(parts) == 0 {
			continue
		}
		txID := common.TxID(parts[len(parts)-1])

		// Check existence first
		if !k.IntentExists(ctx, txID) {
			ctx.Logger().Warn("removing non-existent entry from active intent index", "txid", txID)
			staleKeys = append(staleKeys, iter.Key())
			continue
		}

		intent, err := k.GetIntent(ctx, txID)
		if err != nil {
			ctx.Logger().Error("failed to get intent from active index", "txid", txID, "error", err)
			staleKeys = append(staleKeys, iter.Key())
			continue
		}

		if intent.Status != types.IntentStatus_IntentActive {
			ctx.Logger().Warn("removing stale entry from active intent index", "txid", txID, "status", intent.Status.String())
			staleKeys = append(staleKeys, iter.Key())
		}
	}

	for _, staleKey := range staleKeys {
		store.Delete(staleKey)
	}
}

// GetIntentSolveIterator returns an iterator for all intent solves
func (k KVStore) GetIntentSolveIterator(ctx cosmos.Context) cosmos.Iterator {
	key := k.GetKey(prefixIntentSolve, "")
	return k.getIterator(ctx, kvTypes.DbPrefix(key))
}

// -------------------- IntentSolve Methods --------------------

func (k KVStore) setIntentSolve(ctx cosmos.Context, key []byte, record types.IntentSolve) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getIntentSolve(ctx cosmos.Context, key []byte, record *types.IntentSolve) (bool, error) {
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

// SetIntentSolve saves an intent solve bid and updates indices.
// If the solve already exists and indexed fields (IntentTxID, MatchHeight) have changed,
// old index entries are removed before writing new ones.
func (k KVStore) SetIntentSolve(ctx cosmos.Context, solve types.IntentSolve) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// If updating an existing solve, clean up stale index entries
	if k.IntentSolveExists(ctx, solve.SolveTxID) {
		existing, err := k.GetIntentSolve(ctx, solve.SolveTxID)
		if err == nil {
			// Remove old intent index if IntentTxID changed
			if !existing.IntentTxID.Equals(solve.IntentTxID) {
				oldIntentKey := k.GetKey(prefixIntentSolveByIntent, existing.IntentTxID.String(), "/"+solve.SolveTxID.String())
				store.Delete(oldIntentKey)
			}
			// Remove old match height index if MatchHeight changed
			if existing.MatchHeight != solve.MatchHeight {
				oldHeightStr := strconv.FormatInt(existing.MatchHeight, 10)
				oldHeightKey := k.GetKey(prefixIntentSolveByMatchHeight, oldHeightStr, "/"+solve.SolveTxID.String())
				store.Delete(oldHeightKey)
			}
		}
	}

	// Store the solve record
	k.setIntentSolve(ctx, k.GetKey(prefixIntentSolve, solve.SolveTxID.String()), solve)

	// Index by intent ID using prefix keys (O(1) add)
	intentIndexKey := k.GetKey(prefixIntentSolveByIntent, solve.IntentTxID.String(), "/"+solve.SolveTxID.String())
	store.Set(intentIndexKey, []byte{1})

	// Index by match height
	heightStr := strconv.FormatInt(solve.MatchHeight, 10)
	matchHeightIndexKey := k.GetKey(prefixIntentSolveByMatchHeight, heightStr, "/"+solve.SolveTxID.String())
	store.Set(matchHeightIndexKey, []byte{1})
}

// GetIntentSolve retrieves an intent solve bid from the KV store
func (k KVStore) GetIntentSolve(ctx cosmos.Context, solveTxID common.TxID) (types.IntentSolve, error) {
	record := types.IntentSolve{}
	_, err := k.getIntentSolve(ctx, k.GetKey(prefixIntentSolve, solveTxID.String()), &record)
	return record, err
}

// IntentSolveExists checks whether an intent solve exists
func (k KVStore) IntentSolveExists(ctx cosmos.Context, solveTxID common.TxID) bool {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	return store.Has(k.GetKey(prefixIntentSolve, solveTxID.String()))
}

// RemoveIntentSolve removes an intent solve bid and its indices
func (k KVStore) RemoveIntentSolve(ctx cosmos.Context, solveTxID common.TxID) {
	// Check if the solve record exists before proceeding
	if !k.IntentSolveExists(ctx, solveTxID) {
		return
	}

	// Get the solve record to access its indices
	solve, err := k.GetIntentSolve(ctx, solveTxID)
	if err != nil {
		return
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Remove main record
	store.Delete(k.GetKey(prefixIntentSolve, solveTxID.String()))

	// Remove from intent index (O(1) with prefix keys)
	intentIndexKey := k.GetKey(prefixIntentSolveByIntent, solve.IntentTxID.String(), "/"+solveTxID.String())
	store.Delete(intentIndexKey)

	// Remove match height index
	heightStr := strconv.FormatInt(solve.MatchHeight, 10)
	matchHeightIndexKey := k.GetKey(prefixIntentSolveByMatchHeight, heightStr, "/"+solveTxID.String())
	store.Delete(matchHeightIndexKey)
}

// GetIntentSolvesByIntentID retrieves all solve bids for a specific intent using prefix key iteration
func (k KVStore) GetIntentSolvesByIntentID(ctx cosmos.Context, intentTxID common.TxID) ([]types.IntentSolve, error) {
	key := k.GetKey(prefixIntentSolveByIntent, intentTxID.String(), "")
	iter := k.getIterator(ctx, kvTypes.DbPrefix(key))
	defer iter.Close()

	var solves []types.IntentSolve
	for ; iter.Valid(); iter.Next() {
		keyStr := string(iter.Key())
		parts := splitKey(keyStr)
		if len(parts) < 2 {
			continue
		}
		solveTxID := common.TxID(parts[len(parts)-1])

		solve, err := k.GetIntentSolve(ctx, solveTxID)
		if err != nil {
			ctx.Logger().Error("failed to get intent solve", "txid", solveTxID, "error", err)
			continue
		}
		solves = append(solves, solve)
	}

	return solves, nil
}

// GetIntentSolvesByMatchHeight retrieves all solve bids that match at the given height
func (k KVStore) GetIntentSolvesByMatchHeight(ctx cosmos.Context, height int64) ([]types.IntentSolve, error) {
	var solves []types.IntentSolve
	heightStr := strconv.FormatInt(height, 10)
	// Append "/" to prevent prefix collision: without it, height "50" would also
	// match keys for heights 500, 5000, etc. since "intsh//50" is a string prefix
	// of "intsh//500/TXID".
	key := k.GetKey(prefixIntentSolveByMatchHeight, heightStr, "/")
	iter := k.getIterator(ctx, kvTypes.DbPrefix(key))
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		// Extract the solve TxID from the key
		keyStr := string(iter.Key())
		parts := splitKey(keyStr)
		if len(parts) < 2 {
			continue
		}
		solveTxID := common.TxID(parts[len(parts)-1])

		solve, err := k.GetIntentSolve(ctx, solveTxID)
		if err != nil {
			ctx.Logger().Error("failed to get intent solve", "txid", solveTxID, "error", err)
			continue
		}
		solves = append(solves, solve)
	}

	return solves, nil
}

// Helper function to split a key string by "/" separator
func splitKey(key string) []string {
	parts := []string{}
	current := ""
	for _, ch := range key {
		if ch == '/' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
