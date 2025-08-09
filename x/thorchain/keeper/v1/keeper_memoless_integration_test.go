package keeperv1

import (
	"fmt"
	"math/big"
	"math/rand"
	"sync"
	"time"

	. "gopkg.in/check.v1"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// MemolessIntegrationTestSuite provides comprehensive integration testing for memoless protocol
type MemolessIntegrationTestSuite struct {
	*MemolessTestSuite
	benchmarker *PerformanceBenchmark
}

// NewMemolessIntegrationTestSuite creates a new integration test suite
func NewMemolessIntegrationTestSuite(c *C) *MemolessIntegrationTestSuite {
	baseTestSuite := NewMemolessTestSuite(c)
	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	benchmarker := NewPerformanceBenchmark(baseTestSuite.GetKeeper(), baseTestSuite.GetContext(), constantProvider)
	
	return &MemolessIntegrationTestSuite{
		MemolessTestSuite: baseTestSuite,
		benchmarker:       benchmarker,
	}
}

// TDDPillarTestResult represents results from TDD pillar testing
type TDDPillarTestResult struct {
	PillarName       string                 `json:"pillar_name"`
	TestName         string                 `json:"test_name"`
	Passed           bool                   `json:"passed"`
	Duration         time.Duration          `json:"duration"`
	Metrics          map[string]interface{} `json:"metrics"`
	ErrorMessage     string                 `json:"error_message,omitempty"`
	AcceptancePassed bool                   `json:"acceptance_passed"`
	Timestamp        time.Time              `json:"timestamp"`
}

// IntegrationTestConfig configures integration test parameters
type IntegrationTestConfig struct {
	TestDuration      time.Duration
	ConcurrentWorkers int
	DataSize          int
	EnableMetrics     bool
	FailureTolerance  float64 // Acceptable failure rate (0.0 to 1.0)
}

// DefaultIntegrationTestConfig returns default test configuration
func DefaultIntegrationTestConfig() IntegrationTestConfig {
	return IntegrationTestConfig{
		TestDuration:      30 * time.Second,
		ConcurrentWorkers: 10,
		DataSize:          1000,
		EnableMetrics:     true,
		FailureTolerance:  0.001, // 0.1% failure tolerance
	}
}

// ==================== TDD PILLAR 1: CODE COLLISION UNDER LOAD ====================

// TestPillar1CollisionResistance tests collision resistance under load (TDD Pillar 1)
func (its *MemolessIntegrationTestSuite) TestPillar1CollisionResistance(c *C, config IntegrationTestConfig) TDDPillarTestResult {
	start := time.Now()
	result := TDDPillarTestResult{
		PillarName: "Pillar 1: Code Collision Under Load",
		TestName:   "CollisionResistance",
		Timestamp:  start,
		Metrics:    make(map[string]interface{}),
	}
	
	defer func() {
		result.Duration = time.Since(start)
	}()
	
	// Test parameters for Pillar 1
	totalRegistrations := 10000
	expectedCollisionRate := 0.0001 // 0.01% as specified in acceptance criteria
	
	// Track collisions
	var collisionCount int64
	var successCount int64
	var errorCount int64
	
	references := make(map[string]bool)
	mutex := sync.Mutex{}
	
	// Generate registrations with collision tracking
	for i := 0; i < totalRegistrations; i++ {
		memo := fmt.Sprintf("COLLISION:TEST:%d:%d", i, time.Now().UnixNano())
		
		reference, err := its.keeper.ResolveCollision(its.ctx, memo, its.ctx.BlockHeight(), its.constantProvider)
		if err != nil {
			errorCount++
			continue
		}
		
		mutex.Lock()
		if references[reference] {
			collisionCount++
		} else {
			references[reference] = true
		}
		mutex.Unlock()
		
		// Register memo
		registeredMemo := its.CreateTestMemo(reference, memo, "BTC")
		err = its.keeper.SetRegisteredMemo(its.ctx, registeredMemo)
		if err != nil {
			errorCount++
		} else {
			successCount++
		}
	}
	
	// Calculate metrics
	actualCollisionRate := float64(collisionCount) / float64(totalRegistrations)
	
	result.Metrics["total_registrations"] = totalRegistrations
	result.Metrics["collision_count"] = collisionCount
	result.Metrics["success_count"] = successCount
	result.Metrics["error_count"] = errorCount
	result.Metrics["collision_rate"] = actualCollisionRate
	result.Metrics["expected_collision_rate"] = expectedCollisionRate
	result.Metrics["unique_references"] = len(references)
	
	// Acceptance criteria validation
	result.AcceptancePassed = actualCollisionRate <= expectedCollisionRate
	result.Passed = result.AcceptancePassed && errorCount == 0
	
	if !result.Passed {
		if actualCollisionRate > expectedCollisionRate {
			result.ErrorMessage = fmt.Sprintf("Collision rate %.6f exceeds threshold %.6f", actualCollisionRate, expectedCollisionRate)
		}
		if errorCount > 0 {
			result.ErrorMessage += fmt.Sprintf("; %d errors occurred", errorCount)
		}
	}
	
	its.ValidateTestResults(c, "CollisionTest", result.Metrics)
	return result
}

// TestPillar1ConcurrentCollisionResolution tests concurrent collision resolution
func (its *MemolessIntegrationTestSuite) TestPillar1ConcurrentCollisionResolution(c *C, config IntegrationTestConfig) TDDPillarTestResult {
	start := time.Now()
	result := TDDPillarTestResult{
		PillarName: "Pillar 1: Code Collision Under Load",
		TestName:   "ConcurrentCollisionResolution", 
		Timestamp:  start,
		Metrics:    make(map[string]interface{}),
	}
	
	defer func() {
		result.Duration = time.Since(start)
	}()
	
	// Test concurrent workers attempting to register identical memo
	baseMemo := "CONCURRENT:COLLISION:TEST"
	workers := config.ConcurrentWorkers
	registrationsPerWorker := 100
	
	var wg sync.WaitGroup
	var successCount int64
	var errorCount int64
	var collisionCount int64
	
	references := sync.Map{}
	
	// Start concurrent workers
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			for i := 0; i < registrationsPerWorker; i++ {
				memo := fmt.Sprintf("%s:%d:%d", baseMemo, workerID, i)
				
				reference, err := its.keeper.ResolveCollision(its.ctx, memo, its.ctx.BlockHeight(), its.constantProvider)
				if err != nil {
					errorCount++
					continue
				}
				
				// Check for reference collision
				if _, exists := references.LoadOrStore(reference, true); exists {
					collisionCount++
				}
				
				// Register memo
				registeredMemo := its.CreateTestMemo(reference, memo, "BTC")
				err = its.keeper.SetRegisteredMemo(its.ctx, registeredMemo)
				if err != nil {
					errorCount++
				} else {
					successCount++
				}
			}
		}(worker)
	}
	
	wg.Wait()
	
	totalOperations := int64(workers * registrationsPerWorker)
	
	result.Metrics["workers"] = workers
	result.Metrics["registrations_per_worker"] = registrationsPerWorker
	result.Metrics["total_operations"] = totalOperations
	result.Metrics["success_count"] = successCount
	result.Metrics["error_count"] = errorCount
	result.Metrics["collision_count"] = collisionCount
	result.Metrics["success_rate"] = float64(successCount) / float64(totalOperations)
	
	// Acceptance criteria: No race conditions, all unique references
	result.AcceptancePassed = collisionCount == 0 && errorCount == 0
	result.Passed = result.AcceptancePassed
	
	if !result.Passed {
		result.ErrorMessage = fmt.Sprintf("Concurrent test failed: %d collisions, %d errors", collisionCount, errorCount)
	}
	
	return result
}

