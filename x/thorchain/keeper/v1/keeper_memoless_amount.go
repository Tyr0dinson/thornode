package keeperv1

import (
	"fmt"
	"strconv"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ValidateUserReference validates user-provided reference format and availability
// This implements task1-3 guide line 93: validate user references against Genesis State
func (k KVStore) ValidateUserReference(ctx cosmos.Context, reference string) error {
	// Format validation (handled by types.ValidateUserReference)
	if err := types.ValidateUserReference(reference); err != nil {
		return fmt.Errorf("reference format validation failed: %w", err)
	}

	// Check availability in Genesis State
	if k.HasActiveRegisteredMemo(ctx, reference) {
		return fmt.Errorf("reference %s is already in use", reference)
	}

	return nil
}


// ExtractReferenceFromAmount extracts 5-digit references from transaction amounts
// This implements task1-3 guide line 82: stateless reference extraction
func (k KVStore) ExtractReferenceFromAmount(amount cosmos.Uint) (string, error) {
	amountStr := amount.String()

	// Check minimum length (5 digits required)
	if len(amountStr) < 5 {
		return "", fmt.Errorf("amount too small for reference extraction: %s (need at least 5 digits)", amountStr)
	}

	// Extract last 5 digits
	reference := amountStr[len(amountStr)-5:]

	// Validate range (10000-99999)
	refInt, err := strconv.ParseInt(reference, 10, 64)
	if err != nil {
		return "", fmt.Errorf("failed to parse reference as number: %v", err)
	}

	if refInt < 10000 || refInt > 99999 {
		return "", fmt.Errorf("reference out of valid range: %d (must be 10000-99999)", refInt)
	}

	return reference, nil
}

// EncodeAmountWithReference embeds reference into base transaction amount
// This implements task1-3 guide line 103: amount encoding for user-controlled references
func (k KVStore) EncodeAmountWithReference(baseAmount cosmos.Uint, reference string) (cosmos.Uint, error) {
	// Validate reference format first
	if err := types.ValidateUserReference(reference); err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("invalid reference: %w", err)
	}

	baseAmountStr := baseAmount.String()

	// Check base amount has at least 5 digits
	if len(baseAmountStr) < 5 {
		return cosmos.ZeroUint(), fmt.Errorf("base amount too small for reference encoding: %s (need at least 5 digits)", baseAmountStr)
	}

	// Remove last 5 digits and append reference
	prefix := baseAmountStr[:len(baseAmountStr)-5]
	encodedAmountStr := prefix + reference

	// Parse back to cosmos.Uint
	encodedAmount, err := cosmos.ParseUint(encodedAmountStr)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("failed to encode amount: %v", err)
	}

	return encodedAmount, nil
}

// HasActiveRegisteredMemo checks if a reference is currently in use
// Used for collision detection in user reference validation
func (k KVStore) HasActiveRegisteredMemo(ctx cosmos.Context, reference string) bool {
	key := k.GetKey(prefixMemoRegistration, reference)
	return k.has(ctx, key)
}
