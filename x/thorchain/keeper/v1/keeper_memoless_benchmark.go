package keeperv1

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// BenchmarkResult represents the result of a performance benchmark
type BenchmarkResult struct {
	OperationName    string        `json:"operation_name"`
	TotalOperations  int64         `json:"total_operations"`
	TotalDuration    time.Duration `json:"total_duration"`
	AvgLatency       time.Duration `json:"avg_latency"`
	MinLatency       time.Duration `json:"min_latency"`
	MaxLatency       time.Duration `json:"max_latency"`
	OperationsPerSec float64       `json:"operations_per_sec"`
	ErrorCount       int64         `json:"error_count"`
	SuccessRate      float64       `json:"success_rate"`
	MemoryAllocated  uint64        `json:"memory_allocated"`
	GoroutinesUsed   int           `json:"goroutines_used"`
	Timestamp        time.Time     `json:"timestamp"`
}

// BenchmarkConfig configures benchmark parameters
type BenchmarkConfig struct {
	Operations    int           // Number of operations to perform
	Concurrency   int           // Number of concurrent workers
	Duration      time.Duration // Duration to run benchmark (if 0, use Operations)
	WarmupRounds  int           // Number of warmup rounds
	CollectMemory bool          // Whether to collect memory statistics
}

// PerformanceBenchmark provides performance benchmarking capabilities
type PerformanceBenchmark struct {
	keeper           KVStore
	ctx              cosmos.Context
	constantProvider constants.ConstantValues
	results          []BenchmarkResult
	mutex            sync.RWMutex
}

// NewPerformanceBenchmark creates a new performance benchmark instance
func NewPerformanceBenchmark(keeper KVStore, ctx cosmos.Context, constantProvider constants.ConstantValues) *PerformanceBenchmark {
	return &PerformanceBenchmark{
		keeper:           keeper,
		ctx:              ctx,
		constantProvider: constantProvider,
		results:          make([]BenchmarkResult, 0),
	}
}

// BenchmarkReferenceGeneration benchmarks reference generation performance
func (pb *PerformanceBenchmark) BenchmarkReferenceGeneration(config BenchmarkConfig) BenchmarkResult {
	return pb.runBenchmark("ReferenceGeneration", config, func() error {
		memo := "SWAP:BTC.BTC::thor123"
		blockHeight := pb.ctx.BlockHeight()
		_ = GenerateReference(memo, blockHeight)
		return nil
	})
}

// BenchmarkReferenceGenerationWithSalt benchmarks salted reference generation
func (pb *PerformanceBenchmark) BenchmarkReferenceGenerationWithSalt(config BenchmarkConfig) BenchmarkResult {
	return pb.runBenchmark("ReferenceGenerationWithSalt", config, func() error {
		memo := "SWAP:BTC.BTC::thor123"
		blockHeight := pb.ctx.BlockHeight()
		salt := 42
		_ = GenerateReferenceWithSalt(memo, blockHeight, salt)
		return nil
	})
}

// BenchmarkMemoRegistration benchmarks memo registration performance
func (pb *PerformanceBenchmark) BenchmarkMemoRegistration(config BenchmarkConfig) BenchmarkResult {
	counter := int64(0)
	return pb.runBenchmark("MemoRegistration", config, func() error {
		id := atomic.AddInt64(&counter, 1)
		memo := fmt.Sprintf("SWAP:BTC.BTC::%d", id)

		// Generate reference
		reference, err := pb.keeper.ResolveCollision(pb.ctx, memo, pb.ctx.BlockHeight(), pb.constantProvider)
		if err != nil {
			return err
		}

		// Create and store memo
		registeredMemo := types.RegisteredMemo{
			Reference:    reference,
			Memo:         memo,
			RegisteredAt: pb.ctx.BlockHeight(),
			ExpiresAt:    pb.ctx.BlockHeight() + 14400,
			ChainId:      "BTC",
			Status:       "active",
		}

		return pb.keeper.SetRegisteredMemo(pb.ctx, registeredMemo)
	})
}

