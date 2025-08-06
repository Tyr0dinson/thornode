package thorchain

import (
	"context"
	"fmt"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// EnhancedClient wraps the basic Client interface with enhanced functionality
type EnhancedClient struct {
	client         Client
	quoteService   *QuoteService
	validator      *AssetValidator
	errorMapper    *ErrorMapper
	config         *EnhancedClientConfig
}

// EnhancedClientConfig holds configuration for the enhanced client
type EnhancedClientConfig struct {
	DefaultTimeout          time.Duration `json:"default_timeout"`
	EnableValidation        bool          `json:"enable_validation"`
	ValidationLevel         ValidationLevel `json:"validation_level"`
	EnableErrorMapping      bool          `json:"enable_error_mapping"`
	EnableQuoteEnhancement  bool          `json:"enable_quote_enhancement"`
	MaxRetries              int           `json:"max_retries"`
	RetryDelay              time.Duration `json:"retry_delay"`
}

// NewEnhancedClient creates a new enhanced client with advanced features
func NewEnhancedClient(client Client, config *EnhancedClientConfig) *EnhancedClient {
	if config == nil {
		config = NewDefaultEnhancedClientConfig()
	}
	
	// Initialize enhanced services
	quoteService := NewQuoteService(client, nil)
	validator := NewAssetValidator(client)
	errorMapper := NewErrorMapper(nil)
	
	return &EnhancedClient{
		client:       client,
		quoteService: quoteService,
		validator:    validator,
		errorMapper:  errorMapper,
		config:       config,
	}
}

// NewDefaultEnhancedClientConfig returns default configuration
func NewDefaultEnhancedClientConfig() *EnhancedClientConfig {
	return &EnhancedClientConfig{
		DefaultTimeout:         30 * time.Second,
		EnableValidation:       true,
		ValidationLevel:        ValidationLevelStandard,
		EnableErrorMapping:     true,
		EnableQuoteEnhancement: true,
		MaxRetries:             3,
		RetryDelay:             time.Second,
	}
}

// GetEnhancedSwapQuote gets an enhanced swap quote with comprehensive validation and analysis
func (ec *EnhancedClient) GetEnhancedSwapQuote(ctx context.Context, req *EnhancedSwapQuoteRequest) (*EnhancedSwapQuote, error) {
	// Apply timeout if not set
	if req.QuoteTimeout == 0 {
		req.QuoteTimeout = ec.config.DefaultTimeout
	}
	
	// Validate assets if enabled
	if ec.config.EnableValidation {
		if err := ec.validateSwapRequest(&req.SwapQuoteRequest); err != nil {
			return nil, ec.mapError(err, "enhanced_swap_quote")
		}
	}
	
	// Get enhanced quote
	quote, err := ec.quoteService.GetEnhancedSwapQuote(ctx, req)
	if err != nil {
		return nil, ec.mapError(err, "enhanced_swap_quote")
	}
	
	return quote, nil
}

// ValidateAsset validates an asset with comprehensive checking
func (ec *EnhancedClient) ValidateAsset(ctx context.Context, asset common.Asset) (*ValidationResult, error) {
	if !ec.config.EnableValidation {
		// Return minimal validation
		return &ValidationResult{
			Valid:           !asset.IsEmpty(),
			Asset:           asset,
			ValidationLevel: ValidationLevelBasic,
			Warnings:        make([]ValidationWarning, 0),
			Errors:          make([]ValidationError, 0),
		}, nil
	}
	
	result, err := ec.validator.ValidateAsset(ctx, asset, ec.config.ValidationLevel)
	if err != nil {
		return nil, ec.mapError(err, "asset_validation")
	}
	
	return result, nil
}

// ValidateSwapPair validates a pair of assets for swapping
func (ec *EnhancedClient) ValidateSwapPair(ctx context.Context, fromAsset, toAsset common.Asset) (*SwapPairValidation, error) {
	if !ec.config.EnableValidation {
		// Return minimal validation
		return &SwapPairValidation{
			Valid: !fromAsset.IsEmpty() && !toAsset.IsEmpty() && !fromAsset.Equals(toAsset),
		}, nil
	}
	
	result, err := ec.validator.ValidateSwapPair(ctx, fromAsset, toAsset, ec.config.ValidationLevel)
	if err != nil {
		return nil, ec.mapError(err, "swap_pair_validation")
	}
	
	return result, nil
}

// GetSupportedAssets returns a list of supported assets with metadata
func (ec *EnhancedClient) GetSupportedAssets(ctx context.Context) ([]SupportedAsset, error) {
	pools, err := ec.client.GetPools(ctx)
	if err != nil {
		return nil, ec.mapError(err, "get_supported_assets")
	}
	
	assets := make([]SupportedAsset, 0)
	
	// Add RUNE
	runeAsset := SupportedAsset{
		Asset:       common.RuneNative,
		AssetType:   AssetTypeNative,
		Chain:       common.THORChain,
		Status:      "ACTIVE",
		IsNative:    true,
		Features:    []string{"NATIVE", "STREAMING_SWAPS"},
	}
	assets = append(assets, runeAsset)
	
	// Add pool assets
	for _, pool := range pools {
		analyzer := NewAssetTypeAnalyzer()
		assetType := analyzer.GetAssetType(pool.Asset)
		
		features := []string{}
		if pool.Asset.IsSyntheticAsset() {
			features = append(features, "SYNTHETIC")
		}
		
		// Check chain config for additional features
		if config, exists := ec.validator.GetChainConfig(pool.Asset.GetChain()); exists {
			if config.SupportsStreamSwaps {
				features = append(features, "STREAMING_SWAPS")
			}
		}
		
		supportedAsset := SupportedAsset{
			Asset:        pool.Asset,
			AssetType:    assetType,
			Chain:        pool.Asset.GetChain(),
			Status:       pool.Status,
			IsNative:     false,
			Features:     features,
			PoolDepth:    pool.BalanceRune.Add(pool.BalanceAsset),
			LiquidityUnits: pool.LPUnits,
		}
		
		assets = append(assets, supportedAsset)
	}
	
	return assets, nil
}

// SupportedAsset represents information about a supported asset
type SupportedAsset struct {
	Asset          common.Asset `json:"asset"`
	AssetType      AssetType    `json:"asset_type"`
	Chain          common.Chain `json:"chain"`
	Status         string       `json:"status"`
	IsNative       bool         `json:"is_native"`
	Features       []string     `json:"features"`
	PoolDepth      cosmos.Uint  `json:"pool_depth,omitempty"`
	LiquidityUnits cosmos.Uint  `json:"liquidity_units,omitempty"`
}

// GetOptimalSwapRoute calculates the optimal route for a swap
func (ec *EnhancedClient) GetOptimalSwapRoute(ctx context.Context, fromAsset, toAsset common.Asset, 
	amount cosmos.Uint) (*SwapRoute, error) {
	
	// Validate inputs
	if ec.config.EnableValidation {
		pairValidation, err := ec.ValidateSwapPair(ctx, fromAsset, toAsset)
		if err != nil {
			return nil, ec.mapError(err, "optimal_swap_route")
		}
		if !pairValidation.Valid {
			return nil, NewQuoteError(QuoteErrorInvalidRequest, 
				"invalid asset pair for swap", fmt.Errorf("validation failed"))
		}
	}
	
	route := &SwapRoute{
		FromAsset: fromAsset,
		ToAsset:   toAsset,
		Amount:    amount,
		Steps:     make([]SwapStep, 0),
	}
	
	// Determine optimal route
	if fromAsset.IsRune() || toAsset.IsRune() {
		// Direct route via RUNE
		route.Steps = append(route.Steps, SwapStep{
			FromAsset: fromAsset,
			ToAsset:   toAsset,
			StepType:  "DIRECT",
		})
		route.RouteType = "DIRECT"
	} else {
		// Two-step route via RUNE
		route.Steps = append(route.Steps, SwapStep{
			FromAsset: fromAsset,
			ToAsset:   common.RuneNative,
			StepType:  "INTERMEDIATE",
		})
		route.Steps = append(route.Steps, SwapStep{
			FromAsset: common.RuneNative,
			ToAsset:   toAsset,
			StepType:  "FINAL",
		})
		route.RouteType = "DOUBLE_SWAP"
	}
	
	// Get quotes for each step
	for i := range route.Steps {
		step := &route.Steps[i]
		quote, err := ec.client.GetSwapQuote(ctx, &SwapQuoteRequest{
			FromAsset: step.FromAsset,
			ToAsset:   step.ToAsset,
			Amount:    amount, // Use original amount for first step
		})
		if err != nil {
			return nil, ec.mapError(err, "optimal_swap_route")
		}
		
		step.ExpectedOutput = quote.ExpectedAmountOut
		step.Fees = quote.Fees
		step.SlippageBps = quote.SlippageBasisPoints
		
		// Update amount for next step (if applicable)
		if i == 0 && len(route.Steps) > 1 {
			amount = quote.ExpectedAmountOut
		}
	}
	
	// Calculate total metrics
	route.TotalSlippageBps = ec.calculateTotalSlippage(route.Steps)
	route.EstimatedDuration = ec.calculateEstimatedDuration(route.Steps)
	
	return route, nil
}

// SwapRoute represents an optimal swap route
type SwapRoute struct {
	FromAsset         common.Asset `json:"from_asset"`
	ToAsset           common.Asset `json:"to_asset"`
	Amount            cosmos.Uint  `json:"amount"`
	RouteType         string       `json:"route_type"`
	Steps             []SwapStep   `json:"steps"`
	TotalSlippageBps  int64        `json:"total_slippage_bps"`
	EstimatedDuration time.Duration `json:"estimated_duration"`
}

// SwapStep represents a step in a swap route
type SwapStep struct {
	FromAsset      common.Asset `json:"from_asset"`
	ToAsset        common.Asset `json:"to_asset"`
	StepType       string       `json:"step_type"`
	ExpectedOutput cosmos.Uint  `json:"expected_output"`
	Fees           QuoteFees    `json:"fees"`
	SlippageBps    int64        `json:"slippage_bps"`
}

// EstimateSwapTime estimates the time required for a swap
func (ec *EnhancedClient) EstimateSwapTime(ctx context.Context, fromAsset, toAsset common.Asset) (*SwapTimeEstimate, error) {
	estimate := &SwapTimeEstimate{
		FromAsset: fromAsset,
		ToAsset:   toAsset,
	}
	
	// Get chain configurations
	fromConfig, fromExists := ec.validator.GetChainConfig(fromAsset.GetChain())
	toConfig, toExists := ec.validator.GetChainConfig(toAsset.GetChain())
	
	// Calculate confirmation times
	if fromExists {
		estimate.InboundConfirmTime = time.Duration(fromConfig.BlockTime * fromConfig.ConfirmationBlocks) * time.Second
	} else {
		estimate.InboundConfirmTime = 5 * time.Minute // Default
	}
	
	if toExists {
		estimate.OutboundConfirmTime = time.Duration(toConfig.BlockTime * toConfig.ConfirmationBlocks) * time.Second
	} else {
		estimate.OutboundConfirmTime = 5 * time.Minute // Default
	}
	
	// Processing time (fixed for now)
	estimate.ProcessingTime = 30 * time.Second
	
	// Calculate total time
	estimate.TotalEstimatedTime = estimate.InboundConfirmTime + 
		estimate.ProcessingTime + estimate.OutboundConfirmTime
	
	// Add buffer for cross-chain swaps
	if !fromAsset.GetChain().Equals(toAsset.GetChain()) {
		estimate.TotalEstimatedTime += 1 * time.Minute
		estimate.Notes = append(estimate.Notes, "Additional time added for cross-chain processing")
	}
	
	return estimate, nil
}

// SwapTimeEstimate represents time estimates for a swap
type SwapTimeEstimate struct {
	FromAsset            common.Asset  `json:"from_asset"`
	ToAsset              common.Asset  `json:"to_asset"`
	InboundConfirmTime   time.Duration `json:"inbound_confirm_time"`
	ProcessingTime       time.Duration `json:"processing_time"`
	OutboundConfirmTime  time.Duration `json:"outbound_confirm_time"`
	TotalEstimatedTime   time.Duration `json:"total_estimated_time"`
	Notes                []string      `json:"notes,omitempty"`
}

// Helper methods

// validateSwapRequest validates a swap request
func (ec *EnhancedClient) validateSwapRequest(req *SwapQuoteRequest) error {
	return ValidateSwapRequest(req)
}

// mapError maps errors using the error mapper if enabled
func (ec *EnhancedClient) mapError(err error, operation string) error {
	if err == nil {
		return nil
	}
	
	if !ec.config.EnableErrorMapping {
		return err
	}
	
	return ec.errorMapper.MapError(err, operation)
}

// calculateTotalSlippage calculates total slippage across multiple steps
func (ec *EnhancedClient) calculateTotalSlippage(steps []SwapStep) int64 {
	if len(steps) == 0 {
		return 0
	}
	
	if len(steps) == 1 {
		return steps[0].SlippageBps
	}
	
	// For multiple steps, combine slippage (simplified calculation)
	totalSlippage := int64(0)
	for _, step := range steps {
		totalSlippage += step.SlippageBps
	}
	
	return totalSlippage
}

// calculateEstimatedDuration calculates estimated duration for swap steps
func (ec *EnhancedClient) calculateEstimatedDuration(steps []SwapStep) time.Duration {
	baseDuration := 30 * time.Second
	
	// Add time for each step
	duration := baseDuration * time.Duration(len(steps))
	
	// Add confirmation times based on chains involved
	chainSet := make(map[common.Chain]bool)
	for _, step := range steps {
		chainSet[step.FromAsset.GetChain()] = true
		chainSet[step.ToAsset.GetChain()] = true
	}
	
	// Add time for each unique chain
	for chain := range chainSet {
		if config, exists := ec.validator.GetChainConfig(chain); exists {
			duration += time.Duration(config.BlockTime * config.ConfirmationBlocks) * time.Second
		}
	}
	
	return duration
}

// Delegation methods - delegate to underlying client for standard operations

func (ec *EnhancedClient) SubmitMsgRegisterMemo(ctx context.Context, msg *types.MsgRegisterMemo) (*MsgRegisterMemoResponse, error) {
	resp, err := ec.client.SubmitMsgRegisterMemo(ctx, msg)
	if err != nil {
		return nil, ec.mapError(err, "submit_register_memo")
	}
	return resp, nil
}

func (ec *EnhancedClient) QueryRegisteredMemo(ctx context.Context, reference string) (*types.RegisteredMemo, error) {
	resp, err := ec.client.QueryRegisteredMemo(ctx, reference)
	if err != nil {
		return nil, ec.mapError(err, "query_registered_memo")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetSwapQuote(ctx context.Context, req *SwapQuoteRequest) (*SwapQuote, error) {
	if ec.config.EnableValidation {
		if err := ec.validateSwapRequest(req); err != nil {
			return nil, ec.mapError(err, "get_swap_quote")
		}
	}
	
	resp, err := ec.client.GetSwapQuote(ctx, req)
	if err != nil {
		return nil, ec.mapError(err, "get_swap_quote")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetLiquidityQuote(ctx context.Context, req *LiquidityQuoteRequest) (*LiquidityQuote, error) {
	resp, err := ec.client.GetLiquidityQuote(ctx, req)
	if err != nil {
		return nil, ec.mapError(err, "get_liquidity_quote")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetLoanQuote(ctx context.Context, req *LoanQuoteRequest) (*LoanQuote, error) {
	resp, err := ec.client.GetLoanQuote(ctx, req)
	if err != nil {
		return nil, ec.mapError(err, "get_loan_quote")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetNodeInfo(ctx context.Context) (*NodeInfo, error) {
	resp, err := ec.client.GetNodeInfo(ctx)
	if err != nil {
		return nil, ec.mapError(err, "get_node_info")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetConstants(ctx context.Context) (*NetworkConstants, error) {
	resp, err := ec.client.GetConstants(ctx)
	if err != nil {
		return nil, ec.mapError(err, "get_constants")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetNetwork(ctx context.Context) (*Network, error) {
	resp, err := ec.client.GetNetwork(ctx)
	if err != nil {
		return nil, ec.mapError(err, "get_network")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetMimirs(ctx context.Context) (*Mimirs, error) {
	resp, err := ec.client.GetMimirs(ctx)
	if err != nil {
		return nil, ec.mapError(err, "get_mimirs")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetPool(ctx context.Context, asset common.Asset) (*Pool, error) {
	resp, err := ec.client.GetPool(ctx, asset)
	if err != nil {
		return nil, ec.mapError(err, "get_pool")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetPools(ctx context.Context) ([]*Pool, error) {
	resp, err := ec.client.GetPools(ctx)
	if err != nil {
		return nil, ec.mapError(err, "get_pools")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetPoolStatus(ctx context.Context, asset common.Asset) (*PoolStatus, error) {
	resp, err := ec.client.GetPoolStatus(ctx, asset)
	if err != nil {
		return nil, ec.mapError(err, "get_pool_status")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetTransaction(ctx context.Context, txID string) (*Transaction, error) {
	resp, err := ec.client.GetTransaction(ctx, txID)
	if err != nil {
		return nil, ec.mapError(err, "get_transaction")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetTransactionStatus(ctx context.Context, txID string) (*TransactionStatus, error) {
	resp, err := ec.client.GetTransactionStatus(ctx, txID)
	if err != nil {
		return nil, ec.mapError(err, "get_transaction_status")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetInboundAddresses(ctx context.Context) ([]*InboundAddress, error) {
	resp, err := ec.client.GetInboundAddresses(ctx)
	if err != nil {
		return nil, ec.mapError(err, "get_inbound_addresses")
	}
	return resp, nil
}

func (ec *EnhancedClient) GetBalance(ctx context.Context, address common.Address) (*Balance, error) {
	resp, err := ec.client.GetBalance(ctx, address)
	if err != nil {
		return nil, ec.mapError(err, "get_balance")
	}
	return resp, nil
}

func (ec *EnhancedClient) Ping(ctx context.Context) error {
	err := ec.client.Ping(ctx)
	if err != nil {
		return ec.mapError(err, "ping")
	}
	return nil
}

func (ec *EnhancedClient) GetHealth(ctx context.Context) (*Health, error) {
	resp, err := ec.client.GetHealth(ctx)
	if err != nil {
		return nil, ec.mapError(err, "get_health")
	}
	return resp, nil
}

func (ec *EnhancedClient) Connect() error {
	err := ec.client.Connect()
	if err != nil {
		return ec.mapError(err, "connect")
	}
	return nil
}

func (ec *EnhancedClient) Disconnect() error {
	err := ec.client.Disconnect()
	if err != nil {
		return ec.mapError(err, "disconnect")
	}
	return nil
}

func (ec *EnhancedClient) IsConnected() bool {
	return ec.client.IsConnected()
}