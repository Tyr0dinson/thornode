package keeperv1

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// getModifyLimitSwapTrackerKey returns the key for tracking modify limit swap by address and block height
func (k KVStore) getModifyLimitSwapTrackerKey(height int64, addr cosmos.AccAddress) []byte {
	return []byte(fmt.Sprintf("%s%d/%s", prefixModifyLimitSwapTracker, height, addr.String()))
}

// HasModifiedLimitSwapInBlock returns true if the address has already modified a limit swap in the current block
func (k KVStore) HasModifiedLimitSwapInBlock(ctx cosmos.Context, addr cosmos.AccAddress) bool {
	key := k.getModifyLimitSwapTrackerKey(ctx.BlockHeight(), addr)
	return k.has(ctx, key)
}

// SetModifiedLimitSwapInBlock marks that an address has modified a limit swap in the current block
func (k KVStore) SetModifiedLimitSwapInBlock(ctx cosmos.Context, addr cosmos.AccAddress) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.getModifyLimitSwapTrackerKey(ctx.BlockHeight(), addr)
	store.Set(key, []byte{1})
}

// CleanupModifyLimitSwapTracking removes old tracking entries from previous blocks
// This is called at the beginning of each block to clean up stale data
func (k KVStore) CleanupModifyLimitSwapTracking(ctx cosmos.Context) {
	// Use an iterator to find and delete old entries
	// We only want to keep entries for the current block
	currentHeight := ctx.BlockHeight()

	// Iterate over all entries with the prefix
	iter := k.getIterator(ctx, prefixModifyLimitSwapTracker)
	defer func() { _ = iter.Close() }()

	keysToDelete := make([][]byte, 0)
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		// Parse the height from the key format: "mlst/{height}/{address}"
		var height int64
		var addrStr string
		// The key format after prefix is: {height}/{address}
		keyWithoutPrefix := string(key)[len(prefixModifyLimitSwapTracker):]
		_, err := fmt.Sscanf(keyWithoutPrefix, "%d/%s", &height, &addrStr)
		if err != nil {
			// Invalid format, delete it
			keyCopy := make([]byte, len(key))
			copy(keyCopy, key)
			keysToDelete = append(keysToDelete, keyCopy)
			continue
		}

		// Delete if from a previous block
		if height < currentHeight {
			keyCopy := make([]byte, len(key))
			copy(keyCopy, key)
			keysToDelete = append(keysToDelete, keyCopy)
		}
	}

	// Delete old entries
	for _, key := range keysToDelete {
		k.del(ctx, key)
	}
}