// BenchmarkMemoLookup benchmarks memo lookup performance
func (pb *PerformanceBenchmark) BenchmarkMemoLookup(config BenchmarkConfig) BenchmarkResult {
	// Pre-populate with test data
	references := pb.prepareTestData(config.Operations)
	counter := int64(0)

	return pb.runBenchmark("MemoLookup", config, func() error {
		id := atomic.AddInt64(&counter, 1) % int64(len(references))
		reference := references[id]

		_, err := pb.keeper.GetRegisteredMemo(pb.ctx, reference)
		return err
	})
}

// BenchmarkMemoExists benchmarks memo existence check performance
func (pb *PerformanceBenchmark) BenchmarkMemoExists(config BenchmarkConfig) BenchmarkResult {
	// Pre-populate with test data
	references := pb.prepareTestData(config.Operations)
	counter := int64(0)

	return pb.runBenchmark("MemoExists", config, func() error {
		id := atomic.AddInt64(&counter, 1) % int64(len(references))
		reference := references[id]

		_ = pb.keeper.HasRegisteredMemo(pb.ctx, reference)
		return nil
	})
}

// BenchmarkCollisionResolution benchmarks collision resolution performance
func (pb *PerformanceBenchmark) BenchmarkCollisionResolution(config BenchmarkConfig) BenchmarkResult {
	// Pre-populate with data to force collisions
	memo := "COLLISION:TEST:MEMO"
	for i := 0; i < 50; i++ {
		reference := fmt.Sprintf("%05d", 10000+i)
		registeredMemo := types.RegisteredMemo{
			Reference:    reference,
			Memo:         fmt.Sprintf("%s:%d", memo, i),
			RegisteredAt: pb.ctx.BlockHeight(),
			ExpiresAt:    pb.ctx.BlockHeight() + 14400,
			ChainId:      "BTC",
			Status:       "active",
		}
		pb.keeper.SetRegisteredMemo(pb.ctx, registeredMemo)
	}

	counter := int64(0)
	return pb.runBenchmark("CollisionResolution", config, func() error {
		id := atomic.AddInt64(&counter, 1)
		testMemo := fmt.Sprintf("%s:collision:%d", memo, id)
		_, err := pb.keeper.ResolveCollision(pb.ctx, testMemo, pb.ctx.BlockHeight(), pb.constantProvider)
		return err
	})
}

// BenchmarkChainValidation benchmarks chain validation performance
func (pb *PerformanceBenchmark) BenchmarkChainValidation(config BenchmarkConfig) BenchmarkResult {
	chains := []string{"BTC", "ETH", "BNB", "BCH", "LTC"}
	counter := int64(0)

	return pb.runBenchmark("ChainValidation", config, func() error {
		id := atomic.AddInt64(&counter, 1) % int64(len(chains))
		chainID := chains[id]

		return pb.keeper.ValidateChainForMemoless(pb.ctx, chainID)
	})
}

// BenchmarkAmountValidation benchmarks amount validation performance
func (pb *PerformanceBenchmark) BenchmarkAmountValidation(config BenchmarkConfig) BenchmarkResult {
	amounts := []cosmos.Uint{
		cosmos.NewUint(1000000),
		cosmos.NewUint(10000000),
		cosmos.NewUint(100000000),
		cosmos.NewUint(1000000000),
	}
	counter := int64(0)

	return pb.runBenchmark("AmountValidation", config, func() error {
		id := atomic.AddInt64(&counter, 1) % int64(len(amounts))
		amount := amounts[id]

		return pb.keeper.ValidateAmountForChain(pb.ctx, "BTC", amount)
	})
}

// BenchmarkCleanupOperation benchmarks cleanup operation performance
func (pb *PerformanceBenchmark) BenchmarkCleanupOperation(config BenchmarkConfig) BenchmarkResult {
	// Create expired memos for cleanup
	pb.prepareExpiredTestData(1000)

	return pb.runBenchmark("CleanupOperation", config, func() error {
		return pb.keeper.DeleteExpiredMemos(pb.ctx, pb.ctx.BlockHeight())
	})
}

