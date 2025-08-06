package types

import (
	"fmt"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// MemolessUtils provides utility functions for memoless transaction processing
type MemolessUtils struct{}

// NewMemolessUtils creates a new MemolessUtils instance
func NewMemolessUtils() *MemolessUtils {
	return &MemolessUtils{}
}

// ExtractReferenceFromAmount extracts a 5-digit reference code from a transaction amount
// The reference is encoded as the last 5 digits of the amount
func (m *MemolessUtils) ExtractReferenceFromAmount(amount cosmos.Uint) (string, error) {
	if amount.IsZero() {
		return "", fmt.Errorf("amount cannot be zero for memoless transactions")
	}

	amountStr := amount.String()
	
	// Need at least 5 digits for a reference
	if len(amountStr) < 5 {
		return "", fmt.Errorf("amount too small for memoless reference: %s", amountStr)
	}

	// Extract last 5 digits
	reference := amountStr[len(amountStr)-5:]
	
	// Validate reference format (5 digits)
	if len(reference) != 5 {
		return "", fmt.Errorf("invalid reference length: %d", len(reference))
	}

	// Ensure all characters are digits
	if _, err := strconv.ParseUint(reference, 10, 32); err != nil {
		return "", fmt.Errorf("reference contains non-numeric characters: %s", reference)
	}

	return reference, nil
}

// IsMemolessTransaction determines if a transaction is a memoless transaction
// A transaction is considered memoless if:
// 1. The memo is empty or contains only whitespace/minimal data
// 2. The amount contains a valid 5-digit reference code
// 3. The amount is above minimum threshold for reference encoding
func (m *MemolessUtils) IsMemolessTransaction(memo string, coins common.Coins) (bool, string, error) {
	// Check if memo is empty or minimal (1-2 characters)
	cleanMemo := strings.TrimSpace(memo)
	if len(cleanMemo) > 2 {
		return false, "", nil // Not memoless - has significant memo content
	}

	// Must have exactly one coin for memoless transactions
	if len(coins) != 1 {
		return false, "", nil
	}

	coin := coins[0]
	
	// Check minimum amount threshold (need at least 6 digits for base+reference)
	minAmount := cosmos.NewUint(100000) // 10^5 = 100,000 (6 digits minimum)
	if coin.Amount.LT(minAmount) {
		return false, "", nil
	}

	// Try to extract reference
	reference, err := m.ExtractReferenceFromAmount(coin.Amount)
	if err != nil {
		return false, "", nil // Cannot extract valid reference
	}

	return true, reference, nil
}

// ValidateReference validates that a reference string is properly formatted
func (m *MemolessUtils) ValidateReference(reference string) error {
	if len(reference) != 5 {
		return fmt.Errorf("reference must be exactly 5 digits, got %d", len(reference))
	}

	if _, err := strconv.ParseUint(reference, 10, 32); err != nil {
		return fmt.Errorf("reference must be numeric: %w", err)
	}

	// Additional validation - reference cannot be all zeros
	if reference == "00000" {
		return fmt.Errorf("reference cannot be all zeros")
	}

	return nil
}

// GetMinimumMemolessAmount returns the minimum amount required for memoless transactions
// This ensures there's enough room for both the base amount and 5-digit reference
func (m *MemolessUtils) GetMinimumMemolessAmount() cosmos.Uint {
	return cosmos.NewUint(100000) // 100,000 base units
}

// EncodeAmountWithReference encodes a reference into a transaction amount
// This is used when creating outbound transactions with memoless references
func (m *MemolessUtils) EncodeAmountWithReference(baseAmount cosmos.Uint, reference string) (cosmos.Uint, error) {
	if err := m.ValidateReference(reference); err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("invalid reference: %w", err)
	}

	// Convert base amount to string and append reference
	baseStr := baseAmount.String()
	
	// Ensure base amount has at least 1 digit
	if baseAmount.IsZero() {
		return cosmos.ZeroUint(), fmt.Errorf("base amount cannot be zero")
	}

	// Remove last 5 digits from base if they exist, then append reference
	if len(baseStr) >= 5 {
		// Remove last 5 digits and append new reference
		baseWithoutRef := baseStr[:len(baseStr)-5]
		encodedStr := baseWithoutRef + reference
		encodedAmount := cosmos.NewUintFromString(encodedStr)
		return encodedAmount, nil
	}

	// If base amount is less than 5 digits, just append reference
	encodedStr := baseStr + reference
	encodedAmount := cosmos.NewUintFromString(encodedStr)
	
	return encodedAmount, nil
}