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
	if len(reference) != 5 {
		return fmt.Errorf("reference must be exactly 5 digits")
	}

	for _, char := range reference {
		if char < '0' || char > '9' {
			return fmt.Errorf("reference must contain only digits")
		}
	}

	// Check range
	refInt, err := strconv.Atoi(reference)
	if err != nil {
		return fmt.Errorf("invalid reference format: %w", err)
	}

	if refInt < 10000 || refInt > 99999 {
		return fmt.Errorf("reference must be in range 10000-99999")
	}

	return nil
}