// BenchmarkConcurrentRegistration benchmarks concurrent registration performance
func (pb *PerformanceBenchmark) BenchmarkConcurrentRegistration(config BenchmarkConfig) BenchmarkResult {
	return pb.runConcurrentBenchmark("ConcurrentRegistration", config, func(workerID int) error {
		memo := fmt.Sprintf("SWAP:BTC.BTC::worker:%d:%d", workerID, time.Now().UnixNano())

		reference, err := pb.keeper.ResolveCollision(pb.ctx, memo, pb.ctx.BlockHeight(), pb.constantProvider)
		if err != nil {
			return err
		}

		registeredMemo := types.RegisteredMemo{
			Reference:    reference,
			Memo:         memo,
			RegisteredAt: pb.ctx.BlockHeight(),
			ExpiresAt:    pb.ctx.BlockHeight() + 14400,
			ChainId:      "BTC",
			Status:       "active",
		}

		return pb.keeper.SetRegisteredMemo(pb.ctx, registeredMemo)
	})
}

// BenchmarkMessageValidation benchmarks enhanced message validation performance
func (pb *PerformanceBenchmark) BenchmarkMessageValidation(config BenchmarkConfig) BenchmarkResult {
	version := GetCurrentVersion()
	counter := int64(0)

	return pb.runBenchmark("MessageValidation", config, func() error {
		id := atomic.AddInt64(&counter, 1)
		signer := GetRandomBech32Addr()

		msg := types.MsgRegisterMemo{
			Memo:    fmt.Sprintf("SWAP:BTC.BTC::%d", id),
			Ttl:     14400,
			ChainId: "BTC",
			Signer:  signer,
		}

		return pb.keeper.ValidateMemoRegistrationWithVersion(pb.ctx, msg, version, pb.constantProvider)
	})
}

// runBenchmark runs a single-threaded benchmark
func (pb *PerformanceBenchmark) runBenchmark(name string, config BenchmarkConfig, operation func() error) BenchmarkResult {
	var memBefore, memAfter runtime.MemStats
	if config.CollectMemory {
		runtime.GC()
		runtime.ReadMemStats(&memBefore)
	}

	// Warmup
	for i := 0; i < config.WarmupRounds; i++ {
		operation()
	}

	// Initialize tracking variables
	var totalDuration time.Duration
	var minLatency = time.Hour // Initialize to large value
	var maxLatency time.Duration
	var errorCount int64
	var successCount int64

	start := time.Now()

	// Run benchmark
	for i := 0; i < config.Operations; i++ {
		opStart := time.Now()
		err := operation()
		opDuration := time.Since(opStart)

		totalDuration += opDuration
		if opDuration < minLatency {
			minLatency = opDuration
		}
		if opDuration > maxLatency {
			maxLatency = opDuration
		}

		if err != nil {
			atomic.AddInt64(&errorCount, 1)
		} else {
			atomic.AddInt64(&successCount, 1)
		}
	}

	actualDuration := time.Since(start)

	if config.CollectMemory {
		runtime.ReadMemStats(&memAfter)
	}

	// Calculate results
	result := BenchmarkResult{
		OperationName:    name,
		TotalOperations:  int64(config.Operations),
		TotalDuration:    actualDuration,
		AvgLatency:       totalDuration / time.Duration(config.Operations),
		MinLatency:       minLatency,
		MaxLatency:       maxLatency,
		OperationsPerSec: float64(config.Operations) / actualDuration.Seconds(),
		ErrorCount:       errorCount,
		SuccessRate:      float64(successCount) / float64(config.Operations) * 100,
		GoroutinesUsed:   1,
		Timestamp:        time.Now(),
	}

	if config.CollectMemory {
		result.MemoryAllocated = memAfter.TotalAlloc - memBefore.TotalAlloc
	}

	if minLatency == time.Hour {
		result.MinLatency = 0
	}

	pb.addResult(result)
	return result
}

