package keeperv1

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/constants"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MemolessMonitoringTestSuite struct{}

var _ = Suite(&MemolessMonitoringTestSuite{})

// TestMemolessMetricsCollection tests operational metrics collection
func (s *MemolessMonitoringTestSuite) TestMemolessMetricsCollection(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Register some test memos
	signer := GetRandomValidatorNode(NodeActive).NodeAddress
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	// Active memo
	msg1 := thorchaintypes.MsgRegisterMemo{
		Memo:    "SWAP:BTC.BTC:thor1metrics1:14400",
		ChainId: "BTC",
		Ttl:     14400,
		Signer:  signer,
		Amount:  "100000000",
	}
	_, err = k.ProcessMemoRegistration(ctx, msg1, constantProvider)
	c.Assert(err, IsNil)

	// Expired memo
	expiredMemo := thorchaintypes.RegisteredMemo{
		Reference:    "88888",
		Memo:         "SWAP:BTC.BTC:thor1expired:14400",
		ChainId:      "BTC",
		RegisteredAt: ctx.BlockHeight() - 100,
		ExpiresAt:    ctx.BlockHeight() - 50, // Already expired
		Status:       "active",
		Signer:       signer,
	}
	err = k.SetRegisteredMemo(ctx, expiredMemo)
	c.Assert(err, IsNil)

	// Collect metrics
	metrics, err := k.GetMemolessMetrics(ctx)
	c.Assert(err, IsNil, Commentf("Should collect metrics successfully"))
	c.Assert(metrics, NotNil, Commentf("Metrics should not be nil"))

	// Verify metrics
	c.Assert(metrics.TotalRegistrations >= 2, Equals, true, Commentf("Should count registered memos"))
	c.Assert(metrics.ActiveRegistrations >= 1, Equals, true, Commentf("Should count active memos"))
	c.Assert(metrics.ExpiredRegistrations >= 1, Equals, true, Commentf("Should count expired memos"))

	// Verify counter positions are tracked
	c.Assert(len(metrics.CounterPositions) > 0, Equals, true, Commentf("Should track counter positions"))
	c.Assert(metrics.CounterPositions["BTC"] > 0, Equals, true, Commentf("Should have BTC counter position"))

	// Verify chain health status
	c.Assert(len(metrics.ChainHealthStatus) > 0, Equals, true, Commentf("Should track chain health"))
	c.Assert(metrics.ChainHealthStatus["BTC"], Equals, "HEALTHY", Commentf("BTC should be healthy"))

	c.Logf("✓ Metrics collection verified: %d total, %d active, %d expired",
		metrics.TotalRegistrations, metrics.ActiveRegistrations, metrics.ExpiredRegistrations)
}

// TestAlertThresholdMonitoring tests alert threshold evaluation
func (s *MemolessMonitoringTestSuite) TestAlertThresholdMonitoring(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Create test metrics that should trigger alerts
	testMetrics := &MemolessMetrics{
		ActiveRegistrations: 50,
		TotalRegistrations:  100,
		ValidationFailures:  10,      // 10% failure rate
		AverageResponseTime: 2000000, // 2ms (above 1ms threshold)
		CounterPositions:    map[string]int64{"BTC": 15000},
		ChainHealthStatus:   map[string]string{"BTC": "HEALTHY", "ETH": "ERROR"},
		LastUpdated:         ctx.BlockTime().Unix(),
	}

	// Use thresholds that should trigger alerts
	thresholds := AlertThresholds{
		MaxValidationFailRate: 5.0,     // 5% threshold, we have 10%
		MaxResponseTime:       1000000, // 1ms threshold, we have 2ms
		MinActiveRatio:        0.8,     // 80% threshold, we have 50%
	}

	// Check alerts
	alerts, err := k.CheckAlertThresholds(ctx, testMetrics, thresholds)
	c.Assert(err, IsNil, Commentf("Alert checking should work"))
	c.Assert(len(alerts) > 0, Equals, true, Commentf("Should generate alerts for threshold violations"))

	// Verify specific alerts
	foundValidationAlert := false
	foundResponseTimeAlert := false
	foundActiveRatioAlert := false
	foundChainHealthAlert := false

	for _, alert := range alerts {
		switch alert.Component {
		case "validation":
			foundValidationAlert = true
			c.Assert(alert.Severity, Equals, "ERROR", Commentf("Validation alert should be ERROR"))
		case "performance":
			foundResponseTimeAlert = true
			c.Assert(alert.Severity, Equals, "WARN", Commentf("Performance alert should be WARN"))
		case "memo_lifecycle":
			foundActiveRatioAlert = true
		case "chain_integration":
			foundChainHealthAlert = true
			c.Assert(alert.Severity, Equals, "CRITICAL", Commentf("Chain health alert should be CRITICAL"))
		}
	}

	c.Assert(foundValidationAlert, Equals, true, Commentf("Should find validation failure alert"))
	c.Assert(foundResponseTimeAlert, Equals, true, Commentf("Should find response time alert"))
	c.Assert(foundActiveRatioAlert, Equals, true, Commentf("Should find active ratio alert"))
	c.Assert(foundChainHealthAlert, Equals, true, Commentf("Should find chain health alert"))

	c.Logf("✓ Alert threshold monitoring verified: %d alerts generated", len(alerts))
}

