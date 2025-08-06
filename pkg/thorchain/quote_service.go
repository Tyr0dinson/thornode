package thorchain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// QuoteService provides enhanced quote functionality with proper asset type handling and error mapping
type QuoteService struct {
	client Client
	config *QuoteConfig
}

// QuoteConfig holds configuration for quote service
type QuoteConfig struct {
	DefaultTimeout        time.Duration
	SwapSlippageTolerance int64 // basis points
	MinQuoteAge           time.Duration
	MaxRetries            int
	EnableCaching         bool
}

// NewQuoteService creates a new quote service with enhanced functionality
func NewQuoteService(client Client, config *QuoteConfig) *QuoteService {
	if config == nil {
		config = NewDefaultQuoteConfig()
	}

	return &QuoteService{
		client: client,
		config: config,
	}
}

// NewDefaultQuoteConfig returns default quote configuration
func NewDefaultQuoteConfig() *QuoteConfig {
	return &QuoteConfig{
		DefaultTimeout:        30 * time.Second,
		SwapSlippageTolerance: 300, // 3%
		MinQuoteAge:           5 * time.Second,
		MaxRetries:            3,
		EnableCaching:         true,
	}
}

// EnhancedSwapQuoteRequest extends SwapQuoteRequest with additional validation and options
type EnhancedSwapQuoteRequest struct {
	SwapQuoteRequest
	// Enhanced options
	AllowSynthetic       bool          `json:"allow_synthetic,omitempty"`
	PreferredRouter      string        `json:"preferred_router,omitempty"`
	MaxSlippageBasisPts  int64         `json:"max_slippage_basis_pts,omitempty"`
	MinExpectedAmountOut cosmos.Uint   `json:"min_expected_amount_out,omitempty"`
	QuoteTimeout         time.Duration `json:"quote_timeout,omitempty"`
}

// EnhancedSwapQuote extends SwapQuote with additional validation and metadata
type EnhancedSwapQuote struct {
	SwapQuote
	// Enhanced metadata
	QuoteTimestamp      int64               `json:"quote_timestamp"`
	ValidUntil          int64               `json:"valid_until"`
	AssetValidation     AssetValidation     `json:"asset_validation"`
	RouteAnalysis       RouteAnalysis       `json:"route_analysis"`
	RiskAssessment      RiskAssessment      `json:"risk_assessment"`
	RecommendedSettings RecommendedSettings `json:"recommended_settings"`
}

// AssetValidation provides detailed asset validation information
type AssetValidation struct {
	FromAssetValid    bool          `json:"from_asset_valid"`
	ToAssetValid      bool          `json:"to_asset_valid"`
	FromAssetStatus   string        `json:"from_asset_status"`
	ToAssetStatus     string        `json:"to_asset_status"`
	FromAssetWarnings []string      `json:"from_asset_warnings,omitempty"`
	ToAssetWarnings   []string      `json:"to_asset_warnings,omitempty"`
	ChainAnalysis     ChainAnalysis `json:"chain_analysis"`
}

// ChainAnalysis provides chain-specific analysis
type ChainAnalysis struct {
	FromChain       common.Chain `json:"from_chain"`
	ToChain         common.Chain `json:"to_chain"`
	FromChainHalted bool         `json:"from_chain_halted"`
	ToChainHalted   bool         `json:"to_chain_halted"`
	FromChainHeight int64        `json:"from_chain_height"`
	ToChainHeight   int64        `json:"to_chain_height"`
	CrossChainSwap  bool         `json:"cross_chain_swap"`
}

// RouteAnalysis provides routing analysis
type RouteAnalysis struct {
	DirectRoute        bool           `json:"direct_route"`
	HopCount           int            `json:"hop_count"`
	IntermediateAssets []common.Asset `json:"intermediate_assets,omitempty"`
	RouteEfficiency    float64        `json:"route_efficiency"`
	EstimatedTime      time.Duration  `json:"estimated_time"`
}

// RiskAssessment provides risk analysis
type RiskAssessment struct {
	RiskLevel       string      `json:"risk_level"` // LOW, MEDIUM, HIGH
	RiskFactors     []string    `json:"risk_factors,omitempty"`
	LiquidityDepth  string      `json:"liquidity_depth"` // DEEP, MEDIUM, SHALLOW
	SlippageRisk    string      `json:"slippage_risk"`   // LOW, MEDIUM, HIGH
	ImpermanentLoss cosmos.Uint `json:"impermanent_loss"`
}

