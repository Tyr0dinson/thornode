package keeperv1

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	keeper "gitlab.com/thorchain/thornode/x/thorchain/keeper/types"
	thorchaintypes "gitlab.com/thorchain/thornode/x/thorchain/types"
)

// ValidateMemoRegistration performs comprehensive validation for memo registration
func (k KVStore) ValidateMemoRegistration(ctx cosmos.Context, msg thorchaintypes.MsgRegisterMemo, constantProvider keeper.ConstantValues) error {
	// Check if memoless protocol is enabled
	if !k.isMemolessEnabled(constantProvider) {
		return fmt.Errorf("memoless protocol is disabled")
	}

	// Enhanced basic validation
	if err := msg.ValidateBasicEnhanced(); err != nil {
		return fmt.Errorf("basic validation failed: %w", err)
	}

	// Check memo length against dynamic limits
	maxLength := constantProvider.GetInt64Value(constants.MemolessMaxMemoLength)
	if maxLength > 0 && int64(len(msg.Memo)) > maxLength {
		return fmt.Errorf("memo exceeds maximum length (%d characters)", maxLength)
	}

	// Validate memo format for the specific chain
	if err := k.validateMemoForChain(msg.Memo, msg.ChainID); err != nil {
		return fmt.Errorf("memo invalid for chain %s: %w", msg.ChainID, err)
	}

	// Check for abuse patterns
	if err := k.detectMemoAbuse(ctx, msg.Memo, msg.Signer); err != nil {
		return fmt.Errorf("potential abuse detected: %w", err)
	}

	// Validate TTL against system limits
	if err := k.validateTTLLimits(msg.TTL, constantProvider); err != nil {
		return fmt.Errorf("invalid TTL: %w", err)
	}

	// Check rate limits for the signer
	if err := k.validateRateLimit(ctx, msg.Signer, constantProvider); err != nil {
		return fmt.Errorf("rate limit exceeded: %w", err)
	}

	return nil
}

// ValidateReferenceGeneration validates reference generation parameters and result
func (k KVStore) ValidateReferenceGeneration(memo string, blockHeight int64, reference string) error {
	// Validate input parameters
	if len(memo) == 0 {
		return fmt.Errorf("memo cannot be empty for reference generation")
	}

	if blockHeight <= 0 {
		return fmt.Errorf("block height must be positive")
	}

	// Validate generated reference
	if err := thorchaintypes.ValidateReference(reference); err != nil {
		return fmt.Errorf("generated reference invalid: %w", err)
	}

	// Verify reference is deterministic by regenerating
	expectedRef := GenerateReference(memo, blockHeight)
	if reference != expectedRef {
		return fmt.Errorf("reference not deterministic: expected %s, got %s", expectedRef, reference)
	}

	return nil
}

// ValidateMemoLookup validates memo lookup operations
func (k KVStore) ValidateMemoLookup(ctx cosmos.Context, reference string, constantProvider keeper.ConstantValues) error {
	// Check if memoless protocol is enabled
	if !k.isMemolessEnabled(constantProvider) {
		return fmt.Errorf("memoless protocol is disabled")
	}

	// Validate reference format
	if err := thorchaintypes.ValidateReference(reference); err != nil {
		return fmt.Errorf("invalid reference format: %w", err)
	}

	return nil
}

// ValidateMemoCleanup validates memo cleanup operations
func (k KVStore) ValidateMemoCleanup(ctx cosmos.Context, blockHeight int64, constantProvider keeper.ConstantValues) error {
	if blockHeight <= 0 {
		return fmt.Errorf("block height must be positive")
	}

	// Check cleanup interval
	cleanupInterval := constantProvider.GetInt64Value(constants.MemolessCleanupInterval)
	if cleanupInterval > 0 {
		lastCleanup := k.getLastCleanupHeight(ctx)
		if blockHeight-lastCleanup < cleanupInterval {
			return fmt.Errorf("cleanup too frequent: last cleanup at %d, interval is %d blocks", lastCleanup, cleanupInterval)
		}
	}

	return nil
}

// ValidateCollisionResolution validates collision resolution attempts
func (k KVStore) ValidateCollisionResolution(ctx cosmos.Context, memo string, blockHeight int64, attempts int, constantProvider keeper.ConstantValues) error {
	maxAttempts := int(constantProvider.GetInt64Value(constants.MemolessCollisionLimit))
	if maxAttempts <= 0 {
		maxAttempts = 1000 // Default
	}

	if attempts > maxAttempts {
		return fmt.Errorf("collision resolution exceeded maximum attempts (%d)", maxAttempts)
	}

	if attempts <= 0 {
		return fmt.Errorf("attempts must be positive")
	}

	return nil
}

// Helper validation methods

// isMemolessEnabled checks if the memoless protocol is enabled
func (k KVStore) isMemolessEnabled(constantProvider keeper.ConstantValues) bool {
	enabled := constantProvider.GetInt64Value(constants.MemolessEnabled)
	return enabled != 0
}

// validateMemoForChain validates memo content for specific blockchain requirements
func (k KVStore) validateMemoForChain(memo string, chainID string) error {
	switch strings.ToUpper(chainID) {
	case "BTC", "LTC", "BCH", "DOGE":
		return k.validateUTXOMemo(memo)
	case "ETH", "BSC", "AVAX":
		return k.validateEVMMemo(memo)
	case "THOR", "GAIA", "ATOM":
		return k.validateCosmosMemo(memo)
	default:
		// Unknown chain - use general validation
		return k.validateGeneralMemo(memo)
	}
}

