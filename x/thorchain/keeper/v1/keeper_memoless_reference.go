package keeperv1

import (
	"fmt"
	"strconv"
)

// NOTE: This file now contains incremental counter reference utilities
// Old hash-based GenerateReference and GenerateReferenceWithSalt functions removed
// per architecture decision to use incremental counter approach

// ValidateReference checks if a reference is in valid format and range
func ValidateReference(reference string) error {
	// Strict byte length check (not character count to prevent Unicode attacks)
	if len([]byte(reference)) != 5 {
		return fmt.Errorf("reference must be exactly 5 ASCII bytes")
	}

	// Strict ASCII digit validation (prevents Unicode digit attacks)
	for i, b := range []byte(reference) {
		if b < '0' || b > '9' {
			return fmt.Errorf("reference contains invalid character at position %d: must be ASCII digits 0-9 only", i)
		}
	}

	// Parse as integer (additional validation)
	refInt, err := strconv.Atoi(reference)
	if err != nil {
		return fmt.Errorf("invalid reference format: %w", err)
	}

	// Strict range enforcement
	if refInt < 10000 || refInt > 99999 {
		return fmt.Errorf("reference must be in range 10000-99999, got %d", refInt)
	}

	// Additional security: reject common attack patterns
	if reference == "00000" || reference == "99999" || reference == "12345" {
		return fmt.Errorf("reference %s is reserved and cannot be used", reference)
	}

	return nil
}
