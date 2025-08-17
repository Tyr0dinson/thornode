package types

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// Validation constants for memoless protocol
const (
	MinMemoLength    = 1
	MaxMemoLength    = 1024
	MinTTL           = 0
	MaxTTL           = 864000 // 10 days in seconds
	MinChainIDLength = 1
	MaxChainIDLength = 50

	// Reference constants for 5-digit user-friendly codes
	ReferenceLength   = 5     // All references must be exactly 5 digits
	MinReferenceValue = 10000 // Smallest 5-digit number (ensures exactly 5 digits)
	MaxReferenceValue = 99999 // Largest 5-digit number (prevents 6+ digit codes)
	// This gives us 90,000 possible references (10000-99999) for collision resistance
	// while maintaining simple user experience with consistent 5-digit codes
)

// Regular expressions for validation
var (
	// Chain ID should contain only alphanumeric characters, dots, and hyphens
	chainIDRegex = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)

	// Reference should be exactly 5 digits
	referenceRegex = regexp.MustCompile(`^\d{5}$`)

	// Memo should not contain null bytes or other control characters (except newlines/tabs)
	invalidMemoCharsRegex = regexp.MustCompile(`[\x00-\x08\x0B-\x0C\x0E-\x1F\x7F]`)
)

// ValidateBasicEnhanced performs enhanced validation beyond the standard ValidateBasic
func (m *MsgRegisterMemo) ValidateBasicEnhanced() error {
	// First run standard validation
	if err := m.ValidateBasic(); err != nil {
		return err
	}

	// Enhanced memo validation
	if err := ValidateMemoContent(m.Memo); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, fmt.Sprintf("invalid memo content: %v", err))
	}

	// Enhanced chain ID validation
	if err := ValidateChainID(m.ChainId); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, fmt.Sprintf("invalid chain_id: %v", err))
	}

	// ENHANCED: Chain precision support validation (Task 4)
	if err := ValidateChainPrecisionSupport(m.ChainId); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, fmt.Sprintf("chain precision validation failed: %v", err))
	}

	// ENHANCED: Memo size validation for chain-specific limits (Task 4)
	if err := ValidateMemoSizeForChain(m.Memo, m.ChainId); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, fmt.Sprintf("memo size validation failed: %v", err))
	}

	// Enhanced TTL validation
	if err := ValidateTTL(m.Ttl); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, fmt.Sprintf("invalid TTL: %v", err))
	}

	// Enhanced signer validation
	if err := ValidateSigner(m.Signer); err != nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidAddress, fmt.Sprintf("invalid signer: %v", err))
	}

	return nil
}