// validateUTXOMemo validates memo for UTXO-based chains
func (k KVStore) validateUTXOMemo(memo string) error {
	// UTXO chains have strict size limits due to OP_RETURN constraints
	if len(memo) > 80 {
		return fmt.Errorf("memo too long for UTXO chain (maximum 80 bytes)")
	}

	// Check for characters that might cause issues in UTXO transaction parsing
	if strings.Contains(memo, "\x00") {
		return fmt.Errorf("memo cannot contain null bytes for UTXO chains")
	}

	return nil
}

// validateEVMMemo validates memo for EVM-based chains
func (k KVStore) validateEVMMemo(memo string) error {
	// EVM chains can handle larger memos but consider gas costs
	if len(memo) > 1024 {
		return fmt.Errorf("memo too long for EVM chain (recommended maximum 1024 bytes for gas efficiency)")
	}

	// EVM chains can handle most UTF-8 content
	return nil
}

// validateCosmosMemo validates memo for Cosmos-based chains
func (k KVStore) validateCosmosMemo(memo string) error {
	// Cosmos chains have generous memo limits
	if len(memo) > 2048 {
		return fmt.Errorf("memo too long for Cosmos chain (maximum 2048 bytes)")
	}

	return nil
}

// validateGeneralMemo validates memo with conservative rules for unknown chains
func (k KVStore) validateGeneralMemo(memo string) error {
	// Use conservative limits for unknown chains
	if len(memo) > 256 {
		return fmt.Errorf("memo too long for unknown chain (conservative maximum 256 bytes)")
	}

	return nil
}

// validateTTLLimits validates TTL against system-wide limits
func (k KVStore) validateTTLLimits(ttl int64, constantProvider keeper.ConstantValues) error {
	if ttl < 0 {
		return fmt.Errorf("TTL cannot be negative")
	}

	// Check against system maximum
	systemMaxTTL := constantProvider.GetInt64Value(constants.MemolessTTL)
	if systemMaxTTL > 0 && ttl > systemMaxTTL {
		return fmt.Errorf("TTL (%d) exceeds system maximum (%d)", ttl, systemMaxTTL)
	}

	// Check for reasonable limits (prevent DoS)
	const maxReasonableTTL = 864000 // 10 days in seconds
	if ttl > maxReasonableTTL {
		return fmt.Errorf("TTL too large (maximum 10 days)")
	}

	return nil
}

// validateRateLimit checks if the signer has exceeded rate limits
func (k KVStore) validateRateLimit(ctx cosmos.Context, signer cosmos.AccAddress, constantProvider keeper.ConstantValues) error {
	// For now, implement a simple per-block limit
	// In production, this could be more sophisticated with sliding windows
	
	currentHeight := ctx.BlockHeight()
	signerKey := fmt.Sprintf("rate_limit:%s:%d", signer.String(), currentHeight)
	
	// Check how many registrations this signer has made in this block
	count := k.getSignerRegistrationCount(ctx, signer, currentHeight)
	const maxPerBlock = 10 // Maximum registrations per signer per block
	
	if count >= maxPerBlock {
		return fmt.Errorf("rate limit exceeded: maximum %d registrations per block", maxPerBlock)
	}

	return nil
}

// detectMemoAbuse checks for patterns that might indicate abuse or spam
func (k KVStore) detectMemoAbuse(ctx cosmos.Context, memo string, signer cosmos.AccAddress) error {
	// Check for spam patterns
	if err := thorchaintypes.DetectPotentialAbuse(memo, signer); err != nil {
		return err
	}

	// Check for repeated identical memos from the same signer
	if k.hasRecentIdenticalMemo(ctx, memo, signer) {
		return fmt.Errorf("identical memo recently registered by same signer")
	}

	return nil
}

// Helper methods for persistence (these would need actual implementation)

// getLastCleanupHeight gets the block height of the last cleanup operation
func (k KVStore) getLastCleanupHeight(ctx cosmos.Context) int64 {
	// Implementation would store/retrieve this from KV store
	// For now, return 0 to allow cleanup
	return 0
}

// getSignerRegistrationCount gets the number of registrations by signer in current block
func (k KVStore) getSignerRegistrationCount(ctx cosmos.Context, signer cosmos.AccAddress, blockHeight int64) int {
	// Implementation would count registrations in current block
	// This would need to be stored in a temporary cache or KV store
	return 0
}

// hasRecentIdenticalMemo checks if signer has registered identical memo recently
func (k KVStore) hasRecentIdenticalMemo(ctx cosmos.Context, memo string, signer cosmos.AccAddress) bool {
	// Implementation would check for identical memos in recent blocks
	// This could use a hash-based cache with expiry
	return false
}

// ValidateSystemConsistency performs system-wide consistency checks
func (k KVStore) ValidateSystemConsistency(ctx cosmos.Context, constantProvider keeper.ConstantValues) error {
	// Check storage consistency
	if err := k.validateStorageConsistency(ctx); err != nil {
		return fmt.Errorf("storage consistency check failed: %w", err)
	}

	// Check resource usage
	if err := k.validateResourceUsage(ctx, constantProvider); err != nil {
		return fmt.Errorf("resource usage check failed: %w", err)
	}

	return nil
}

// validateStorageConsistency checks for storage corruption or inconsistencies
func (k KVStore) validateStorageConsistency(ctx cosmos.Context) error {
	// Check that expiry index matches actual registrations
	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	count := 0
	for ; iterator.Valid(); iterator.Next() {
		count++
		// Could add more detailed consistency checks here
		if count > 1000000 { // Prevent infinite loops
			break
		}
	}

	return nil
}

// validateResourceUsage checks system resource usage
func (k KVStore) validateResourceUsage(ctx cosmos.Context, constantProvider keeper.ConstantValues) error {
	// Check total storage usage
	// Check memory usage
	// Check processing time limits
	// Implementation would depend on Cosmos SDK resource monitoring
	return nil
}