// RecommendedSettings provides recommended settings
type RecommendedSettings struct {
	RecommendedSlippageTolerance int64            `json:"recommended_slippage_tolerance"`
	RecommendedTimeout           time.Duration    `json:"recommended_timeout"`
	SuggestedStreamingParams     *StreamingParams `json:"suggested_streaming_params,omitempty"`
	OptimalTiming                string           `json:"optimal_timing"`
}

// StreamingParams represents streaming swap parameters
type StreamingParams struct {
	Interval uint64 `json:"interval"`
	Quantity uint64 `json:"quantity"`
	Limit    uint64 `json:"limit,omitempty"`
}

// GetEnhancedSwapQuote gets a swap quote with enhanced validation and analysis
func (qs *QuoteService) GetEnhancedSwapQuote(ctx context.Context, req *EnhancedSwapQuoteRequest) (*EnhancedSwapQuote, error) {
	if req == nil {
		return nil, NewQuoteError(QuoteErrorInvalidRequest, "request cannot be nil", nil)
	}

	// Set timeout if not specified
	if req.QuoteTimeout == 0 {
		req.QuoteTimeout = qs.config.DefaultTimeout
	}

	// Create context with timeout
	quoteCtx, cancel := context.WithTimeout(ctx, req.QuoteTimeout)
	defer cancel()

	// Validate assets
	assetValidation, err := qs.validateAssets(quoteCtx, req.FromAsset, req.ToAsset)
	if err != nil {
		return nil, NewQuoteError(QuoteErrorAssetValidation, "asset validation failed", err)
	}

	// Check for critical asset issues
	if !assetValidation.FromAssetValid || !assetValidation.ToAssetValid {
		return nil, NewQuoteError(QuoteErrorInvalidAsset,
			fmt.Sprintf("invalid assets: from=%v, to=%v",
				assetValidation.FromAssetValid, assetValidation.ToAssetValid), nil)
	}

	// Get basic quote
	basicQuote, err := qs.client.GetSwapQuote(quoteCtx, &req.SwapQuoteRequest)
	if err != nil {
		return nil, qs.mapQuoteError(err)
	}

	// Perform enhanced analysis
	routeAnalysis := qs.analyzeRoute(req.FromAsset, req.ToAsset, basicQuote)
	riskAssessment := qs.assessRisk(req, basicQuote, assetValidation)
	recommendedSettings := qs.generateRecommendations(req, basicQuote, riskAssessment)

	timestamp := time.Now().Unix()

	enhancedQuote := &EnhancedSwapQuote{
		SwapQuote:           *basicQuote,
		QuoteTimestamp:      timestamp,
		ValidUntil:          timestamp + int64(qs.config.MinQuoteAge.Seconds()),
		AssetValidation:     *assetValidation,
		RouteAnalysis:       *routeAnalysis,
		RiskAssessment:      *riskAssessment,
		RecommendedSettings: *recommendedSettings,
	}

	return enhancedQuote, nil
}

// validateAssets performs comprehensive asset validation
func (qs *QuoteService) validateAssets(ctx context.Context, fromAsset, toAsset common.Asset) (*AssetValidation, error) {
	validation := &AssetValidation{
		FromAssetValid:    true,
		ToAssetValid:      true,
		FromAssetStatus:   "ACTIVE",
		ToAssetStatus:     "ACTIVE",
		FromAssetWarnings: make([]string, 0),
		ToAssetWarnings:   make([]string, 0),
	}

	// Validate asset format
	if fromAsset.IsEmpty() {
		validation.FromAssetValid = false
		validation.FromAssetStatus = "INVALID_FORMAT"
	}

	if toAsset.IsEmpty() {
		validation.ToAssetValid = false
		validation.ToAssetStatus = "INVALID_FORMAT"
	}

	// Check if assets are the same
	if fromAsset.Equals(toAsset) {
		return nil, NewQuoteError(QuoteErrorSameAsset, "from and to assets cannot be the same", nil)
	}

	// Get pool information for validation
	if validation.FromAssetValid && !fromAsset.IsRune() {
		if err := qs.validatePoolAsset(ctx, fromAsset, &validation.FromAssetValid,
			&validation.FromAssetStatus, &validation.FromAssetWarnings); err != nil {
			return nil, fmt.Errorf("failed to validate from asset: %w", err)
		}
	}

	if validation.ToAssetValid && !toAsset.IsRune() {
		if err := qs.validatePoolAsset(ctx, toAsset, &validation.ToAssetValid,
			&validation.ToAssetStatus, &validation.ToAssetWarnings); err != nil {
			return nil, fmt.Errorf("failed to validate to asset: %w", err)
		}
	}

	// Analyze chains
	validation.ChainAnalysis = ChainAnalysis{
		FromChain:      fromAsset.GetChain(),
		ToChain:        toAsset.GetChain(),
		CrossChainSwap: !fromAsset.GetChain().Equals(toAsset.GetChain()),
	}

	return validation, nil
}