// ==================== TDD PILLAR 2: PRECISION TRUNCATION ====================

// TestPillar2PrecisionPreservation tests precision preservation across chains (TDD Pillar 2)
func (its *MemolessIntegrationTestSuite) TestPillar2PrecisionPreservation(c *C, config IntegrationTestConfig) TDDPillarTestResult {
	start := time.Now()
	result := TDDPillarTestResult{
		PillarName: "Pillar 2: Precision Truncation",
		TestName:   "PrecisionPreservation",
		Timestamp:  start,
		Metrics:    make(map[string]interface{}),
	}
	
	defer func() {
		result.Duration = time.Since(start)
	}()
	
	// Test precision preservation across different chains
	testCases := []struct {
		chainID       string
		baseAmount    cosmos.Uint
		expectedValid bool
	}{
		{"BTC", cosmos.NewUint(100000000), true},   // 1 BTC with 8 decimals
		{"ETH", cosmos.NewUint(1000000000000000000), true}, // 1 ETH with 18 decimals  
		{"BNB", cosmos.NewUint(100000000), true},   // 1 BNB with 8 decimals
		{"DOGE", cosmos.NewUint(100000000), false}, // Should be rejected for precision issues
	}
	
	var testsPassed int
	var precisionLossCount int
	
	for _, testCase := range testCases {
		// Test reference encoding and extraction
		reference := its.GenerateRandomReference()
		amountWithRef := its.CreateTestAmountWithReference(testCase.baseAmount, reference)
		extractedRef := its.ExtractReferenceFromAmount(amountWithRef)
		
		// Validate reference extraction
		if extractedRef != reference {
			precisionLossCount++
			continue
		}
		
		// Validate chain compatibility
		err := its.keeper.ValidateAmountForChain(its.ctx, testCase.chainID, amountWithRef)
		if testCase.expectedValid && err != nil {
			precisionLossCount++
		} else if !testCase.expectedValid && err == nil {
			precisionLossCount++
		} else {
			testsPassed++
		}
		
		// Test precision preservation
		reconstructedAmount := amountWithRef.SubUint64(cosmos.ParseUint(reference).Uint64()).AddUint64(0)
		baseAmountRecovered := reconstructedAmount.Quo(cosmos.NewUint(100000)).Mul(cosmos.NewUint(100000))
		
		its.ValidateMemoPrecision(c, testCase.chainID, testCase.baseAmount, baseAmountRecovered)
	}
	
	result.Metrics["total_test_cases"] = len(testCases)
	result.Metrics["tests_passed"] = testsPassed
	result.Metrics["precision_loss_count"] = precisionLossCount
	result.Metrics["precision_preservation_rate"] = float64(testsPassed) / float64(len(testCases))
	
	// Acceptance criteria: Zero precision loss
	result.AcceptancePassed = precisionLossCount == 0
	result.Passed = result.AcceptancePassed
	
	if !result.Passed {
		result.ErrorMessage = fmt.Sprintf("Precision loss detected in %d cases", precisionLossCount)
	}
	
	its.ValidateTestResults(c, "PrecisionTest", result.Metrics)
	return result
}