// ValidateMemoContent validates memo content for security and format requirements
func ValidateMemoContent(memo string) error {
	if len(memo) == 0 {
		return fmt.Errorf("memo cannot be empty")
	}

	if len(memo) < MinMemoLength {
		return fmt.Errorf("memo too short (minimum %d characters)", MinMemoLength)
	}

	if len(memo) > MaxMemoLength {
		return fmt.Errorf("memo too long (maximum %d characters)", MaxMemoLength)
	}

	// Check for valid UTF-8
	if !utf8.ValidString(memo) {
		return fmt.Errorf("memo contains invalid UTF-8 characters")
	}

	// Check for dangerous control characters
	if invalidMemoCharsRegex.MatchString(memo) {
		return fmt.Errorf("memo contains invalid control characters")
	}

	// Check for null bytes specifically (extra security)
	if strings.Contains(memo, "\x00") {
		return fmt.Errorf("memo cannot contain null bytes")
	}

	// Enhanced security: Block injection attack patterns
	dangerousPatterns := []string{
		"';", "')", "'; --", "'; DROP", "'; DELETE", "'; UPDATE",
		"<script", "</script>", "javascript:", "vbscript:",
		"${", "%{", "#{", "${{", "#{", "{{",
		"\\x", "\\u00", "\\r\\n", "\r\n\r\n",
		"jndi:", "ldap://", "rmi://", "dns://",
		"../", "..\\", "/etc/", "\\windows\\",
		"union select", "union all", "or 1=1", "and 1=1",
		"eval(", "exec(", "system(", "shell_exec(",
	}

	lowerMemo := strings.ToLower(memo)
	for _, pattern := range dangerousPatterns {
		if strings.Contains(lowerMemo, pattern) {
			return fmt.Errorf("memo contains potentially dangerous content: %s", pattern)
		}
	}

	// Check for excessive special characters (potential obfuscation attempt)
	specialCharCount := 0
	for _, char := range memo {
		if char < 32 || char > 126 || strings.ContainsRune("'\"<>{}()[];|&$", char) {
			specialCharCount++
		}
	}
	if float64(specialCharCount)/float64(len(memo)) > 0.3 {
		return fmt.Errorf("memo contains excessive special characters (potential obfuscation)")
	}

	// Check for excessively long lines (potential DoS protection)
	lines := strings.Split(memo, "\n")
	for i, line := range lines {
		if len(line) > 512 {
			return fmt.Errorf("memo line %d exceeds maximum length (512 characters)", i+1)
		}
	}

	// Check for excessive repetition (potential DoS protection)
	if err := validateMemoRepetition(memo); err != nil {
		return err
	}

	return nil
}

// validateMemoRepetition checks for excessive character repetition
func validateMemoRepetition(memo string) error {
	if len(memo) < 10 {
		return nil // Skip check for very short memos
	}

	// Check for more than 50 consecutive identical characters
	var lastChar rune
	var count int
	for _, char := range memo {
		if char == lastChar {
			count++
			if count > 50 {
				return fmt.Errorf("memo contains excessive character repetition")
			}
		} else {
			count = 1
			lastChar = char
		}
	}

	return nil
}

// ValidateChainID validates chain identifier format and content
func ValidateChainID(chainID string) error {
	if len(chainID) == 0 {
		return fmt.Errorf("chain_id cannot be empty")
	}

	if len(chainID) < MinChainIDLength {
		return fmt.Errorf("chain_id too short (minimum %d characters)", MinChainIDLength)
	}

	if len(chainID) > MaxChainIDLength {
		return fmt.Errorf("chain_id too long (maximum %d characters)", MaxChainIDLength)
	}

	// Check format
	if !chainIDRegex.MatchString(chainID) {
		return fmt.Errorf("chain_id contains invalid characters (only alphanumeric, dots, and hyphens allowed)")
	}

	// Check for valid THORChain chain identifiers
	validChains := map[string]bool{
		"BTC":  true,
		"ETH":  true,
		"BNB":  true,
		"LTC":  true,
		"BCH":  true,
		"DOGE": true,
		"AVAX": true,
		"ATOM": true,
		"BSC":  true,
		"GAIA": true,
		"THOR": true,
	}

	if !validChains[strings.ToUpper(chainID)] {
		// Allow but warn about unknown chain IDs
		// In production, this might be more restrictive
	}

	return nil
}

// ValidateTTL validates time-to-live value
func ValidateTTL(ttl int64) error {
	if ttl < MinTTL {
		return fmt.Errorf("TTL cannot be negative")
	}

	if ttl > MaxTTL {
		return fmt.Errorf("TTL too large (maximum %d seconds = 10 days)", MaxTTL)
	}

	return nil
}

// ValidateSigner validates signer address format and properties
func ValidateSigner(signer sdk.AccAddress) error {
	if signer.Empty() {
		return fmt.Errorf("signer cannot be empty")
	}

	// Check address length (should be 20 bytes for most Cosmos addresses)
	if len(signer) != 20 {
		return fmt.Errorf("signer address has invalid length (expected 20 bytes, got %d)", len(signer))
	}

	// Additional validation could include:
	// - Checking if address is on a blacklist
	// - Verifying address format matches expected patterns
	// - Rate limiting per address

	return nil
}

