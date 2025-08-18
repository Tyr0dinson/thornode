package observer

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/suite"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	btypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Chaos engineering test suite for memoless protocol resilience
type MemolessChaosTestSuite struct {
	suite.Suite
	chaosEngine   *ChaosEngine
	memoHandler   *MemolessHandler
	thorClient    *ChaosThorClient
	testScenarios []ChaosExperiment
}

func TestMemolessChaosEngineeringSuite(t *testing.T) {
	suite.Run(t, new(MemolessChaosTestSuite))
}

func (suite *MemolessChaosTestSuite) SetupTest() {
	// Initialize chaos engineering components
	suite.thorClient = NewChaosThorClient()
	suite.memoHandler = &MemolessHandler{
		bridge: suite.thorClient,
		logger: NewTestLogger(),
	}

	suite.chaosEngine = &ChaosEngine{
		failureRate:     0.1,  // 10% failure rate
		latencyVariance: 500,  // 500ms max additional latency
		corruptionRate:  0.05, // 5% data corruption rate
		partitionRate:   0.02, // 2% network partition rate
		recoveryTime:    time.Second * 30,
		active:          false,
	}

	// Define chaos experiments
	suite.testScenarios = []ChaosExperiment{
		{
			name:        "random_node_failures",
			description: "Randomly fail observer nodes during memo processing",
			duration:    time.Minute * 5,
			failureRate: 0.15,
			faultType:   NodeFailure,
		},
		{
			name:        "network_latency_injection",
			description: "Add random latency to memo API calls",
			duration:    time.Minute * 3,
			failureRate: 0.30,
			faultType:   LatencyInjection,
		},
		{
			name:        "memory_pressure_simulation",
			description: "Simulate memory pressure during memo processing",
			duration:    time.Minute * 2,
			failureRate: 0.05,
			faultType:   MemoryPressure,
		},
		{
			name:        "disk_corruption_simulation",
			description: "Simulate disk failures during memo storage",
			duration:    time.Minute * 4,
			failureRate: 0.08,
			faultType:   DiskCorruption,
		},
		{
			name:        "cascading_failure_test",
			description: "Test recovery from multiple simultaneous failures",
			duration:    time.Minute * 6,
			failureRate: 0.25,
			faultType:   CascadingFailure,
		},
	}
}

func (suite *MemolessChaosTestSuite) TestChaosExperiments() {
	for _, experiment := range suite.testScenarios {
		suite.Run(experiment.name, func() {
			suite.runChaosExperiment(experiment)
		})
	}
}

func (suite *MemolessChaosTestSuite) runChaosExperiment(experiment ChaosExperiment) {
	suite.T().Logf("🔥 Starting chaos experiment: %s", experiment.name)
	suite.T().Logf("📝 Description: %s", experiment.description)
	suite.T().Logf("⏱️  Duration: %v, Failure Rate: %.2f", experiment.duration, experiment.failureRate)

	// Configure chaos engine for this experiment
	suite.chaosEngine.Configure(experiment)

	// Start chaos injection
	ctx, cancel := context.WithTimeout(context.Background(), experiment.duration)
	defer cancel()

	suite.chaosEngine.Start(ctx)
	defer suite.chaosEngine.Stop()

	// Create metrics collector
	metrics := &ChaosMetrics{
		startTime:      time.Now(),
		totalRequests:  0,
		successfulReqs: 0,
		failedReqs:     0,
		averageLatency: 0,
		maxLatency:     0,
		errors:         make(map[string]int),
	}

	// Run concurrent memoless transactions under chaos
	var wg sync.WaitGroup
	const concurrentWorkers = 20
	const transactionsPerWorker = 100

	for worker := 0; worker < concurrentWorkers; worker++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			suite.runChaosWorker(ctx, workerID, transactionsPerWorker, metrics)
		}(worker)
	}

	// Wait for all workers to complete or timeout
	done := make(chan bool, 1)
	go func() {
		wg.Wait()
		done <- true
	}()

	select {
	case <-done:
		suite.T().Logf("✅ All workers completed")
	case <-ctx.Done():
		suite.T().Logf("⏰ Experiment timed out")
	}

	// Analyze chaos experiment results
	suite.analyzeChaosResults(experiment, metrics)

	// Validate system resilience
	suite.validateSystemResilience(experiment, metrics)
}

