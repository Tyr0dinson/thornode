package keeperv1

import (
	"encoding/json"
	"fmt"

	"github.com/cosmos/cosmos-sdk/store/prefix"
	"github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	keeper "gitlab.com/thorchain/thornode/x/thorchain/keeper/types"
	thorchaintypes "gitlab.com/thorchain/thornode/x/thorchain/types"
)

// Key prefixes for memo storage
var (
	MemoRegistrationPrefix = []byte("memo_reg:")
	MemoExpiryPrefix      = []byte("memo_exp:")
)

// SetRegisteredMemo stores a registered memo in the keeper
func (k KVStore) SetRegisteredMemo(ctx cosmos.Context, memo thorchaintypes.RegisteredMemo) error {
	store := ctx.KVStore(k.storeKey)

	// Store by reference
	key := append(MemoRegistrationPrefix, []byte(memo.Reference)...)
	bz, err := json.Marshal(memo)
	if err != nil {
		return fmt.Errorf("failed to marshal memo: %w", err)
	}
	store.Set(key, bz)

	// Store expiry index for cleanup
	expiryKey := append(MemoExpiryPrefix, types.Uint64ToBigEndian(uint64(memo.ExpiresAt))...)
	expiryKey = append(expiryKey, []byte(memo.Reference)...)
	store.Set(expiryKey, []byte(memo.Reference))

	return nil
}

// GetRegisteredMemo retrieves a registered memo by reference
func (k KVStore) GetRegisteredMemo(ctx cosmos.Context, reference string) (thorchaintypes.RegisteredMemo, error) {
	store := ctx.KVStore(k.storeKey)
	key := append(MemoRegistrationPrefix, []byte(reference)...)

	bz := store.Get(key)
	if bz == nil {
		return thorchaintypes.RegisteredMemo{}, fmt.Errorf("memo not found for reference: %s", reference)
	}

	var memo thorchaintypes.RegisteredMemo
	err := json.Unmarshal(bz, &memo)
	if err != nil {
		return thorchaintypes.RegisteredMemo{}, fmt.Errorf("failed to unmarshal memo: %w", err)
	}

	// Check if expired
	if ctx.BlockHeight() > memo.ExpiresAt {
		return thorchaintypes.RegisteredMemo{}, fmt.Errorf("memo expired for reference: %s", reference)
	}

	return memo, nil
}

// HasRegisteredMemo checks if a memo exists for the given reference
func (k KVStore) HasRegisteredMemo(ctx cosmos.Context, reference string) bool {
	_, err := k.GetRegisteredMemo(ctx, reference)
	return err == nil
}

// DeleteRegisteredMemo removes a registered memo
func (k KVStore) DeleteRegisteredMemo(ctx cosmos.Context, reference string) error {
	store := ctx.KVStore(k.storeKey)
	key := append(MemoRegistrationPrefix, []byte(reference)...)

	// Get memo for cleanup
	memo, err := k.GetRegisteredMemo(ctx, reference)
	if err != nil {
		return err // Memo doesn't exist
	}

	// Delete main entry
	store.Delete(key)

	// Delete expiry index
	expiryKey := append(MemoExpiryPrefix, types.Uint64ToBigEndian(uint64(memo.ExpiresAt))...)
	expiryKey = append(expiryKey, []byte(reference)...)
	store.Delete(expiryKey)

	return nil
}

// DeleteExpiredMemos removes all expired memos up to the given block height
func (k KVStore) DeleteExpiredMemos(ctx cosmos.Context, blockHeight int64) error {
	store := ctx.KVStore(k.storeKey)
	expiryStore := prefix.NewStore(store, MemoExpiryPrefix)

	// Iterate through expired memos
	iterator := expiryStore.Iterator(nil, types.Uint64ToBigEndian(uint64(blockHeight+1)))
	defer iterator.Close()

	var toDelete []string
	for ; iterator.Valid(); iterator.Next() {
		reference := string(iterator.Value())
		toDelete = append(toDelete, reference)
	}

	// Delete expired memos
	for _, reference := range toDelete {
		if err := k.DeleteRegisteredMemo(ctx, reference); err != nil {
			ctx.Logger().Error("Failed to delete expired memo", "reference", reference, "error", err)
		}
	}

	if len(toDelete) > 0 {
		ctx.Logger().Info("Cleaned up expired memos", "count", len(toDelete), "block_height", blockHeight)
	}

	return nil
}

// GetRegisteredMemoIterator returns an iterator over all registered memos
func (k KVStore) GetRegisteredMemoIterator(ctx cosmos.Context) types.Iterator {
	store := ctx.KVStore(k.storeKey)
	return types.KVStorePrefixIterator(store, MemoRegistrationPrefix)
}

// QueryRegisteredMemo queries a registered memo by reference with usage tracking
func (k KVStore) QueryRegisteredMemo(ctx cosmos.Context, reference string) (thorchaintypes.RegisteredMemo, error) {
	memo, err := k.GetRegisteredMemo(ctx, reference)
	if err != nil {
		return thorchaintypes.RegisteredMemo{}, err
	}

	// Log memo usage for monitoring
	ctx.Logger().Debug("Memo accessed", "reference", reference, "chain_id", memo.ChainID)

	return memo, nil
}

// ResolveCollision handles reference collisions through sequential generation with salt
func (k KVStore) ResolveCollision(ctx cosmos.Context, memo string, blockHeight int64, constantProvider keeper.ConstantValues) (string, error) {
	maxAttempts := int(constantProvider.GetInt64Value(constants.MemolessCollisionLimit))
	if maxAttempts <= 0 {
		maxAttempts = 1000 // Default fallback
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		candidate := GenerateReferenceWithSalt(memo, blockHeight, attempt)

		if !k.HasRegisteredMemo(ctx, candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("unable to resolve collision after %d attempts", maxAttempts)
}

// QueryActiveMemos returns active memos with pagination support
func (k KVStore) QueryActiveMemos(ctx cosmos.Context, limit, offset int) ([]thorchaintypes.RegisteredMemo, error) {
	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	var memos []thorchaintypes.RegisteredMemo
	count := 0
	skipped := 0

	for ; iterator.Valid() && count < limit; iterator.Next() {
		if skipped < offset {
			skipped++
			continue
		}

		var memo thorchaintypes.RegisteredMemo
		err := json.Unmarshal(iterator.Value(), &memo)
		if err != nil {
			ctx.Logger().Error("Failed to unmarshal memo in iterator", "error", err)
			continue
		}

		// Skip expired memos
		if ctx.BlockHeight() > memo.ExpiresAt {
			continue
		}

		memos = append(memos, memo)
		count++
	}

	return memos, nil
}