// validatePoolAsset validates a single pool asset
func (qs *QuoteService) validatePoolAsset(ctx context.Context, asset common.Asset,
	valid *bool, status *string, warnings *[]string,
) error {
	// Try to get pool information
	pool, err := qs.client.GetPool(ctx, asset)
	if err != nil {
		if clientErr, ok := err.(*ClientError); ok && clientErr.Code == ErrCodeNotFound {
			*valid = false
			*status = "POOL_NOT_FOUND"
			*warnings = append(*warnings, "Pool does not exist")
			return nil
		}
		return err
	}

	// Check pool status
	switch strings.ToUpper(pool.Status) {
	case "AVAILABLE":
		*status = "ACTIVE"
	case "STAGED":
		*status = "STAGED"
		*warnings = append(*warnings, "Pool is in staged status - limited functionality")
	case "SUSPENDED":
		*valid = false
		*status = "SUSPENDED"
		*warnings = append(*warnings, "Pool is suspended")
	default:
		*valid = false
		*status = "UNKNOWN"
		*warnings = append(*warnings, fmt.Sprintf("Unknown pool status: %s", pool.Status))
	}

	// Check liquidity depth
	if pool.BalanceRune.IsZero() || pool.BalanceAsset.IsZero() {
		*warnings = append(*warnings, "Pool has very low liquidity")
	}

	return nil
}

// analyzeRoute analyzes the swap route
func (qs *QuoteService) analyzeRoute(fromAsset, toAsset common.Asset, quote *SwapQuote) *RouteAnalysis {
	analysis := &RouteAnalysis{
		HopCount:        1,
		RouteEfficiency: 1.0,
		EstimatedTime:   time.Duration(quote.OutboundDelaySeconds) * time.Second,
	}

	// Determine if it's a direct route
	if fromAsset.IsRune() || toAsset.IsRune() {
		analysis.DirectRoute = true
	} else {
		analysis.DirectRoute = false
		analysis.HopCount = 2
		analysis.IntermediateAssets = []common.Asset{common.RuneNative}
		analysis.RouteEfficiency = 0.95 // Slightly less efficient for double swaps
	}

	return analysis
}

// assessRisk assesses swap risk
func (qs *QuoteService) assessRisk(req *EnhancedSwapQuoteRequest, quote *SwapQuote,
	validation *AssetValidation,
) *RiskAssessment {
	assessment := &RiskAssessment{
		RiskLevel:      "LOW",
		RiskFactors:    make([]string, 0),
		LiquidityDepth: "DEEP",
		SlippageRisk:   "LOW",
	}

	// Assess slippage risk
	slippageBps := quote.SlippageBasisPoints
	if slippageBps > 500 { // 5%
		assessment.SlippageRisk = "HIGH"
		assessment.RiskLevel = "HIGH"
		assessment.RiskFactors = append(assessment.RiskFactors, "High slippage detected")
	} else if slippageBps > 100 { // 1%
		assessment.SlippageRisk = "MEDIUM"
		if assessment.RiskLevel == "LOW" {
			assessment.RiskLevel = "MEDIUM"
		}
		assessment.RiskFactors = append(assessment.RiskFactors, "Moderate slippage detected")
	}

	// Check for cross-chain risks
	if validation.ChainAnalysis.CrossChainSwap {
		assessment.RiskFactors = append(assessment.RiskFactors, "Cross-chain swap involves additional confirmations")
	}

	// Check for asset warnings
	if len(validation.FromAssetWarnings) > 0 || len(validation.ToAssetWarnings) > 0 {
		assessment.RiskLevel = "MEDIUM"
		assessment.RiskFactors = append(assessment.RiskFactors, "Asset-specific warnings detected")
	}

	return assessment
}