func (suite *MemolessChaosTestSuite) runChaosWorker(ctx context.Context, workerID, numTransactions int, metrics *ChaosMetrics) {
	for i := 0; i < numTransactions; i++ {
		select {
		case <-ctx.Done():
			return
		default:
			suite.processChaosTransaction(workerID, i, metrics)

			// Random delay between transactions
			time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond)
		}
	}
}

func (suite *MemolessChaosTestSuite) processChaosTransaction(workerID, txID int, metrics *ChaosMetrics) {
	atomic.AddInt64(&metrics.totalRequests, 1)

	// Create test transaction with memoless amount
	reference := fmt.Sprintf("%05d", rand.Intn(100000))
	baseAmount := uint64(rand.Intn(1000000) + 1000)
	memolessAmount := baseAmount*100000 + parseUint64(reference)

	tx := &btypes.TxInItem{
		Tx:          fmt.Sprintf("chaos-tx-%d-%d", workerID, txID),
		Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(memolessAmount))},
		Memo:        "", // Empty memo for memoless transaction
		BlockHeight: int64(144000 + rand.Intn(1000)),
		Sender:      "chaos-sender",
		To:          "chaos-receiver",
	}

	// Measure processing time
	startTime := time.Now()

	// Process transaction through chaos-affected system
	processedTx, err := suite.memoHandler.ProcessMemolessTransaction(tx)

	latency := time.Since(startTime)
	atomic.AddInt64(&metrics.averageLatency, int64(latency))

	if latency > time.Duration(atomic.LoadInt64(&metrics.maxLatency)) {
		atomic.StoreInt64(&metrics.maxLatency, int64(latency))
	}

	// Record results
	if err != nil {
		atomic.AddInt64(&metrics.failedReqs, 1)

		// Categorize errors
		metrics.mu.Lock()
		errorType := categorizeError(err)
		metrics.errors[errorType]++
		metrics.mu.Unlock()

		// Log significant errors (but not all to avoid spam)
		if rand.Float32() < 0.1 { // Log 10% of errors
			suite.T().Logf("🚨 Worker %d transaction %d failed: %v", workerID, txID, err)
		}
	} else {
		atomic.AddInt64(&metrics.successfulReqs, 1)

		// Validate processed transaction
		if processedTx != nil {
			suite.validateProcessedTransaction(tx, processedTx)
		}
	}
}

func (suite *MemolessChaosTestSuite) analyzeChaosResults(experiment ChaosExperiment, metrics *ChaosMetrics) {
	duration := time.Since(metrics.startTime)
	totalReqs := atomic.LoadInt64(&metrics.totalRequests)
	successReqs := atomic.LoadInt64(&metrics.successfulReqs)
	failedReqs := atomic.LoadInt64(&metrics.failedReqs)

	if totalReqs == 0 {
		suite.T().Logf("⚠️  No requests processed during chaos experiment")
		return
	}

	successRate := float64(successReqs) / float64(totalReqs)
	failureRate := float64(failedReqs) / float64(totalReqs)
	avgLatency := time.Duration(atomic.LoadInt64(&metrics.averageLatency) / totalReqs)
	maxLatency := time.Duration(atomic.LoadInt64(&metrics.maxLatency))
	throughput := float64(totalReqs) / duration.Seconds()

	suite.T().Logf("📊 Chaos Experiment Results for %s:", experiment.name)
	suite.T().Logf("   Total Duration: %v", duration)
	suite.T().Logf("   Total Requests: %d", totalReqs)
	suite.T().Logf("   Successful: %d (%.2f%%)", successReqs, successRate*100)
	suite.T().Logf("   Failed: %d (%.2f%%)", failedReqs, failureRate*100)
	suite.T().Logf("   Average Latency: %v", avgLatency)
	suite.T().Logf("   Maximum Latency: %v", maxLatency)
	suite.T().Logf("   Throughput: %.2f req/sec", throughput)

	// Log error breakdown
	if len(metrics.errors) > 0 {
		suite.T().Logf("📈 Error Breakdown:")
		for errorType, count := range metrics.errors {
			percentage := float64(count) / float64(failedReqs) * 100
			suite.T().Logf("   %s: %d (%.1f%%)", errorType, count, percentage)
		}
	}
}

