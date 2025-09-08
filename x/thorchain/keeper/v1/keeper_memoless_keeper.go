package keeperv1

import (
	"encoding/binary"
	"fmt"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// MemoRegistrationResult contains the result of a memo registration
type MemoRegistrationResult struct {
	Reference    string `json:"reference"`
	Memo         string `json:"memo"`
	ChainId      string `json:"chain_id"`
	RegisteredAt int64  `json:"registered_at"`
	ExpiresAt    int64  `json:"expires_at"`
	Status       string `json:"status"`
}

// MemolessStats tracks memoless protocol statistics
type MemolessStats struct {
	TotalRegistrations  int64 `json:"total_registrations"`
	ActiveRegistrations int64 `json:"active_registrations"`
	TotalCollisions     int64 `json:"total_collisions"`
	LastCleanupHeight   int64 `json:"last_cleanup_height"`
	LastExpiredCount    int64 `json:"last_expired_count"`
}

// ProcessMemoRegistration processes a memo registration request with optimized validation flow
func (k KVStore) ProcessMemoRegistration(ctx cosmos.Context, msg thorchaintypes.MsgRegisterMemo, constantProvider constants.ConstantValues) (*MemoRegistrationResult, error) {
	// Fast-fail: Check if memoless is enabled first (most common failure case)
	if constantProvider.GetInt64Value(constants.MemolessEnabled) == 0 {
		return nil, fmt.Errorf("memoless protocol disabled")
	}

	// Pre-calculate values used in multiple places
	currentHeight := ctx.BlockHeight()
    // v1 policy: use global TTL via Mimir (capped), ignore client-provided TTL
    ttl := k.GetCurrentTTL(ctx)
    expirationHeight := currentHeight + ttl

	// Comprehensive validation with early returns for performance
	if err := k.ValidateMemoRegistrationComplete(ctx, msg, constantProvider); err != nil {
		return nil, fmt.Errorf("memo registration validation failed: %w", err)
	}

	// Use user-provided reference (already validated in Layer 3)
	reference := msg.Reference

	// Create and store registered memo atomically
	registeredMemo := thorchaintypes.RegisteredMemo{
		Reference:    reference,
		Memo:         msg.Memo,
		ChainId:      msg.ChainId,
		RegisteredAt: currentHeight,
		ExpiresAt:    expirationHeight,
		Status:       "active",
		Signer:       msg.Signer,
	}

	if err := k.SetRegisteredMemo(ctx, registeredMemo); err != nil {
		ctx.Logger().Error("Failed to store registered memo", "reference", reference, "error", err)
		return nil, fmt.Errorf("failed to store registered memo: %w", err)
	}

	// Log successful registration for monitoring
	ctx.Logger().Info("Memo registration successful",
		"reference", reference,
		"chain", msg.ChainId,
		"expires_at", expirationHeight)

	return &MemoRegistrationResult{
		Reference:    reference,
		Memo:         msg.Memo,
		ChainId:      msg.ChainId,
		RegisteredAt: currentHeight,
		ExpiresAt:    expirationHeight,
		Status:       "active",
	}, nil
}

// Helper function for min value (Go doesn't have built-in min for int)
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// LookupMemoByReference looks up a registered memo by its reference
func (k KVStore) LookupMemoByReference(ctx cosmos.Context, reference string) (*thorchaintypes.RegisteredMemo, error) {
	memo, err := k.GetRegisteredMemo(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("memo not found for reference %s: %w", reference, err)
	}

	// Check if expired
	if memo.IsExpired(ctx.BlockHeight()) {
		return nil, fmt.Errorf("memo reference %s has expired", reference)
	}

	return &memo, nil
}

// ValidateMemoRegistrationComplete performs comprehensive validation of memo registration
func (k KVStore) ValidateMemoRegistrationComplete(ctx cosmos.Context, msg thorchaintypes.MsgRegisterMemo, constantProvider constants.ConstantValues) error {
	// Layer 1: Basic validation
	if err := k.validateBasicMemoRegistration(ctx, msg, constantProvider); err != nil {
		return fmt.Errorf("basic validation failed: %w", err)
	}

	// Layer 2: Chain-specific validation
	if err := k.validateChainSpecificRequirements(ctx, msg); err != nil {
		return fmt.Errorf("chain validation failed: %w", err)
	}

	// Layer 3: Reference validation (user-provided)
	if err := k.ValidateUserReference(ctx, msg.Reference); err != nil {
		return fmt.Errorf("reference validation failed: %w", err)
	}

	// Layer 4: Business logic validation
	if err := k.validateBusinessLogic(ctx, msg, constantProvider); err != nil {
		return fmt.Errorf("business logic validation failed: %w", err)
	}

	// Layer 5: Security validation
	if err := k.validateSecurityRequirements(ctx, msg, constantProvider); err != nil {
		return fmt.Errorf("security validation failed: %w", err)
	}

	// Ensure enhanced (guardrail) validations are applied consistently (kill-switch,
	// per-signer active limit, and other Layer 4 checks implemented in the enhanced path).
	// Build a minimal validation context and delegate to ValidateMemoRegistrationEnhanced.
	chainConfig, err := k.GetChainConfig(ctx, msg.ChainId)
	if err != nil {
		return fmt.Errorf("failed to get chain configuration: %w", err)
	}
	validationCtx := EnhancedValidationContext{
		Version:          GetCurrentVersion(),
		BlockHeight:      ctx.BlockHeight(),
		ConstantProvider: constantProvider,
		ChainConfig:      chainConfig,
	}
	if err := k.ValidateMemoRegistrationEnhanced(ctx, msg, validationCtx); err != nil {
		return err
	}

	return nil
}

// CleanupExpiredMemos cleans up expired memo registrations with batch processing
func (k KVStore) CleanupExpiredMemos(ctx cosmos.Context, currentHeight int64) (int64, error) {
	cleanedCount := int64(0)
	batchSize := int64(100) // Process in batches to avoid blocking
	processedCount := int64(0)

	// Get iterator for all registered memos
	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	// Collect expired memos in batches
	expiredMemos := make([]thorchaintypes.RegisteredMemo, 0, batchSize)

	for ; iterator.Valid(); iterator.Next() {
		var memo thorchaintypes.RegisteredMemo
		if err := k.cdc.Unmarshal(iterator.Value(), &memo); err != nil {
			ctx.Logger().Error("Failed to unmarshal memo during cleanup", "error", err)
			continue
		}

		processedCount++

		// Check if expired
		if memo.IsExpired(currentHeight) {
			memo.Status = "expired" // Mark as expired
			expiredMemos = append(expiredMemos, memo)

			// Process batch when full
			if len(expiredMemos) >= int(batchSize) {
				if err := k.processBatchExpiredMemos(ctx, expiredMemos); err != nil {
					ctx.Logger().Error("Failed to process batch of expired memos", "error", err)
					// Continue processing remaining memos
				}
				cleanedCount += int64(len(expiredMemos))
				expiredMemos = expiredMemos[:0] // Reset slice but keep capacity
			}
		}
	}

	// Process remaining expired memos
	if len(expiredMemos) > 0 {
		if err := k.processBatchExpiredMemos(ctx, expiredMemos); err != nil {
			ctx.Logger().Error("Failed to process final batch of expired memos", "error", err)
		} else {
			cleanedCount += int64(len(expiredMemos))
		}
	}

	ctx.Logger().Info("Cleanup completed",
		"expired_count", cleanedCount,
		"processed_count", processedCount,
		"height", currentHeight)

	return cleanedCount, nil
}

// processBatchExpiredMemos processes a batch of expired memos for performance
func (k KVStore) processBatchExpiredMemos(ctx cosmos.Context, expiredMemos []thorchaintypes.RegisteredMemo) error {
	for _, memo := range expiredMemos {
		if err := k.DeleteRegisteredMemo(ctx, memo.Reference); err != nil {
			ctx.Logger().Error("Failed to delete expired memo",
				"reference", memo.Reference,
				"error", err)
			return err
		}
	}
	return nil
}

// Helper methods for validation layers

func (k KVStore) validateBasicMemoRegistration(ctx cosmos.Context, msg thorchaintypes.MsgRegisterMemo, constantProvider constants.ConstantValues) error {
	// Check if memo is empty
	if msg.Memo == "" {
		return fmt.Errorf("memo cannot be empty")
	}

	// Check memo length
	maxLength := constantProvider.GetInt64Value(constants.MemolessMaxMemoLength)
	if int64(len(msg.Memo)) > maxLength {
		return fmt.Errorf("memo length %d exceeds maximum %d", len(msg.Memo), maxLength)
	}

    // TTL (v1): advisory and ignored by protocol; only non-negative is enforced elsewhere

	// Check signer is valid
	if msg.Signer.Empty() {
		return fmt.Errorf("signer cannot be empty")
	}

	return nil
}

func (k KVStore) validateChainSpecificRequirements(ctx cosmos.Context, msg thorchaintypes.MsgRegisterMemo) error {
	if msg.ChainId == "" {
		return fmt.Errorf("chain_id cannot be empty")
	}

	// Validate chain supports memoless
	if err := k.ValidateChainForMemoless(ctx, msg.ChainId); err != nil {
		return err
	}

	return nil
}

func (k KVStore) validateBusinessLogic(ctx cosmos.Context, msg thorchaintypes.MsgRegisterMemo, constantProvider constants.ConstantValues) error {
	// Check rate limiting (simplified implementation)
	// In production, this would check recent registrations by signer

	// Check destination format if provided

	return nil
}

func (k KVStore) validateSecurityRequirements(ctx cosmos.Context, msg thorchaintypes.MsgRegisterMemo, constantProvider constants.ConstantValues) error {
	// Check for suspicious memo patterns
	if len(msg.Memo) > 100 && msg.Memo[:4] != "SWAP" && msg.Memo[:4] != "ADD:" && msg.Memo[:4] != "WITHDRAW" {
		return fmt.Errorf("suspicious memo pattern detected")
	}

	// Additional security checks can be added here
	return nil
}

// Handler integration methods - these are thin wrappers for the existing methods

// GetMemolessStats retrieves memoless protocol statistics
func (k KVStore) GetMemolessStats(ctx cosmos.Context) MemolessStats {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Read individual stats using binary encoding (following existing pattern)
	stats := MemolessStats{}

	// Get total registrations
	if bz := store.Get(k.GetKey(prefixMemoStats, "registrations")); bz != nil {
		stats.TotalRegistrations = int64(binary.BigEndian.Uint64(bz))
	}

	// Get total collisions
	if bz := store.Get(k.GetKey(prefixMemoStats, "collisions")); bz != nil {
		stats.TotalCollisions = int64(binary.BigEndian.Uint64(bz))
	}

	// Get last cleanup height
	if bz := store.Get(k.GetKey(prefixMemoStats, "last_cleanup_height")); bz != nil {
		stats.LastCleanupHeight = int64(binary.BigEndian.Uint64(bz))
	}

	// Get last expired count
	if bz := store.Get(k.GetKey(prefixMemoStats, "last_expired_count")); bz != nil {
		stats.LastExpiredCount = int64(binary.BigEndian.Uint64(bz))
	}

	// Calculate active registrations (this is a derived value, not stored separately)
	// For now, just return total - we can improve this later with actual counting
	stats.ActiveRegistrations = stats.TotalRegistrations

	return stats
}

// SetMemolessStats stores memoless protocol statistics
func (k KVStore) SetMemolessStats(ctx cosmos.Context, stats MemolessStats) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Store individual stats using binary encoding (following existing pattern)
	buf := make([]byte, 8)

	// Store total registrations
	binary.BigEndian.PutUint64(buf, uint64(stats.TotalRegistrations))
	store.Set(k.GetKey(prefixMemoStats, "registrations"), buf)

	// Store total collisions
	binary.BigEndian.PutUint64(buf, uint64(stats.TotalCollisions))
	store.Set(k.GetKey(prefixMemoStats, "collisions"), buf)

	// Store last cleanup height
	binary.BigEndian.PutUint64(buf, uint64(stats.LastCleanupHeight))
	store.Set(k.GetKey(prefixMemoStats, "last_cleanup_height"), buf)

	// Store last expired count
	binary.BigEndian.PutUint64(buf, uint64(stats.LastExpiredCount))
	store.Set(k.GetKey(prefixMemoStats, "last_expired_count"), buf)
}