// generateRecommendations generates recommendations for the swap
func (qs *QuoteService) generateRecommendations(req *EnhancedSwapQuoteRequest, quote *SwapQuote,
	risk *RiskAssessment,
) *RecommendedSettings {
	settings := &RecommendedSettings{
		RecommendedSlippageTolerance: qs.config.SwapSlippageTolerance,
		RecommendedTimeout:           qs.config.DefaultTimeout,
		OptimalTiming:                "NOW",
	}

	// Adjust recommendations based on risk
	switch risk.RiskLevel {
	case "HIGH":
		settings.RecommendedSlippageTolerance = 1000 // 10%
		settings.RecommendedTimeout = 60 * time.Second
		settings.OptimalTiming = "CONSIDER_WAITING"
	case "MEDIUM":
		settings.RecommendedSlippageTolerance = 500 // 5%
		settings.RecommendedTimeout = 45 * time.Second
	}

	// Consider streaming swaps for large amounts
	if req.Amount.GT(cosmos.NewUint(1000000000000)) { // Large amount threshold
		settings.SuggestedStreamingParams = &StreamingParams{
			Interval: 10,
			Quantity: 5,
		}
	}

	return settings
}

// mapQuoteError maps internal errors to quote-specific errors
func (qs *QuoteService) mapQuoteError(err error) error {
	if clientErr, ok := err.(*ClientError); ok {
		switch clientErr.Code {
		case ErrCodeNotFound:
			return NewQuoteError(QuoteErrorPoolNotFound, "pool not found", err)
		case ErrCodeBadRequest:
			return NewQuoteError(QuoteErrorInvalidRequest, "invalid request", err)
		case ErrCodeTimeout:
			return NewQuoteError(QuoteErrorTimeout, "quote request timeout", err)
		case ErrCodeTooManyRequests:
			return NewQuoteError(QuoteErrorRateLimit, "rate limit exceeded", err)
		case ErrCodeServiceUnavailable:
			return NewQuoteError(QuoteErrorServiceUnavailable, "service unavailable", err)
		default:
			return NewQuoteError(QuoteErrorInternal, "internal error", err)
		}
	}

	return NewQuoteError(QuoteErrorInternal, "unknown error", err)
}

// QuoteError represents a quote-specific error
type QuoteError struct {
	Code    QuoteErrorCode `json:"code"`
	Message string         `json:"message"`
	Details string         `json:"details,omitempty"`
	Err     error          `json:"-"`
}

type QuoteErrorCode int

const (
	QuoteErrorInvalidRequest QuoteErrorCode = iota + 1000
	QuoteErrorInvalidAsset
	QuoteErrorSameAsset
	QuoteErrorAssetValidation
	QuoteErrorPoolNotFound
	QuoteErrorInsufficientLiquidity
	QuoteErrorSlippageTooHigh
	QuoteErrorAmountTooSmall
	QuoteErrorAmountTooLarge
	QuoteErrorTimeout
	QuoteErrorRateLimit
	QuoteErrorServiceUnavailable
	QuoteErrorInternal
)

func (e *QuoteError) Error() string {
	if e.Details != "" {
		return fmt.Sprintf("quote error %d: %s (%s)", e.Code, e.Message, e.Details)
	}
	return fmt.Sprintf("quote error %d: %s", e.Code, e.Message)
}

func (e *QuoteError) Unwrap() error {
	return e.Err
}

// NewQuoteError creates a new quote error
func NewQuoteError(code QuoteErrorCode, message string, err error) *QuoteError {
	quoteErr := &QuoteError{
		Code:    code,
		Message: message,
		Err:     err,
	}

	if err != nil {
		quoteErr.Details = err.Error()
	}

	return quoteErr
}