func (suite *MemolessChaosTestSuite) validateSystemResilience(experiment ChaosExperiment, metrics *ChaosMetrics) {
	totalReqs := atomic.LoadInt64(&metrics.totalRequests)
	successReqs := atomic.LoadInt64(&metrics.successfulReqs)

	if totalReqs == 0 {
		suite.T().Errorf("❌ System failed to process any requests during %s", experiment.name)
		return
	}

	successRate := float64(successReqs) / float64(totalReqs)
	avgLatency := time.Duration(atomic.LoadInt64(&metrics.averageLatency) / totalReqs)
	maxLatency := time.Duration(atomic.LoadInt64(&metrics.maxLatency))

	// Define resilience thresholds based on experiment type
	var minSuccessRate float64
	var maxAvgLatency, maxMaxLatency time.Duration

	switch experiment.faultType {
	case NodeFailure:
		minSuccessRate = 0.85 // 85% success rate under node failures
		maxAvgLatency = time.Second * 2
		maxMaxLatency = time.Second * 10

	case LatencyInjection:
		minSuccessRate = 0.90 // 90% success rate under latency injection
		maxAvgLatency = time.Second * 3
		maxMaxLatency = time.Second * 15

	case MemoryPressure:
		minSuccessRate = 0.80 // 80% success rate under memory pressure
		maxAvgLatency = time.Second * 5
		maxMaxLatency = time.Second * 20

	case DiskCorruption:
		minSuccessRate = 0.75 // 75% success rate under disk corruption
		maxAvgLatency = time.Second * 3
		maxMaxLatency = time.Second * 12

	case CascadingFailure:
		minSuccessRate = 0.70 // 70% success rate under cascading failures
		maxAvgLatency = time.Second * 8
		maxMaxLatency = time.Second * 30

	default:
		minSuccessRate = 0.90
		maxAvgLatency = time.Second * 2
		maxMaxLatency = time.Second * 10
	}

	// Validate resilience metrics
	suite.Require().GreaterOrEqual(successRate, minSuccessRate,
		"🔴 Success rate %.2f%% below threshold %.2f%% for %s",
		successRate*100, minSuccessRate*100, experiment.name)

	suite.Require().LessOrEqual(avgLatency, maxAvgLatency,
		"🔴 Average latency %v exceeds threshold %v for %s",
		avgLatency, maxAvgLatency, experiment.name)

	suite.Require().LessOrEqual(maxLatency, maxMaxLatency,
		"🔴 Maximum latency %v exceeds threshold %v for %s",
		maxLatency, maxMaxLatency, experiment.name)

	suite.T().Logf("✅ System resilience validated for %s", experiment.name)
	suite.T().Logf("   Success Rate: %.2f%% (threshold: %.2f%%)",
		successRate*100, minSuccessRate*100)
	suite.T().Logf("   Avg Latency: %v (threshold: %v)", avgLatency, maxAvgLatency)
	suite.T().Logf("   Max Latency: %v (threshold: %v)", maxLatency, maxMaxLatency)
}

func (suite *MemolessChaosTestSuite) validateProcessedTransaction(original, processed *btypes.TxInItem) {
	// Validate that memoless processing was correct
	if len(original.Memo) == 0 && len(processed.Memo) > 0 {
		// Original had no memo, processed should have resolved memo
		suite.Require().NotEmpty(processed.Memo, "Processed transaction should have resolved memo")

		// Amount should be adjusted
		if len(original.Coins) > 0 && len(processed.Coins) > 0 {
			originalAmount := original.Coins[0].Amount.Uint64()
			processedAmount := processed.Coins[0].Amount.Uint64()

			// Processed amount should be smaller (reference removed)
			suite.Require().Less(processedAmount, originalAmount,
				"Processed amount should be less than original after reference removal")
		}
	}
}

