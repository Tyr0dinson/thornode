package thorchain

import (
	"fmt"
	"sync"
	"time"
)

// CircuitBreakerState represents the state of the circuit breaker
type CircuitBreakerState int

const (
	// StateClosed - normal operation, requests are allowed
	StateClosed CircuitBreakerState = iota
	// StateOpen - circuit breaker is open, requests are rejected
	StateOpen
	// StateHalfOpen - circuit breaker is testing if service is recovered
	StateHalfOpen
)

func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	config           *CircuitBreakerConfig
	mutex            sync.RWMutex
	state            CircuitBreakerState
	failureCount     int
	successCount     int
	lastFailureTime  time.Time
	halfOpenRequests int
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config *CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		config: config,
		state:  StateClosed,
	}
}

// Execute executes a function with circuit breaker protection
func (cb *CircuitBreaker) Execute(fn func() ([]byte, error)) ([]byte, error) {
	if !cb.canExecute() {
		return nil, NewClientError(ErrCodeServiceUnavailable, "circuit breaker is open", 
			fmt.Sprintf("circuit breaker state: %s", cb.GetState()), nil)
	}

	result, err := fn()
	
	if err != nil {
		cb.recordFailure()
		return nil, err
	}
	
	cb.recordSuccess()
	return result, nil
}

// canExecute determines if a request can be executed based on circuit breaker state
func (cb *CircuitBreaker) canExecute() bool {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	switch cb.state {
	case StateClosed:
		return true
	case StateOpen:
		// Check if enough time has passed to try half-open
		if time.Since(cb.lastFailureTime) >= cb.config.Timeout {
			cb.state = StateHalfOpen
			cb.halfOpenRequests = 0
			return true
		}
		return false
	case StateHalfOpen:
		// Allow limited requests in half-open state
		if cb.halfOpenRequests < cb.config.HalfOpenMaxRequests {
			cb.halfOpenRequests++
			return true
		}
		return false
	default:
		return false
	}
}

// recordFailure records a failure and updates circuit breaker state
func (cb *CircuitBreaker) recordFailure() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.failureCount++
	cb.lastFailureTime = time.Now()
	cb.successCount = 0 // Reset success count on failure

	switch cb.state {
	case StateClosed:
		// Check if we should open the circuit
		if cb.failureCount >= cb.config.FailureThreshold {
			cb.state = StateOpen
			cb.halfOpenRequests = 0
		}
	case StateHalfOpen:
		// Failure in half-open state means service is still down
		cb.state = StateOpen
		cb.halfOpenRequests = 0
	}
}

// recordSuccess records a success and updates circuit breaker state
func (cb *CircuitBreaker) recordSuccess() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.successCount++

	switch cb.state {
	case StateHalfOpen:
		// Check if we should close the circuit
		if cb.successCount >= cb.config.SuccessThreshold {
			cb.state = StateClosed
			cb.failureCount = 0
			cb.successCount = 0
			cb.halfOpenRequests = 0
		}
	case StateClosed:
		// Reset failure count on successful operations
		if cb.successCount >= cb.config.SuccessThreshold {
			cb.failureCount = 0
		}
	}
}

// GetState returns the current state of the circuit breaker
func (cb *CircuitBreaker) GetState() CircuitBreakerState {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.state
}

// GetFailureCount returns the current failure count
func (cb *CircuitBreaker) GetFailureCount() int {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.failureCount
}

// GetSuccessCount returns the current success count
func (cb *CircuitBreaker) GetSuccessCount() int {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.successCount
}

// GetLastFailureTime returns the time of the last failure
func (cb *CircuitBreaker) GetLastFailureTime() time.Time {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.lastFailureTime
}

// IsOpen returns true if the circuit breaker is open
func (cb *CircuitBreaker) IsOpen() bool {
	return cb.GetState() == StateOpen
}

// IsClosed returns true if the circuit breaker is closed
func (cb *CircuitBreaker) IsClosed() bool {
	return cb.GetState() == StateClosed
}

// IsHalfOpen returns true if the circuit breaker is half-open
func (cb *CircuitBreaker) IsHalfOpen() bool {
	return cb.GetState() == StateHalfOpen
}

// Reset resets the circuit breaker to its initial state
func (cb *CircuitBreaker) Reset() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	
	cb.state = StateClosed
	cb.failureCount = 0
	cb.successCount = 0
	cb.halfOpenRequests = 0
	cb.lastFailureTime = time.Time{}
}

