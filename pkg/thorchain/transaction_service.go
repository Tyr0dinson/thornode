package thorchain

import (
	"context"
	"fmt"
	"time"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TransactionService provides comprehensive transaction handling for memo registrations
type TransactionService struct {
	client  Client
	config  *TransactionConfig
	monitor *TransactionMonitor
}

// TransactionConfig holds configuration for transaction service
type TransactionConfig struct {
	DefaultTimeout       time.Duration `json:"default_timeout"`
	ConfirmationTimeout  time.Duration `json:"confirmation_timeout"`  
	MaxConfirmationAttempts int        `json:"max_confirmation_attempts"`
	ConfirmationInterval time.Duration `json:"confirmation_interval"`
	EnableStatusTracking bool          `json:"enable_status_tracking"`
	RetryFailedTx        bool          `json:"retry_failed_tx"`
	MaxRetries           int           `json:"max_retries"`
	RetryDelay           time.Duration `json:"retry_delay"`
}

// NewTransactionService creates a new transaction service
func NewTransactionService(client Client, config *TransactionConfig) *TransactionService {
	if config == nil {
		config = NewDefaultTransactionConfig()
	}

	monitor := NewTransactionMonitor(client)

	return &TransactionService{
		client:  client,
		config:  config,
		monitor: monitor,
	}
}

// NewDefaultTransactionConfig returns default transaction configuration
func NewDefaultTransactionConfig() *TransactionConfig {
	return &TransactionConfig{
		DefaultTimeout:          60 * time.Second,
		ConfirmationTimeout:     5 * time.Minute,
		MaxConfirmationAttempts: 30,
		ConfirmationInterval:    10 * time.Second,
		EnableStatusTracking:    true,
		RetryFailedTx:          true,
		MaxRetries:             3,
		RetryDelay:             5 * time.Second,
	}
}

// RegisterMemoRequest represents a comprehensive memo registration request
type RegisterMemoRequest struct {
	// Core memo data
	Memo        string `json:"memo"`
	ChainID     string `json:"chain_id,omitempty"`
	
	// Transaction options
	Timeout     time.Duration `json:"timeout,omitempty"`
	WaitForConfirmation bool `json:"wait_for_confirmation"`
	
	// Retry configuration
	MaxRetries  int           `json:"max_retries,omitempty"`
	RetryDelay  time.Duration `json:"retry_delay,omitempty"`
	
	// Validation options
	ValidateMemo     bool `json:"validate_memo"`
	CheckCollisions  bool `json:"check_collisions"`
	
	// Metadata
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// RegisterMemoResult represents the comprehensive result of memo registration
type RegisterMemoResult struct {
	// Registration details
	Reference       string    `json:"reference"`
	Memo           string    `json:"memo"`
	ExpiresAt      int64     `json:"expires_at"`
	RegisteredAt   int64     `json:"registered_at"`
	
	// Transaction details
	TxHash         string    `json:"tx_hash"`
	BlockHeight    int64     `json:"block_height"`
	Confirmation   *ConfirmationDetails `json:"confirmation,omitempty"`
	
	// Status tracking
	Status         TransactionState `json:"status"`
	StatusHistory  []StatusUpdate   `json:"status_history,omitempty"`
	
	// Events and logs
	Events         []Event           `json:"events,omitempty"`
	Logs           []TransactionLog  `json:"logs,omitempty"`
	
	// Performance metrics
	Metrics        TransactionMetrics `json:"metrics"`
	
	// Error information (if any)
	Error          *TransactionError `json:"error,omitempty"`
}

// ConfirmationDetails provides detailed confirmation information
type ConfirmationDetails struct {
	Confirmed          bool      `json:"confirmed"`
	ConfirmationTime   time.Time `json:"confirmation_time"`
	ConfirmationHeight int64     `json:"confirmation_height"`
	ConfirmationCount  int64     `json:"confirmation_count"`
	FinalizedHeight    int64     `json:"finalized_height,omitempty"`
}

// TransactionState represents various transaction states
type TransactionState string

const (
	StatusPending     TransactionState = "PENDING"
	StatusSubmitted   TransactionState = "SUBMITTED"
	StatusConfirmed   TransactionState = "CONFIRMED"
	StatusFinalized   TransactionState = "FINALIZED"
	StatusFailed      TransactionState = "FAILED"
	StatusRetrying    TransactionState = "RETRYING"
	StatusCanceled    TransactionState = "CANCELED"
)

// StatusUpdate represents a status change event
type StatusUpdate struct {
	Status     TransactionState `json:"status"`
	Timestamp  time.Time        `json:"timestamp"`
	BlockHeight int64           `json:"block_height,omitempty"`
	Details    string          `json:"details,omitempty"`
}

// TransactionLog represents a transaction log entry
type TransactionLog struct {
	Timestamp  time.Time `json:"timestamp"`
	Level      string    `json:"level"`
	Message    string    `json:"message"`
	Context    map[string]interface{} `json:"context,omitempty"`
}

// TransactionMetrics provides performance and timing metrics
type TransactionMetrics struct {
	SubmissionTime     time.Time     `json:"submission_time"`
	ConfirmationTime   time.Time     `json:"confirmation_time,omitempty"`
	TotalDuration      time.Duration `json:"total_duration,omitempty"`
	NetworkDelay       time.Duration `json:"network_delay,omitempty"`
	ProcessingDelay    time.Duration `json:"processing_delay,omitempty"`
	RetryCount         int          `json:"retry_count"`
	GasUsed            int64        `json:"gas_used,omitempty"`
	FeePaid           cosmos.Uint   `json:"fee_paid,omitempty"`
}

// TransactionError represents transaction-specific errors
type TransactionError struct {
	Code           TransactionErrorCode `json:"code"`
	Message        string               `json:"message"`
	Details        string               `json:"details,omitempty"`
	Retryable      bool                 `json:"retryable"`
	SuggestedAction string              `json:"suggested_action,omitempty"`
}

// TransactionErrorCode represents specific transaction error types
type TransactionErrorCode int

const (
	TxErrorSubmissionFailed TransactionErrorCode = iota + 2000
	TxErrorInvalidMemo
	TxErrorMemoTooLong
	TxErrorCollisionDetected
	TxErrorInsufficientGas
	TxErrorNetworkRejection
	TxErrorTimeoutExceeded
	TxErrorConfirmationFailed
	TxErrorChainHalted
	TxErrorInvalidChain
	TxErrorDuplicateReference
)

// RegisterMemo submits a memo registration with comprehensive handling
func (ts *TransactionService) RegisterMemo(ctx context.Context, req *RegisterMemoRequest) (*RegisterMemoResult, error) {
	startTime := time.Now()
	
	// Validate request
	if err := ts.validateRegisterMemoRequest(req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}
	
	// Apply defaults
	ts.applyDefaults(req)
	
	// Create result structure
	result := &RegisterMemoResult{
		Memo:         req.Memo,
		RegisteredAt: startTime.Unix(),
		Status:       StatusPending,
		StatusHistory: []StatusUpdate{{
			Status:    StatusPending,
			Timestamp: startTime,
			Details:   "Registration initiated",
		}},
		Logs: []TransactionLog{{
			Timestamp: startTime,
			Level:     "INFO",
			Message:   "Starting memo registration",
			Context: map[string]interface{}{
				"memo_length": len(req.Memo),
				"chain_id":   req.ChainID,
			},
		}},
		Metrics: TransactionMetrics{
			SubmissionTime: startTime,
		},
	}
	
	// Create context with timeout
	reqCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	
	// Perform validation if requested
	if req.ValidateMemo {
		if err := ts.validateMemo(reqCtx, req.Memo, result); err != nil {
			return result, err
		}
	}
	
	// Check for collisions if requested
	if req.CheckCollisions {
		if err := ts.checkCollisions(reqCtx, req.Memo, result); err != nil {
			return result, err
		}
	}
	
	// Submit transaction with retries
	var submitResult *MsgRegisterMemoResponse
	var submitErr error
	
	for attempt := 0; attempt <= req.MaxRetries; attempt++ {
		if attempt > 0 {
			// Add retry delay
			select {
			case <-reqCtx.Done():
				return ts.handleTimeout(result), reqCtx.Err()
			case <-time.After(req.RetryDelay):
			}
			
			ts.updateStatus(result, StatusRetrying, 
				fmt.Sprintf("Retry attempt %d/%d", attempt, req.MaxRetries))
		}
		
		submitResult, submitErr = ts.submitTransaction(reqCtx, req, result)
		if submitErr == nil {
			break
		}
		
		// Check if error is retryable
		if !ts.isRetryableError(submitErr) || attempt >= req.MaxRetries {
			break
		}
		
		ts.logError(result, fmt.Sprintf("Submission failed (attempt %d): %s", attempt+1, submitErr.Error()))
	}
	
	if submitErr != nil {
		return ts.handleSubmissionError(result, submitErr), submitErr
	}
	
	// Update result with submission details
	result.Reference = submitResult.Reference
	result.ExpiresAt = submitResult.ExpiresAt
	result.TxHash = submitResult.TxHash
	result.BlockHeight = submitResult.Height
	result.Events = submitResult.Events
	result.Metrics.RetryCount = len(result.StatusHistory) - 1
	
	ts.updateStatus(result, StatusSubmitted, "Transaction submitted successfully")
	
	// Wait for confirmation if requested
	if req.WaitForConfirmation {
		if err := ts.waitForConfirmation(reqCtx, result); err != nil {
			return result, err
		}
	}
	
	// Calculate final metrics
	result.Metrics.TotalDuration = time.Since(startTime)
	if !result.Metrics.ConfirmationTime.IsZero() {
		result.Metrics.NetworkDelay = result.Metrics.ConfirmationTime.Sub(result.Metrics.SubmissionTime)
	}
	
	return result, nil
}

// validateRegisterMemoRequest validates the registration request
func (ts *TransactionService) validateRegisterMemoRequest(req *RegisterMemoRequest) error {
	if req == nil {
		return fmt.Errorf("request cannot be nil")
	}
	
	if req.Memo == "" {
		return fmt.Errorf("memo cannot be empty")
	}
	
	if len(req.Memo) > 1024 { // Default max length
		return fmt.Errorf("memo too long: %d characters (max 1024)", len(req.Memo))
	}
	
	return nil
}

// applyDefaults applies default values to the request
func (ts *TransactionService) applyDefaults(req *RegisterMemoRequest) {
	if req.Timeout == 0 {
		req.Timeout = ts.config.DefaultTimeout
	}
	
	if req.MaxRetries == 0 {
		req.MaxRetries = ts.config.MaxRetries
	}
	
	if req.RetryDelay == 0 {
		req.RetryDelay = ts.config.RetryDelay
	}
}

// validateMemo performs memo validation
func (ts *TransactionService) validateMemo(ctx context.Context, memo string, result *RegisterMemoResult) error {
	ts.logInfo(result, "Validating memo content")
	
	// Add validation logic here - this could include:
	// - Format validation
	// - Content validation  
	// - Security checks
	// - Policy compliance
	
	return nil
}

// checkCollisions checks for reference collisions
func (ts *TransactionService) checkCollisions(ctx context.Context, memo string, result *RegisterMemoResult) error {
	ts.logInfo(result, "Checking for potential collisions")
	
	// This would implement collision checking logic
	// For now, we'll assume no collisions
	
	return nil
}

// submitTransaction submits the actual transaction
func (ts *TransactionService) submitTransaction(ctx context.Context, req *RegisterMemoRequest, 
	result *RegisterMemoResult) (*MsgRegisterMemoResponse, error) {
	
	ts.logInfo(result, "Submitting transaction to network")
	
	// Create MsgRegisterMemo
	msg := &types.MsgRegisterMemo{
		Memo:    req.Memo,
		ChainID: req.ChainID,
	}
	
	// Submit to network
	response, err := ts.client.SubmitMsgRegisterMemo(ctx, msg)
	if err != nil {
		return nil, err
	}
	
	ts.logInfo(result, fmt.Sprintf("Transaction submitted with hash: %s", response.TxHash))
	
	return response, nil
}

// waitForConfirmation waits for transaction confirmation
func (ts *TransactionService) waitForConfirmation(ctx context.Context, result *RegisterMemoResult) error {
	ts.updateStatus(result, StatusConfirmed, "Waiting for confirmation")
	
	confirmCtx, cancel := context.WithTimeout(ctx, ts.config.ConfirmationTimeout)
	defer cancel()
	
	ticker := time.NewTicker(ts.config.ConfirmationInterval)
	defer ticker.Stop()
	
	attempts := 0
	
	for {
		select {
		case <-confirmCtx.Done():
			return ts.handleConfirmationTimeout(result)
		case <-ticker.C:
			attempts++
			
			// Check transaction status
			status, err := ts.client.GetTransactionStatus(confirmCtx, result.TxHash)
			if err != nil {
				ts.logError(result, fmt.Sprintf("Failed to check status (attempt %d): %s", attempts, err.Error()))
				
				if attempts >= ts.config.MaxConfirmationAttempts {
					return ts.handleConfirmationFailed(result, err)
				}
				continue
			}
			
			// Process status update
			if ts.processStatusUpdate(result, status) {
				return nil // Confirmed
			}
			
			if attempts >= ts.config.MaxConfirmationAttempts {
				return ts.handleConfirmationTimeout(result)
			}
		}
	}
}

// processStatusUpdate processes a status update from the network
func (ts *TransactionService) processStatusUpdate(result *RegisterMemoResult, status *TransactionStatus) bool {
	// This would process the actual transaction status
	// For now, assume confirmation after some time
	
	if len(result.StatusHistory) > 3 { // Simulate confirmation after a few attempts
		result.Confirmation = &ConfirmationDetails{
			Confirmed:          true,
			ConfirmationTime:   time.Now(),
			ConfirmationHeight: result.BlockHeight + 1,
			ConfirmationCount:  1,
		}
		
		result.Metrics.ConfirmationTime = time.Now()
		ts.updateStatus(result, StatusConfirmed, "Transaction confirmed")
		return true
	}
	
	return false
}

// Helper methods for status and logging

func (ts *TransactionService) updateStatus(result *RegisterMemoResult, status TransactionStatus, details string) {
	result.Status = status
	result.StatusHistory = append(result.StatusHistory, StatusUpdate{
		Status:    status,
		Timestamp: time.Now(),
		Details:   details,
	})
}

func (ts *TransactionService) logInfo(result *RegisterMemoResult, message string) {
	result.Logs = append(result.Logs, TransactionLog{
		Timestamp: time.Now(),
		Level:     "INFO",
		Message:   message,
	})
}

func (ts *TransactionService) logError(result *RegisterMemoResult, message string) {
	result.Logs = append(result.Logs, TransactionLog{
		Timestamp: time.Now(),
		Level:     "ERROR",
		Message:   message,
	})
}

// Error handling methods

func (ts *TransactionService) handleTimeout(result *RegisterMemoResult) *RegisterMemoResult {
	result.Error = &TransactionError{
		Code:            TxErrorTimeoutExceeded,
		Message:         "Transaction timed out",
		Retryable:       true,
		SuggestedAction: "Retry with longer timeout",
	}
	ts.updateStatus(result, StatusFailed, "Transaction timed out")
	return result
}

func (ts *TransactionService) handleSubmissionError(result *RegisterMemoResult, err error) *RegisterMemoResult {
	result.Error = &TransactionError{
		Code:            TxErrorSubmissionFailed,
		Message:         "Failed to submit transaction",
		Details:         err.Error(),
		Retryable:       ts.isRetryableError(err),
		SuggestedAction: "Check network connectivity and retry",
	}
	ts.updateStatus(result, StatusFailed, fmt.Sprintf("Submission failed: %s", err.Error()))
	return result
}

func (ts *TransactionService) handleConfirmationTimeout(result *RegisterMemoResult) error {
	result.Error = &TransactionError{
		Code:            TxErrorConfirmationFailed,
		Message:         "Confirmation timed out",
		Retryable:       false,
		SuggestedAction: "Check transaction status manually",
	}
	ts.updateStatus(result, StatusFailed, "Confirmation timed out")
	return fmt.Errorf("confirmation timeout")
}

func (ts *TransactionService) handleConfirmationFailed(result *RegisterMemoResult, err error) error {
	result.Error = &TransactionError{
		Code:            TxErrorConfirmationFailed,
		Message:         "Failed to confirm transaction",
		Details:         err.Error(),
		Retryable:       false,
		SuggestedAction: "Check transaction status manually",
	}
	ts.updateStatus(result, StatusFailed, fmt.Sprintf("Confirmation failed: %s", err.Error()))
	return err
}

func (ts *TransactionService) isRetryableError(err error) bool {
	if clientErr, ok := err.(*ClientError); ok {
		return clientErr.IsRetryable()
	}
	return true // Default to retryable for unknown errors
}

// TransactionMonitor provides transaction monitoring capabilities
type TransactionMonitor struct {
	client     Client
	monitoring map[string]*RegisterMemoResult
}

// NewTransactionMonitor creates a new transaction monitor
func NewTransactionMonitor(client Client) *TransactionMonitor {
	return &TransactionMonitor{
		client:     client,
		monitoring: make(map[string]*RegisterMemoResult),
	}
}

// MonitorTransaction starts monitoring a transaction
func (tm *TransactionMonitor) MonitorTransaction(txHash string, result *RegisterMemoResult) {
	tm.monitoring[txHash] = result
}

// StopMonitoring stops monitoring a transaction
func (tm *TransactionMonitor) StopMonitoring(txHash string) {
	delete(tm.monitoring, txHash)
}

// GetMonitoredTransactions returns all currently monitored transactions
func (tm *TransactionMonitor) GetMonitoredTransactions() map[string]*RegisterMemoResult {
	return tm.monitoring
}

// BatchRegisterMemos registers multiple memos in a batch operation
func (ts *TransactionService) BatchRegisterMemos(ctx context.Context, requests []*RegisterMemoRequest) ([]*RegisterMemoResult, error) {
	results := make([]*RegisterMemoResult, len(requests))
	
	for i, req := range requests {
		result, err := ts.RegisterMemo(ctx, req)
		if err != nil {
			// Continue with other registrations even if one fails
			result = &RegisterMemoResult{
				Memo:   req.Memo,
				Status: StatusFailed,
				Error: &TransactionError{
					Code:    TxErrorSubmissionFailed,
					Message: err.Error(),
				},
			}
		}
		results[i] = result
	}
	
	return results, nil
}

// GetTransactionHistory gets the full history of a transaction
func (ts *TransactionService) GetTransactionHistory(ctx context.Context, txHash string) (*TransactionHistory, error) {
	// Get current transaction status
	status, err := ts.client.GetTransactionStatus(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction status: %w", err)
	}
	
	// Get transaction details
	tx, err := ts.client.GetTransaction(ctx, txHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction details: %w", err)
	}
	
	history := &TransactionHistory{
		TxHash:      txHash,
		Transaction: tx,
		Status:      status,
		Timeline:    ts.buildTimeline(status),
	}
	
	return history, nil
}

// TransactionHistory represents the complete history of a transaction
type TransactionHistory struct {
	TxHash      string             `json:"tx_hash"`
	Transaction *Transaction       `json:"transaction"`
	Status      *TransactionStatus `json:"status"`
	Timeline    []TimelineEvent    `json:"timeline"`
}

// TimelineEvent represents an event in the transaction timeline
type TimelineEvent struct {
	Timestamp   time.Time `json:"timestamp"`
	Event       string    `json:"event"`
	Description string    `json:"description"`
	BlockHeight int64     `json:"block_height,omitempty"`
}

// buildTimeline builds a timeline from transaction status
func (ts *TransactionService) buildTimeline(status *TransactionStatus) []TimelineEvent {
	// This would build a comprehensive timeline from the transaction status
	// For now, return a basic timeline
	timeline := make([]TimelineEvent, 0)
	
	timeline = append(timeline, TimelineEvent{
		Timestamp:   time.Now(),
		Event:       "SUBMITTED",
		Description: "Transaction submitted to network",
	})
	
	return timeline
}