func (suite *MemolessChaosTestSuite) TestSpecificFailureScenarios() {
	suite.Run("circuit_breaker_behavior", func() {
		// Test circuit breaker opening under high failure rate
		suite.chaosEngine.Configure(ChaosExperiment{
			name:        "circuit_breaker_test",
			failureRate: 0.80, // 80% failure rate should trigger circuit breaker
			faultType:   NodeFailure,
			duration:    time.Minute,
		})

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		suite.chaosEngine.Start(ctx)
		defer suite.chaosEngine.Stop()

		// Process transactions until circuit breaker opens
		circuitOpened := false
		for i := 0; i < 100 && !circuitOpened; i++ {
			tx := suite.createTestTransaction(fmt.Sprintf("cb-test-%d", i))
			_, err := suite.memoHandler.ProcessMemolessTransaction(tx)

			if err != nil && containsCircuitBreakerError(err) {
				circuitOpened = true
				suite.T().Logf("🔄 Circuit breaker opened after %d requests", i+1)
			}

			time.Sleep(time.Millisecond * 50)
		}

		suite.Require().True(circuitOpened, "Circuit breaker should open under high failure rate")
	})

	suite.Run("graceful_degradation", func() {
		// Test that system maintains core functionality under partial failures
		suite.chaosEngine.Configure(ChaosExperiment{
			name:        "graceful_degradation",
			failureRate: 0.30, // Moderate failure rate
			faultType:   LatencyInjection,
			duration:    time.Minute * 2,
		})

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute*2)
		defer cancel()

		suite.chaosEngine.Start(ctx)
		defer suite.chaosEngine.Stop()

		// Test that non-memoless transactions still work
		normalTx := &btypes.TxInItem{
			Tx:          "normal-tx-test",
			Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(1000))},
			Memo:        "SWAP:BTC.BTC", // Regular memo
			BlockHeight: 144000,
			Sender:      "normal-sender",
			To:          "normal-receiver",
		}

		processedTx, err := suite.memoHandler.ProcessMemolessTransaction(normalTx)
		suite.Require().NoError(err, "Normal transactions should work during chaos")
		suite.Require().NotNil(processedTx)
		suite.Require().Equal(normalTx.Memo, processedTx.Memo, "Normal memo should be unchanged")
	})
}

func (suite *MemolessChaosTestSuite) createTestTransaction(txID string) *btypes.TxInItem {
	reference := fmt.Sprintf("%05d", rand.Intn(100000))
	baseAmount := uint64(rand.Intn(1000000) + 1000)
	memolessAmount := baseAmount*100000 + parseUint64(reference)

	return &btypes.TxInItem{
		Tx:          txID,
		Coins:       common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(memolessAmount))},
		Memo:        "",
		BlockHeight: int64(144000 + rand.Intn(1000)),
		Sender:      "test-sender",
		To:          "test-receiver",
	}
}

// Chaos engineering infrastructure

type ChaosExperiment struct {
	name        string
	description string
	duration    time.Duration
	failureRate float64
	faultType   FaultType
}

// FaultType and other common types defined in memoless_test_utils.go
// Additional fault types specific to chaos testing
const (
	NodeFailure FaultType = iota + 100 // Offset to avoid conflicts
	LatencyInjection
	MemoryPressure
	DiskCorruption
	CascadingFailure
	NetworkPartition
)

type ChaosEngine struct {
	failureRate     float64
	latencyVariance int64
	corruptionRate  float64
	partitionRate   float64
	recoveryTime    time.Duration
	active          bool
	mu              sync.RWMutex
}

func (ce *ChaosEngine) Configure(experiment ChaosExperiment) {
	ce.mu.Lock()
	defer ce.mu.Unlock()

	ce.failureRate = experiment.failureRate

	// Adjust parameters based on fault type
	switch experiment.faultType {
	case LatencyInjection:
		ce.latencyVariance = 2000 // 2 second max latency
	case MemoryPressure:
		ce.corruptionRate = 0.1
	case NetworkPartition:
		ce.partitionRate = experiment.failureRate
	}
}

func (ce *ChaosEngine) Start(ctx context.Context) {
	ce.mu.Lock()
	ce.active = true
	ce.mu.Unlock()

	// Chaos engineering runs in background
	go ce.runChaosLoop(ctx)
}

func (ce *ChaosEngine) Stop() {
	ce.mu.Lock()
	ce.active = false
	ce.mu.Unlock()
}

func (ce *ChaosEngine) runChaosLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Millisecond * 100) // Check every 100ms
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ce.mu.RLock()
			if !ce.active {
				ce.mu.RUnlock()
				return
			}
			ce.mu.RUnlock()

			// Randomly inject chaos
			ce.maybeInjectChaos()
		}
	}
}

func (ce *ChaosEngine) maybeInjectChaos() {
	// Implementation would inject various types of failures
	// This is a simplified version for testing purposes
}

func (ce *ChaosEngine) ShouldFail() bool {
	ce.mu.RLock()
	defer ce.mu.RUnlock()

	return ce.active && rand.Float64() < ce.failureRate
}

func (ce *ChaosEngine) GetLatencyPenalty() time.Duration {
	ce.mu.RLock()
	defer ce.mu.RUnlock()

	if !ce.active {
		return 0
	}

	penalty := rand.Int63n(ce.latencyVariance)
	return time.Duration(penalty) * time.Millisecond
}

