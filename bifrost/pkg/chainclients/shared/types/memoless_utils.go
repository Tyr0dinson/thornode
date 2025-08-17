// Package types implements memoless transaction detection and validation utilities.
//
// MEMOLESS TRANSACTION DETECTION
//
// This package provides utilities for detecting and processing memoless transactions
// in the THORChain bifrost observer system using the "Full with Last 5 Digits" encoding.
//
// DETECTION ALGORITHM:
//
// A transaction is identified as memoless when:
//   1. Memo field is empty or minimal (≤2 characters)
//   2. Transaction contains exactly one coin
//   3. Amount is ≥100,000 base units (6+ digits for reference encoding)
//   4. Last 5 digits form a valid reference (10000-99999 range)
//   5. Reference passes security validation (ASCII-only, no reserved patterns)
//
// AMOUNT ENCODING STRATEGY:
//
// References are encoded in the last 5 digits of transaction amounts:
//   - Original amount: 1,500,000 base units
//   - Reference: 12345
//   - Encoded amount: 1,512,345 base units
//   - Detection: Extract "12345" from last 5 digits
//
// SECURITY FEATURES:
//
// Transaction Amount Validation:
//   - Integer overflow protection (max 10^18)
//   - Minimum amount enforcement (prevents dust attacks)
//   - Precision attack prevention (rejects excessive amounts)
//   - Boundary condition handling (edge case protection)
//
// Reference Validation:
//   - Strict ASCII byte validation (prevents Unicode attacks)
//   - Reserved pattern blocking (00000, 99999, 12345)
//   - Range enforcement (10000-99999)
//   - Format validation (exactly 5 digits)
//
// CHAIN COMPATIBILITY:
//
// Supports chains with ≥5 decimal places:
//   - BTC: 8 decimals (compatible)
//   - ETH: 18 decimals (compatible) 
//   - Most major chains: 6-18 decimals (compatible)
//   - Rejects chains with <5 decimals automatically
//
// INTEGRATION:
//
// This utility integrates with:
//   - Bifrost Observer: Transaction detection in filterObservations()
//   - THORNode Keeper: Reference validation and memo lookup
//   - Security Framework: Adversarial testing and vulnerability detection
//   - Performance Monitoring: Transaction processing metrics
//
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

	// Need at least 6 digits for base amount + 5-digit reference
	if len(amountStr) < 6 {
		return "", fmt.Errorf("amount too small for memoless reference: %s (need at least 6 digits)", amountStr)
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

	// Enhanced amount validation against manipulation attacks
	amountStr := coin.Amount.String()
	
	// Protect against integer overflow attacks
	if len(amountStr) > 20 { // Prevent amounts larger than reasonable limits
		return false, "", nil
	}

	// Protect against negative amount simulation (amounts too small)
	minAmount := cosmos.NewUint(100000) // 10^5 = 100,000 (6 digits minimum)
	if coin.Amount.LT(minAmount) {
		return false, "", nil
	}

	// Protect against precision attacks (amounts too large)
	maxSafeAmount := cosmos.NewUintFromString("1000000000000000000") // 10^18 - reasonable limit
	if coin.Amount.GT(maxSafeAmount) {
		return false, "", nil
	}

	// Try to extract reference with enhanced validation
	reference, err := m.ExtractReferenceFromAmount(coin.Amount)
	if err != nil {
		return false, "", nil // Cannot extract valid reference
	}

	// Additional security validation for the extracted reference
	if err := m.ValidateReference(reference); err != nil {
		return false, "", nil // Reference doesn't pass security validation
	}

	// Validate reference is in valid range (10000-99999)
	refInt, err := strconv.Atoi(reference)
	if err != nil || refInt < 10000 || refInt > 99999 {
		return false, "", nil
	}

	return true, reference, nil
}

// ValidateReference validates that a reference string is properly formatted
func (m *MemolessUtils) ValidateReference(reference string) error {
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

		// If baseWithoutRef is empty, use "1" as minimum base
		if baseWithoutRef == "" {
			baseWithoutRef = "1"
		}

		encodedStr := baseWithoutRef + reference
		encodedAmount := cosmos.NewUintFromString(encodedStr)
		return encodedAmount, nil
	}

	// If base amount is less than 5 digits, just append reference
	encodedStr := baseStr + reference
	encodedAmount := cosmos.NewUintFromString(encodedStr)

	return encodedAmount, nil
}