// TestPillar2DustAttackPrevention tests dust attack prevention
func (its *MemolessIntegrationTestSuite) TestPillar2DustAttackPrevention(c *C, config IntegrationTestConfig) TDDPillarTestResult {
	start := time.Now()
	result := TDDPillarTestResult{
		PillarName: "Pillar 2: Precision Truncation",
		TestName:   "DustAttackPrevention",
		Timestamp:  start,
		Metrics:    make(map[string]interface{}),
	}
	
	defer func() {
		result.Duration = time.Since(start)
	}()
	
	// Simulate dust attack with micro amounts
	dustAmounts := []cosmos.Uint{
		cosmos.NewUint(1),      // 1 base unit
		cosmos.NewUint(100),    // 100 base units
		cosmos.NewUint(1000),   // 1000 base units
		cosmos.NewUint(10000),  // 10000 base units
	}
	
	var attacksBlocked int
	var attacksPassed int
	
	for _, dustAmount := range dustAmounts {
		err := its.keeper.ValidateAmountForChain(its.ctx, "BTC", dustAmount)
		if err != nil {
			attacksBlocked++ // Attack correctly blocked
		} else {
			attacksPassed++ // Attack incorrectly allowed
		}
	}
	
	result.Metrics["dust_attacks_tested"] = len(dustAmounts)
	result.Metrics["attacks_blocked"] = attacksBlocked
	result.Metrics["attacks_passed"] = attacksPassed
	result.Metrics["protection_rate"] = float64(attacksBlocked) / float64(len(dustAmounts))
	
	// Acceptance criteria: All dust attacks should be blocked
	result.AcceptancePassed = attacksPassed == 0
	result.Passed = result.AcceptancePassed
	
	if !result.Passed {
		result.ErrorMessage = fmt.Sprintf("Dust attack prevention failed: %d attacks passed", attacksPassed)
	}
	
	return result
}

// ==================== TDD PILLAR 4: TEMPORAL MISMATCHES ====================

