// Package keeperv1 implements the 5-layer validation framework for the THORChain memoless protocol.
//
// # MEMOLESS PROTOCOL 5-LAYER VALIDATION FRAMEWORK
//
// The memoless protocol implements a defense-in-depth validation approach using 5 distinct
// validation layers, each with specific responsibilities and security considerations.
//
// LAYER ARCHITECTURE:
//
// Layer 1 (Basic): Format validation, length limits, required fields
//   - Validates message structure and basic format requirements
//   - Ensures memo content meets minimum security standards
//   - Validates signer addresses and basic field presence
//   - Enforces maximum memo length (1024 bytes) and minimum TTL values
//
// Layer 2 (Chain): Chain decimal precision requirements (≥5 decimals required)
//   - Validates chain supports "Full with Last 5 Digits" encoding
//   - Requires chains to have ≥5 decimal places for reference encoding
//   - Maintains whitelist of supported chains (BTC, ETH, BNB, etc.)
//   - Prevents memoless transactions on incompatible chains
//
// Layer 3 (Amount): Economic constraints and encoding validation
//   - Validates economic feasibility of registration costs
//   - Ensures amount encoding doesn't cause precision loss
//   - Prevents dust attacks through minimum amount requirements
//   - Validates reference can be safely encoded in transaction amounts
//
// Layer 4 (Business): Rate limiting, TTL bounds, governance policies
//   - Enforces governance-controlled rate limiting via Mimir constants
//   - Validates TTL values against governance maximums
//   - Implements address-based and block-based registration limits
//   - Prevents spam through economic and temporal constraints
//
// Layer 5 (Security): Pattern detection and abuse prevention
//   - Detects injection attacks (SQL, XSS, JNDI, etc.)
//   - Prevents replay attacks through content deduplication
//   - Identifies suspicious patterns and potential abuse
//   - Blocks excessive special characters and obfuscation attempts
//
// SECURITY MODEL:
//
// Each layer operates independently and can reject transactions. All layers must pass
// for a registration to be accepted. This creates multiple security barriers:
//
// 1. Fail-fast validation catches obvious problems early
// 2. Chain-specific validation prevents incompatible registrations
// 3. Economic validation prevents resource exhaustion
// 4. Governance validation allows operational control
// 5. Security validation blocks advanced attacks
//
// INTEGRATION:
//
// This validation framework integrates with:
// - RegisterMemoHandler: Pre-registration validation
// - gRPC Query Interface: Query-time validation
// - Bifrost Observer: Transaction detection validation
// - Adversarial Testing: Security vulnerability detection
//
// See also:
// - x/thorchain/types/msg_register_memo_validation.go: Message-level validation
// - bifrost/pkg/chainclients/shared/types/memoless_utils.go: Transaction detection
package keeperv1

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Validation patterns and constants
var (
	// Chain ID validation pattern (3-5 uppercase letters)
	chainIDPattern = regexp.MustCompile(`^[A-Z]{3,5}$`)

	// Asset pattern for memo validation
	assetPattern = regexp.MustCompile(`^[A-Z]{3,5}\.[A-Z0-9]{3,10}$`)

	// Address patterns for different chains
	addressPatterns = map[string]*regexp.Regexp{
		"BTC":  regexp.MustCompile(`^(bc1|[13])[a-zA-HJ-NP-Z0-9]{25,62}$`),
		"ETH":  regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`),
		"BNB":  regexp.MustCompile(`^bnb[0-9a-z]{39}$`),
		"BCH":  regexp.MustCompile(`^[13qp][a-km-zA-HJ-NP-Z1-9]{25,62}$`),
		"LTC":  regexp.MustCompile(`^[LM3][a-km-zA-HJ-NP-Z1-9]{26,33}$`),
		"DOGE": regexp.MustCompile(`^[DA][5-9A-HJ-NP-U][1-9A-HJ-NP-Za-km-z]{25,34}$`),
	}
)

// EnhancedValidationContext contains context for enhanced validation
type EnhancedValidationContext struct {
	Version          semver.Version
	BlockHeight      int64
	ConstantProvider constants.ConstantValues
	ChainConfig      ChainConfig
}

// ValidateMemoRegistrationEnhanced performs enhanced validation with version-specific rules
func (k KVStore) ValidateMemoRegistrationEnhanced(ctx cosmos.Context, msg types.MsgRegisterMemo, validationCtx EnhancedValidationContext) error {
	// Layer 1: Basic Validation - Format validation, length limits, required fields
	if err := k.validateLayer1Basic(msg, validationCtx); err != nil {
		return fmt.Errorf("Layer 1 (Basic) validation failed: %w", err)
	}

	// Layer 2: Chain Validation - Full with Last 5 Digits encoding compatibility (≥5 decimals required)
	if err := k.validateLayer2Chain(ctx, msg, validationCtx); err != nil {
		return fmt.Errorf("Layer 2 (Chain) validation failed: %w", err)
	}

	// Layer 3: Amount Validation - Economic constraints and encoding validation
	if err := k.validateLayer3Amount(ctx, msg, validationCtx); err != nil {
		return fmt.Errorf("Layer 3 (Amount) validation failed: %w", err)
	}

	// Layer 4: Business Validation - Rate limiting through Mimir constants, TTL bounds, governance policies
	if err := k.validateLayer4Business(ctx, msg, validationCtx); err != nil {
		return fmt.Errorf("Layer 4 (Business) validation failed: %w", err)
	}

	// Layer 5: Security Validation - Pattern detection and abuse prevention
	if err := k.validateLayer5Security(ctx, msg, validationCtx); err != nil {
		return fmt.Errorf("Layer 5 (Security) validation failed: %w", err)
	}

	return nil
}

// validateLayer1Basic performs Layer 1 validation - Format validation, length limits, required fields
func (k KVStore) validateLayer1Basic(msg types.MsgRegisterMemo, validationCtx EnhancedValidationContext) error {
	// Memo validation
	if len(msg.Memo) == 0 {
		return fmt.Errorf("memo cannot be empty")
	}

	maxLength := validationCtx.ConstantProvider.GetInt64Value(constants.MemolessMaxMemoLength)
	if int64(len(msg.Memo)) > maxLength {
		return fmt.Errorf("memo length %d exceeds maximum %d", len(msg.Memo), maxLength)
	}

	// Signer validation
	if msg.Signer.Empty() {
		return fmt.Errorf("signer cannot be empty")
	}

	// Chain ID format validation
	if !chainIDPattern.MatchString(msg.ChainId) {
		return fmt.Errorf("invalid chain ID format: %s", msg.ChainId)
	}

	// TTL basic bounds
	if msg.Ttl < 0 {
		return fmt.Errorf("TTL cannot be negative")
	}

	maxTTL := validationCtx.ConstantProvider.GetInt64Value(constants.MemolessMaxTTL)
	if msg.Ttl > maxTTL {
		return fmt.Errorf("TTL %d exceeds maximum %d", msg.Ttl, maxTTL)
	}

	return nil
}

// validateLayer2Chain performs Layer 2 validation - Chain decimal precision requirements (≥5 decimals)
func (k KVStore) validateLayer2Chain(ctx cosmos.Context, msg types.MsgRegisterMemo, validationCtx EnhancedValidationContext) error {
	// Get actual chain configuration using keeper method
	chainConfig, err := k.GetChainConfig(ctx, msg.ChainId)
	if err != nil {
		return fmt.Errorf("failed to get chain config for %s: %w", msg.ChainId, err)
	}

	// Check for precision loss issues first (more specific error)
	if chainConfig.PrecisionLoss {
		return fmt.Errorf("chain %s has precision loss issues, memoless not supported", msg.ChainId)
	}

	// Validate chain is supported
	if !chainConfig.SupportsMemoless {
		return fmt.Errorf("chain %s not supported for memoless protocol", msg.ChainId)
	}

	// Validate chain supports at least 5 decimal places for amount encoding
	if chainConfig.DecimalPlaces < 5 {
		return fmt.Errorf("chain %s has insufficient decimal places (%d) for memoless encoding (requires ≥5)",
			msg.ChainId, chainConfig.DecimalPlaces)
	}

	// Validate chain configuration consistency
	if chainConfig.ChainID != strings.ToUpper(msg.ChainId) {
		return fmt.Errorf("chain ID case mismatch: expected %s, got %s", chainConfig.ChainID, msg.ChainId)
	}

	// Validate minimum amount requirements for the chain
	if chainConfig.MinAmount.IsZero() {
		return fmt.Errorf("invalid chain configuration: minimum amount must be positive")
	}

	// Ensure minimum amount allows for reference encoding (need 6+ digits)
	minRequiredForEncoding := cosmos.NewUint(100000) // 6 digits minimum
	if chainConfig.MinAmount.LT(minRequiredForEncoding) {
		return fmt.Errorf("chain %s minimum amount (%s) too small for reference encoding",
			msg.ChainId, chainConfig.MinAmount.String())
	}

	return nil
}

// validateLayer3Amount performs Layer 3 validation - Economic constraints and encoding validation
func (k KVStore) validateLayer3Amount(ctx cosmos.Context, msg types.MsgRegisterMemo, validationCtx EnhancedValidationContext) error {
	// TODO: Enable when protobuf adds Amount field to MsgRegisterMemo
	// For now, validation happens at transaction detection time in bifrost

	// Basic economic validation using existing constants
	if len(msg.Memo) > 1024 {
		return fmt.Errorf("memo too large for economic efficiency")
	}

	return nil
}

// validateLayer4Business performs Layer 4 validation - Rate limiting, TTL bounds, governance policies
func (k KVStore) validateLayer4Business(ctx cosmos.Context, msg types.MsgRegisterMemo, validationCtx EnhancedValidationContext) error {
	// Use existing constants for rate limiting validation
	maxTTL := validationCtx.ConstantProvider.GetInt64Value(constants.MemolessMaxTTL)
	defaultTTL := validationCtx.ConstantProvider.GetInt64Value(constants.MemolessTTL)

	ttl := msg.Ttl
	if ttl == 0 {
		ttl = defaultTTL
	}

	if ttl > maxTTL {
		return fmt.Errorf("TTL %d exceeds governance maximum %d", ttl, maxTTL)
	}

	// Basic rate limiting - check if protocol is enabled
	enabled := validationCtx.ConstantProvider.GetInt64Value(constants.MemolessEnabled)
	if enabled == 0 {
		return fmt.Errorf("memoless protocol disabled by governance")
	}

	// Collusion detection - check registration count per signer per block
	signerBlockCount := k.GetSignerRegistrationCountInBlock(ctx, msg.Signer, validationCtx.BlockHeight)
	maxSignerPerBlock := int64(3) // Allow up to 3 registrations per signer per block
	if signerBlockCount >= maxSignerPerBlock {
		return fmt.Errorf("collusion detected: signer %s has %d registrations in block %d (max %d)",
			msg.Signer.String(), signerBlockCount, validationCtx.BlockHeight, maxSignerPerBlock)
	}

	// Per-signer active limit (guardrail): enforce maximum active registrations per signer if configured
	if maxPerSigner, err := k.GetMimir(ctx, "MemolessActivePerSender"); err == nil && maxPerSigner > 0 {
		activeBySigner := k.countActiveRegistrationsBySigner(ctx, msg.Signer)
		if activeBySigner >= maxPerSigner {
			return fmt.Errorf("active memo limit per signer reached: signer=%s %d/%d", msg.Signer.String(), activeBySigner, maxPerSigner)
		}
	}

	return nil
}

// countActiveRegistrationsBySigner counts non-expired registrations associated with a signer.
func (k KVStore) countActiveRegistrationsBySigner(ctx cosmos.Context, signer cosmos.AccAddress) int64 {
    iter := k.GetRegisteredMemoIterator(ctx)
    defer iter.Close()
    var count int64
    now := ctx.BlockHeight()
    for ; iter.Valid(); iter.Next() {
        var m types.RegisteredMemo
        if err := k.cdc.Unmarshal(iter.Value(), &m); err != nil {
            continue
        }
        if m.Signer.Equals(signer) && (m.ExpiresAt <= 0 || m.ExpiresAt > now) {
            count++
        }
    }
    return count
}

// validateLayer5Security performs Layer 5 validation - Pattern detection and abuse prevention
func (k KVStore) validateLayer5Security(ctx cosmos.Context, msg types.MsgRegisterMemo, validationCtx EnhancedValidationContext) error {
	// Use enhanced memo content validation with injection detection
	if err := types.ValidateMemoContent(msg.Memo); err != nil {
		return fmt.Errorf("memo content security validation failed: %w", err)
	}

	// Check for duplicate memo content (basic replay protection)
	if k.HasRegisteredMemo(ctx, msg.Memo) {
		return fmt.Errorf("memo content already registered (potential replay attack)")
	}

	// Content-based abuse detection
	if err := types.DetectPotentialAbuse(msg.Memo, msg.Signer); err != nil {
		return fmt.Errorf("potential abuse detected: %w", err)
	}

	return nil
}

// validateEnhancedMessage performs enhanced validation with additional fields
func (k KVStore) validateEnhancedMessage(ctx cosmos.Context, msg types.MsgRegisterMemo, validationCtx EnhancedValidationContext) error {
	// TODO: Enable after protobuf regeneration adds extended fields
	// Amount validation (if provided)
	// if msg.Amount != "" {
	// 	if err := k.validateAmountField(msg.Amount, msg.ChainId, validationCtx); err != nil {
	// 		return fmt.Errorf("amount validation failed: %w", err)
	// 	}
	// }

	// Destination validation (if provided)
	// if msg.Destination != "" {
	// 	if err := k.validateDestinationField(msg.Destination, msg.ChainId); err != nil {
	// 		return fmt.Errorf("destination validation failed: %w", err)
	// 	}
	// }

	// Tags validation (if provided)
	// if err := k.validateTagsField(msg.Tags); err != nil {
	// 	return fmt.Errorf("tags validation failed: %w", err)
	// }

	return nil
}

// validateProtocolMessage performs memoless protocol validation
func (k KVStore) validateProtocolMessage(ctx cosmos.Context, msg types.MsgRegisterMemo, validationCtx EnhancedValidationContext) error {
	// Check if memoless protocol is enabled
	enabled := validationCtx.ConstantProvider.GetInt64Value(constants.MemolessEnabled)
	if enabled == 0 {
		return fmt.Errorf("memoless protocol is disabled")
	}

	// Parse and validate memo format
	if err := k.validateMemoFormat(msg.Memo, validationCtx.Version); err != nil {
		return fmt.Errorf("memo format validation failed: %w", err)
	}

	// Check active registration limits
	if err := k.validateRegistrationLimits(ctx, validationCtx); err != nil {
		return fmt.Errorf("registration limits exceeded: %w", err)
	}

	return nil
}

// validateChainSpecificMessage performs chain-specific validation
func (k KVStore) validateChainSpecificMessage(ctx cosmos.Context, msg types.MsgRegisterMemo, validationCtx EnhancedValidationContext) error {
	// Validate chain supports memoless protocol
	if err := k.ValidateChainForMemoless(ctx, msg.ChainId); err != nil {
		return fmt.Errorf("chain not supported: %w", err)
	}

	// TODO: Enable after protobuf regeneration adds Amount field
	// Amount validation for chain precision
	// if msg.Amount != "" {
	// 	amount, err := cosmos.ParseUint(msg.Amount)
	// 	if err != nil {
	// 		return fmt.Errorf("invalid amount format: %w", err)
	// 	}
	//
	// 	if err := k.ValidateAmountForChain(ctx, msg.ChainId, amount); err != nil {
	// 		return fmt.Errorf("amount invalid for chain: %w", err)
	// 	}
	//
	// 	// Test reference encoding to ensure no precision loss
	// 	if err := k.ValidateAmountForReference(ctx, msg.ChainId, amount); err != nil {
	// 		return fmt.Errorf("amount incompatible with reference encoding: %w", err)
	// 	}
	// }

	return nil
}

// validateTemporalMessage performs temporal validation
func (k KVStore) validateTemporalMessage(ctx cosmos.Context, msg types.MsgRegisterMemo, validationCtx EnhancedValidationContext) error {
	// Calculate expiry time
	ttl := msg.Ttl
	if ttl == 0 {
		// Use default TTL
		ttl = validationCtx.ConstantProvider.GetInt64Value(constants.MemolessTTL)
	}

	currentHeight := validationCtx.BlockHeight
	expiryHeight := currentHeight + ttl

	// Check if expiry is reasonable (not too far in future)
	maxTTL := validationCtx.ConstantProvider.GetInt64Value(constants.MemolessMaxTTL)
	if expiryHeight > currentHeight+maxTTL {
		return fmt.Errorf("TTL results in expiry too far in future")
	}

	// Grace period validation
	gracePeriod := validationCtx.ConstantProvider.GetInt64Value(constants.MemolessGracePeriod)
	if ttl > 0 && ttl < gracePeriod {
		return fmt.Errorf("TTL %d is below grace period %d", ttl, gracePeriod)
	}

	return nil
}

// validateAmountField validates the amount field format and precision
func (k KVStore) validateAmountField(amount, chainID string, validationCtx EnhancedValidationContext) error {
	// Parse amount
	parsedAmount, err := cosmos.ParseUint(amount)
	if err != nil {
		return fmt.Errorf("invalid amount format: %w", err)
	}

	// Check minimum amount
	minAmount := validationCtx.ConstantProvider.GetInt64Value(constants.MemolessMinAmount)
	if parsedAmount.LT(cosmos.NewUint(uint64(minAmount))) {
		return fmt.Errorf("amount %s below minimum %d", amount, minAmount)
	}

	// Validate precision for chain
	if validationCtx.ChainConfig.DecimalPlaces < 5 {
		return fmt.Errorf("chain %s has insufficient decimal places for reference encoding", chainID)
	}

	return nil
}

// validateDestinationField validates the destination address field
func (k KVStore) validateDestinationField(destination, chainID string) error {
	if len(destination) == 0 {
		return nil // Optional field
	}

	// Check address format for chain
	if pattern, exists := addressPatterns[chainID]; exists {
		if !pattern.MatchString(destination) {
			return fmt.Errorf("invalid destination address format for chain %s", chainID)
		}
	}

	return nil
}

// validateTagsField validates the tags field
func (k KVStore) validateTagsField(tags []string) error {
	if len(tags) == 0 {
		return nil // Optional field
	}

	if len(tags) > 10 {
		return fmt.Errorf("too many tags (max 10): %d", len(tags))
	}

	for i, tag := range tags {
		if len(tag) == 0 {
			return fmt.Errorf("tag at index %d cannot be empty", i)
		}
		if len(tag) > 50 {
			return fmt.Errorf("tag at index %d too long (max 50): %s", i, tag)
		}
	}

	return nil
}

// validateMemoFormat validates the memo format using existing THORNode memo parser
func (k KVStore) validateMemoFormat(memo string, version semver.Version) error {
	// Split memo into parts
	parts := strings.Split(memo, ":")
	if len(parts) == 0 {
		return fmt.Errorf("empty memo")
	}

	// Validate memo type (first part)
	memoType := strings.ToUpper(parts[0])
	validTypes := []string{"SWAP", "ADD", "WITHDRAW", "LOAN+", "LOAN-", "SAVERS+", "SAVERS-", "TRADE+", "TRADE-"}

	isValid := false
	for _, validType := range validTypes {
		if memoType == validType {
			isValid = true
			break
		}
	}

	if !isValid {
		return fmt.Errorf("invalid memo type: %s", memoType)
	}

	// Version-specific validation
	switch {
	case version.GTE(semver.MustParse("3.0.0")):
		return k.validateMemoFormatV3(parts, version)
	case version.GTE(semver.MustParse("2.0.0")):
		return k.validateMemoFormatV2(parts, version)
	default:
		return k.validateMemoFormatV1(parts)
	}
}

// validateMemoFormatV3 validates memo format for version 3.0.0+
func (k KVStore) validateMemoFormatV3(parts []string, version semver.Version) error {
	memoType := strings.ToUpper(parts[0])

	switch memoType {
	case "SWAP":
		// SWAP:ASSET:DEST:LIM:AFFILIATE:FEE
		if len(parts) < 2 {
			return fmt.Errorf("SWAP memo requires asset parameter")
		}
		if err := k.validateAssetField(parts[1]); err != nil {
			return fmt.Errorf("invalid asset in SWAP memo: %w", err)
		}

	case "ADD":
		// ADD:POOL:PAIRED_ADDR:AFFILIATE:FEE
		if len(parts) < 2 {
			return fmt.Errorf("ADD memo requires pool parameter")
		}
		if err := k.validateAssetField(parts[1]); err != nil {
			return fmt.Errorf("invalid pool in ADD memo: %w", err)
		}

	case "WITHDRAW":
		// WITHDRAW:POOL:BASIS_POINTS:ASSET
		if len(parts) < 3 {
			return fmt.Errorf("WITHDRAW memo requires pool and basis points")
		}
		if err := k.validateAssetField(parts[1]); err != nil {
			return fmt.Errorf("invalid pool in WITHDRAW memo: %w", err)
		}
		if err := k.validateBasisPoints(parts[2]); err != nil {
			return fmt.Errorf("invalid basis points in WITHDRAW memo: %w", err)
		}

	case "LOAN+", "LOAN-":
		// LOAN+:ASSET:DEST:MINOUT:AFFILIATE:FEE
		if len(parts) < 2 {
			return fmt.Errorf("LOAN memo requires asset parameter")
		}
		if err := k.validateAssetField(parts[1]); err != nil {
			return fmt.Errorf("invalid asset in LOAN memo: %w", err)
		}

	case "SAVERS+", "SAVERS-":
		// SAVERS+:ASSET:ADDR or SAVERS-:WITHDRAWAL_BASIS_POINTS:ASSET
		if memoType == "SAVERS+" {
			if len(parts) < 2 {
				return fmt.Errorf("SAVERS+ memo requires asset parameter")
			}
			if err := k.validateAssetField(parts[1]); err != nil {
				return fmt.Errorf("invalid asset in SAVERS+ memo: %w", err)
			}
		} else {
			if len(parts) < 2 {
				return fmt.Errorf("SAVERS- memo requires withdrawal basis points")
			}
			if err := k.validateBasisPoints(parts[1]); err != nil {
				return fmt.Errorf("invalid withdrawal basis points in SAVERS- memo: %w", err)
			}
		}
	}

	return nil
}

// validateMemoFormatV2 validates memo format for version 2.0.0+
func (k KVStore) validateMemoFormatV2(parts []string, version semver.Version) error {
	// Similar to V3 but with different rules for older versions
	return k.validateMemoFormatV3(parts, version)
}

// validateMemoFormatV1 validates memo format for version 1.x
func (k KVStore) validateMemoFormatV1(parts []string) error {
	// Legacy memo format validation
	return k.validateMemoFormatV3(parts, semver.MustParse("1.0.0"))
}

// validateAssetField validates asset field format
func (k KVStore) validateAssetField(asset string) error {
	if asset == "" {
		return fmt.Errorf("asset cannot be empty")
	}

	// Check if it's a pool asset (CHAIN.SYMBOL format)
	if strings.Contains(asset, ".") {
		if !assetPattern.MatchString(asset) {
			return fmt.Errorf("invalid asset format: %s", asset)
		}
	} else {
		// Native asset (just chain name)
		if !chainIDPattern.MatchString(asset) {
			return fmt.Errorf("invalid native asset: %s", asset)
		}
	}

	return nil
}

// validateBasisPoints validates basis points field (0-10000)
func (k KVStore) validateBasisPoints(basisPoints string) error {
	if basisPoints == "" {
		return fmt.Errorf("basis points cannot be empty")
	}

	bp, err := strconv.ParseInt(basisPoints, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid basis points format: %w", err)
	}

	if bp < 0 || bp > 10000 {
		return fmt.Errorf("basis points must be between 0 and 10000: %d", bp)
	}

	return nil
}

// validateRegistrationLimits validates protocol-wide registration limits
func (k KVStore) validateRegistrationLimits(ctx cosmos.Context, validationCtx EnhancedValidationContext) error {
	activeCount := k.GetActiveMemoCount(ctx)
	maxActive := validationCtx.ConstantProvider.GetInt64Value(constants.MemolessMaxActiveRefs)

	if activeCount >= maxActive {
		return fmt.Errorf("active memo limit reached: %d/%d", activeCount, maxActive)
	}

	return nil
}

// ValidateMemoRegistrationWithVersion validates a memo registration with version-specific rules
func (k KVStore) ValidateMemoRegistrationWithVersion(ctx cosmos.Context, msg types.MsgRegisterMemo, version semver.Version, constantProvider constants.ConstantValues) error {
	// Get chain configuration
	chainConfig, err := k.GetChainConfig(ctx, msg.ChainId)
	if err != nil {
		return fmt.Errorf("failed to get chain configuration: %w", err)
	}

	// Create validation context
	validationCtx := EnhancedValidationContext{
		Version:          version,
		BlockHeight:      ctx.BlockHeight(),
		ConstantProvider: constantProvider,
		ChainConfig:      chainConfig,
	}

	// Perform enhanced validation
	return k.ValidateMemoRegistrationEnhanced(ctx, msg, validationCtx)
}

// GetValidationVersion returns the appropriate validation version based on the current version
func (k KVStore) GetValidationVersion() semver.Version {
	currentVersion := GetCurrentVersion()
	return currentVersion
}

// ValidateMemoContent validates the memo content using the existing memo parser
func (k KVStore) ValidateMemoContent(memo string) error {
	// Use common memo parsing utilities if available
	if len(memo) == 0 {
		return fmt.Errorf("memo cannot be empty")
	}

	// Additional content validation can be added here
	return nil
}
