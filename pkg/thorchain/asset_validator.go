package thorchain

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// AssetValidator provides comprehensive asset validation functionality
type AssetValidator struct {
	client       Client
	chainConfigs map[common.Chain]*ChainConfig
}

// ChainConfig holds chain-specific configuration for validation
type ChainConfig struct {
	Chain                common.Chain  `json:"chain"`
	MinAmount            cosmos.Uint   `json:"min_amount"`
	MaxAmount            cosmos.Uint   `json:"max_amount"`
	AssetRegex           string        `json:"asset_regex"`
	AddressRegex         string        `json:"address_regex"`
	SupportsSynthetics   bool          `json:"supports_synthetics"`
	SupportsStreamSwaps  bool          `json:"supports_stream_swaps"`
	DecimalPlaces        int           `json:"decimal_places"`
	BlockTime            int64         `json:"block_time_seconds"`
	ConfirmationBlocks   int64         `json:"confirmation_blocks"`
}

// NewAssetValidator creates a new asset validator with default chain configurations
func NewAssetValidator(client Client) *AssetValidator {
	return &AssetValidator{
		client:       client,
		chainConfigs: getDefaultChainConfigs(),
	}
}

// getDefaultChainConfigs returns default configurations for supported chains
func getDefaultChainConfigs() map[common.Chain]*ChainConfig {
	configs := make(map[common.Chain]*ChainConfig)
	
	configs[common.BTCChain] = &ChainConfig{
		Chain:               common.BTCChain,
		MinAmount:           cosmos.NewUint(10000),      // 0.0001 BTC
		MaxAmount:           cosmos.NewUint(2100000000000000), // 21M BTC
		AssetRegex:          `^BTC\.BTC$`,
		AddressRegex:        `^(bc1|[13])[a-zA-HJ-NP-Z0-9]{25,62}$`,
		SupportsSynthetics:  true,
		SupportsStreamSwaps: true,
		DecimalPlaces:       8,
		BlockTime:          600,
		ConfirmationBlocks: 6,
	}
	
	configs[common.ETHChain] = &ChainConfig{
		Chain:               common.ETHChain,
		MinAmount:           cosmos.NewUint(1000000000000000), // 0.001 ETH
		MaxAmount:           cosmos.NewUintFromString("1000000000000000000000000"), // 1M ETH
		AssetRegex:          `^ETH\.[A-Z0-9-]+$`,
		AddressRegex:        `^0x[a-fA-F0-9]{40}$`,
		SupportsSynthetics:  true,
		SupportsStreamSwaps: true,
		DecimalPlaces:       18,
		BlockTime:          12,
		ConfirmationBlocks: 50,
	}
	
	configs[common.THORChain] = &ChainConfig{
		Chain:               common.THORChain,
		MinAmount:           cosmos.NewUint(1000000),     // 1 RUNE
		MaxAmount:           cosmos.NewUint(500000000000000), // 500M RUNE
		AssetRegex:          `^THOR\.RUNE$`,
		AddressRegex:        `^thor[a-z0-9]{39}$`,
		SupportsSynthetics:  false,
		SupportsStreamSwaps: true,
		DecimalPlaces:       8,
		BlockTime:          6,
		ConfirmationBlocks: 1,
	}
	
	configs[common.BSCChain] = &ChainConfig{
		Chain:               common.BSCChain,
		MinAmount:           cosmos.NewUint(10000000000000000), // 0.01 BNB
		MaxAmount:           cosmos.NewUintFromString("200000000000000000000000"), // 200K BNB
		AssetRegex:          `^BSC\.[A-Z0-9-]+$`,
		AddressRegex:        `^0x[a-fA-F0-9]{40}$`,
		SupportsSynthetics:  true,
		SupportsStreamSwaps: true,
		DecimalPlaces:       18,
		BlockTime:          3,
		ConfirmationBlocks: 15,
	}
	
	configs[common.LTCChain] = &ChainConfig{
		Chain:               common.LTCChain,
		MinAmount:           cosmos.NewUint(100000),      // 0.001 LTC
		MaxAmount:           cosmos.NewUint(8400000000000000), // 84M LTC
		AssetRegex:          `^LTC\.LTC$`,
		AddressRegex:        `^[LM3][a-km-zA-HJ-NP-Z1-9]{26,33}$`,
		SupportsSynthetics:  true,
		SupportsStreamSwaps: true,
		DecimalPlaces:       8,
		BlockTime:          150,
		ConfirmationBlocks: 6,
	}
	
	configs[common.BCHChain] = &ChainConfig{
		Chain:               common.BCHChain,
		MinAmount:           cosmos.NewUint(10000),       // 0.0001 BCH
		MaxAmount:           cosmos.NewUint(2100000000000000), // 21M BCH
		AssetRegex:          `^BCH\.BCH$`,
		AddressRegex:        `^[13qp][a-zA-HJ-NP-Z0-9]{25,62}$`,
		SupportsSynthetics:  true,
		SupportsStreamSwaps: true,
		DecimalPlaces:       8,
		BlockTime:          600,
		ConfirmationBlocks: 6,
	}
	
	configs[common.DOGEChain] = &ChainConfig{
		Chain:               common.DOGEChain,
		MinAmount:           cosmos.NewUint(100000000),   // 1 DOGE
		MaxAmount:           cosmos.NewUint(100000000000000000000), // Large supply
		AssetRegex:          `^DOGE\.DOGE$`,
		AddressRegex:        `^[AD9][a-zA-HJ-NP-Z0-9]{25,34}$`,
		SupportsSynthetics:  true,
		SupportsStreamSwaps: true,
		DecimalPlaces:       8,
		BlockTime:          60,
		ConfirmationBlocks: 20,
	}
	
	configs[common.ATOMChain] = &ChainConfig{
		Chain:               common.ATOMChain,
		MinAmount:           cosmos.NewUint(1000000),     // 1 ATOM
		MaxAmount:           cosmos.NewUint(100000000000000), // Large supply
		AssetRegex:          `^GAIA\.[A-Z0-9]+$`,
		AddressRegex:        `^cosmos[a-z0-9]{39}$`,
		SupportsSynthetics:  true,
		SupportsStreamSwaps: true,
		DecimalPlaces:       6,
		BlockTime:          6,
		ConfirmationBlocks: 1,
	}
	
	return configs
}