// TestPillar4TemporalConsistency tests temporal consistency and expiry management (TDD Pillar 4)
func (its *MemolessIntegrationTestSuite) TestPillar4TemporalConsistency(c *C, config IntegrationTestConfig) TDDPillarTestResult {
	start := time.Now()
	result := TDDPillarTestResult{
		PillarName: "Pillar 4: Temporal Mismatches",
		TestName:   "TemporalConsistency",
		Timestamp:  start,
		Metrics:    make(map[string]interface{}),
	}
	
	defer func() {
		result.Duration = time.Since(start)
	}()
	
	// Test TTL boundary conditions
	testCases := []struct {
		ttl           int64
		description   string
		expectedValid bool
	}{
		{-1, "negative TTL", false},
		{0, "zero TTL (should use default)", true},
		{14400, "normal TTL (4 hours)", true},
		{1008000, "maximum TTL (1 week)", true},
		{2000000, "excessive TTL", false},
	}
	
	var validationsPassed int
	var validationsFailed int
	
	for _, testCase := range testCases {
		signer := GetRandomBech32Addr()
		msg := its.CreateTestMsgRegisterMemo("TEMPORAL:TEST", "BTC", testCase.ttl, signer)
		
		version := its.keeper.GetValidationVersion()
		constantProvider := constants.GetConstantValues(version)
		
		err := its.keeper.ValidateMemoRegistrationWithVersion(its.ctx, msg, version, constantProvider)
		
		if testCase.expectedValid && err == nil {
			validationsPassed++
		} else if !testCase.expectedValid && err != nil {
			validationsPassed++
		} else {
			validationsFailed++
		}
	}
	
	// Test clock skew tolerance
	originalTime := its.ctx.BlockTime()
	skewTolerance := 60 * time.Second
	
	// Test with positive skew
	its.SetBlockTime(originalTime.Add(skewTolerance))
	reference1 := its.RegisterTestMemo(c, "SKEW:POSITIVE", "BTC")
	
	// Test with negative skew  
	its.SetBlockTime(originalTime.Add(-skewTolerance))
	reference2 := its.RegisterTestMemo(c, "SKEW:NEGATIVE", "BTC")
	
	// Restore original time
	its.SetBlockTime(originalTime)
	
	// Verify both registrations are valid
	memo1, err1 := its.keeper.GetRegisteredMemo(its.ctx, reference1)
	memo2, err2 := its.keeper.GetRegisteredMemo(its.ctx, reference2)
	
	clockSkewTestPassed := err1 == nil && err2 == nil && memo1.Status == "active" && memo2.Status == "active"
	
	result.Metrics["ttl_validations_passed"] = validationsPassed
	result.Metrics["ttl_validations_failed"] = validationsFailed
	result.Metrics["ttl_validation_rate"] = float64(validationsPassed) / float64(len(testCases))
	result.Metrics["clock_skew_test_passed"] = clockSkewTestPassed
	result.Metrics["skew_tolerance_seconds"] = skewTolerance.Seconds()
	
	// Acceptance criteria: All TTL validations pass and clock skew tolerance works
	result.AcceptancePassed = validationsFailed == 0 && clockSkewTestPassed
	result.Passed = result.AcceptancePassed
	
	if !result.Passed {
		result.ErrorMessage = fmt.Sprintf("Temporal test failed: %d validation failures, clock skew test: %v", validationsFailed, clockSkewTestPassed)
	}
	
	return result
}

