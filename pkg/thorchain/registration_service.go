package thorchain

import (
	"context"
	"fmt"
	"time"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// RegistrationService provides the main interface for memo registration functionality
type RegistrationService struct {
	client             Client
	enhancedClient     *EnhancedClient
	transactionService *TransactionService
	quoteService       *QuoteService
	config             *RegistrationConfig
}

// RegistrationConfig holds comprehensive configuration for the registration service
type RegistrationConfig struct {
	// Core settings
	DefaultTimeout      time.Duration `json:"default_timeout"`
	EnableValidation    bool          `json:"enable_validation"`
	EnableQuoteChecking bool          `json:"enable_quote_checking"`

	// Transaction settings
	WaitForConfirmation bool          `json:"wait_for_confirmation"`
	MaxRetries          int           `json:"max_retries"`
	RetryDelay          time.Duration `json:"retry_delay"`

	// Safety settings
	RequireQuoteValidation bool `json:"require_quote_validation"`
	MaxMemoLength          int  `json:"max_memo_length"`
	CheckDuplicates        bool `json:"check_duplicates"`

	// Performance settings
	EnableBatching bool          `json:"enable_batching"`
	BatchSize      int           `json:"batch_size"`
	BatchTimeout   time.Duration `json:"batch_timeout"`
}

// NewRegistrationService creates a new registration service with all dependencies
func NewRegistrationService(client Client, config *RegistrationConfig) *RegistrationService {
	if config == nil {
		config = NewDefaultRegistrationConfig()
	}

	enhancedClient := NewEnhancedClient(client, nil)
	transactionService := NewTransactionService(client, nil)
	quoteService := NewQuoteService(client, nil)

	return &RegistrationService{
		client:             client,
		enhancedClient:     enhancedClient,
		transactionService: transactionService,
		quoteService:       quoteService,
		config:             config,
	}
}

// NewDefaultRegistrationConfig returns default registration configuration
func NewDefaultRegistrationConfig() *RegistrationConfig {
	return &RegistrationConfig{
		DefaultTimeout:         60 * time.Second,
		EnableValidation:       true,
		EnableQuoteChecking:    true,
		WaitForConfirmation:    true,
		MaxRetries:             3,
		RetryDelay:             5 * time.Second,
		RequireQuoteValidation: false,
		MaxMemoLength:          1024,
		CheckDuplicates:        true,
		EnableBatching:         false,
		BatchSize:              10,
		BatchTimeout:           30 * time.Second,
	}
}

// MemoRegistrationRequest represents a comprehensive memo registration request
type MemoRegistrationRequest struct {
	// Core memo information
	Memo    string `json:"memo"`
	ChainID string `json:"chain_id,omitempty"`

	// Quote validation (optional)
	ValidateQuote bool              `json:"validate_quote"`
	SwapDetails   *SwapQuoteRequest `json:"swap_details,omitempty"`

	// Transaction preferences
	Priority            TransactionPriority `json:"priority"`
	Timeout             time.Duration       `json:"timeout,omitempty"`
	WaitForConfirmation bool                `json:"wait_for_confirmation"`

	// Validation preferences
	SkipValidation  bool            `json:"skip_validation"`
	ValidationLevel ValidationLevel `json:"validation_level"`

	// User context
	UserAddress string            `json:"user_address,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// TransactionPriority represents transaction priority levels
type TransactionPriority int

const (
	PriorityLow TransactionPriority = iota
	PriorityNormal
	PriorityHigh
	PriorityUrgent
)

func (tp TransactionPriority) String() string {
	switch tp {
	case PriorityLow:
		return "LOW"
	case PriorityNormal:
		return "NORMAL"
	case PriorityHigh:
		return "HIGH"
	case PriorityUrgent:
		return "URGENT"
	default:
		return "NORMAL"
	}
}

// MemoRegistrationResponse represents the comprehensive response from memo registration
type MemoRegistrationResponse struct {
	// Registration result
	Success   bool   `json:"success"`
	Reference string `json:"reference,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`

	// Transaction details
	TxHash      string `json:"tx_hash,omitempty"`
	BlockHeight int64  `json:"block_height,omitempty"`

	// Quote validation results (if performed)
	QuoteValidation *QuoteValidationResult `json:"quote_validation,omitempty"`

	// Full transaction result
	TransactionResult *RegisterMemoResult `json:"transaction_result,omitempty"`

	// Usage information
	Instructions *UsageInstructions `json:"instructions,omitempty"`

	// Error information (if failed)
	Error *RegistrationError `json:"error,omitempty"`

	// Performance metrics
	Metrics RegistrationMetrics `json:"metrics"`
}

// QuoteValidationResult represents quote validation results
type QuoteValidationResult struct {
	Valid           bool                `json:"valid"`
	SwapQuote       *EnhancedSwapQuote  `json:"swap_quote,omitempty"`
	AssetValidation *SwapPairValidation `json:"asset_validation,omitempty"`
	Warnings        []string            `json:"warnings,omitempty"`
	Errors          []string            `json:"errors,omitempty"`
}

// UsageInstructions provides instructions for using the registered memo
type UsageInstructions struct {
	Reference       string        `json:"reference"`
	ValidUntil      time.Time     `json:"valid_until"`
	UsageSteps      []string      `json:"usage_steps"`
	RouterAddress   string        `json:"router_address,omitempty"`
	MinAmount       cosmos.Uint   `json:"min_amount,omitempty"`
	MaxAmount       cosmos.Uint   `json:"max_amount,omitempty"`
	EstimatedTime   time.Duration `json:"estimated_time,omitempty"`
	WarningsAndTips []string      `json:"warnings_and_tips,omitempty"`
}

// RegistrationError represents registration-specific errors
type RegistrationError struct {
	Code                RegistrationErrorCode `json:"code"`
	Message             string                `json:"message"`
	Details             string                `json:"details,omitempty"`
	SuggestedFix        string                `json:"suggested_fix,omitempty"`
	UserFriendlyMessage string                `json:"user_friendly_message,omitempty"`
	Retryable           bool                  `json:"retryable"`
}

// RegistrationErrorCode represents specific registration error types
type RegistrationErrorCode int

const (
	RegErrorInvalidMemo RegistrationErrorCode = iota + 3000
	RegErrorMemoTooLong
	RegErrorInvalidAssets
	RegErrorQuoteValidationFailed
	RegErrorTransactionFailed
	RegErrorNetworkUnavailable
	RegErrorRateLimited
	RegErrorDuplicateRegistration
	RegErrorInsufficientBalance
	RegErrorChainNotSupported
)

// RegistrationMetrics provides detailed metrics about the registration process
type RegistrationMetrics struct {
	StartTime            time.Time     `json:"start_time"`
	EndTime              time.Time     `json:"end_time"`
	TotalDuration        time.Duration `json:"total_duration"`
	ValidationDuration   time.Duration `json:"validation_duration,omitempty"`
	QuoteDuration        time.Duration `json:"quote_duration,omitempty"`
	SubmissionDuration   time.Duration `json:"submission_duration,omitempty"`
	ConfirmationDuration time.Duration `json:"confirmation_duration,omitempty"`
	RetryCount           int           `json:"retry_count"`
}

// RegisterMemo registers a memo with comprehensive validation and handling
func (rs *RegistrationService) RegisterMemo(ctx context.Context, req *MemoRegistrationRequest) (*MemoRegistrationResponse, error) {
	startTime := time.Now()

	// Initialize response
	response := &MemoRegistrationResponse{
		Metrics: RegistrationMetrics{
			StartTime: startTime,
		},
	}

	// Apply defaults and validate request
	if err := rs.validateAndPrepareRequest(req); err != nil {
		response.Error = rs.mapRegistrationError(RegErrorInvalidMemo, err)
		return response, err
	}

	// Set timeout context
	timeout := req.Timeout
	if timeout == 0 {
		timeout = rs.config.DefaultTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Perform quote validation if requested
	if req.ValidateQuote && req.SwapDetails != nil {
		if err := rs.validateQuote(reqCtx, req, response); err != nil {
			if rs.config.RequireQuoteValidation {
				response.Error = rs.mapRegistrationError(RegErrorQuoteValidationFailed, err)
				return response, err
			}
			// Continue with warning if validation is not required
		}
	}

	// Prepare transaction request
	txRequest := &RegisterMemoRequest{
		Memo:                req.Memo,
		ChainID:             req.ChainID,
		Timeout:             timeout,
		WaitForConfirmation: req.WaitForConfirmation || rs.config.WaitForConfirmation,
		ValidateMemo:        rs.config.EnableValidation && !req.SkipValidation,
		CheckCollisions:     rs.config.CheckDuplicates,
		MaxRetries:          rs.config.MaxRetries,
		RetryDelay:          rs.config.RetryDelay,
		Metadata:            req.Metadata,
	}

	// Register the memo
	submissionStart := time.Now()
	txResult, err := rs.transactionService.RegisterMemo(reqCtx, txRequest)
	response.Metrics.SubmissionDuration = time.Since(submissionStart)

	if err != nil {
		response.Error = rs.mapRegistrationError(RegErrorTransactionFailed, err)
		return response, err
	}

	// Update response with results
	response.Success = true
	response.Reference = txResult.Reference
	response.ExpiresAt = txResult.ExpiresAt
	response.TxHash = txResult.TxHash
	response.BlockHeight = txResult.BlockHeight
	response.TransactionResult = txResult
	response.Metrics.RetryCount = txResult.Metrics.RetryCount

	// Generate usage instructions
	response.Instructions = rs.generateUsageInstructions(req, txResult)

	// Set confirmation duration if available
	if txResult.Confirmation != nil {
		response.Metrics.ConfirmationDuration = txResult.Confirmation.ConfirmationTime.Sub(submissionStart)
	}

	// Calculate total duration
	response.Metrics.EndTime = time.Now()
	response.Metrics.TotalDuration = response.Metrics.EndTime.Sub(startTime)

	return response, nil
}

// validateAndPrepareRequest validates and prepares the registration request
func (rs *RegistrationService) validateAndPrepareRequest(req *MemoRegistrationRequest) error {
	if req == nil {
		return fmt.Errorf("request cannot be nil")
	}

	if req.Memo == "" {
		return fmt.Errorf("memo cannot be empty")
	}

	if len(req.Memo) > rs.config.MaxMemoLength {
		return fmt.Errorf("memo too long: %d characters (max %d)", len(req.Memo), rs.config.MaxMemoLength)
	}

	// Set defaults
	if req.ValidationLevel == 0 {
		req.ValidationLevel = ValidationLevelStandard
	}

	if req.Priority == 0 {
		req.Priority = PriorityNormal
	}

	return nil
}

// validateQuote performs quote validation if swap details are provided
func (rs *RegistrationService) validateQuote(ctx context.Context, req *MemoRegistrationRequest,
	response *MemoRegistrationResponse,
) error {
	quoteStart := time.Now()
	defer func() {
		response.Metrics.QuoteDuration = time.Since(quoteStart)
	}()

	// Validate assets
	pairValidation, err := rs.enhancedClient.ValidateSwapPair(ctx,
		req.SwapDetails.FromAsset, req.SwapDetails.ToAsset)
	if err != nil {
		return fmt.Errorf("failed to validate asset pair: %w", err)
	}

	// Get enhanced quote
	enhancedReq := &EnhancedSwapQuoteRequest{
		SwapQuoteRequest: *req.SwapDetails,
	}

	quote, err := rs.enhancedClient.GetEnhancedSwapQuote(ctx, enhancedReq)
	if err != nil {
		return fmt.Errorf("failed to get swap quote: %w", err)
	}

	// Prepare validation result
	validation := &QuoteValidationResult{
		Valid:           pairValidation.Valid && len(quote.Errors) == 0,
		SwapQuote:       quote,
		AssetValidation: pairValidation,
		Warnings:        make([]string, 0),
		Errors:          make([]string, 0),
	}

	// Collect warnings
	for _, warning := range pairValidation.Warnings {
		validation.Warnings = append(validation.Warnings, warning.Message)
	}
	if quote.Warning != "" {
		validation.Warnings = append(validation.Warnings, quote.Warning)
	}

	// Collect errors
	for _, err := range pairValidation.Errors {
		validation.Errors = append(validation.Errors, err.Message)
	}
	validation.Errors = append(validation.Errors, quote.Errors...)

	response.QuoteValidation = validation

	if !validation.Valid {
		return fmt.Errorf("quote validation failed: %v", validation.Errors)
	}

	return nil
}

// generateUsageInstructions generates usage instructions for the registered memo
func (rs *RegistrationService) generateUsageInstructions(req *MemoRegistrationRequest,
	result *RegisterMemoResult,
) *UsageInstructions {
	instructions := &UsageInstructions{
		Reference:  result.Reference,
		ValidUntil: time.Unix(result.ExpiresAt, 0),
		UsageSteps: []string{
			fmt.Sprintf("Use reference: %s", result.Reference),
			"Send your transaction to the THORChain router",
			"Include the reference in your transaction memo",
		},
		WarningsAndTips: []string{
			"Double-check the reference before sending",
			"Ensure you send to the correct router address",
			fmt.Sprintf("Registration expires at %s", time.Unix(result.ExpiresAt, 0).Format(time.RFC3339)),
		},
	}

	// Add swap-specific instructions if quote validation was performed
	if req.SwapDetails != nil {
		instructions.UsageSteps = append(instructions.UsageSteps,
			fmt.Sprintf("Send %s %s", req.SwapDetails.Amount.String(), req.SwapDetails.FromAsset.String()),
			fmt.Sprintf("Receive approximately %s", req.SwapDetails.ToAsset.String()),
		)

		instructions.MinAmount = cosmos.NewUint(1000000)                       // Default minimum
		instructions.MaxAmount = req.SwapDetails.Amount.Mul(cosmos.NewUint(2)) // 2x as max
		instructions.EstimatedTime = 5 * time.Minute                           // Default estimate
	}

	return instructions
}

// mapRegistrationError maps errors to registration-specific errors
func (rs *RegistrationService) mapRegistrationError(code RegistrationErrorCode, err error) *RegistrationError {
	regErr := &RegistrationError{
		Code:      code,
		Details:   err.Error(),
		Retryable: false,
	}

	switch code {
	case RegErrorInvalidMemo:
		regErr.Message = "Invalid memo format"
		regErr.UserFriendlyMessage = "The memo you provided is not in the correct format"
		regErr.SuggestedFix = "Check the memo format and try again"

	case RegErrorMemoTooLong:
		regErr.Message = "Memo exceeds maximum length"
		regErr.UserFriendlyMessage = "Your memo is too long"
		regErr.SuggestedFix = fmt.Sprintf("Shorten your memo to %d characters or less", rs.config.MaxMemoLength)

	case RegErrorQuoteValidationFailed:
		regErr.Message = "Quote validation failed"
		regErr.UserFriendlyMessage = "The swap details could not be validated"
		regErr.SuggestedFix = "Check your asset symbols and amounts"

	case RegErrorTransactionFailed:
		regErr.Message = "Transaction submission failed"
		regErr.UserFriendlyMessage = "Failed to register your memo on the network"
		regErr.SuggestedFix = "Check network connectivity and try again"
		regErr.Retryable = true

	case RegErrorNetworkUnavailable:
		regErr.Message = "Network unavailable"
		regErr.UserFriendlyMessage = "The THORChain network is currently unavailable"
		regErr.SuggestedFix = "Please try again later"
		regErr.Retryable = true

	default:
		regErr.Message = "Unknown error"
		regErr.UserFriendlyMessage = "An unexpected error occurred"
		regErr.SuggestedFix = "Please try again or contact support"
	}

	return regErr
}

// GetRegistrationStatus gets the status of a memo registration
func (rs *RegistrationService) GetRegistrationStatus(ctx context.Context, reference string) (*RegistrationStatus, error) {
	// Query the registered memo
	memo, err := rs.client.QueryRegisteredMemo(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("failed to query memo: %w", err)
	}

	status := &RegistrationStatus{
		Reference:    reference,
		Status:       "ACTIVE",
		RegisteredAt: time.Unix(memo.RegisteredAt, 0),
		ExpiresAt:    time.Unix(memo.ExpiresAt, 0),
		Memo:         memo.Memo,
		ChainID:      memo.ChainId,
		IsExpired:    time.Now().Unix() > memo.ExpiresAt,
	}

	if status.IsExpired {
		status.Status = "EXPIRED"
	}

	return status, nil
}

// RegistrationStatus represents the current status of a memo registration
type RegistrationStatus struct {
	Reference    string    `json:"reference"`
	Status       string    `json:"status"`
	RegisteredAt time.Time `json:"registered_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Memo         string    `json:"memo"`
	ChainID      string    `json:"chain_id"`
	IsExpired    bool      `json:"is_expired"`
	UsageCount   int       `json:"usage_count,omitempty"`
}

// ListActiveRegistrations lists all active registrations (if supported)
func (rs *RegistrationService) ListActiveRegistrations(ctx context.Context, limit, offset int) ([]*RegistrationStatus, error) {
	// This would query active memos from the keeper
	// For now, return empty list as this requires keeper access
	return []*RegistrationStatus{}, nil
}

// EstimateRegistrationCost estimates the cost of registering a memo
func (rs *RegistrationService) EstimateRegistrationCost(ctx context.Context, req *MemoRegistrationRequest) (*RegistrationCostEstimate, error) {
	estimate := &RegistrationCostEstimate{
		BaseFee:    cosmos.NewUint(1000000), // 1 RUNE base fee
		NetworkFee: cosmos.NewUint(500000),  // 0.5 RUNE network fee
		Priority:   req.Priority,
	}

	// Adjust based on priority
	switch req.Priority {
	case PriorityHigh:
		estimate.PriorityFee = cosmos.NewUint(500000) // 0.5 RUNE
	case PriorityUrgent:
		estimate.PriorityFee = cosmos.NewUint(1000000) // 1 RUNE
	}

	// Calculate total
	estimate.TotalFee = estimate.BaseFee.Add(estimate.NetworkFee).Add(estimate.PriorityFee)

	// Add estimated time
	switch req.Priority {
	case PriorityLow:
		estimate.EstimatedTime = 10 * time.Minute
	case PriorityNormal:
		estimate.EstimatedTime = 5 * time.Minute
	case PriorityHigh:
		estimate.EstimatedTime = 2 * time.Minute
	case PriorityUrgent:
		estimate.EstimatedTime = 1 * time.Minute
	}

	return estimate, nil
}

// RegistrationCostEstimate represents cost estimation for memo registration
type RegistrationCostEstimate struct {
	BaseFee       cosmos.Uint         `json:"base_fee"`
	NetworkFee    cosmos.Uint         `json:"network_fee"`
	PriorityFee   cosmos.Uint         `json:"priority_fee"`
	TotalFee      cosmos.Uint         `json:"total_fee"`
	Priority      TransactionPriority `json:"priority"`
	EstimatedTime time.Duration       `json:"estimated_time"`
	Currency      string              `json:"currency"`
}

// GetServiceHealth returns the health status of the registration service
func (rs *RegistrationService) GetServiceHealth(ctx context.Context) (*ServiceHealth, error) {
	health := &ServiceHealth{
		Service:      "RegistrationService",
		Status:       "HEALTHY",
		Version:      "1.0.0",
		Timestamp:    time.Now(),
		Dependencies: make(map[string]string),
	}

	// Check client connectivity
	if err := rs.client.Ping(ctx); err != nil {
		health.Status = "DEGRADED"
		health.Dependencies["thornode_client"] = "UNHEALTHY"
		health.Issues = append(health.Issues, fmt.Sprintf("Client ping failed: %s", err.Error()))
	} else {
		health.Dependencies["thornode_client"] = "HEALTHY"
	}

	// Check network constants availability
	if _, err := rs.client.GetConstants(ctx); err != nil {
		health.Status = "DEGRADED"
		health.Dependencies["network_constants"] = "UNHEALTHY"
		health.Issues = append(health.Issues, fmt.Sprintf("Constants unavailable: %s", err.Error()))
	} else {
		health.Dependencies["network_constants"] = "HEALTHY"
	}

	if len(health.Issues) > 2 {
		health.Status = "UNHEALTHY"
	}

	return health, nil
}

// ServiceHealth represents the health status of the registration service
type ServiceHealth struct {
	Service      string            `json:"service"`
	Status       string            `json:"status"`
	Version      string            `json:"version"`
	Timestamp    time.Time         `json:"timestamp"`
	Dependencies map[string]string `json:"dependencies"`
	Issues       []string          `json:"issues,omitempty"`
}
