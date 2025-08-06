package thorchain

import (
	"context"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"
)

// ErrorMapper provides comprehensive error mapping for THORNode client operations
type ErrorMapper struct {
	config *ErrorMapperConfig
}

// ErrorMapperConfig holds configuration for error mapping
type ErrorMapperConfig struct {
	RetryableStatusCodes []int
	NetworkErrorRetries  int
	TimeoutRetries       int
	EnableDetailedErrors bool
}

// NewErrorMapper creates a new error mapper with default configuration
func NewErrorMapper(config *ErrorMapperConfig) *ErrorMapper {
	if config == nil {
		config = NewDefaultErrorMapperConfig()
	}

	return &ErrorMapper{
		config: config,
	}
}

// NewDefaultErrorMapperConfig returns default error mapper configuration
func NewDefaultErrorMapperConfig() *ErrorMapperConfig {
	return &ErrorMapperConfig{
		RetryableStatusCodes: []int{408, 429, 500, 502, 503, 504},
		NetworkErrorRetries:  3,
		TimeoutRetries:       2,
		EnableDetailedErrors: true,
	}
}

// MapError maps various error types to standardized THORNode client errors
func (em *ErrorMapper) MapError(err error, operation string) error {
	if err == nil {
		return nil
	}

	// Handle context errors
	if contextErr := em.mapContextError(err, operation); contextErr != nil {
		return contextErr
	}

	// Handle network errors
	if networkErr := em.mapNetworkError(err, operation); networkErr != nil {
		return networkErr
	}

	// Handle client errors
	if clientErr, ok := err.(*ClientError); ok {
		return em.enhanceClientError(clientErr, operation)
	}

	// Handle quote errors
	if quoteErr, ok := err.(*QuoteError); ok {
		return em.enhanceQuoteError(quoteErr, operation)
	}

	// Default mapping
	return NewClientError(ErrCodeInternalServer,
		fmt.Sprintf("unexpected error in %s", operation),
		err.Error(), err)
}

// mapContextError maps context-related errors
func (em *ErrorMapper) mapContextError(err error, operation string) error {
	switch err {
	case context.Canceled:
		return NewClientError(ErrCodeBadRequest,
			fmt.Sprintf("%s was canceled", operation),
			"request canceled by user", err)
	case context.DeadlineExceeded:
		return NewClientError(ErrCodeTimeout,
			fmt.Sprintf("%s timed out", operation),
			"operation exceeded deadline", err)
	}
	return nil
}

// mapNetworkError maps network-related errors
func (em *ErrorMapper) mapNetworkError(err error, operation string) error {
	errStr := strings.ToLower(err.Error())

	// DNS resolution errors
	if strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "dns") {
		return NewClientError(ErrCodeNetworkError,
			fmt.Sprintf("DNS resolution failed for %s", operation),
			"unable to resolve hostname", err)
	}

	// Connection refused
	if strings.Contains(errStr, "connection refused") {
		return NewClientError(ErrCodeServiceUnavailable,
			fmt.Sprintf("connection refused for %s", operation),
			"service is not available", err)
	}

	// Timeout errors
	if strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") {
		return NewClientError(ErrCodeTimeout,
			fmt.Sprintf("timeout during %s", operation),
			"operation timed out", err)
	}

	// TLS/SSL errors
	if strings.Contains(errStr, "tls") ||
		strings.Contains(errStr, "ssl") ||
		strings.Contains(errStr, "certificate") {
		return NewClientError(ErrCodeNetworkError,
			fmt.Sprintf("TLS error during %s", operation),
			"TLS/SSL connection error", err)
	}

	// Network unreachable
	if strings.Contains(errStr, "network unreachable") ||
		strings.Contains(errStr, "host unreachable") {
		return NewClientError(ErrCodeNetworkError,
			fmt.Sprintf("network unreachable for %s", operation),
			"network is unreachable", err)
	}

	// Check for specific network error types
	if netErr, ok := err.(net.Error); ok {
		if netErr.Timeout() {
			return NewClientError(ErrCodeTimeout,
				fmt.Sprintf("network timeout during %s", operation),
				"network operation timed out", err)
		}
		if netErr.Temporary() {
			return NewClientError(ErrCodeServiceUnavailable,
				fmt.Sprintf("temporary network error during %s", operation),
				"temporary network failure", err)
		}
	}

	// System call errors
	if opErr, ok := err.(*net.OpError); ok {
		if syscallErr, ok := opErr.Err.(*net.DNSError); ok && syscallErr.IsTimeout {
			return NewClientError(ErrCodeTimeout,
				fmt.Sprintf("DNS timeout during %s", operation),
				"DNS lookup timed out", err)
		}

		if sysErr, ok := opErr.Err.(syscall.Errno); ok {
			switch sysErr {
			case syscall.ECONNREFUSED:
				return NewClientError(ErrCodeServiceUnavailable,
					fmt.Sprintf("connection refused for %s", operation),
					"target service is not accepting connections", err)
			case syscall.ECONNRESET:
				return NewClientError(ErrCodeNetworkError,
					fmt.Sprintf("connection reset during %s", operation),
					"connection was reset by peer", err)
			case syscall.EHOSTUNREACH:
				return NewClientError(ErrCodeNetworkError,
					fmt.Sprintf("host unreachable for %s", operation),
					"destination host is unreachable", err)
			case syscall.ENETUNREACH:
				return NewClientError(ErrCodeNetworkError,
					fmt.Sprintf("network unreachable for %s", operation),
					"destination network is unreachable", err)
			}
		}
	}

	return nil
}