// ForceOpen forces the circuit breaker to open state
func (cb *CircuitBreaker) ForceOpen() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	
	cb.state = StateOpen
	cb.lastFailureTime = time.Now()
	cb.halfOpenRequests = 0
}

// ForceClose forces the circuit breaker to closed state
func (cb *CircuitBreaker) ForceClose() {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()
	
	cb.state = StateClosed
	cb.failureCount = 0
	cb.successCount = 0
	cb.halfOpenRequests = 0
}

// GetStats returns circuit breaker statistics
func (cb *CircuitBreaker) GetStats() CircuitBreakerStats {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	
	return CircuitBreakerStats{
		State:               cb.state,
		FailureCount:        cb.failureCount,
		SuccessCount:        cb.successCount,
		LastFailureTime:     cb.lastFailureTime,
		HalfOpenRequests:    cb.halfOpenRequests,
		FailureThreshold:    cb.config.FailureThreshold,
		SuccessThreshold:    cb.config.SuccessThreshold,
		HalfOpenMaxRequests: cb.config.HalfOpenMaxRequests,
		Timeout:             cb.config.Timeout,
	}
}

// CircuitBreakerStats represents circuit breaker statistics
type CircuitBreakerStats struct {
	State               CircuitBreakerState `json:"state"`
	FailureCount        int                 `json:"failure_count"`
	SuccessCount        int                 `json:"success_count"`
	LastFailureTime     time.Time           `json:"last_failure_time"`
	HalfOpenRequests    int                 `json:"half_open_requests"`
	FailureThreshold    int                 `json:"failure_threshold"`
	SuccessThreshold    int                 `json:"success_threshold"`
	HalfOpenMaxRequests int                 `json:"half_open_max_requests"`
	Timeout             time.Duration       `json:"timeout"`
}

// String returns a string representation of the circuit breaker stats
func (s CircuitBreakerStats) String() string {
	return fmt.Sprintf("CircuitBreaker{state=%s, failures=%d/%d, successes=%d/%d, half_open_requests=%d/%d}",
		s.State, s.FailureCount, s.FailureThreshold, s.SuccessCount, s.SuccessThreshold,
		s.HalfOpenRequests, s.HalfOpenMaxRequests)
}

// CircuitBreakerMiddleware can be used to add circuit breaker functionality to existing clients
type CircuitBreakerMiddleware struct {
	breakers map[string]*CircuitBreaker
	mutex    sync.RWMutex
	config   *CircuitBreakerConfig
}

// NewCircuitBreakerMiddleware creates a new circuit breaker middleware
func NewCircuitBreakerMiddleware(config *CircuitBreakerConfig) *CircuitBreakerMiddleware {
	return &CircuitBreakerMiddleware{
		breakers: make(map[string]*CircuitBreaker),
		config:   config,
	}
}

// GetBreaker gets or creates a circuit breaker for a specific key (e.g., endpoint)
func (m *CircuitBreakerMiddleware) GetBreaker(key string) *CircuitBreaker {
	m.mutex.RLock()
	if breaker, exists := m.breakers[key]; exists {
		m.mutex.RUnlock()
		return breaker
	}
	m.mutex.RUnlock()

	m.mutex.Lock()
	defer m.mutex.Unlock()
	
	// Double-check pattern
	if breaker, exists := m.breakers[key]; exists {
		return breaker
	}
	
	breaker := NewCircuitBreaker(m.config)
	m.breakers[key] = breaker
	return breaker
}

// Execute executes a function with circuit breaker protection for a specific key
func (m *CircuitBreakerMiddleware) Execute(key string, fn func() ([]byte, error)) ([]byte, error) {
	breaker := m.GetBreaker(key)
	return breaker.Execute(fn)
}

// GetAllStats returns statistics for all circuit breakers
func (m *CircuitBreakerMiddleware) GetAllStats() map[string]CircuitBreakerStats {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	
	stats := make(map[string]CircuitBreakerStats)
	for key, breaker := range m.breakers {
		stats[key] = breaker.GetStats()
	}
	return stats
}

// ResetAll resets all circuit breakers
func (m *CircuitBreakerMiddleware) ResetAll() {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	
	for _, breaker := range m.breakers {
		breaker.Reset()
	}
}