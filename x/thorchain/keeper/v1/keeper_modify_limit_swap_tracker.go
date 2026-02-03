package keeperv1

import (
	"encoding/binary"
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// getModifyLimitSwapTrackerKey returns the key for tracking modify limit swap by address
func (k KVStore) getModifyLimitSwapTrackerKey(addr cosmos.AccAddress) []byte {
	return []byte(fmt.Sprintf("%s%s", prefixModifyLimitSwapTracker, addr.String()))
}

// HasModifiedLimitSwapInBlock returns true if the address has already modified a limit swap in the current block
func (k KVStore) HasModifiedLimitSwapInBlock(ctx cosmos.Context, addr cosmos.AccAddress) bool {
	key := k.getModifyLimitSwapTrackerKey(addr)
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	value := store.Get(key)
	if value == nil {
		return false
	}
	// Check if the stored height matches the current block height
	storedHeight := int64(binary.BigEndian.Uint64(value))
	return storedHeight == ctx.BlockHeight()
}

// SetModifiedLimitSwapInBlock marks that an address has modified a limit swap in the current block
func (k KVStore) SetModifiedLimitSwapInBlock(ctx cosmos.Context, addr cosmos.AccAddress) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.getModifyLimitSwapTrackerKey(addr)
	// Store the current block height as the value
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, uint64(ctx.BlockHeight()))
	store.Set(key, value)
}
