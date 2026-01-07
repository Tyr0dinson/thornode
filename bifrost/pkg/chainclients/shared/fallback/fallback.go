package fallback

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// EndpointState represents which endpoint is currently active
type EndpointState int32

const (
	EndpointPrimary EndpointState = iota
	EndpointFallback
)

func (s EndpointState) String() string {
	switch s {
	case EndpointPrimary:
		return "primary"
	case EndpointFallback:
		return "fallback"
	default:
		return "unknown"
	}
}

// HealthChecker is a function that tests if an endpoint is healthy
type HealthChecker func(ctx context.Context) error

// Manager handles automatic failover between primary and fallback RPC endpoints
type Manager struct {
	state               atomic.Int32
	primaryHealthCheck  HealthChecker
	healthCheckInterval time.Duration
	logger              zerolog.Logger
	stopChan            chan struct{}
	wg                  sync.WaitGroup
	chain               string
}

// NewManager creates a new fallback manager
func NewManager(
	chain string,
	primaryHealthCheck HealthChecker,
	healthCheckInterval time.Duration,
	logger zerolog.Logger,
) *Manager {
	m := &Manager{
		chain:               chain,
		primaryHealthCheck:  primaryHealthCheck,
		healthCheckInterval: healthCheckInterval,
		logger:              logger.With().Str("module", "fallback").Str("chain", chain).Logger(),
		stopChan:            make(chan struct{}),
	}
	m.state.Store(int32(EndpointPrimary))

	// Check if primary is healthy on startup - if not, start with fallback
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := primaryHealthCheck(ctx)
	cancel()
	if err != nil {
		m.logger.Warn().Err(err).Msg("primary RPC not ready, starting with fallback")
		m.state.Store(int32(EndpointFallback))
	} else {
		m.logger.Info().Msg("primary RPC healthy, using primary")
	}

	return m
}

// Start begins the background health check loop
func (m *Manager) Start() {
	m.wg.Add(1)
	go m.healthCheckLoop()
	m.logger.Info().Dur("interval", m.healthCheckInterval).Msg("fallback manager started")
}

// Stop stops the health check loop
func (m *Manager) Stop() {
	close(m.stopChan)
	m.wg.Wait()
	m.logger.Info().Msg("fallback manager stopped")
}

// GetState returns the current endpoint state
func (m *Manager) GetState() EndpointState {
	return EndpointState(m.state.Load())
}

// IsFallback returns true if currently using fallback endpoint
func (m *Manager) IsFallback() bool {
	return m.GetState() == EndpointFallback
}

// SetFallback switches to fallback endpoint
func (m *Manager) SetFallback() {
	if m.state.CompareAndSwap(int32(EndpointPrimary), int32(EndpointFallback)) {
		m.logger.Warn().Msg("switching to fallback RPC endpoint")
	}
}

// SetPrimary switches back to primary endpoint
func (m *Manager) SetPrimary() {
	if m.state.CompareAndSwap(int32(EndpointFallback), int32(EndpointPrimary)) {
		m.logger.Info().Msg("recovered to primary RPC endpoint")
	}
}

// healthCheckLoop periodically checks if primary endpoint has recovered
func (m *Manager) healthCheckLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(m.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			if m.GetState() == EndpointFallback {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := m.primaryHealthCheck(ctx)
				cancel()
				if err == nil {
					m.SetPrimary()
				} else {
					m.logger.Debug().Err(err).Msg("primary endpoint still unhealthy")
				}
			}
		}
	}
}

// ShouldSwitchToFallback determines if an error warrants failover
func ShouldSwitchToFallback(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())

	// Connection failures that warrant failover
	return strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "no such host") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "deadline exceeded") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "eof") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "network is unreachable")
}