// runConcurrentBenchmark runs a concurrent benchmark
func (pb *PerformanceBenchmark) runConcurrentBenchmark(name string, config BenchmarkConfig, operation func(int) error) BenchmarkResult {
	var memBefore, memAfter runtime.MemStats
	if config.CollectMemory {
		runtime.GC()
		runtime.ReadMemStats(&memBefore)
	}

	// Shared counters
	var totalOps int64
	var errorCount int64
	var totalDuration int64     // in nanoseconds
	var minLatency int64 = 1e18 // Initialize to large value
	var maxLatency int64

	operationsPerWorker := config.Operations / config.Concurrency
	if operationsPerWorker == 0 {
		operationsPerWorker = 1
	}

	var wg sync.WaitGroup
	start := time.Now()

	for worker := 0; worker < config.Concurrency; worker++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for i := 0; i < operationsPerWorker; i++ {
				opStart := time.Now()
				err := operation(workerID)
				opDuration := time.Since(opStart)

				atomic.AddInt64(&totalOps, 1)
				atomic.AddInt64(&totalDuration, opDuration.Nanoseconds())

				// Update min latency
				for {
					current := atomic.LoadInt64(&minLatency)
					if opDuration.Nanoseconds() >= current {
						break
					}
					if atomic.CompareAndSwapInt64(&minLatency, current, opDuration.Nanoseconds()) {
						break
					}
				}

				// Update max latency
				for {
					current := atomic.LoadInt64(&maxLatency)
					if opDuration.Nanoseconds() <= current {
						break
					}
					if atomic.CompareAndSwapInt64(&maxLatency, current, opDuration.Nanoseconds()) {
						break
					}
				}

				if err != nil {
					atomic.AddInt64(&errorCount, 1)
				}
			}
		}(worker)
	}

	wg.Wait()
	actualDuration := time.Since(start)

	if config.CollectMemory {
		runtime.ReadMemStats(&memAfter)
	}

	successCount := totalOps - errorCount

	// Calculate results
	result := BenchmarkResult{
		OperationName:    name,
		TotalOperations:  totalOps,
		TotalDuration:    actualDuration,
		AvgLatency:       time.Duration(totalDuration / totalOps),
		MinLatency:       time.Duration(atomic.LoadInt64(&minLatency)),
		MaxLatency:       time.Duration(atomic.LoadInt64(&maxLatency)),
		OperationsPerSec: float64(totalOps) / actualDuration.Seconds(),
		ErrorCount:       errorCount,
		SuccessRate:      float64(successCount) / float64(totalOps) * 100,
		GoroutinesUsed:   config.Concurrency,
		Timestamp:        time.Now(),
	}

	if config.CollectMemory {
		result.MemoryAllocated = memAfter.TotalAlloc - memBefore.TotalAlloc
	}

	if atomic.LoadInt64(&minLatency) == 1e18 {
		result.MinLatency = 0
	}

	pb.addResult(result)
	return result
}

// prepareTestData creates test data for benchmarks
func (pb *PerformanceBenchmark) prepareTestData(count int) []string {
	references := make([]string, 0, count)

	for i := 0; i < count; i++ {
		memo := fmt.Sprintf("BENCH:TEST:MEMO:%d", i)
		reference := fmt.Sprintf("%05d", (i%90000)+10000)

		registeredMemo := types.RegisteredMemo{
			Reference:    reference,
			Memo:         memo,
			RegisteredAt: pb.ctx.BlockHeight(),
			ExpiresAt:    pb.ctx.BlockHeight() + 14400,
			ChainId:      "BTC",
			Status:       "active",
		}

		pb.keeper.SetRegisteredMemo(pb.ctx, registeredMemo)
		references = append(references, reference)
	}

	return references
}

// prepareExpiredTestData creates expired test data for cleanup benchmarks
func (pb *PerformanceBenchmark) prepareExpiredTestData(count int) {
	currentHeight := pb.ctx.BlockHeight()

	for i := 0; i < count; i++ {
		memo := fmt.Sprintf("EXPIRED:BENCH:MEMO:%d", i)
		reference := fmt.Sprintf("%05d", (i%90000)+10000)

		registeredMemo := types.RegisteredMemo{
			Reference:    reference,
			Memo:         memo,
			RegisteredAt: currentHeight - 20000,
			ExpiresAt:    currentHeight - 1000, // Expired
			ChainId:      "BTC",
			Status:       "expired",
		}

		pb.keeper.SetRegisteredMemo(pb.ctx, registeredMemo)
	}
}