// TestSystemHealthMonitoring tests comprehensive system health checks
func (s *MemolessMonitoringTestSuite) TestSystemHealthMonitoring(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Perform health monitoring
	healthReport, err := k.MonitorSystemHealth(ctx)
	c.Assert(err, IsNil, Commentf("Health monitoring should work"))
	c.Assert(healthReport, NotNil, Commentf("Health report should not be nil"))

	// Verify report structure
	c.Assert(healthReport.OverallStatus != "", Equals, true, Commentf("Should have overall status"))
	c.Assert(len(healthReport.Components) > 0, Equals, true, Commentf("Should have component health data"))

	// Verify required components are monitored
	_, hasRefGen := healthReport.Components["reference_generation"]
	c.Assert(hasRefGen, Equals, true, Commentf("Should monitor reference generation"))

	_, hasStorage := healthReport.Components["memo_storage"]
	c.Assert(hasStorage, Equals, true, Commentf("Should monitor memo storage"))

	_, hasValidation := healthReport.Components["validation_system"]
	c.Assert(hasValidation, Equals, true, Commentf("Should monitor validation system"))

	// Verify component health details
	refGenHealth := healthReport.Components["reference_generation"]
	c.Assert(refGenHealth.Status != "", Equals, true, Commentf("Reference generation should have status"))
	c.Assert(len(refGenHealth.Metrics) > 0, Equals, true, Commentf("Should have reference generation metrics"))

	c.Logf("✓ System health monitoring verified: %s overall status", healthReport.OverallStatus)
}

// TestOperationalReportGeneration tests comprehensive operational reporting
func (s *MemolessMonitoringTestSuite) TestOperationalReportGeneration(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Register some memos for realistic report data
	signer := GetRandomValidatorNode(NodeActive).NodeAddress
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	msg := thorchaintypes.MsgRegisterMemo{
		Memo:    "SWAP:BTC.BTC:thor1report:14400",
		ChainId: "BTC",
		Ttl:     14400,
		Signer:  signer,
		Amount:  "100000000",
	}
	_, err = k.ProcessMemoRegistration(ctx, msg, constantProvider)
	c.Assert(err, IsNil)

	// Generate operational report
	report, err := k.GenerateOperationalReport(ctx)
	c.Assert(err, IsNil, Commentf("Should generate operational report"))
	c.Assert(report, NotNil, Commentf("Report should not be nil"))

	// Verify report structure
	c.Assert(report.BlockHeight >= 0, Equals, true, Commentf("Should have block height"))
	c.Assert(report.Summary.SystemStatus != "", Equals, true, Commentf("Should have system status"))
	c.Logf("Report timestamp: %d, block height: %d", report.Timestamp, report.BlockHeight)

	// Verify metrics are included
	c.Assert(report.Metrics.TotalRegistrations > 0, Equals, true, Commentf("Should include registration metrics"))

	// Verify health report is included
	c.Assert(len(report.HealthReport.Components) > 0, Equals, true, Commentf("Should include health components"))

	// Verify summary is calculated
	c.Assert(report.Summary.ActiveRegistrations >= 0, Equals, true, Commentf("Should have active registration count"))

	c.Logf("✓ Operational report verified: %s status, %d registrations, %d alerts",
		report.Summary.SystemStatus, report.Summary.ActiveRegistrations, report.Summary.AlertCount)
}

// TestMonitoringConfiguration tests monitoring system configuration
func (s *MemolessMonitoringTestSuite) TestMonitoringConfiguration(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Initialize chain configs
	err := k.InitializeDefaultChainConfigs(ctx)
	c.Assert(err, IsNil)

	// Configure monitoring
	config := MonitoringConfig{
		Enabled:         true,
		MetricsInterval: 60, // 1 minute
		AlertingEnabled: true,
		AlertThresholds: GetDefaultAlertThresholds(),
		RetentionPeriod: 1000, // 1000 blocks
	}

	err = k.EnableMonitoring(ctx, config)
	c.Assert(err, IsNil, Commentf("Should enable monitoring successfully"))

	c.Logf("✓ Monitoring configuration verified: enabled=%t, interval=%ds", config.Enabled, config.MetricsInterval)
}