// ValidateSwapRequest validates a swap request thoroughly
func ValidateSwapRequest(req *SwapQuoteRequest) error {
	if req == nil {
		return NewQuoteError(QuoteErrorInvalidRequest, "request cannot be nil", nil)
	}

	if req.FromAsset.IsEmpty() {
		return NewQuoteError(QuoteErrorInvalidAsset, "from asset cannot be empty", nil)
	}

	if req.ToAsset.IsEmpty() {
		return NewQuoteError(QuoteErrorInvalidAsset, "to asset cannot be empty", nil)
	}

	if req.FromAsset.Equals(req.ToAsset) {
		return NewQuoteError(QuoteErrorSameAsset, "from and to assets cannot be the same", nil)
	}

	if req.Amount.IsZero() {
		return NewQuoteError(QuoteErrorAmountTooSmall, "amount must be greater than zero", nil)
	}

	// Validate basis points
	if !req.TolerateBasisPts.IsZero() && req.TolerateBasisPts.GT(cosmos.NewUint(10000)) {
		return NewQuoteError(QuoteErrorInvalidRequest, "tolerate basis points cannot exceed 10000", nil)
	}

	if !req.AffiliateBasisPts.IsZero() && req.AffiliateBasisPts.GT(cosmos.NewUint(10000)) {
		return NewQuoteError(QuoteErrorInvalidRequest, "affiliate basis points cannot exceed 10000", nil)
	}

	return nil
}

// AssetTypeAnalyzer provides utilities for analyzing asset types
type AssetTypeAnalyzer struct{}

// NewAssetTypeAnalyzer creates a new asset type analyzer
func NewAssetTypeAnalyzer() *AssetTypeAnalyzer {
	return &AssetTypeAnalyzer{}
}

// GetAssetType determines the type of an asset
func (ata *AssetTypeAnalyzer) GetAssetType(asset common.Asset) AssetType {
	if asset.IsRune() {
		return AssetTypeNative
	}

	if asset.IsSyntheticAsset() {
		return AssetTypeSynthetic
	}

	if asset.IsVaultAsset() {
		return AssetTypeVault
	}

	if asset.IsDerivedAsset() {
		return AssetTypeDerived
	}

	return AssetTypeExternal
}

// AssetType represents different types of assets
type AssetType int

const (
	AssetTypeNative AssetType = iota
	AssetTypeExternal
	AssetTypeSynthetic
	AssetTypeVault
	AssetTypeDerived
)

func (at AssetType) String() string {
	switch at {
	case AssetTypeNative:
		return "NATIVE"
	case AssetTypeExternal:
		return "EXTERNAL"
	case AssetTypeSynthetic:
		return "SYNTHETIC"
	case AssetTypeVault:
		return "VAULT"
	case AssetTypeDerived:
		return "DERIVED"
	default:
		return "UNKNOWN"
	}
}

// GetSupportedChains returns chains supported by the quote service
func (qs *QuoteService) GetSupportedChains(ctx context.Context) ([]common.Chain, error) {
	// Get inbound addresses to determine supported chains
	addresses, err := qs.client.GetInboundAddresses(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get inbound addresses: %w", err)
	}

	chains := make([]common.Chain, 0, len(addresses))
	for _, addr := range addresses {
		chains = append(chains, addr.Chain)
	}

	return chains, nil
}

// GetAssetPairs returns valid asset pairs for swapping
func (qs *QuoteService) GetAssetPairs(ctx context.Context) ([]AssetPair, error) {
	pools, err := qs.client.GetPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pools: %w", err)
	}

	pairs := make([]AssetPair, 0)
	runeAsset := common.RuneNative

	// Add RUNE pairs
	for _, pool := range pools {
		if strings.ToUpper(pool.Status) == "AVAILABLE" {
			pairs = append(pairs, AssetPair{
				FromAsset: runeAsset,
				ToAsset:   pool.Asset,
			})
			pairs = append(pairs, AssetPair{
				FromAsset: pool.Asset,
				ToAsset:   runeAsset,
			})
		}
	}

	// Add asset-to-asset pairs (via RUNE)
	availablePools := make([]common.Asset, 0)
	for _, pool := range pools {
		if strings.ToUpper(pool.Status) == "AVAILABLE" {
			availablePools = append(availablePools, pool.Asset)
		}
	}

	for i, asset1 := range availablePools {
		for j, asset2 := range availablePools {
			if i != j {
				pairs = append(pairs, AssetPair{
					FromAsset: asset1,
					ToAsset:   asset2,
				})
			}
		}
	}

	return pairs, nil
}

// AssetPair represents a pair of assets that can be swapped
type AssetPair struct {
	FromAsset common.Asset `json:"from_asset"`
	ToAsset   common.Asset `json:"to_asset"`
}