// addResult adds a benchmark result to the collection
func (pb *PerformanceBenchmark) addResult(result BenchmarkResult) {
	pb.mutex.Lock()
	defer pb.mutex.Unlock()
	pb.results = append(pb.results, result)
}

// GetResults returns all benchmark results
func (pb *PerformanceBenchmark) GetResults() []BenchmarkResult {
	pb.mutex.RLock()
	defer pb.mutex.RUnlock()

	results := make([]BenchmarkResult, len(pb.results))
	copy(results, pb.results)
	return results
}

// GenerateReport generates a comprehensive benchmark report
func (pb *PerformanceBenchmark) GenerateReport() string {
	results := pb.GetResults()
	if len(results) == 0 {
		return "No benchmark results available"
	}

	report := "=== MEMOLESS PROTOCOL PERFORMANCE BENCHMARK REPORT ===\n\n"

	for _, result := range results {
		report += fmt.Sprintf("Operation: %s\n", result.OperationName)
		report += fmt.Sprintf("  Total Operations: %d\n", result.TotalOperations)
		report += fmt.Sprintf("  Total Duration: %v\n", result.TotalDuration)
		report += fmt.Sprintf("  Average Latency: %v\n", result.AvgLatency)
		report += fmt.Sprintf("  Min Latency: %v\n", result.MinLatency)
		report += fmt.Sprintf("  Max Latency: %v\n", result.MaxLatency)
		report += fmt.Sprintf("  Operations/sec: %.2f\n", result.OperationsPerSec)
		report += fmt.Sprintf("  Success Rate: %.2f%%\n", result.SuccessRate)
		report += fmt.Sprintf("  Error Count: %d\n", result.ErrorCount)
		report += fmt.Sprintf("  Goroutines Used: %d\n", result.GoroutinesUsed)
		if result.MemoryAllocated > 0 {
			report += fmt.Sprintf("  Memory Allocated: %d bytes\n", result.MemoryAllocated)
		}
		report += fmt.Sprintf("  Timestamp: %v\n\n", result.Timestamp.Format(time.RFC3339))
	}

	return report
}

// RunFullBenchmarkSuite runs a comprehensive benchmark suite
func (pb *PerformanceBenchmark) RunFullBenchmarkSuite(baseConfig BenchmarkConfig) map[string]BenchmarkResult {
	results := make(map[string]BenchmarkResult)

	// Reference generation benchmarks
	config := baseConfig
	config.Operations = 10000
	results["ReferenceGeneration"] = pb.BenchmarkReferenceGeneration(config)

	config.Operations = 10000
	results["ReferenceGenerationWithSalt"] = pb.BenchmarkReferenceGenerationWithSalt(config)

	// Storage operation benchmarks
	config.Operations = 5000
	results["MemoRegistration"] = pb.BenchmarkMemoRegistration(config)

	config.Operations = 10000
	results["MemoLookup"] = pb.BenchmarkMemoLookup(config)

	config.Operations = 10000
	results["MemoExists"] = pb.BenchmarkMemoExists(config)

	// Collision resolution benchmarks
	config.Operations = 1000
	results["CollisionResolution"] = pb.BenchmarkCollisionResolution(config)

	// Validation benchmarks
	config.Operations = 5000
	results["ChainValidation"] = pb.BenchmarkChainValidation(config)

	config.Operations = 5000
	results["AmountValidation"] = pb.BenchmarkAmountValidation(config)

	config.Operations = 5000
	results["MessageValidation"] = pb.BenchmarkMessageValidation(config)

	// Cleanup benchmarks
	config.Operations = 10
	results["CleanupOperation"] = pb.BenchmarkCleanupOperation(config)

	// Concurrent benchmarks
	config.Operations = 1000
	config.Concurrency = 10
	results["ConcurrentRegistration"] = pb.BenchmarkConcurrentRegistration(config)

	return results
}
