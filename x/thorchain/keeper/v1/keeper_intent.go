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

	// Manage active status index - this is a single key storing all active intent TxIDs
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	activeIndexKey := k.GetKey(prefixIntentStatusIndex, "")

	// Load current active intent index
	var activeIndex types.ActiveIntentIndex
	if store.Has(activeIndexKey) {
		bz := store.Get(activeIndexKey)
		if err := k.cdc.Unmarshal(bz, &activeIndex); err != nil {
			ctx.Logger().Error("failed to unmarshal active intent index", "error", err)
			activeIndex = types.ActiveIntentIndex{}
		}
	}

	if intent.Status == types.IntentStatus_IntentActive {
		// Add to active index if not already present
		found := false
		for _, txID := range activeIndex.ActiveIntentTxIDs {
			if txID.Equals(intent.TxID) {
				found = true
				break
			}
		}
		if !found {
			activeIndex.ActiveIntentTxIDs = append(activeIndex.ActiveIntentTxIDs, intent.TxID)
		}
	} else {
		// Remove from active index if present
		newList := make([]common.TxID, 0, len(activeIndex.ActiveIntentTxIDs))
		for _, txID := range activeIndex.ActiveIntentTxIDs {
			if !txID.Equals(intent.TxID) {
				newList = append(newList, txID)
			}
		}
		activeIndex.ActiveIntentTxIDs = newList
	}

	// Save updated index
	buf := k.cdc.MustMarshal(&activeIndex)
	store.Set(activeIndexKey, buf)
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

// RemoveIntent removes an intent from the KV store and updates active status index
func (k KVStore) RemoveIntent(ctx cosmos.Context, txID common.TxID) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Delete(k.GetKey(prefixIntent, txID.String()))

	// Remove from active intent index
	activeIndexKey := k.GetKey(prefixIntentStatusIndex, "")
	if !store.Has(activeIndexKey) {
		return
	}

	// Load current active intent index
	var activeIndex types.ActiveIntentIndex
	bz := store.Get(activeIndexKey)
	if err := k.cdc.Unmarshal(bz, &activeIndex); err != nil {
		ctx.Logger().Error("failed to unmarshal active intent index", "error", err)
		return
	}

	// Remove the TxID from the list
	newList := make([]common.TxID, 0, len(activeIndex.ActiveIntentTxIDs))
	for _, id := range activeIndex.ActiveIntentTxIDs {
		if !id.Equals(txID) {
			newList = append(newList, id)
		}
	}
	activeIndex.ActiveIntentTxIDs = newList

	// Save updated index
	buf := k.cdc.MustMarshal(&activeIndex)
	store.Set(activeIndexKey, buf)
}

// GetIntentIterator returns an iterator for all intents
func (k KVStore) GetIntentIterator(ctx cosmos.Context) cosmos.Iterator {
	key := k.GetKey(prefixIntent, "")
	return k.getIterator(ctx, kvTypes.DbPrefix(key))
}

// GetActiveIntents retrieves all active intents from the active status index
// and verifies each intent is actually active before returning
func (k KVStore) GetActiveIntents(ctx cosmos.Context) ([]types.Intent, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	activeIndexKey := k.GetKey(prefixIntentStatusIndex, "")

	// If the index doesn't exist, return empty list
	if !store.Has(activeIndexKey) {
		return []types.Intent{}, nil
	}

	// Load and unmarshal the active intent index
	var activeIndex types.ActiveIntentIndex
	bz := store.Get(activeIndexKey)
	if err := k.cdc.Unmarshal(bz, &activeIndex); err != nil {
		return nil, fmt.Errorf("failed to unmarshal active intent index: %w", err)
	}

	// Verify each intent is actually active and filter out stale entries
	verifiedActiveIntents := make([]types.Intent, 0, len(activeIndex.ActiveIntentTxIDs))
	verifiedActiveTxIDs := make([]common.TxID, 0, len(activeIndex.ActiveIntentTxIDs))
	staleEntries := false

	for _, txID := range activeIndex.ActiveIntentTxIDs {
		intent, err := k.GetIntent(ctx, txID)
		if err != nil {
			ctx.Logger().Error("failed to get intent from active index", "txid", txID, "error", err)
			staleEntries = true
			continue
		}

		// Only include intents that are truly active
		if intent.Status == types.IntentStatus_IntentActive {
			verifiedActiveIntents = append(verifiedActiveIntents, intent)
			verifiedActiveTxIDs = append(verifiedActiveTxIDs, txID)
		} else {
			// Intent status changed but index wasn't updated - mark as stale
			ctx.Logger().Warn("stale entry in active intent index", "txid", txID, "status", intent.Status.String())
			staleEntries = true
		}
	}

	// Clean up stale entries from the index if any were found
	if staleEntries {
		ctx.Logger().Info("cleaning up stale entries from active intent index", "original_count", len(activeIndex.ActiveIntentTxIDs), "verified_count", len(verifiedActiveTxIDs))
		activeIndex.ActiveIntentTxIDs = verifiedActiveTxIDs
		buf := k.cdc.MustMarshal(&activeIndex)
		store.Set(activeIndexKey, buf)
	}

	return verifiedActiveIntents, nil
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

// SetIntentSolve saves an intent solve bid and updates indices
func (k KVStore) SetIntentSolve(ctx cosmos.Context, solve types.IntentSolve) {
	// Store the solve record
	k.setIntentSolve(ctx, k.GetKey(prefixIntentSolve, solve.SolveTxID.String()), solve)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Index by intent ID
	intentIndexKey := k.GetKey(prefixIntentSolveByIntent, solve.IntentTxID.String(), solve.SolveTxID.String())
	store.Set(intentIndexKey, []byte{1})

	// Index by match height
	heightStr := strconv.FormatInt(solve.MatchHeight, 10)
	matchHeightIndexKey := k.GetKey(prefixIntentSolveByMatchHeight, heightStr, solve.SolveTxID.String())
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
	// Get the solve record first to access its indices
	solve, err := k.GetIntentSolve(ctx, solveTxID)
	if err != nil {
		return
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Remove main record
	store.Delete(k.GetKey(prefixIntentSolve, solveTxID.String()))

	// Remove intent index
	intentIndexKey := k.GetKey(prefixIntentSolveByIntent, solve.IntentTxID.String(), solve.SolveTxID.String())
	store.Delete(intentIndexKey)

	// Remove match height index
	heightStr := strconv.FormatInt(solve.MatchHeight, 10)
	matchHeightIndexKey := k.GetKey(prefixIntentSolveByMatchHeight, heightStr, solve.SolveTxID.String())
	store.Delete(matchHeightIndexKey)
}

// GetIntentSolvesByIntentID retrieves all solve bids for a specific intent
func (k KVStore) GetIntentSolvesByIntentID(ctx cosmos.Context, intentTxID common.TxID) ([]types.IntentSolve, error) {
	var solves []types.IntentSolve
	key := k.GetKey(prefixIntentSolveByIntent, intentTxID.String(), "")
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

// GetIntentSolvesByMatchHeight retrieves all solve bids that match at the given height
func (k KVStore) GetIntentSolvesByMatchHeight(ctx cosmos.Context, height int64) ([]types.IntentSolve, error) {
	var solves []types.IntentSolve
	heightStr := strconv.FormatInt(height, 10)
	key := k.GetKey(prefixIntentSolveByMatchHeight, heightStr, "")
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