// enhanceClientError enhances existing client errors with additional context
func (em *ErrorMapper) enhanceClientError(clientErr *ClientError, operation string) error {
	enhancedErr := &ClientError{
		Code:    clientErr.Code,
		Message: fmt.Sprintf("%s failed: %s", operation, clientErr.Message),
		Details: clientErr.Details,
		Err:     clientErr.Err,
	}

	// Add operation-specific context
	if em.config.EnableDetailedErrors {
		switch clientErr.Code {
		case ErrCodeNotFound:
			enhancedErr.Details = fmt.Sprintf("Resource not found during %s. %s",
				operation, clientErr.Details)
		case ErrCodeBadRequest:
			enhancedErr.Details = fmt.Sprintf("Invalid request for %s. %s",
				operation, clientErr.Details)
		case ErrCodeUnauthorized:
			enhancedErr.Details = fmt.Sprintf("Authentication failed for %s. %s",
				operation, clientErr.Details)
		case ErrCodeForbidden:
			enhancedErr.Details = fmt.Sprintf("Access denied for %s. %s",
				operation, clientErr.Details)
		case ErrCodeTimeout:
			enhancedErr.Details = fmt.Sprintf("Timeout during %s. %s",
				operation, clientErr.Details)
		case ErrCodeTooManyRequests:
			enhancedErr.Details = fmt.Sprintf("Rate limit exceeded for %s. %s",
				operation, clientErr.Details)
		case ErrCodeServiceUnavailable:
			enhancedErr.Details = fmt.Sprintf("Service unavailable for %s. %s",
				operation, clientErr.Details)
		}
	}

	return enhancedErr
}

// enhanceQuoteError enhances quote-specific errors
func (em *ErrorMapper) enhanceQuoteError(quoteErr *QuoteError, operation string) error {
	enhancedErr := &QuoteError{
		Code:    quoteErr.Code,
		Message: fmt.Sprintf("%s failed: %s", operation, quoteErr.Message),
		Details: quoteErr.Details,
		Err:     quoteErr.Err,
	}

	// Add operation-specific context for quote errors
	if em.config.EnableDetailedErrors {
		switch quoteErr.Code {
		case QuoteErrorPoolNotFound:
			enhancedErr.Details = fmt.Sprintf("Pool not found during %s. Check asset symbols and pool availability.", operation)
		case QuoteErrorInvalidAsset:
			enhancedErr.Details = fmt.Sprintf("Invalid asset during %s. Verify asset format and chain support.", operation)
		case QuoteErrorInsufficientLiquidity:
			enhancedErr.Details = fmt.Sprintf("Insufficient liquidity during %s. Try reducing swap amount.", operation)
		case QuoteErrorSlippageTooHigh:
			enhancedErr.Details = fmt.Sprintf("Slippage too high during %s. Consider increasing tolerance or reducing amount.", operation)
		}
	}

	return enhancedErr
}