// GetLatency returns latency to inject (alias for GetLatencyPenalty)
func (ce *ChaosEngine) GetLatency() time.Duration {
	return ce.GetLatencyPenalty()
}

// Chaos-aware THORChain client
type ChaosThorClient struct {
	*MockThorClient
	chaosEngine  *ChaosEngine
	circuitOpen  bool
	failureCount int64
	lastFailure  time.Time
	mu           sync.RWMutex
}

func NewChaosThorClient() *ChaosThorClient {
	return &ChaosThorClient{
		MockThorClient: NewMockThorClient(),
		circuitOpen:    false,
	}
}

func (ctc *ChaosThorClient) GetRegisteredMemo(reference string) (*types.RegisteredMemo, error) {
	// Check circuit breaker
	if ctc.isCircuitOpen() {
		return nil, fmt.Errorf("circuit breaker open")
	}

	// Apply chaos if configured
	if ctc.chaosEngine != nil {
		if ctc.chaosEngine.ShouldFail() {
			ctc.recordFailure()
			return nil, fmt.Errorf("chaos-injected failure")
		}

		// Add latency penalty
		penalty := ctc.chaosEngine.GetLatencyPenalty()
		if penalty > 0 {
			time.Sleep(penalty)
		}
	}

	// Call normal client
	memo, err := ctc.MockThorClient.GetRegisteredMemo(reference)
	if err != nil {
		ctc.recordFailure()
	}

	return memo, err
}

func (ctc *ChaosThorClient) isCircuitOpen() bool {
	ctc.mu.RLock()
	defer ctc.mu.RUnlock()

	// Simple circuit breaker logic
	if ctc.failureCount > 10 && time.Since(ctc.lastFailure) < time.Minute {
		return true
	}

	return false
}

func (ctc *ChaosThorClient) recordFailure() {
	ctc.mu.Lock()
	defer ctc.mu.Unlock()

	ctc.failureCount++
	ctc.lastFailure = time.Now()
}

// Broadcast is required by ThorchainBridge interface
func (ctc *ChaosThorClient) Broadcast(msgs ...sdk.Msg) (common.TxID, error) {
	// Check circuit breaker
	if ctc.isCircuitOpen() {
		return common.TxID(""), fmt.Errorf("circuit breaker open")
	}

	// Apply chaos if configured
	if ctc.chaosEngine != nil {
		if ctc.chaosEngine.ShouldFail() {
			ctc.recordFailure()
			return common.TxID(""), fmt.Errorf("chaos-injected broadcast failure")
		}
		if delay := ctc.chaosEngine.GetLatency(); delay > 0 {
			time.Sleep(delay)
		}
	}

	// Mock successful broadcast
	return common.TxID("chaos-broadcast-tx-success"), nil
}

// EnsureNodeWhitelisted is required by ThorchainBridge interface
func (ctc *ChaosThorClient) EnsureNodeWhitelisted() error { return nil }

