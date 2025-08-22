package types

import (
	"fmt"
	"strconv"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// ExtractReferenceFromAmount extracts 5-digit references from transaction amounts
// by taking the last 5 digits. Validates range 10000-99999.
// 
// Examples:
//   100012345 → "12345" 
//   12345 → "12345"
//   1234 → error (too small)
//
// This implements the amount-based reference extraction per task1-3 guide line 82.
func ExtractReferenceFromAmount(amount cosmos.Uint) (string, error) {
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
// by replacing last 5 digits with chosen reference.
//
// Examples:
//   baseAmount=100067890, reference="12345" → 100012345
//   baseAmount=100000000, reference="54321" → 100054321
//
// This implements amount encoding per task1-3 guide line 103.
func EncodeAmountWithReference(baseAmount cosmos.Uint, reference string) (cosmos.Uint, error) {
	// Validate reference format first
	if len(reference) != 5 {
		return cosmos.ZeroUint(), fmt.Errorf("reference must be exactly 5 digits, got %d", len(reference))
	}
	
	// Validate reference is numeric
	refInt, err := strconv.ParseInt(reference, 10, 64)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("reference must be numeric: %v", err)
	}
	
	// Validate reference range
	if refInt < 10000 || refInt > 99999 {
		return cosmos.ZeroUint(), fmt.Errorf("reference out of valid range: %d (must be 10000-99999)", refInt)
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