// IsRetryable determines if an error should trigger a retry
func (em *ErrorMapper) IsRetryable(err error) bool {
	if err == nil {
		return false
	}

	// Context cancellation is not retryable
	if err == context.Canceled {
		return false
	}

	// Check client errors
	if clientErr, ok := err.(*ClientError); ok {
		return clientErr.IsRetryable()
	}

	// Check quote errors
	if quoteErr, ok := err.(*QuoteError); ok {
		return em.isQuoteErrorRetryable(quoteErr)
	}

	// Network errors are generally retryable
	if netErr, ok := err.(net.Error); ok {
		return netErr.Temporary() || netErr.Timeout()
	}

	// Context deadline exceeded might be retryable with longer timeout
	if err == context.DeadlineExceeded {
		return true
	}

	return false
}

// isQuoteErrorRetryable determines if a quote error is retryable
func (em *ErrorMapper) isQuoteErrorRetryable(quoteErr *QuoteError) bool {
	switch quoteErr.Code {
	case QuoteErrorTimeout, QuoteErrorRateLimit, QuoteErrorServiceUnavailable, QuoteErrorInternal:
		return true
	case QuoteErrorInvalidRequest, QuoteErrorInvalidAsset, QuoteErrorSameAsset:
		return false
	default:
		return false
	}
}

// GetRetryDelay calculates appropriate retry delay based on error type
func (em *ErrorMapper) GetRetryDelay(err error, attempt int) time.Duration {
	baseDelay := time.Second
	maxDelay := 30 * time.Second

	// Exponential backoff
	delay := baseDelay * time.Duration(1<<uint(attempt))
	if delay > maxDelay {
		delay = maxDelay
	}

	// Adjust based on error type
	if clientErr, ok := err.(*ClientError); ok {
		switch clientErr.Code {
		case ErrCodeTooManyRequests:
			// Longer delay for rate limiting
			return delay * 2
		case ErrCodeServiceUnavailable:
			// Shorter delay for service unavailable
			return delay / 2
		case ErrCodeTimeout:
			// Standard delay for timeouts
			return delay
		}
	}

	return delay
}

// ErrorSeverity represents the severity level of an error
type ErrorSeverity int

const (
	SeverityLow ErrorSeverity = iota
	SeverityMedium
	SeverityHigh
	SeverityCritical
)

