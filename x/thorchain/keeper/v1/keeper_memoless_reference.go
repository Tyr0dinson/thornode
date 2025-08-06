package keeperv1

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strconv"
)

// GenerateReference creates a deterministic 5-digit reference from memo and timestamp
func GenerateReference(memo string, blockHeight int64) string {
	// Create deterministic hash from memo + block height
	data := fmt.Sprintf("%s_%d", memo, blockHeight)
	hash := sha256.Sum256([]byte(data))

	// Extract 5 digits from hash
	entropy := binary.BigEndian.Uint64(hash[:8])
	reference := (entropy % 90000) + 10000 // Range: 10000-99999

	return fmt.Sprintf("%05d", reference)
}

// GenerateReferenceWithSalt generates reference with additional salt for collision resolution
func GenerateReferenceWithSalt(memo string, blockHeight int64, salt int) string {
	// Add salt to prevent collisions
	data := fmt.Sprintf("%s_%d_%d", memo, blockHeight, salt)
	hash := sha256.Sum256([]byte(data))

	entropy := binary.BigEndian.Uint64(hash[:8])
	reference := (entropy % 90000) + 10000

	return fmt.Sprintf("%05d", reference)
}

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