// ValidateRegisteredMemo validates a complete RegisteredMemo structure
func (rm RegisteredMemo) Validate() error {
	// Validate reference
	if err := ValidateReference(rm.Reference); err != nil {
		return fmt.Errorf("invalid reference: %w", err)
	}

	// Validate memo content
	if err := ValidateMemoContent(rm.Memo); err != nil {
		return fmt.Errorf("invalid memo: %w", err)
	}

	// Validate chain ID
	if err := ValidateChainID(rm.ChainId); err != nil {
		return fmt.Errorf("invalid chain_id: %w", err)
	}

	// Validate timestamps
	if rm.RegisteredAt <= 0 {
		return fmt.Errorf("registered_at must be positive")
	}

	if rm.ExpiresAt <= 0 {
		return fmt.Errorf("expires_at must be positive")
	}

	if rm.ExpiresAt <= rm.RegisteredAt {
		return fmt.Errorf("expires_at (%d) must be after registered_at (%d)", rm.ExpiresAt, rm.RegisteredAt)
	}

	// Validate status
	validStatuses := map[string]bool{
		"active":  true,
		"expired": true,
		"used":    true,
	}

	if !validStatuses[rm.Status] {
		return fmt.Errorf("invalid status: %s (must be: active, expired, or used)", rm.Status)
	}

	return nil
}

// ValidateReference validates a 5-digit reference format and range
func ValidateReference(reference string) error {
	if len(reference) != ReferenceLength {
		return fmt.Errorf("reference must be exactly %d digits", ReferenceLength)
	}

	if !referenceRegex.MatchString(reference) {
		return fmt.Errorf("reference must contain only digits")
	}

	// Parse and check range
	refValue := 0
	for _, digit := range reference {
		refValue = refValue*10 + int(digit-'0')
	}

	if refValue < MinReferenceValue || refValue > MaxReferenceValue {
		return fmt.Errorf("reference must be in range %d-%d", MinReferenceValue, MaxReferenceValue)
	}

	return nil
}

// ValidateReferenceUniqueness checks if a reference is unique (requires keeper context)
func ValidateReferenceUniqueness(reference string, existingRefs map[string]bool) error {
	if existingRefs[reference] {
		return fmt.Errorf("reference %s already exists", reference)
	}
	return nil
}

// ValidateMemoRegistrationRequest validates the complete registration request
func ValidateMemoRegistrationRequest(memo string, chainID string, ttl int64, signer sdk.AccAddress) error {
	// Create a temp message for validation
	msg := &MsgRegisterMemo{
		Memo:    memo,
		ChainId: chainID,
		Ttl:     ttl,
		Signer:  signer,
	}

	return msg.ValidateBasicEnhanced()
}

// ENHANCED VALIDATION FUNCTIONS (Task 4)

// ValidateMemoRegistrationWithAmount validates registration with amount-based reference encoding
func ValidateMemoRegistrationWithAmount(memo string, chainID string, amount string, reference string, ttl int64, signer sdk.AccAddress) error {
	// First validate basic registration parameters
	if err := ValidateMemoRegistrationRequest(memo, chainID, ttl, signer); err != nil {
		return err
	}

	// ENHANCED: Validate amount can safely encode reference without precision loss
	if err := ValidateAmountForReference(amount, reference, chainID); err != nil {
		return fmt.Errorf("amount-reference validation failed: %w", err)
	}

	// ENHANCED: Check integer overflow protection
	if err := CheckIntegerOverflowProtection(amount, chainID); err != nil {
		return fmt.Errorf("overflow protection failed: %w", err)
	}

	// ENHANCED: Validate dust threshold
	if err := ValidateDustThreshold(amount, chainID); err != nil {
		return fmt.Errorf("dust threshold validation failed: %w", err)
	}

	return nil
}