func (es ErrorSeverity) String() string {
	switch es {
	case SeverityLow:
		return "LOW"
	case SeverityMedium:
		return "MEDIUM"
	case SeverityHigh:
		return "HIGH"
	case SeverityCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// GetErrorSeverity determines the severity of an error
func (em *ErrorMapper) GetErrorSeverity(err error) ErrorSeverity {
	if err == nil {
		return SeverityLow
	}

	if clientErr, ok := err.(*ClientError); ok {
		switch clientErr.Code {
		case ErrCodeBadRequest, ErrCodeNotFound:
			return SeverityLow
		case ErrCodeUnauthorized, ErrCodeForbidden:
			return SeverityMedium
		case ErrCodeTimeout, ErrCodeTooManyRequests:
			return SeverityMedium
		case ErrCodeInternalServer, ErrCodeBadGateway, ErrCodeServiceUnavailable:
			return SeverityHigh
		case ErrCodeNetworkError:
			return SeverityHigh
		default:
			return SeverityMedium
		}
	}

	if quoteErr, ok := err.(*QuoteError); ok {
		switch quoteErr.Code {
		case QuoteErrorInvalidRequest, QuoteErrorInvalidAsset:
			return SeverityLow
		case QuoteErrorPoolNotFound, QuoteErrorInsufficientLiquidity:
			return SeverityMedium
		case QuoteErrorServiceUnavailable, QuoteErrorInternal:
			return SeverityHigh
		default:
			return SeverityMedium
		}
	}

	// Network errors
	if netErr, ok := err.(net.Error); ok {
		if netErr.Temporary() {
			return SeverityMedium
		}
		return SeverityHigh
	}

	return SeverityMedium
}

// ErrorContext provides additional context for errors
type ErrorContext struct {
	Operation   string            `json:"operation"`
	Timestamp   time.Time         `json:"timestamp"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Severity    ErrorSeverity     `json:"severity"`
	Retryable   bool              `json:"retryable"`
	RetryDelay  time.Duration     `json:"retry_delay,omitempty"`
	Suggestions []string          `json:"suggestions,omitempty"`
}

// AnalyzeError provides comprehensive error analysis
func (em *ErrorMapper) AnalyzeError(err error, operation string, metadata map[string]string) *ErrorContext {
	if err == nil {
		return nil
	}

	ctx := &ErrorContext{
		Operation:   operation,
		Timestamp:   time.Now(),
		Metadata:    metadata,
		Severity:    em.GetErrorSeverity(err),
		Retryable:   em.IsRetryable(err),
		RetryDelay:  em.GetRetryDelay(err, 1),
		Suggestions: em.generateSuggestions(err),
	}

	return ctx
}

// generateSuggestions generates suggestions for resolving errors
func (em *ErrorMapper) generateSuggestions(err error) []string {
	suggestions := make([]string, 0)

	if clientErr, ok := err.(*ClientError); ok {
		switch clientErr.Code {
		case ErrCodeNotFound:
			suggestions = append(suggestions, "Verify the requested resource exists")
			suggestions = append(suggestions, "Check the API endpoint path")
		case ErrCodeBadRequest:
			suggestions = append(suggestions, "Validate request parameters")
			suggestions = append(suggestions, "Check request format and required fields")
		case ErrCodeUnauthorized:
			suggestions = append(suggestions, "Check API key or authentication credentials")
			suggestions = append(suggestions, "Verify token is not expired")
		case ErrCodeForbidden:
			suggestions = append(suggestions, "Check user permissions")
			suggestions = append(suggestions, "Verify account access level")
		case ErrCodeTimeout:
			suggestions = append(suggestions, "Increase request timeout")
			suggestions = append(suggestions, "Check network connectivity")
		case ErrCodeTooManyRequests:
			suggestions = append(suggestions, "Reduce request frequency")
			suggestions = append(suggestions, "Implement request batching")
		case ErrCodeServiceUnavailable:
			suggestions = append(suggestions, "Wait and retry")
			suggestions = append(suggestions, "Check service status")
		case ErrCodeNetworkError:
			suggestions = append(suggestions, "Check internet connection")
			suggestions = append(suggestions, "Verify firewall settings")
		}
	}

	if quoteErr, ok := err.(*QuoteError); ok {
		switch quoteErr.Code {
		case QuoteErrorInvalidAsset:
			suggestions = append(suggestions, "Verify asset symbol format (CHAIN.ASSET)")
			suggestions = append(suggestions, "Check supported assets list")
		case QuoteErrorPoolNotFound:
			suggestions = append(suggestions, "Verify pool exists for the asset")
			suggestions = append(suggestions, "Check pool status and availability")
		case QuoteErrorInsufficientLiquidity:
			suggestions = append(suggestions, "Reduce swap amount")
			suggestions = append(suggestions, "Wait for more liquidity to be added")
		case QuoteErrorSlippageTooHigh:
			suggestions = append(suggestions, "Increase slippage tolerance")
			suggestions = append(suggestions, "Reduce swap amount")
			suggestions = append(suggestions, "Consider streaming swaps for large amounts")
		}
	}

	return suggestions
}

// FormatError formats an error for user-friendly display
func (em *ErrorMapper) FormatError(err error) string {
	if err == nil {
		return "No error"
	}

	ctx := em.AnalyzeError(err, "operation", nil)

	var formatted strings.Builder
	formatted.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
	formatted.WriteString(fmt.Sprintf("Severity: %s\n", ctx.Severity))

	if ctx.Retryable {
		formatted.WriteString("This error is retryable\n")
		if ctx.RetryDelay > 0 {
			formatted.WriteString(fmt.Sprintf("Suggested retry delay: %s\n", ctx.RetryDelay))
		}
	} else {
		formatted.WriteString("This error is not retryable\n")
	}

	if len(ctx.Suggestions) > 0 {
		formatted.WriteString("Suggestions:\n")
		for i, suggestion := range ctx.Suggestions {
			formatted.WriteString(fmt.Sprintf("  %d. %s\n", i+1, suggestion))
		}
	}

	return formatted.String()
}
