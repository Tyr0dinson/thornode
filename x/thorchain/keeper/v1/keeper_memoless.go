package keeperv1

import (
	"encoding/binary"
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Key prefixes for memo storage
const (
	prefixMemoRegistration types.DbPrefix = "memo_registration/"
	prefixMemoExpiry       types.DbPrefix = "memo_expiry/"
	prefixMemoStats        types.DbPrefix = "memo_stats/"
)

// SetRegisteredMemo stores a registered memo in the keeper
func (k KVStore) SetRegisteredMemo(ctx cosmos.Context, memo thorchaintypes.RegisteredMemo) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoRegistration, memo.Reference)

	buf := k.cdc.MustMarshal(&memo)
	if buf == nil {
		return dbError(ctx, "SetRegisteredMemo", fmt.Errorf("could not marshal registered memo"))
	}
	store.Set(key, buf)

	// Store expiry index for cleanup
	expiryKey := k.GetKey(prefixMemoExpiry, fmt.Sprintf("%d", memo.ExpiresAt), memo.Reference)
	store.Set(expiryKey, []byte(memo.Reference))

	return nil
}

// GetRegisteredMemo retrieves a registered memo by reference
func (k KVStore) GetRegisteredMemo(ctx cosmos.Context, reference string) (thorchaintypes.RegisteredMemo, error) {
	memo := thorchaintypes.RegisteredMemo{}
	key := k.GetKey(prefixMemoRegistration, reference)
	ok, err := k.getRegisteredMemo(ctx, key, &memo)
	if err != nil {
		return memo, err
	}
	if !ok {
		return memo, fmt.Errorf("registered memo not found: %s", reference)
	}
	return memo, nil
}

// getRegisteredMemo is a helper method for retrieving memo data
func (k KVStore) getRegisteredMemo(ctx cosmos.Context, key []byte, memo *thorchaintypes.RegisteredMemo) (bool, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	if !store.Has(key) {
		return false, nil
	}
	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, memo); err != nil {
		return false, dbError(ctx, "getRegisteredMemo", err)
	}
	return true, nil
}

// HasRegisteredMemo checks if a memo exists for the given reference
func (k KVStore) HasRegisteredMemo(ctx cosmos.Context, reference string) bool {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoRegistration, reference)
	return store.Has(key)
}

// DeleteRegisteredMemo removes a registered memo from storage
func (k KVStore) DeleteRegisteredMemo(ctx cosmos.Context, reference string) error {
	// First get the memo to get expiry time
	memo, err := k.GetRegisteredMemo(ctx, reference)
	if err != nil {
		return err // Memo doesn't exist
	}

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Delete main record
	key := k.GetKey(prefixMemoRegistration, reference)
	store.Delete(key)

	// Delete expiry index
	expiryKey := k.GetKey(prefixMemoExpiry, fmt.Sprintf("%d", memo.ExpiresAt), reference)
	store.Delete(expiryKey)

	return nil
}

// DeleteExpiredMemos removes all memos that have expired before the given block height
func (k KVStore) DeleteExpiredMemos(ctx cosmos.Context, blockHeight int64) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	expiredCount := int64(0)
	for ; iterator.Valid(); iterator.Next() {
		var memo thorchaintypes.RegisteredMemo
		if err := k.cdc.Unmarshal(iterator.Value(), &memo); err != nil {
			ctx.Logger().Error("Failed to unmarshal memo during cleanup", "error", err)
			continue
		}

		// Check if expired (using block timestamp approximation)
		if memo.ExpiresAt < blockHeight {
			// Delete expired memo
			store.Delete(iterator.Key())

			// Delete expiry index
			expiryKey := k.GetKey(prefixMemoExpiry, fmt.Sprintf("%d", memo.ExpiresAt), memo.Reference)
			store.Delete(expiryKey)

			expiredCount++
		}
	}

	if expiredCount > 0 {
		ctx.Logger().Info("Cleaned up expired memos", "count", expiredCount, "block_height", blockHeight)
	}

	return k.UpdateLastCleanup(ctx, blockHeight, expiredCount)
}

// GetRegisteredMemoIterator returns an iterator for all registered memos
func (k KVStore) GetRegisteredMemoIterator(ctx cosmos.Context) cosmos.Iterator {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoRegistration, "")
	return cosmos.KVStorePrefixIterator(store, key)
}

// QueryRegisteredMemo queries a memo (alias for GetRegisteredMemo for compatibility)
func (k KVStore) QueryRegisteredMemo(ctx cosmos.Context, reference string) (thorchaintypes.RegisteredMemo, error) {
	return k.GetRegisteredMemo(ctx, reference)
}

// QueryActiveMemos returns active memos with pagination
func (k KVStore) QueryActiveMemos(ctx cosmos.Context, limit, offset int) ([]thorchaintypes.RegisteredMemo, error) {
	memos := make([]thorchaintypes.RegisteredMemo, 0)
	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	currentOffset := 0
	added := 0

	for ; iterator.Valid() && (limit == 0 || added < limit); iterator.Next() {
		// Skip items for offset
		if currentOffset < offset {
			currentOffset++
			continue
		}

		var memo thorchaintypes.RegisteredMemo
		if err := k.cdc.Unmarshal(iterator.Value(), &memo); err != nil {
			continue // Skip invalid entries
		}

		// Only include active (non-expired) memos
		currentTime := ctx.BlockTime().Unix()
		if memo.ExpiresAt > currentTime {
			memos = append(memos, memo)
			added++
		}
		currentOffset++
	}

	return memos, nil
}