// ValidateMemoRegistrationWithAsset validates registration with asset-specific precision requirements
func ValidateMemoRegistrationWithAsset(memo string, chainID string, amount string, asset string, reference string, ttl int64, signer sdk.AccAddress) error {
	// First validate with amount
	if err := ValidateMemoRegistrationWithAmount(memo, chainID, amount, reference, ttl, signer); err != nil {
		return err
	}

	// ENHANCED: Validate asset-specific precision
	if err := ValidateAssetPrecision(amount, asset, chainID); err != nil {
		return fmt.Errorf("asset precision validation failed: %w", err)
	}

	return nil
}

// ValidateMemoRegistrationBatch validates multiple registrations for dust attack prevention
func ValidateMemoRegistrationBatch(registrations []MemoRegistrationRequest, timeWindow int64) error {
	if len(registrations) == 0 {
		return nil
	}

	// Group by chain for chain-specific validation
	chainAmounts := make(map[string][]string)
	for _, reg := range registrations {
		if reg.Amount != "" {
			chainAmounts[reg.ChainID] = append(chainAmounts[reg.ChainID], reg.Amount)
		}
	}

	// Check each chain for dust attack patterns
	for chainID, amounts := range chainAmounts {
		if err := PreventDustAttack(amounts, chainID, timeWindow); err != nil {
			return fmt.Errorf("dust attack prevention for chain %s: %w", chainID, err)
		}
	}

	return nil
}

// MemoRegistrationRequest represents a registration request for batch validation
type MemoRegistrationRequest struct {
	Memo      string
	ChainID   string
	Amount    string
	Asset     string
	Reference string
	TTL       int64
	Signer    sdk.AccAddress
}

// ValidateMemoLookupRequest validates parameters for memo lookup
func ValidateMemoLookupRequest(reference string) error {
	return ValidateReference(reference)
}

// ValidateMemoCleanupParameters validates parameters for memo cleanup operations
func ValidateMemoCleanupParameters(blockHeight int64, maxItems int) error {
	if blockHeight <= 0 {
		return fmt.Errorf("block_height must be positive")
	}

	if maxItems <= 0 {
		return fmt.Errorf("max_items must be positive")
	}

	if maxItems > 10000 {
		return fmt.Errorf("max_items too large (maximum 10000)")
	}

	return nil
}

// Security validation helpers

// IsValidMemoForChain checks if memo is valid for the specific chain
func IsValidMemoForChain(memo string, chainID string) error {
	// Chain-specific memo validation rules
	switch strings.ToUpper(chainID) {
	case "BTC", "LTC", "BCH", "DOGE":
		// UTXO chains have memo size limits
		if len(memo) > 80 {
			return fmt.Errorf("memo too long for UTXO chain (maximum 80 bytes)")
		}
	case "ETH", "BSC", "AVAX":
		// EVM chains support longer memos but check for gas efficiency
		if len(memo) > 512 {
			return fmt.Errorf("memo too long for EVM chain (recommended maximum 512 bytes)")
		}
	}

	return nil
}

// DetectPotentialAbuse checks for patterns that might indicate abuse
func DetectPotentialAbuse(memo string, signer sdk.AccAddress) error {
	// Check for common spam patterns
	lowerMemo := strings.ToLower(memo)

	spamKeywords := []string{
		"click here", "free money", "guaranteed profit", "lottery winner",
		"nigerian prince", "inheritance", "tax refund", "suspended account",
	}

	for _, keyword := range spamKeywords {
		if strings.Contains(lowerMemo, keyword) {
			return fmt.Errorf("memo contains potential spam content")
		}
	}

	// Check for excessive URL patterns
	urlCount := strings.Count(lowerMemo, "http://") + strings.Count(lowerMemo, "https://")
	if urlCount > 3 {
		return fmt.Errorf("memo contains excessive URLs")
	}

	return nil
}