// ValidationResult represents the result of asset validation
type ValidationResult struct {
	Valid           bool                   `json:"valid"`
	Asset           common.Asset          `json:"asset"`
	AssetType       AssetType             `json:"asset_type"`
	Chain           common.Chain          `json:"chain"`
	ValidationLevel ValidationLevel       `json:"validation_level"`
	Warnings        []ValidationWarning   `json:"warnings,omitempty"`
	Errors          []ValidationError     `json:"errors,omitempty"`
	Metadata        ValidationMetadata    `json:"metadata"`
}

// ValidationLevel represents different levels of validation
type ValidationLevel int

const (
	ValidationLevelBasic ValidationLevel = iota
	ValidationLevelStandard
	ValidationLevelStrict
)

func (vl ValidationLevel) String() string {
	switch vl {
	case ValidationLevelBasic:
		return "BASIC"
	case ValidationLevelStandard:
		return "STANDARD"
	case ValidationLevelStrict:
		return "STRICT"
	default:
		return "UNKNOWN"
	}
}

// ValidationWarning represents a validation warning
type ValidationWarning struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Severity    string `json:"severity"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// ValidationError represents a validation error
type ValidationError struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Field       string `json:"field,omitempty"`
	Suggestion  string `json:"suggestion,omitempty"`
}

// ValidationMetadata provides additional metadata about the asset
type ValidationMetadata struct {
	PoolExists          bool        `json:"pool_exists"`
	PoolStatus          string      `json:"pool_status,omitempty"`
	LiquidityDepth      cosmos.Uint `json:"liquidity_depth,omitempty"`
	ChainHeight         int64       `json:"chain_height,omitempty"`
	LastBlockTime       int64       `json:"last_block_time,omitempty"`
	EstimatedConfirmTime int64      `json:"estimated_confirm_time,omitempty"`
	SupportedFeatures   []string    `json:"supported_features,omitempty"`
}

// ValidateAsset performs comprehensive asset validation
func (av *AssetValidator) ValidateAsset(ctx context.Context, asset common.Asset, level ValidationLevel) (*ValidationResult, error) {
	result := &ValidationResult{
		Asset:           asset,
		ValidationLevel: level,
		Valid:           true,
		Warnings:        make([]ValidationWarning, 0),
		Errors:          make([]ValidationError, 0),
		Metadata:        ValidationMetadata{
			SupportedFeatures: make([]string, 0),
		},
	}

	// Basic format validation
	if err := av.validateAssetFormat(asset, result); err != nil {
		return result, err
	}

	// Determine asset type
	analyzer := NewAssetTypeAnalyzer()
	result.AssetType = analyzer.GetAssetType(asset)
	result.Chain = asset.GetChain()

	// Chain-specific validation
	if err := av.validateChainSpecific(ctx, asset, result); err != nil {
		return result, err
	}

	// Pool validation (if level >= Standard)
	if level >= ValidationLevelStandard {
		if err := av.validatePool(ctx, asset, result); err != nil {
			return result, err
		}
	}

	// Liquidity validation (if level == Strict)
	if level == ValidationLevelStrict {
		if err := av.validateLiquidity(ctx, asset, result); err != nil {
			return result, err
		}
	}

	// Set final validity
	result.Valid = len(result.Errors) == 0

	return result, nil
}

// validateAssetFormat validates basic asset format
func (av *AssetValidator) validateAssetFormat(asset common.Asset, result *ValidationResult) error {
	if asset.IsEmpty() {
		result.Errors = append(result.Errors, ValidationError{
			Code:    "EMPTY_ASSET",
			Message: "Asset cannot be empty",
			Field:   "asset",
		})
		return nil
	}

	// Validate asset string format
	assetStr := asset.String()
	parts := strings.Split(assetStr, ".")
	
	if len(parts) < 2 {
		result.Errors = append(result.Errors, ValidationError{
			Code:    "INVALID_FORMAT",
			Message: "Asset must be in CHAIN.SYMBOL format",
			Field:   "asset",
			Suggestion: "Use format like BTC.BTC or ETH.ETH",
		})
		return nil
	}

	chain := parts[0]
	symbol := parts[1]

	// Validate chain part
	if chain == "" {
		result.Errors = append(result.Errors, ValidationError{
			Code:    "EMPTY_CHAIN",
			Message: "Chain cannot be empty",
			Field:   "chain",
		})
	}

	// Validate symbol part
	if symbol == "" {
		result.Errors = append(result.Errors, ValidationError{
			Code:    "EMPTY_SYMBOL",
			Message: "Symbol cannot be empty",
			Field:   "symbol",
		})
	}

	// Check for synthetic assets
	if asset.IsSyntheticAsset() {
		result.Metadata.SupportedFeatures = append(result.Metadata.SupportedFeatures, "SYNTHETIC")
		
		// Extract base asset for synthetics
		if !strings.HasSuffix(symbol, "/") {
			result.Warnings = append(result.Warnings, ValidationWarning{
				Code:     "SYNTHETIC_FORMAT",
				Message:  "Synthetic asset format may be incorrect",
				Severity: "LOW",
				Suggestion: "Synthetic assets should end with '/' suffix",
			})
		}
	}

	return nil
}

// validateChainSpecific performs chain-specific validation
func (av *AssetValidator) validateChainSpecific(ctx context.Context, asset common.Asset, result *ValidationResult) error {
	chain := asset.GetChain()
	config, exists := av.chainConfigs[chain]
	
	if !exists {
		result.Warnings = append(result.Warnings, ValidationWarning{
			Code:     "UNSUPPORTED_CHAIN",
			Message:  fmt.Sprintf("Chain %s may not be supported", chain.String()),
			Severity: "MEDIUM",
		})
		return nil
	}

	// Validate asset format against chain regex
	if config.AssetRegex != "" {
		matched, err := regexp.MatchString(config.AssetRegex, asset.String())
		if err != nil {
			return fmt.Errorf("failed to validate asset regex: %w", err)
		}
		if !matched {
			result.Errors = append(result.Errors, ValidationError{
				Code:    "INVALID_ASSET_FORMAT",
				Message: fmt.Sprintf("Asset format does not match %s chain requirements", chain.String()),
				Field:   "asset",
				Suggestion: fmt.Sprintf("Expected format matching: %s", config.AssetRegex),
			})
		}
	}

	// Add supported features
	if config.SupportsSynthetics {
		result.Metadata.SupportedFeatures = append(result.Metadata.SupportedFeatures, "SYNTHETICS")
	}
	if config.SupportsStreamSwaps {
		result.Metadata.SupportedFeatures = append(result.Metadata.SupportedFeatures, "STREAMING_SWAPS")
	}

	// Set metadata
	result.Metadata.EstimatedConfirmTime = config.BlockTime * config.ConfirmationBlocks

	return nil
}

// validatePool validates pool existence and status
func (av *AssetValidator) validatePool(ctx context.Context, asset common.Asset, result *ValidationResult) error {
	// Skip pool validation for RUNE
	if asset.IsRune() {
		result.Metadata.PoolExists = true
		result.Metadata.PoolStatus = "NATIVE"
		return nil
	}

	// Try to get pool information
	pool, err := av.client.GetPool(ctx, asset)
	if err != nil {
		if clientErr, ok := err.(*ClientError); ok && clientErr.Code == ErrCodeNotFound {
			result.Metadata.PoolExists = false
			result.Errors = append(result.Errors, ValidationError{
				Code:    "POOL_NOT_FOUND",
				Message: "Pool does not exist for this asset",
				Field:   "asset",
				Suggestion: "Check if the asset is supported and has an active pool",
			})
			return nil
		}
		return fmt.Errorf("failed to get pool information: %w", err)
	}

	result.Metadata.PoolExists = true
	result.Metadata.PoolStatus = pool.Status

	// Check pool status
	switch strings.ToUpper(pool.Status) {
	case "AVAILABLE":
		// Pool is active and ready
	case "STAGED":
		result.Warnings = append(result.Warnings, ValidationWarning{
			Code:     "POOL_STAGED",
			Message:  "Pool is in staged status",
			Severity: "MEDIUM",
			Suggestion: "Pool functionality may be limited",
		})
	case "SUSPENDED":
		result.Errors = append(result.Errors, ValidationError{
			Code:    "POOL_SUSPENDED",
			Message: "Pool is suspended",
			Field:   "asset",
			Suggestion: "Wait for pool to be reactivated",
		})
	default:
		result.Warnings = append(result.Warnings, ValidationWarning{
			Code:     "UNKNOWN_POOL_STATUS",
			Message:  fmt.Sprintf("Unknown pool status: %s", pool.Status),
			Severity: "HIGH",
		})
	}

	// Set liquidity depth
	totalLiquidity := pool.BalanceRune.Add(pool.BalanceAsset)
	result.Metadata.LiquidityDepth = totalLiquidity

	return nil
}

// validateLiquidity performs detailed liquidity validation
func (av *AssetValidator) validateLiquidity(ctx context.Context, asset common.Asset, result *ValidationResult) error {
	// Skip for RUNE
	if asset.IsRune() {
		return nil
	}

	// Require pool to exist for liquidity validation
	if !result.Metadata.PoolExists {
		return nil // Already handled in pool validation
	}

	liquidity := result.Metadata.LiquidityDepth
	
	// Define liquidity thresholds (in RUNE terms)
	lowLiquidityThreshold := cosmos.NewUint(100000000000) // 1000 RUNE
	mediumLiquidityThreshold := cosmos.NewUint(1000000000000) // 10K RUNE
	
	if liquidity.LT(lowLiquidityThreshold) {
		result.Warnings = append(result.Warnings, ValidationWarning{
			Code:     "LOW_LIQUIDITY",
			Message:  "Pool has low liquidity",
			Severity: "HIGH",
			Suggestion: "Large swaps may experience high slippage",
		})
	} else if liquidity.LT(mediumLiquidityThreshold) {
		result.Warnings = append(result.Warnings, ValidationWarning{
			Code:     "MEDIUM_LIQUIDITY",
			Message:  "Pool has moderate liquidity",
			Severity: "MEDIUM",
			Suggestion: "Monitor slippage for large swaps",
		})
	}

	return nil
}

// ValidateSwapPair validates a pair of assets for swapping
func (av *AssetValidator) ValidateSwapPair(ctx context.Context, fromAsset, toAsset common.Asset, 
	level ValidationLevel) (*SwapPairValidation, error) {
	
	// Validate individual assets
	fromResult, err := av.ValidateAsset(ctx, fromAsset, level)
	if err != nil {
		return nil, fmt.Errorf("failed to validate from asset: %w", err)
	}
	
	toResult, err := av.ValidateAsset(ctx, toAsset, level)
	if err != nil {
		return nil, fmt.Errorf("failed to validate to asset: %w", err)
	}
	
	pairValidation := &SwapPairValidation{
		FromAsset:       fromResult,
		ToAsset:         toResult,
		Valid:           fromResult.Valid && toResult.Valid,
		SwapType:        av.determineSwapType(fromAsset, toAsset),
		EstimatedSteps:  av.calculateSwapSteps(fromAsset, toAsset),
		Warnings:        make([]ValidationWarning, 0),
		Errors:          make([]ValidationError, 0),
	}
	
	// Check for same asset
	if fromAsset.Equals(toAsset) {
		pairValidation.Valid = false
		pairValidation.Errors = append(pairValidation.Errors, ValidationError{
			Code:    "SAME_ASSET",
			Message: "From and to assets cannot be the same",
			Suggestion: "Choose different assets for swapping",
		})
	}
	
	// Add cross-chain warnings
	if !fromAsset.GetChain().Equals(toAsset.GetChain()) {
		pairValidation.Warnings = append(pairValidation.Warnings, ValidationWarning{
			Code:     "CROSS_CHAIN_SWAP",
			Message:  "Cross-chain swap will require additional confirmations",
			Severity: "LOW",
			Suggestion: "Allow extra time for cross-chain transactions",
		})
	}
	
	return pairValidation, nil
}

// SwapPairValidation represents validation results for a swap pair
type SwapPairValidation struct {
	FromAsset      *ValidationResult   `json:"from_asset"`
	ToAsset        *ValidationResult   `json:"to_asset"`
	Valid          bool                `json:"valid"`
	SwapType       SwapType           `json:"swap_type"`
	EstimatedSteps int                `json:"estimated_steps"`
	Warnings       []ValidationWarning `json:"warnings,omitempty"`
	Errors         []ValidationError   `json:"errors,omitempty"`
}

// SwapType represents different types of swaps
type SwapType int

const (
	SwapTypeRuneToAsset SwapType = iota
	SwapTypeAssetToRune
	SwapTypeAssetToAsset
	SwapTypeSynthetic
)

func (st SwapType) String() string {
	switch st {
	case SwapTypeRuneToAsset:
		return "RUNE_TO_ASSET"
	case SwapTypeAssetToRune:
		return "ASSET_TO_RUNE"
	case SwapTypeAssetToAsset:
		return "ASSET_TO_ASSET"
	case SwapTypeSynthetic:
		return "SYNTHETIC"
	default:
		return "UNKNOWN"
	}
}

// determineSwapType determines the type of swap
func (av *AssetValidator) determineSwapType(fromAsset, toAsset common.Asset) SwapType {
	if fromAsset.IsSyntheticAsset() || toAsset.IsSyntheticAsset() {
		return SwapTypeSynthetic
	}
	
	if fromAsset.IsRune() && !toAsset.IsRune() {
		return SwapTypeRuneToAsset
	}
	
	if !fromAsset.IsRune() && toAsset.IsRune() {
		return SwapTypeAssetToRune
	}
	
	return SwapTypeAssetToAsset
}

// calculateSwapSteps calculates the number of steps required for a swap
func (av *AssetValidator) calculateSwapSteps(fromAsset, toAsset common.Asset) int {
	if fromAsset.IsRune() || toAsset.IsRune() {
		return 1 // Direct swap with RUNE
	}
	return 2 // Asset to RUNE to asset
}

// GetSupportedAssetTypes returns supported asset types for a chain
func (av *AssetValidator) GetSupportedAssetTypes(chain common.Chain) []AssetType {
	config, exists := av.chainConfigs[chain]
	if !exists {
		return []AssetType{AssetTypeExternal}
	}
	
	types := []AssetType{AssetTypeExternal}
	
	if chain == common.THORChain {
		types = append(types, AssetTypeNative)
	}
	
	if config.SupportsSynthetics {
		types = append(types, AssetTypeSynthetic)
	}
	
	return types
}

// GetChainConfig returns configuration for a specific chain
func (av *AssetValidator) GetChainConfig(chain common.Chain) (*ChainConfig, bool) {
	config, exists := av.chainConfigs[chain]
	return config, exists
}