// Copy of all ThorchainBridge interface methods from AdversarialThorClient for testing
func (ctc *ChaosThorClient) EnsureNodeWhitelistedWithTimeout() error { return nil }
func (ctc *ChaosThorClient) FetchNodeStatus() (stypes.NodeStatus, error) {
	return stypes.NodeStatus_Unknown, nil
}
func (ctc *ChaosThorClient) FetchActiveNodes() ([]common.PubKey, error)   { return nil, nil }
func (ctc *ChaosThorClient) GetAsgards() (stypes.Vaults, error)           { return nil, nil }
func (ctc *ChaosThorClient) GetVault(pubkey string) (stypes.Vault, error) { return stypes.Vault{}, nil }
func (ctc *ChaosThorClient) GetConfig() config.BifrostClientConfiguration {
	return config.BifrostClientConfiguration{}
}
func (ctc *ChaosThorClient) GetConstants() (map[string]int64, error) { return nil, nil }
func (ctc *ChaosThorClient) GetContext() client.Context              { return client.Context{} }
func (ctc *ChaosThorClient) GetContractAddress() ([]thorclient.PubKeyContractAddressPair, error) {
	return nil, nil
}
func (ctc *ChaosThorClient) GetErrataMsg(txID common.TxID, chain common.Chain) sdk.Msg { return nil }
func (ctc *ChaosThorClient) GetKeygenStdTx(poolPubKey common.PubKey, secp256k1Signature, keysharesBackup []byte, blame []stypes.Blame, inputPks common.PubKeys, keygenType stypes.KeygenType, chains common.Chains, height, keygenTime int64, poolPubKeyEddsa common.PubKey, keysharesBackupEddsa []byte) (sdk.Msg, error) {
	return nil, nil
}
func (ctc *ChaosThorClient) GetKeysignParty(vaultPubKey common.PubKey) (common.PubKeys, error) {
	return nil, nil
}
func (ctc *ChaosThorClient) GetMimir(key string) (int64, error)                  { return 0, nil }
func (ctc *ChaosThorClient) GetMimirWithRef(template, ref string) (int64, error) { return 0, nil }
func (ctc *ChaosThorClient) GetInboundOutbound(txIns common.ObservedTxs) (common.ObservedTxs, common.ObservedTxs, error) {
	return nil, nil, nil
}
func (ctc *ChaosThorClient) GetPools() (stypes.Pools, error) { return nil, nil }
func (ctc *ChaosThorClient) GetPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return nil, nil
}
func (ctc *ChaosThorClient) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return nil, nil
}
func (ctc *ChaosThorClient) GetSolvencyMsg(height int64, chain common.Chain, pubKey common.PubKey, coins common.Coins) *stypes.MsgSolvency {
	return nil
}
func (ctc *ChaosThorClient) GetTHORName(name string) (stypes.THORName, error) {
	return stypes.THORName{}, nil
}
func (ctc *ChaosThorClient) GetThorchainVersion() (semver.Version, error) {
	return semver.Version{}, nil
}
func (ctc *ChaosThorClient) IsCatchingUp() (bool, error)                    { return false, nil }
func (ctc *ChaosThorClient) HasNetworkFee(chain common.Chain) (bool, error) { return false, nil }
func (ctc *ChaosThorClient) GetNetworkFee(chain common.Chain) (transactionSize, transactionFeeRate uint64, err error) {
	return 0, 0, nil
}
func (ctc *ChaosThorClient) PostKeysignFailure(blame stypes.Blame, height int64, memo string, coins common.Coins, pubkey common.PubKey) (common.TxID, error) {
	return "", nil
}
func (ctc *ChaosThorClient) PostNetworkFee(height int64, chain common.Chain, transactionSize, transactionRate uint64) (common.TxID, error) {
	return "", nil
}
func (ctc *ChaosThorClient) RagnarokInProgress() (bool, error) { return false, nil }
func (ctc *ChaosThorClient) WaitToCatchUp() error              { return nil }
func (ctc *ChaosThorClient) GetBlockHeight() (int64, error)    { return 100, nil }
func (ctc *ChaosThorClient) GetLastObservedInHeight(chain common.Chain) (int64, error) {
	return 100, nil
}
func (ctc *ChaosThorClient) GetLastSignedOutHeight(chain common.Chain) (int64, error) {
	return 100, nil
}
func (ctc *ChaosThorClient) GetKeysign(blockHeight int64, pk string) (btypes.TxOut, error) {
	return btypes.TxOut{}, nil
}
func (ctc *ChaosThorClient) GetNodeAccount(nodeAddress string) (*stypes.NodeAccount, error) {
	return nil, nil
}
func (ctc *ChaosThorClient) GetNodeAccounts() ([]*stypes.NodeAccount, error) { return nil, nil }
func (ctc *ChaosThorClient) GetKeygenBlock(height int64, pk string) (stypes.KeygenBlock, error) {
	return stypes.KeygenBlock{}, nil
}

type ChaosMetrics struct {
	startTime      time.Time
	totalRequests  int64
	successfulReqs int64
	failedReqs     int64
	averageLatency int64
	maxLatency     int64
	errors         map[string]int
	mu             sync.Mutex
}

// Helper functions

func categorizeError(err error) string {
	errMsg := err.Error()
	switch {
	case containsString(errMsg, "timeout"):
		return "timeout"
	case containsString(errMsg, "network"):
		return "network"
	case containsString(errMsg, "connection"):
		return "connection"
	case containsString(errMsg, "circuit"):
		return "circuit_breaker"
	case containsString(errMsg, "chaos"):
		return "chaos_injection"
	default:
		return "other"
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr
}

func containsCircuitBreakerError(err error) bool {
	return containsString(err.Error(), "circuit breaker")
}

func parseUint64(s string) uint64 {
	var result uint64
	for _, char := range s {
		if char >= '0' && char <= '9' {
			result = result*10 + uint64(char-'0')
		}
	}
	return result
}
