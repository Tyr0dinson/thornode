package keeperv1

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// GetCurrentTTL gets the current TTL from Mimir constants with fallback to default
func (k KVStore) GetCurrentTTL(ctx cosmos.Context) int64 {
	// Try to get from Mimir first
	mimirTTL, err := k.GetMimir(ctx, constants.MemolessTTL.String())
	if err == nil && mimirTTL > 0 {
		// Validate against maximum
		maxTTL := k.getMaxTTL(ctx)

		if mimirTTL > maxTTL {
			// Log when TTL is capped for monitoring
			ctx.Logger().Info("TTL capped to maximum",
				"requested", mimirTTL,
				"maximum", maxTTL)
			return maxTTL
		}

		return mimirTTL
	}

	// Fallback to constants
	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	ttl := constantProvider.GetInt64Value(constants.MemolessTTL)

	if ttl <= 0 {
		return int64(14400) // 4 hour default
	}

	// Validate against maximum
	maxTTL := k.getMaxTTL(ctx)

	if ttl > maxTTL {
		return maxTTL
	}

	return ttl
}

// SetTTL sets the TTL value with validation
func (k KVStore) SetTTL(ctx cosmos.Context, ttl int64) error {
	if ttl <= 0 {
		return fmt.Errorf("TTL must be positive, got: %d", ttl)
	}

	// Check minimum TTL limit (10 minutes)
	minTTL := k.getMinTTL()
	if ttl < minTTL {
		return fmt.Errorf("TTL %d too short: %d blocks (minimum %d)", ttl, ttl, minTTL)
	}

	// Check maximum TTL limit
	maxTTL := k.getMaxTTL(ctx)
	if ttl > maxTTL {
		return fmt.Errorf("TTL %d exceeds maximum TTL %d", ttl, maxTTL)
	}

	// Log TTL update for monitoring
	ctx.Logger().Info("TTL configuration updated",
		"old_ttl", k.GetCurrentTTL(ctx),
		"new_ttl", ttl,
		"duration_hours", float64(ttl)/720.0)

	// Update Mimir constant
	k.SetMimir(ctx, constants.MemolessTTL.String(), ttl)
	return nil
}

// ValidateTTLConfiguration validates the current TTL configuration
func (k KVStore) ValidateTTLConfiguration(ctx cosmos.Context) error {
	currentTTL := k.GetCurrentTTL(ctx)

	if currentTTL <= 0 {
		return fmt.Errorf("invalid TTL configuration: %d", currentTTL)
	}

	// Verify TTL propagation
	mimirTTL, err := k.GetMimir(ctx, constants.MemolessTTL.String())
	if err == nil && mimirTTL > 0 {
		// Check if mimir value matches current value (allowing for max TTL capping)
		maxTTL, err := k.GetMimir(ctx, constants.MemolessMaxTTL.String())
		if err != nil || maxTTL <= 0 {
			maxTTL = int64(1008000) // 1 week default maximum
		}

		expectedTTL := mimirTTL
		if mimirTTL > maxTTL {
			expectedTTL = maxTTL
		}

		if expectedTTL != currentTTL {
			return fmt.Errorf("TTL propagation failed: expected=%d, current=%d", expectedTTL, currentTTL)
		}
	}

	return nil
}

// GetCurrentTTLWithValidation gets the current TTL with comprehensive validation
func (k KVStore) GetCurrentTTLWithValidation(ctx cosmos.Context) (int64, error) {
	ttl := k.GetCurrentTTL(ctx)

	// Additional validation checks
	minTTL := k.getMinTTL()
	if ttl < minTTL {
		return 0, fmt.Errorf("TTL too short: %d blocks (minimum %d)", ttl, minTTL)
	}

	// Log TTL configuration for monitoring
	ctx.Logger().Debug("TTL configuration",
		"blocks", ttl,
		"duration", fmt.Sprintf("%.1f hours", float64(ttl)/720.0))

	return ttl, nil
}

// Helper functions

// getMaxTTL gets the maximum TTL with fallback to constants
func (k KVStore) getMaxTTL(ctx cosmos.Context) int64 {
	maxTTL, err := k.GetMimir(ctx, constants.MemolessMaxTTL.String())
	if err != nil || maxTTL <= 0 {
		// Fallback to constants
		constantProvider := constants.GetConstantValues(GetCurrentVersion())
		maxTTL = constantProvider.GetInt64Value(constants.MemolessMaxTTL)
		if maxTTL <= 0 {
			maxTTL = int64(1008000) // 1 week default maximum
		}
	}
	return maxTTL
}

// getMinTTL gets the minimum TTL (10 minutes in blocks)
func (k KVStore) getMinTTL() int64 {
	return int64(600) // 10 minutes in blocks (~5s per block)
}