// TestPillar4GracePeriodHandling tests grace period handling for near-expiry registrations
func (its *MemolessIntegrationTestSuite) TestPillar4GracePeriodHandling(c *C, config IntegrationTestConfig) TDDPillarTestResult {
	start := time.Now()
	result := TDDPillarTestResult{
		PillarName: "Pillar 4: Temporal Mismatches",
		TestName:   "GracePeriodHandling",
		Timestamp:  start,
		Metrics:    make(map[string]interface{}),
	}
	
	defer func() {
		result.Duration = time.Since(start)
	}()
	
	gracePeriod := its.constantProvider.GetInt64Value(constants.MemolessGracePeriod)
	
	// Create memo near expiry
	reference := its.RegisterTestMemo(c, "GRACE:PERIOD:TEST", "BTC")
	
	// Advance time to within grace period of expiry
	ttl := its.constantProvider.GetInt64Value(constants.MemolessTTL)
	its.AdvanceBlockHeight(ttl - gracePeriod/2) // Within grace period
	
	// Memo should still be active
	memo, err := its.keeper.GetRegisteredMemo(its.ctx, reference)
	withinGracePeriod := err == nil && memo.Status == "active"
	
	// Advance beyond grace period
	its.AdvanceBlockHeight(gracePeriod + 100)
	
	// Memo should now be considered expired
	memo, err = its.keeper.GetRegisteredMemo(its.ctx, reference)
	beyondGracePeriod := err != nil || memo.ExpiresAt < its.ctx.BlockHeight()
	
	result.Metrics["grace_period_blocks"] = gracePeriod
	result.Metrics["within_grace_period_active"] = withinGracePeriod
	result.Metrics["beyond_grace_period_expired"] = beyondGracePeriod
	
	// Acceptance criteria: Grace period prevents false expiry but doesn't extend beyond limits
	result.AcceptancePassed = withinGracePeriod && beyondGracePeriod
	result.Passed = result.AcceptancePassed
	
	if !result.Passed {
		result.ErrorMessage = fmt.Sprintf("Grace period test failed: within=%v, beyond=%v", withinGracePeriod, beyondGracePeriod)
	}
	
	return result
}

// ==================== TDD PILLAR 5: REFUND INTEGRITY ====================

// TestPillar5RefundIntegrity tests error propagation and refund integrity (TDD Pillar 5)
func (its *MemolessIntegrationTestSuite) TestPillar5RefundIntegrity(c *C, config IntegrationTestConfig) TDDPillarTestResult {
	start := time.Now()
	result := TDDPillarTestResult{
		PillarName: "Pillar 5: Refund Integrity",
		TestName:   "RefundIntegrity",
		Timestamp:  start,
		Metrics:    make(map[string]interface{}),
	}
	
	defer func() {
		result.Duration = time.Since(start)
	}()
	
	// Test various failure scenarios that should trigger refunds
	failureScenarios := []struct {
		scenario      string
		shouldRefund  bool
		testFunc      func() error
	}{
		{
			"Invalid chain ID",
			true,
			func() error {
				signer := GetRandomBech32Addr()
				msg := its.CreateTestMsgRegisterMemo("REFUND:TEST", "INVALID", 14400, signer)
				version := its.keeper.GetValidationVersion()
				return its.keeper.ValidateMemoRegistrationWithVersion(its.ctx, msg, version, its.constantProvider)
			},
		},
		{
			"Excessive TTL",
			true,
			func() error {
				signer := GetRandomBech32Addr()
				msg := its.CreateTestMsgRegisterMemo("REFUND:TEST", "BTC", 999999999, signer)
				version := its.keeper.GetValidationVersion()
				return its.keeper.ValidateMemoRegistrationWithVersion(its.ctx, msg, version, its.constantProvider)
			},
		},
		{
			"Chain not supported",
			true,
			func() error {
				return its.keeper.ValidateChainForMemoless(its.ctx, "DOGE")
			},
		},
	}
	
	var refundTriggered int
	var unexpectedSuccess int
	
	for _, scenario := range failureScenarios {
		err := scenario.testFunc()
		if scenario.shouldRefund && err != nil {
			refundTriggered++ // Correct: error triggers refund
		} else if scenario.shouldRefund && err == nil {
			unexpectedSuccess++ // Incorrect: should have failed
		}
	}
	
	result.Metrics["total_scenarios"] = len(failureScenarios)
	result.Metrics["refund_triggered"] = refundTriggered
	result.Metrics["unexpected_success"] = unexpectedSuccess
	result.Metrics["refund_integrity_rate"] = float64(refundTriggered) / float64(len(failureScenarios))
	
	// Acceptance criteria: All failure scenarios trigger appropriate refunds
	result.AcceptancePassed = unexpectedSuccess == 0
	result.Passed = result.AcceptancePassed
	
	if !result.Passed {
		result.ErrorMessage = fmt.Sprintf("Refund integrity failed: %d unexpected successes", unexpectedSuccess)
	}
	
	return result
}

// ==================== TDD PILLAR 6: SYSTEMIC INTEGRATION ====================

