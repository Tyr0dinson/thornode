package keeperv1

import (
	"fmt"
	"time"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// MemolessMetrics tracks operational metrics for monitoring
type MemolessMetrics struct {
	ActiveRegistrations  int64             `json:"active_registrations"`
	TotalRegistrations   int64             `json:"total_registrations"`
	ExpiredRegistrations int64             `json:"expired_registrations"`
	ReferenceCollisions  int64             `json:"reference_collisions"`
	ValidationFailures   int64             `json:"validation_failures"`
	SystemErrors         int64             `json:"system_errors"`
	AverageResponseTime  int64             `json:"avg_response_time_ns"`
	LastCollisionTime    int64             `json:"last_collision_time"`
	CounterPositions     map[string]int64  `json:"counter_positions"`
	ChainHealthStatus    map[string]string `json:"chain_health_status"`
	LastUpdated          int64             `json:"last_updated"`
}

// AlertThresholds defines thresholds for operational alerts
type AlertThresholds struct {
	MaxCollisionRate      float64 `json:"max_collision_rate"`       // Per hour
	MaxValidationFailRate float64 `json:"max_validation_fail_rate"` // Percentage
	MaxResponseTime       int64   `json:"max_response_time_ns"`     // Nanoseconds
	MinActiveRatio        float64 `json:"min_active_ratio"`         // Active/Total ratio
	MaxStorageSize        int64   `json:"max_storage_size_mb"`      // Megabytes
}

// MonitoringAlert represents a system alert
type MonitoringAlert struct {
	AlertID   string                 `json:"alert_id"`
	Severity  string                 `json:"severity"` // INFO, WARN, ERROR, CRITICAL
	Component string                 `json:"component"`
	Message   string                 `json:"message"`
	Timestamp int64                  `json:"timestamp"`
	ChainID   string                 `json:"chain_id,omitempty"`
	Reference string                 `json:"reference,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// GetMemolessMetrics collects comprehensive operational metrics
func (k KVStore) GetMemolessMetrics(ctx cosmos.Context) (*MemolessMetrics, error) {
	ctx.Logger().Info("Collecting memoless operational metrics")

	timestamp := ctx.BlockTime().Unix()
	if timestamp == 0 {
		timestamp = ctx.BlockHeight() // Fallback for test environments
	}

	metrics := &MemolessMetrics{
		CounterPositions:  make(map[string]int64),
		ChainHealthStatus: make(map[string]string),
		LastUpdated:       timestamp,
	}

	// Collect registration statistics
	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	currentBlock := ctx.BlockHeight()

	for ; iterator.Valid(); iterator.Next() {
		var memo thorchaintypes.RegisteredMemo
		if err := k.cdc.Unmarshal(iterator.Value(), &memo); err != nil {
			metrics.SystemErrors++
			continue
		}

		metrics.TotalRegistrations++

		if memo.IsExpired(currentBlock) {
			metrics.ExpiredRegistrations++
		} else if memo.Status == "active" {
			metrics.ActiveRegistrations++
		}
	}

	// Collect counter positions for each supported chain
	supportedChains, err := k.GetSupportedChains(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get supported chains: %w", err)
	}

	for _, chainID := range supportedChains {
		counter, err := k.GetCurrentCounter(ctx, chainID)
		if err != nil {
			metrics.SystemErrors++
			metrics.ChainHealthStatus[chainID] = "ERROR"
		} else {
			metrics.CounterPositions[chainID] = counter
			metrics.ChainHealthStatus[chainID] = "HEALTHY"
		}
	}

	ctx.Logger().Info("Metrics collection completed",
		"active_registrations", metrics.ActiveRegistrations,
		"total_registrations", metrics.TotalRegistrations,
		"system_errors", metrics.SystemErrors)

	return metrics, nil
}

// CheckAlertThresholds evaluates current metrics against alert thresholds
func (k KVStore) CheckAlertThresholds(ctx cosmos.Context, metrics *MemolessMetrics, thresholds AlertThresholds) ([]MonitoringAlert, error) {
	ctx.Logger().Info("Checking alert thresholds")

	var alerts []MonitoringAlert
	timestamp := ctx.BlockTime().Unix()

	// Check collision rate (if we had collision tracking)
	if metrics.ReferenceCollisions > 0 {
		collisionRate := float64(metrics.ReferenceCollisions) // Per hour approximation
		if collisionRate > thresholds.MaxCollisionRate {
			alerts = append(alerts, MonitoringAlert{
				AlertID:   fmt.Sprintf("collision_rate_%d", timestamp),
				Severity:  "WARN",
				Component: "reference_generation",
				Message:   fmt.Sprintf("High collision rate: %.2f/hour (threshold: %.2f)", collisionRate, thresholds.MaxCollisionRate),
				Timestamp: timestamp,
			})
		}
	}

	// Check validation failure rate
	if metrics.TotalRegistrations > 0 {
		failureRate := float64(metrics.ValidationFailures) / float64(metrics.TotalRegistrations) * 100
		if failureRate > thresholds.MaxValidationFailRate {
			alerts = append(alerts, MonitoringAlert{
				AlertID:   fmt.Sprintf("validation_fail_%d", timestamp),
				Severity:  "ERROR",
				Component: "validation",
				Message:   fmt.Sprintf("High validation failure rate: %.2f%% (threshold: %.2f%%)", failureRate, thresholds.MaxValidationFailRate),
				Timestamp: timestamp,
			})
		}
	}

	// Check active ratio
	if metrics.TotalRegistrations > 0 {
		activeRatio := float64(metrics.ActiveRegistrations) / float64(metrics.TotalRegistrations)
		if activeRatio < thresholds.MinActiveRatio {
			alerts = append(alerts, MonitoringAlert{
				AlertID:   fmt.Sprintf("active_ratio_%d", timestamp),
				Severity:  "WARN",
				Component: "memo_lifecycle",
				Message:   fmt.Sprintf("Low active memo ratio: %.2f (threshold: %.2f)", activeRatio, thresholds.MinActiveRatio),
				Timestamp: timestamp,
			})
		}
	}

	// Check response time
	if metrics.AverageResponseTime > thresholds.MaxResponseTime {
		alerts = append(alerts, MonitoringAlert{
			AlertID:   fmt.Sprintf("response_time_%d", timestamp),
			Severity:  "WARN",
			Component: "performance",
			Message:   fmt.Sprintf("High response time: %dns (threshold: %dns)", metrics.AverageResponseTime, thresholds.MaxResponseTime),
			Timestamp: timestamp,
		})
	}

	// Check chain health
	for chainID, status := range metrics.ChainHealthStatus {
		if status == "ERROR" {
			alerts = append(alerts, MonitoringAlert{
				AlertID:   fmt.Sprintf("chain_health_%s_%d", chainID, timestamp),
				Severity:  "CRITICAL",
				Component: "chain_integration",
				Message:   fmt.Sprintf("Chain %s is unhealthy", chainID),
				Timestamp: timestamp,
				ChainID:   chainID,
			})
		}
	}

	ctx.Logger().Info("Alert threshold checking completed", "alerts_generated", len(alerts))
	return alerts, nil
}

// GetDefaultAlertThresholds returns production-ready alert thresholds
func GetDefaultAlertThresholds() AlertThresholds {
	return AlertThresholds{
		MaxCollisionRate:      10.0,    // 10 collisions per hour
		MaxValidationFailRate: 5.0,     // 5% validation failure rate
		MaxResponseTime:       1000000, // 1ms maximum response time
		MinActiveRatio:        0.7,     // 70% of memos should be active
		MaxStorageSize:        100,     // 100MB storage limit
	}
}

// RecordPerformanceMetric records a performance measurement
func (k KVStore) RecordPerformanceMetric(ctx cosmos.Context, operation string, duration time.Duration, success bool) error {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	// Simple performance tracking - in production this would integrate with proper metrics systems
	key := k.GetKey(prefixMemoStats, fmt.Sprintf("perf_%s_%d", operation, ctx.BlockHeight()))

	_ = map[string]interface{}{
		"operation":    operation,
		"duration_ns":  duration.Nanoseconds(),
		"success":      success,
		"timestamp":    ctx.BlockTime().Unix(),
		"block_height": ctx.BlockHeight(),
	}

	// Store as simple key-value for now (production would use structured metrics)
	value := fmt.Sprintf("op=%s,duration=%dns,success=%t", operation, duration.Nanoseconds(), success)
	store.Set(key, []byte(value))

	return nil
}

// GetMemolessPerformanceMetrics retrieves recent performance metrics for memoless operations
func (k KVStore) GetMemolessPerformanceMetrics(ctx cosmos.Context, operation string, blockRange int64) ([]map[string]interface{}, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	var metrics []map[string]interface{}
	startBlock := ctx.BlockHeight() - blockRange

	for block := startBlock; block <= ctx.BlockHeight(); block++ {
		key := k.GetKey(prefixMemoStats, fmt.Sprintf("perf_%s_%d", operation, block))
		if store.Has(key) {
			value := store.Get(key)
			// Parse the stored metric (simplified parsing)
			metric := map[string]interface{}{
				"block_height": block,
				"raw_data":     string(value),
			}
			metrics = append(metrics, metric)
		}
	}

	return metrics, nil
}

// MonitorSystemHealth performs comprehensive system health checks
func (k KVStore) MonitorSystemHealth(ctx cosmos.Context) (*SystemHealthReport, error) {
	ctx.Logger().Info("Performing system health monitoring")

	report := &SystemHealthReport{
		Timestamp:   ctx.BlockTime().Unix(),
		BlockHeight: ctx.BlockHeight(),
		Components:  make(map[string]ComponentHealth),
	}

	// Check reference generation health
	refHealth, err := k.checkReferenceGenerationHealth(ctx)
	if err != nil {
		return nil, err
	}
	report.Components["reference_generation"] = refHealth

	// Check memo storage health
	storageHealth, err := k.checkMemoStorageHealth(ctx)
	if err != nil {
		return nil, err
	}
	report.Components["memo_storage"] = storageHealth

	// Check validation system health
	validationHealth, err := k.checkValidationSystemHealth(ctx)
	if err != nil {
		return nil, err
	}
	report.Components["validation_system"] = validationHealth

	// Overall system status
	report.OverallStatus = k.calculateOverallHealth(report.Components)

	ctx.Logger().Info("System health monitoring completed", "overall_status", report.OverallStatus)
	return report, nil
}

// SystemHealthReport represents the overall system health status
type SystemHealthReport struct {
	Timestamp     int64                      `json:"timestamp"`
	BlockHeight   int64                      `json:"block_height"`
	OverallStatus string                     `json:"overall_status"`
	Components    map[string]ComponentHealth `json:"components"`
}

// ComponentHealth represents the health status of a system component
type ComponentHealth struct {
	Status      string                 `json:"status"` // HEALTHY, DEGRADED, UNHEALTHY
	Message     string                 `json:"message"`
	Metrics     map[string]interface{} `json:"metrics"`
	LastChecked int64                  `json:"last_checked"`
}

// checkReferenceGenerationHealth checks the health of reference generation
func (k KVStore) checkReferenceGenerationHealth(ctx cosmos.Context) (ComponentHealth, error) {
	health := ComponentHealth{
		Metrics:     make(map[string]interface{}),
		LastChecked: ctx.BlockTime().Unix(),
	}

	// Test reference generation for all supported chains
	supportedChains, err := k.GetSupportedChains(ctx)
	if err != nil {
		health.Status = "UNHEALTHY"
		health.Message = fmt.Sprintf("Failed to get supported chains: %v", err)
		return health, nil
	}

	healthyChains := 0
	for _, chainID := range supportedChains {
		startTime := time.Now()
		_, err := k.GetNextReferenceID(ctx, chainID)
		duration := time.Since(startTime)

		if err != nil {
			health.Metrics[fmt.Sprintf("%s_error", chainID)] = err.Error()
		} else {
			health.Metrics[fmt.Sprintf("%s_latency_ns", chainID)] = duration.Nanoseconds()
			healthyChains++
		}
	}

	if healthyChains == len(supportedChains) {
		health.Status = "HEALTHY"
		health.Message = "All chains generating references successfully"
	} else if healthyChains > len(supportedChains)/2 {
		health.Status = "DEGRADED"
		health.Message = fmt.Sprintf("%d/%d chains healthy", healthyChains, len(supportedChains))
	} else {
		health.Status = "UNHEALTHY"
		health.Message = fmt.Sprintf("Only %d/%d chains healthy", healthyChains, len(supportedChains))
	}

	health.Metrics["healthy_chains"] = healthyChains
	health.Metrics["total_chains"] = len(supportedChains)

	return health, nil
}

// checkMemoStorageHealth checks the health of memo storage operations
func (k KVStore) checkMemoStorageHealth(ctx cosmos.Context) (ComponentHealth, error) {
	health := ComponentHealth{
		Metrics:     make(map[string]interface{}),
		LastChecked: ctx.BlockTime().Unix(),
	}

	// Test basic storage operations
	testMemo := thorchaintypes.RegisteredMemo{
		Reference:    "99998", // Use high reference to avoid conflicts
		Memo:         "HEALTH:CHECK:thor1test:1",
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight(),
		ExpiresAt:    ctx.BlockHeight() + 1,
		Status:       "active",
		Signer:       GetRandomValidatorNode(NodeActive).NodeAddress,
	}

	// Test write
	startTime := time.Now()
	err := k.SetRegisteredMemo(ctx, testMemo)
	writeLatency := time.Since(startTime)

	if err != nil {
		health.Status = "UNHEALTHY"
		health.Message = fmt.Sprintf("Storage write failed: %v", err)
		health.Metrics["write_error"] = err.Error()
		return health, nil
	}

	// Test read
	startTime = time.Now()
	_, err = k.GetRegisteredMemo(ctx, testMemo.Reference)
	readLatency := time.Since(startTime)

	if err != nil {
		health.Status = "DEGRADED"
		health.Message = fmt.Sprintf("Storage read failed: %v", err)
		health.Metrics["read_error"] = err.Error()
	} else {
		health.Status = "HEALTHY"
		health.Message = "Storage operations functioning normally"
	}

	// Test delete (cleanup test memo)
	startTime = time.Now()
	err = k.DeleteRegisteredMemo(ctx, testMemo.Reference)
	deleteLatency := time.Since(startTime)

	health.Metrics["write_latency_ns"] = writeLatency.Nanoseconds()
	health.Metrics["read_latency_ns"] = readLatency.Nanoseconds()
	health.Metrics["delete_latency_ns"] = deleteLatency.Nanoseconds()

	return health, nil
}

// checkValidationSystemHealth checks the health of the validation system
func (k KVStore) checkValidationSystemHealth(ctx cosmos.Context) (ComponentHealth, error) {
	health := ComponentHealth{
		Metrics:     make(map[string]interface{}),
		LastChecked: ctx.BlockTime().Unix(),
	}

	// Test validation system with various scenarios
	validationTests := []struct {
		name       string
		shouldPass bool
		msg        thorchaintypes.MsgRegisterMemo
	}{
		{
			name:       "valid_registration",
			shouldPass: true,
			msg: thorchaintypes.MsgRegisterMemo{
				Memo:    "SWAP:BTC.BTC:thor1health:14400",
				ChainId: "BTC",
				Ttl:     14400,
				Signer:  GetRandomValidatorNode(NodeActive).NodeAddress,
				Amount:  "100000000",
			},
		},
		{
			name:       "invalid_chain",
			shouldPass: false,
			msg: thorchaintypes.MsgRegisterMemo{
				Memo:    "SWAP:INVALID.INVALID:thor1test:14400",
				ChainId: "INVALID",
				Ttl:     14400,
				Signer:  GetRandomValidatorNode(NodeActive).NodeAddress,
				Amount:  "100000000",
			},
		},
		{
			name:       "invalid_ttl",
			shouldPass: false,
			msg: thorchaintypes.MsgRegisterMemo{
				Memo:    "SWAP:BTC.BTC:thor1test:14400",
				ChainId: "BTC",
				Ttl:     -1,
				Signer:  GetRandomValidatorNode(NodeActive).NodeAddress,
				Amount:  "100000000",
			},
		},
	}

	passCount := 0
	for _, test := range validationTests {
		err := k.ValidateBasicMemoRegistration(ctx, test.msg)
		if (err == nil) == test.shouldPass {
			passCount++
		}
		health.Metrics[fmt.Sprintf("test_%s", test.name)] = (err == nil) == test.shouldPass
	}

	if passCount == len(validationTests) {
		health.Status = "HEALTHY"
		health.Message = "All validation tests passing"
	} else if passCount > len(validationTests)/2 {
		health.Status = "DEGRADED"
		health.Message = fmt.Sprintf("%d/%d validation tests passing", passCount, len(validationTests))
	} else {
		health.Status = "UNHEALTHY"
		health.Message = fmt.Sprintf("Only %d/%d validation tests passing", passCount, len(validationTests))
	}

	health.Metrics["tests_passed"] = passCount
	health.Metrics["total_tests"] = len(validationTests)

	return health, nil
}

// calculateOverallHealth determines overall system health from component health
func (k KVStore) calculateOverallHealth(components map[string]ComponentHealth) string {
	healthyCount := 0
	degradedCount := 0
	unhealthyCount := 0

	for _, component := range components {
		switch component.Status {
		case "HEALTHY":
			healthyCount++
		case "DEGRADED":
			degradedCount++
		case "UNHEALTHY":
			unhealthyCount++
		}
	}

	total := len(components)

	if unhealthyCount > 0 {
		return "UNHEALTHY"
	} else if degradedCount > total/2 {
		return "DEGRADED"
	} else if healthyCount == total {
		return "HEALTHY"
	} else {
		return "DEGRADED"
	}
}

// EnableMonitoring configures monitoring for the memoless protocol
func (k KVStore) EnableMonitoring(ctx cosmos.Context, config MonitoringConfig) error {
	ctx.Logger().Info("Enabling memoless protocol monitoring")

	// Store monitoring configuration as simple key-value
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoStats, "monitoring_config")

	// Simple serialization for configuration
	value := fmt.Sprintf("enabled=%t,interval=%d,alerting=%t",
		config.Enabled, config.MetricsInterval, config.AlertingEnabled)
	store.Set(key, []byte(value))

	ctx.Logger().Info("Monitoring enabled successfully")
	return nil
}

// MonitoringConfig defines monitoring configuration
type MonitoringConfig struct {
	Enabled         bool            `json:"enabled"`
	MetricsInterval int64           `json:"metrics_interval"` // Seconds
	AlertingEnabled bool            `json:"alerting_enabled"`
	AlertThresholds AlertThresholds `json:"alert_thresholds"`
	RetentionPeriod int64           `json:"retention_period"` // Blocks
}

// GenerateOperationalReport creates a comprehensive operational report
func (k KVStore) GenerateOperationalReport(ctx cosmos.Context) (*OperationalReport, error) {
	ctx.Logger().Info("Generating operational report")

	// Collect metrics
	metrics, err := k.GetMemolessMetrics(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to collect metrics: %w", err)
	}

	// Perform health checks
	healthReport, err := k.MonitorSystemHealth(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to check system health: %w", err)
	}

	// Check alerts
	thresholds := GetDefaultAlertThresholds()
	alerts, err := k.CheckAlertThresholds(ctx, metrics, thresholds)
	if err != nil {
		return nil, fmt.Errorf("failed to check alert thresholds: %w", err)
	}

	timestamp := ctx.BlockTime().Unix()
	if timestamp == 0 {
		timestamp = ctx.BlockHeight() // Fallback for test environments
	}

	report := &OperationalReport{
		Timestamp:    timestamp,
		BlockHeight:  ctx.BlockHeight(),
		Metrics:      *metrics,
		HealthReport: *healthReport,
		ActiveAlerts: alerts,
		Summary: OperationalSummary{
			SystemStatus:        healthReport.OverallStatus,
			ActiveRegistrations: metrics.ActiveRegistrations,
			ErrorCount:          metrics.SystemErrors,
			AlertCount:          int64(len(alerts)),
		},
	}

	ctx.Logger().Info("Operational report generated",
		"system_status", report.Summary.SystemStatus,
		"active_registrations", report.Summary.ActiveRegistrations,
		"alert_count", report.Summary.AlertCount)

	return report, nil
}

// OperationalReport represents a comprehensive operational status report
type OperationalReport struct {
	Timestamp    int64              `json:"timestamp"`
	BlockHeight  int64              `json:"block_height"`
	Metrics      MemolessMetrics    `json:"metrics"`
	HealthReport SystemHealthReport `json:"health_report"`
	ActiveAlerts []MonitoringAlert  `json:"active_alerts"`
	Summary      OperationalSummary `json:"summary"`
}

// OperationalSummary provides a high-level summary of system status
type OperationalSummary struct {
	SystemStatus        string `json:"system_status"`
	ActiveRegistrations int64  `json:"active_registrations"`
	ErrorCount          int64  `json:"error_count"`
	AlertCount          int64  `json:"alert_count"`
}

// ValidateBasicMemoRegistration performs basic validation for monitoring health checks
func (k KVStore) ValidateBasicMemoRegistration(ctx cosmos.Context, msg thorchaintypes.MsgRegisterMemo) error {
	// Basic validation for health checks - uses existing validation logic
	return msg.ValidateBasic()
}
