package keeperv1

import (
	"fmt"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// CleanupResult represents the result of a batch cleanup operation
type CleanupResult struct {
	RemovedCount    int64
	ProcessedBlocks int64
	Duration        time.Duration
	ErrorCount      int64
}

// CleanupExpiredMemosWithMetrics performs batch cleanup with comprehensive metrics
func (k KVStore) CleanupExpiredMemosWithMetrics(ctx cosmos.Context, batchSize int) (CleanupResult, error) {
	start := time.Now()
	result := CleanupResult{}

	currentHeight := ctx.BlockHeight()

	// Simple direct approach - iterate through all registered memos and delete expired ones
	removedCount, err := k.cleanupExpiredMemosSimple(ctx, currentHeight, batchSize)
	if err != nil {
		ctx.Logger().Error("cleanup failed", "error", err)
		result.ErrorCount++
	} else {
		result.RemovedCount = removedCount
	}

	// Update last cleanup height
	k.setLastCleanupHeight(ctx, currentHeight)

	result.Duration = time.Since(start)

	// Emit metrics for monitoring
	k.recordCleanupMetrics(ctx, result)

	ctx.Logger().Debug("Batch cleanup completed",
		"removed_count", result.RemovedCount,
		"duration_ms", result.Duration.Milliseconds(),
		"error_count", result.ErrorCount)

	return result, nil
}

// BatchCleanupExpiredMemos is the enhanced version that matches test expectations
func (k KVStore) BatchCleanupExpiredMemos(ctx cosmos.Context, batchSize int) (CleanupResult, error) {
	return k.CleanupExpiredMemosWithMetrics(ctx, batchSize)
}

// ShouldRunCleanup determines if cleanup should run based on interval
func (k KVStore) ShouldRunCleanup(ctx cosmos.Context) (bool, error) {
	currentHeight := ctx.BlockHeight()
	lastCleanupHeight := k.getLastCleanupHeight(ctx)

	// Get cleanup interval from constants (default: 100 blocks)
	cleanupInterval := k.getCleanupInterval()

	// If no previous cleanup (lastCleanupHeight == 0), should always run
	shouldRun := lastCleanupHeight == 0 || currentHeight-lastCleanupHeight >= cleanupInterval

	ctx.Logger().Debug("Cleanup interval check",
		"current_height", currentHeight,
		"last_cleanup_height", lastCleanupHeight,
		"interval", cleanupInterval,
		"should_run", shouldRun)

	return shouldRun, nil
}

// Helper functions

// cleanupExpiredMemosSimple performs simple cleanup by iterating through all memos
func (k KVStore) cleanupExpiredMemosSimple(ctx cosmos.Context, currentHeight int64, batchSize int) (int64, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	removedCount := int64(0)
	processed := 0

	// Iterate through all registered memos
	prefix := k.GetKey(prefixMemoRegistration, "")
	iterator := store.Iterator(prefix, nil)
	defer iterator.Close()

	// Collect expired references to delete
	var expiredRefs []string

	for ; iterator.Valid(); iterator.Next() {
		var memo thorchaintypes.RegisteredMemo
		if err := k.cdc.Unmarshal(iterator.Value(), &memo); err != nil {
			ctx.Logger().Error("Failed to unmarshal memo during cleanup", "error", err)
			continue
		}

		// Check if expired
		if memo.ExpiresAt <= currentHeight {
			expiredRefs = append(expiredRefs, memo.Reference)

			// Stop if we've reached batch size
			if len(expiredRefs) >= batchSize {
				break
			}
		}

		processed++
	}

	// Delete expired memos
	for _, ref := range expiredRefs {
		if err := k.DeleteRegisteredMemo(ctx, ref); err != nil {
			ctx.Logger().Error("Failed to delete expired memo",
				"reference", ref,
				"error", err)
		} else {
			removedCount++
		}
	}

	ctx.Logger().Debug("Simple cleanup completed",
		"processed", processed,
		"expired_found", len(expiredRefs),
		"removed", removedCount)

	return removedCount, nil
}

// getExpiredBlockHeights returns block heights that have expired memos to clean
func (k KVStore) getExpiredBlockHeights(ctx cosmos.Context, currentHeight int64) []int64 {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	var expiredHeights []int64
	seenHeights := make(map[int64]bool)

	// Iterate through all expiry entries to find expired ones
	expiryPrefix := k.GetKey(prefixMemoExpiry, "")
	iterator := store.Iterator(expiryPrefix, nil)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		// Parse height from expiry key: memo_expiry/{height}/{reference}
		key := string(iterator.Key())
		if height := k.parseHeightFromExpiryKey(key); height > 0 && height <= currentHeight {
			if !seenHeights[height] {
				expiredHeights = append(expiredHeights, height)
				seenHeights[height] = true
			}
		}
	}

	return expiredHeights
}