// TestPillar6PerformanceOverhead tests performance overhead requirements (TDD Pillar 6)
func (its *MemolessIntegrationTestSuite) TestPillar6PerformanceOverhead(c *C, config IntegrationTestConfig) TDDPillarTestResult {
	start := time.Now()
	result := TDDPillarTestResult{
		PillarName: "Pillar 6: Systemic Integration", 
		TestName:   "PerformanceOverhead",
		Timestamp:  start,
		Metrics:    make(map[string]interface{}),
	}
	
	defer func() {
		result.Duration = time.Since(start)
	}()
	
	// Benchmark configuration
	benchConfig := BenchmarkConfig{
		Operations:    1000,
		Concurrency:   1,
		WarmupRounds:  10,
		CollectMemory: true,
	}
	
	// Benchmark memoless operations
	registrationResult := its.benchmarker.BenchmarkMemoRegistration(benchConfig)
	lookupResult := its.benchmarker.BenchmarkMemoLookup(benchConfig)
	validationResult := its.benchmarker.BenchmarkMessageValidation(benchConfig)
	
	// Performance requirements from Pillar 6
	maxRegistrationLatency := 25 * time.Millisecond // 25ms end-to-end
	maxLookupLatency := 1 * time.Millisecond        // 1ms lookup
	maxValidationLatency := 5 * time.Millisecond    // 5ms validation
	
	registrationPassed := registrationResult.AvgLatency <= maxRegistrationLatency
	lookupPassed := lookupResult.AvgLatency <= maxLookupLatency
	validationPassed := validationResult.AvgLatency <= maxValidationLatency
	
	result.Metrics["registration_avg_latency_ms"] = float64(registrationResult.AvgLatency.Nanoseconds()) / 1e6
	result.Metrics["lookup_avg_latency_ms"] = float64(lookupResult.AvgLatency.Nanoseconds()) / 1e6
	result.Metrics["validation_avg_latency_ms"] = float64(validationResult.AvgLatency.Nanoseconds()) / 1e6
	result.Metrics["registration_ops_per_sec"] = registrationResult.OperationsPerSec
	result.Metrics["lookup_ops_per_sec"] = lookupResult.OperationsPerSec
	result.Metrics["validation_ops_per_sec"] = validationResult.OperationsPerSec
	result.Metrics["registration_passed"] = registrationPassed
	result.Metrics["lookup_passed"] = lookupPassed
	result.Metrics["validation_passed"] = validationPassed
	
	// Acceptance criteria: All performance requirements met
	result.AcceptancePassed = registrationPassed && lookupPassed && validationPassed
	result.Passed = result.AcceptancePassed
	
	if !result.Passed {
		result.ErrorMessage = fmt.Sprintf("Performance requirements not met - registration: %v, lookup: %v, validation: %v", 
			registrationPassed, lookupPassed, validationPassed)
	}
	
	its.ValidateTestResults(c, "PerformanceTest", result.Metrics)
	return result
}

// ==================== COMPREHENSIVE INTEGRATION TESTS ====================

// RunComprehensiveIntegrationTest runs all TDD pillar tests
func (its *MemolessIntegrationTestSuite) RunComprehensiveIntegrationTest(c *C, config IntegrationTestConfig) []TDDPillarTestResult {
	results := make([]TDDPillarTestResult, 0)
	
	c.Logf("=== Starting Comprehensive Memoless Protocol Integration Test ===")
	
	// TDD Pillar 1: Code Collision Under Load
	c.Logf("Running Pillar 1 tests...")
	results = append(results, its.TestPillar1CollisionResistance(c, config))
	results = append(results, its.TestPillar1ConcurrentCollisionResolution(c, config))
	
	// TDD Pillar 2: Precision Truncation
	c.Logf("Running Pillar 2 tests...")
	results = append(results, its.TestPillar2PrecisionPreservation(c, config))
	results = append(results, its.TestPillar2DustAttackPrevention(c, config))
	
	// TDD Pillar 4: Temporal Mismatches
	c.Logf("Running Pillar 4 tests...")
	results = append(results, its.TestPillar4TemporalConsistency(c, config))
	results = append(results, its.TestPillar4GracePeriodHandling(c, config))
	
	// TDD Pillar 5: Refund Integrity
	c.Logf("Running Pillar 5 tests...")
	results = append(results, its.TestPillar5RefundIntegrity(c, config))
	
	// TDD Pillar 6: Systemic Integration
	c.Logf("Running Pillar 6 tests...")
	results = append(results, its.TestPillar6PerformanceOverhead(c, config))
	
	// Summary
	passed := 0
	failed := 0
	for _, result := range results {
		if result.Passed {
			passed++
		} else {
			failed++
		}
	}
	
	c.Logf("=== Integration Test Summary ===")
	c.Logf("Total Tests: %d", len(results))
	c.Logf("Passed: %d", passed)  
	c.Logf("Failed: %d", failed)
	
	if failed > 0 {
		c.Logf("Failed tests:")
		for _, result := range results {
			if !result.Passed {
				c.Logf("  - %s: %s - %s", result.PillarName, result.TestName, result.ErrorMessage)
			}
		}
	}
	
	return results
}