// ValidateMemoRegistration validates a memo registration message
func (k KVStore) ValidateMemoRegistration(ctx cosmos.Context, msg thorchaintypes.MsgRegisterMemo, constantProvider constants.ConstantValues) error {
	// Get configuration values
	enabled := constantProvider.GetInt64Value(constants.MemolessEnabled)
	if enabled == 0 {
		return fmt.Errorf("memoless protocol is disabled")
	}

	maxLength := constantProvider.GetInt64Value(constants.MemolessMaxMemoLength)
	if int64(len(msg.Memo)) > maxLength {
		return fmt.Errorf("memo length %d exceeds maximum %d", len(msg.Memo), maxLength)
	}

	// Validate chain support and amount requirements
	if err := k.ValidateChainForMemoless(ctx, msg.ChainId); err != nil {
		return fmt.Errorf("chain validation failed: %w", err)
	}

	// Get chain configuration for amount validation
	chainConfig, err := k.GetChainConfig(ctx, msg.ChainId)
	if err != nil {
		return fmt.Errorf("failed to get chain config for %s: %w", msg.ChainId, err)
	}

	// Parse and validate amount
	amount := cosmos.NewUintFromString(msg.Amount)
	if amount.IsZero() && msg.Amount != "0" {
		return fmt.Errorf("invalid amount format: %s", msg.Amount)
	}

	// Check minimum amount requirement
	if amount.LT(chainConfig.MinAmount) {
		return fmt.Errorf("amount %s below minimum %s for chain %s", msg.Amount, chainConfig.MinAmount.String(), msg.ChainId)
	}

	// Check maximum amount requirement
	if amount.GT(chainConfig.MaxAmount) {
		return fmt.Errorf("amount %s exceeds maximum %s for chain %s", msg.Amount, chainConfig.MaxAmount.String(), msg.ChainId)
	}

	// Basic validation is handled by the message ValidateBasic method
	return nil
}

// ResolveCollision resolves reference collisions by generating a new reference
func (k KVStore) ResolveCollision(ctx cosmos.Context, memo string, blockHeight int64, constantProvider constants.ConstantValues) (string, error) {
	collisionLimit := constantProvider.GetInt64Value(constants.MemolessCollisionLimit)

	for attempt := int64(0); attempt < collisionLimit; attempt++ {
		// Generate reference with collision attempt offset
		reference := GenerateReference(memo, blockHeight+attempt)

		// Check if this reference is available
		if !k.HasRegisteredMemo(ctx, reference) {
			if attempt > 0 {
				ctx.Logger().Info("Resolved memo collision", "reference", reference, "attempts", attempt+1)
				k.IncrementCollisions(ctx) // Track collision metrics
			}
			return reference, nil
		}
	}

	return "", fmt.Errorf("failed to resolve collision after %d attempts", collisionLimit)
}

// GetActiveMemoCount returns the count of active (non-expired) memos
func (k KVStore) GetActiveMemoCount(ctx cosmos.Context) int64 {
	count := int64(0)
	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	currentTime := ctx.BlockTime().Unix()
	for ; iterator.Valid(); iterator.Next() {
		var memo thorchaintypes.RegisteredMemo
		if err := k.cdc.Unmarshal(iterator.Value(), &memo); err != nil {
			continue
		}

		if memo.ExpiresAt > currentTime {
			count++
		}
	}

	return count
}

// Statistics and monitoring methods

// IncrementRegistrations increments the registration counter
func (k KVStore) IncrementRegistrations(ctx cosmos.Context) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoStats, "registrations")

	// Get current count
	bz := store.Get(key)
	count := int64(0)
	if bz != nil {
		count = int64(binary.BigEndian.Uint64(bz))
	}

	// Increment and store
	count++
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(count))
	store.Set(key, buf)

	return nil
}

// IncrementCollisions increments the collision counter
func (k KVStore) IncrementCollisions(ctx cosmos.Context) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoStats, "collisions")

	// Get current count
	bz := store.Get(key)
	count := int64(0)
	if bz != nil {
		count = int64(binary.BigEndian.Uint64(bz))
	}

	// Increment and store
	count++
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(count))
	store.Set(key, buf)

	return nil
}

// UpdateLastCleanup updates the last cleanup timestamp and expired count
func (k KVStore) UpdateLastCleanup(ctx cosmos.Context, height int64, expiredCount int64) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Store last cleanup height
	heightKey := k.GetKey(prefixMemoStats, "last_cleanup_height")
	heightBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(heightBuf, uint64(height))
	store.Set(heightKey, heightBuf)

	// Store expired count
	countKey := k.GetKey(prefixMemoStats, "last_expired_count")
	countBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(countBuf, uint64(expiredCount))
	store.Set(countKey, countBuf)

	return nil
}