// cleanupMemosAtHeight removes expired memos at a specific block height
func (k KVStore) cleanupMemosAtHeight(ctx cosmos.Context, blockHeight int64, batchSize int) (int64, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Get expiry prefix for this block height
	expiredPrefix := k.getExpiryPrefix(blockHeight)
	iterator := store.Iterator(expiredPrefix, nil)
	defer iterator.Close()

	removedCount := int64(0)
	processed := 0

	for ; iterator.Valid() && processed < batchSize; iterator.Next() {
		// Extract reference from expiry key
		key := iterator.Key()
		reference := k.extractReferenceFromExpiryKey(key)

		if reference == "" {
			continue
		}

		// Remove memo registration
		err := k.DeleteRegisteredMemo(ctx, reference)
		if err != nil {
			ctx.Logger().Error("failed to delete expired memo",
				"reference", reference,
				"block_height", blockHeight,
				"error", err)
			continue
		}

		removedCount++
		processed++
	}

	return removedCount, nil
}

// getCleanupInterval returns the cleanup interval from configuration
func (k KVStore) getCleanupInterval() int64 {
	// Default cleanup interval: 100 blocks (~8 minutes)
	return int64(100)
}

// getLastCleanupHeight retrieves the last cleanup height
func (k KVStore) getLastCleanupHeight(ctx cosmos.Context) int64 {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte("last_cleanup_height")

	bz := store.Get(key)
	if bz == nil {
		return 0 // Never cleaned up before
	}

	return k.bytesToInt64(bz)
}

// setLastCleanupHeight stores the last cleanup height
func (k KVStore) setLastCleanupHeight(ctx cosmos.Context, height int64) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := []byte("last_cleanup_height")
	value := k.int64ToBytes(height)

	store.Set(key, value)
}

// getExpiryPrefix returns the key prefix for expired memos at a block height
func (k KVStore) getExpiryPrefix(blockHeight int64) []byte {
	return k.GetKey(prefixMemoExpiry, fmt.Sprintf("%d", blockHeight))
}

// parseHeightFromExpiryKey extracts block height from expiry key format
func (k KVStore) parseHeightFromExpiryKey(key string) int64 {
	// Expected format: "memo_expiry/{height}/{reference}"
	prefix := string(prefixMemoExpiry)
	if !strings.HasPrefix(key, prefix) {
		return 0
	}

	// Remove prefix to get "{height}/{reference}"
	remainder := key[len(prefix):]

	// Find the first slash to separate height from reference
	slashPos := strings.Index(remainder, "/")
	if slashPos == -1 {
		return 0
	}

	// Parse height
	heightStr := remainder[:slashPos]
	var height int64
	if n, err := fmt.Sscanf(heightStr, "%d", &height); n != 1 || err != nil {
		return 0
	}

	return height
}

// extractReferenceFromExpiryKey extracts the reference from an expiry index key
func (k KVStore) extractReferenceFromExpiryKey(key []byte) string {
	// Expected format: "memo_expiry/{height}/{reference}"
	keyStr := string(key)
	prefix := string(prefixMemoExpiry)

	if !strings.HasPrefix(keyStr, prefix) {
		return ""
	}

	// Remove prefix to get "{height}/{reference}"
	remainder := keyStr[len(prefix):]

	// Find the first slash to get past height
	slashPos := strings.Index(remainder, "/")
	if slashPos == -1 || slashPos == len(remainder)-1 {
		return ""
	}

	// Return the reference part after "{height}/"
	return remainder[slashPos+1:]
}

// recordCleanupMetrics emits telemetry metrics for cleanup operations
func (k KVStore) recordCleanupMetrics(ctx cosmos.Context, result CleanupResult) {
	// In production, this would emit metrics to telemetry system
	// For now, we'll log the metrics
	ctx.Logger().Info("Cleanup metrics emitted",
		"removed_count", result.RemovedCount,
		"processed_blocks", result.ProcessedBlocks,
		"duration_ms", result.Duration.Milliseconds(),
		"error_count", result.ErrorCount)
}

// Helper functions for byte conversion
func (k KVStore) int64ToBytes(val int64) []byte {
	return []byte(fmt.Sprintf("%d", val))
}

func (k KVStore) bytesToInt64(bz []byte) int64 {
	var val int64
	fmt.Sscanf(string(bz), "%d", &val)
	return val
}