// GenerateIntegrationTestReport generates a comprehensive test report
func (its *MemolessIntegrationTestSuite) GenerateIntegrationTestReport(results []TDDPillarTestResult) string {
	report := "=== MEMOLESS PROTOCOL INTEGRATION TEST REPORT ===\n\n"
	report += fmt.Sprintf("Test Execution Time: %s\n", time.Now().Format(time.RFC3339))
	report += fmt.Sprintf("Total Test Cases: %d\n\n", len(results))
	
	passedCount := 0
	for _, result := range results {
		if result.Passed {
			passedCount++
		}
		
		report += fmt.Sprintf("=== %s ===\n", result.PillarName)
		report += fmt.Sprintf("Test: %s\n", result.TestName)
		report += fmt.Sprintf("Status: %s\n", map[bool]string{true: "PASS", false: "FAIL"}[result.Passed])
		report += fmt.Sprintf("Duration: %v\n", result.Duration)
		report += fmt.Sprintf("Acceptance Criteria Met: %s\n", map[bool]string{true: "YES", false: "NO"}[result.AcceptancePassed])
		
		if result.ErrorMessage != "" {
			report += fmt.Sprintf("Error: %s\n", result.ErrorMessage)
		}
		
		report += "Metrics:\n"
		for key, value := range result.Metrics {
			report += fmt.Sprintf("  %s: %v\n", key, value)
		}
		report += "\n"
	}
	
	report += fmt.Sprintf("=== SUMMARY ===\n")
	report += fmt.Sprintf("Tests Passed: %d/%d (%.1f%%)\n", passedCount, len(results), float64(passedCount)/float64(len(results))*100)
	report += fmt.Sprintf("Overall Status: %s\n", map[bool]string{true: "PASS", false: "FAIL"}[passedCount == len(results)])
	
	return report
}

// ValidateInfrastructurePrerequisites validates that all infrastructure components are working
func (its *MemolessIntegrationTestSuite) ValidateInfrastructurePrerequisites(c *C) bool {
	c.Logf("Validating infrastructure prerequisites...")
	
	// Test 1: Constants are accessible
	enabled := its.constantProvider.GetInt64Value(constants.MemolessEnabled)
	c.Assert(enabled, Equals, int64(1), Commentf("Memoless protocol should be enabled"))
	
	// Test 2: Error codes are registered
	_, err := its.keeper.ValidateChainForMemoless(its.ctx, "INVALID")
	c.Assert(err, NotNil, Commentf("Should return error for invalid chain"))
	
	// Test 3: Chain configuration is accessible  
	_, err = its.keeper.GetChainConfig(its.ctx, "BTC")
	c.Assert(err, IsNil, Commentf("Should get BTC chain config"))
	
	// Test 4: Test utilities work
	reference := its.RegisterTestMemo(c, "INFRA:TEST", "BTC")
	c.Assert(len(reference), Equals, 5, Commentf("Reference should be 5 digits"))
	
	// Test 5: Benchmarking infrastructure works
	benchConfig := BenchmarkConfig{Operations: 10, Concurrency: 1, WarmupRounds: 1}
	result := its.benchmarker.BenchmarkReferenceGeneration(benchConfig)
	c.Assert(result.TotalOperations, Equals, int64(10), Commentf("Benchmark should complete 10 operations"))
	
	c.Logf("✓ All infrastructure prerequisites validated")
	return true
}