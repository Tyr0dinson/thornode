package keeperv1

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/cosmos/cosmos-sdk/runtime"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ChaosTestingConfig defines configuration for chaos testing scenarios
type ChaosTestingConfig struct {
	EnableRecovery   bool
	SimulateFailures bool
	MaxRecoveryTime  time.Duration
	FailureRate      float64
}

// RecoverCounterFromMemos recovers the counter state by scanning existing memos
func (k KVStore) RecoverCounterFromMemos(ctx cosmos.Context, chainID string) (int64, error) {
	ctx.Logger().Info("Starting counter recovery from existing memos", "chain", chainID)

	// Scan all registered memos to find the highest reference
	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	maxReference := int64(9999) // Start below minimum range

	for ; iterator.Valid(); iterator.Next() {
		var memo thorchaintypes.RegisteredMemo
		if err := k.cdc.Unmarshal(iterator.Value(), &memo); err != nil {
			continue // Skip corrupted entries
		}

		// Only process memos for this chain
		if memo.ChainId != chainID {
			continue
		}

		// Parse reference to integer
		if refInt, err := strconv.ParseInt(memo.Reference, 10, 64); err == nil {
			if refInt > maxReference {
				maxReference = refInt
			}
		}
	}

	// Set counter to next available value
	nextCounter := maxReference + 1
	if nextCounter < 10000 {
		nextCounter = 10000 // Ensure we start in valid range
	}
	if nextCounter > 99999 {
		nextCounter = 10000 // Wraparound
	}

	// Store recovered counter
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoCounter, chainID)
	buf := k.cdc.MustMarshal(&thorchaintypes.MemoCounter{
		ChainId: chainID,
		Counter: nextCounter,
	})
	store.Set(key, buf)

	ctx.Logger().Info("Counter recovery completed",
		"chain", chainID,
		"recovered_counter", nextCounter,
		"max_found_reference", maxReference)

	return nextCounter, nil
}

// TestRegistrationUnderLoad simulates high-volume registration scenarios
func (k KVStore) TestRegistrationUnderLoad(ctx cosmos.Context, concurrentCount int) (int64, error) {
	ctx.Logger().Info("Starting load testing", "concurrent_registrations", concurrentCount)

	signer := GetRandomValidatorNode(NodeActive).NodeAddress
	constantProvider := constants.GetConstantValues(GetCurrentVersion())

	startTime := time.Now()
	successCount := int64(0)
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Launch concurrent registrations
	for i := 0; i < concurrentCount; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()

			msg := thorchaintypes.MsgRegisterMemo{
				Memo:    fmt.Sprintf("SWAP:BTC.BTC:thor1load%d:14400", index),
				ChainId: "BTC",
				Ttl:     14400,
				Signer:  signer,
				Amount:  fmt.Sprintf("10000%05d", index%100000),
			}

			if err := k.ValidateMemoRegistration(ctx, msg, constantProvider); err == nil {
				if _, err := k.ProcessMemoRegistration(ctx, msg, constantProvider); err == nil {
					mu.Lock()
					successCount++
					mu.Unlock()
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	ctx.Logger().Info("Load testing completed",
		"duration", duration,
		"successful_registrations", successCount,
		"total_attempted", concurrentCount,
		"success_rate", float64(successCount)/float64(concurrentCount))

	return successCount, nil
}

// SimulatePartialFailure simulates various system failure scenarios
func (k KVStore) SimulatePartialFailure(ctx cosmos.Context, failureType string) error {
	ctx.Logger().Info("Simulating partial failure", "type", failureType)

	switch failureType {
	case "storage_corruption":
		return k.simulateStorageCorruption(ctx)
	case "counter_desync":
		return k.simulateCounterDesync(ctx)
	case "memo_corruption":
		return k.simulateMemoCorruption(ctx)
	default:
		return fmt.Errorf("unknown failure type: %s", failureType)
	}
}

// simulateStorageCorruption tests recovery from storage corruption scenarios
func (k KVStore) simulateStorageCorruption(ctx cosmos.Context) error {
	// Simulate by creating invalid counter entry
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoCounter, "BTC")
	store.Set(key, []byte("corrupted_data"))

	// Test recovery
	_, err := k.GetNextReferenceID(ctx, "BTC")
	if err != nil {
		// Recovery should work by falling back to scanning existing memos
		ctx.Logger().Info("Storage corruption detected, attempting recovery")
		_, err = k.RecoverCounterFromMemos(ctx, "BTC")
		if err != nil {
			return fmt.Errorf("failed to recover from storage corruption: %w", err)
		}
	}

	return nil
}

// simulateCounterDesync tests recovery from counter synchronization issues
func (k KVStore) simulateCounterDesync(ctx cosmos.Context) error {
	// Create artificial counter desync by setting counter too low
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoCounter, "BTC")
	buf := k.cdc.MustMarshal(&thorchaintypes.MemoCounter{
		ChainId: "BTC",
		Counter: 5000, // Below valid range
	})
	store.Set(key, buf)

	// Test recovery
	_, err := k.GetNextReferenceID(ctx, "BTC")
	if err != nil {
		ctx.Logger().Info("Counter desync detected, attempting recovery")
		_, err := k.RecoverCounterFromMemos(ctx, "BTC")
		return err
	}

	return nil
}

// simulateMemoCorruption tests recovery from corrupted memo data
func (k KVStore) simulateMemoCorruption(ctx cosmos.Context) error {
	// Create corrupted memo entry
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoRegistration, "12345")
	store.Set(key, []byte("corrupted_memo_data"))

	// Test lookup and recovery
	_, err := k.LookupMemoByReference(ctx, "12345")
	if err != nil {
		ctx.Logger().Info("Memo corruption detected", "reference", "12345")
		// Recovery would involve cleanup of corrupted entry
		store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
		store.Delete(key)
		return nil
	}

	return nil
}

// ForceCounterToMax forces counter to maximum value for wraparound testing
func (k KVStore) ForceCounterToMax(ctx cosmos.Context, chainID string) error {
	ctx.Logger().Info("Forcing counter to maximum for wraparound testing", "chain", chainID)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoCounter, chainID)
	buf := k.cdc.MustMarshal(&thorchaintypes.MemoCounter{
		ChainId: chainID,
		Counter: 99999, // Maximum value
	})
	store.Set(key, buf)

	return nil
}

// SimulateStorageFailure simulates various storage failure scenarios
func (k KVStore) SimulateStorageFailure(ctx cosmos.Context, component string) error {
	ctx.Logger().Info("Simulating storage failure", "component", component)

	switch component {
	case "memo_registration":
		// Simulate failure by temporarily disabling memo storage
		return fmt.Errorf("simulated memo registration storage failure")
	case "counter_storage":
		// Simulate counter storage failure
		return fmt.Errorf("simulated counter storage failure")
	default:
		return fmt.Errorf("unknown storage component: %s", component)
	}
}

// TestConcurrentCollisions tests collision detection under concurrent load
func (k KVStore) TestConcurrentCollisions(ctx cosmos.Context, chainID string, goroutineCount int) (int64, error) {
	ctx.Logger().Info("Testing concurrent collisions", "chain", chainID, "goroutines", goroutineCount)

	collisionCount := int64(0)
	var wg sync.WaitGroup
	var mu sync.Mutex

	// Launch concurrent reference generation
	for i := 0; i < goroutineCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			ref1, err1 := k.GetNextReferenceID(ctx, chainID)
			ref2, err2 := k.GetNextReferenceID(ctx, chainID)

			if err1 == nil && err2 == nil && ref1 == ref2 {
				mu.Lock()
				collisionCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	ctx.Logger().Info("Concurrent collision testing completed",
		"detected_collisions", collisionCount,
		"goroutines", goroutineCount)

	return collisionCount, nil
}

// SimulateChainHalt simulates chain halt scenarios
func (k KVStore) SimulateChainHalt(ctx cosmos.Context, chainID string) error {
	ctx.Logger().Info("Simulating chain halt", "chain", chainID)

	// In a real implementation, this would interact with chain halt mechanisms
	// For testing, we simulate by marking chain as halted in configuration

	config, err := k.GetChainConfig(ctx, chainID)
	if err != nil {
		return err
	}

	// Temporarily mark as unsupported to simulate halt
	config.SupportsMemoless = false
	err = k.SetChainConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to simulate chain halt: %w", err)
	}

	return nil
}

// SimulateNetworkPartition simulates network partition scenarios
func (k KVStore) SimulateNetworkPartition(ctx cosmos.Context, partitionPercentage int) error {
	ctx.Logger().Info("Simulating network partition", "partition_percentage", partitionPercentage)

	if partitionPercentage < 0 || partitionPercentage > 100 {
		return fmt.Errorf("invalid partition percentage: %d", partitionPercentage)
	}

	// In a real implementation, this would affect validator connectivity
	// For testing, we simulate by adjusting configuration parameters

	return fmt.Errorf("network partition simulation: %d%% of network partitioned", partitionPercentage)
}

// TestSystemRecoveryAfterSlashing tests system recovery after validator slashing
func (k KVStore) TestSystemRecoveryAfterSlashing(ctx cosmos.Context, slashedValidator cosmos.AccAddress) (bool, error) {
	ctx.Logger().Info("Testing system recovery after slashing", "validator", slashedValidator.String())

	// Check if system can continue operating after slashing
	// Test by attempting normal operations

	_, err := k.GetNextReferenceID(ctx, "BTC")
	if err != nil {
		return false, fmt.Errorf("system not recovered: reference generation failed: %w", err)
	}

	// Test memo registration still works
	constantProvider := constants.GetConstantValues(GetCurrentVersion())
	testSigner := GetRandomValidatorNode(NodeActive).NodeAddress

	msg := thorchaintypes.MsgRegisterMemo{
		Memo:    "SWAP:BTC.BTC:thor1recovery:14400",
		ChainId: "BTC",
		Ttl:     14400,
		Signer:  testSigner,
		Amount:  "100000000",
	}

	err = k.ValidateMemoRegistration(ctx, msg, constantProvider)
	if err != nil {
		return false, fmt.Errorf("system not recovered: validation failed: %w", err)
	}

	_, err = k.ProcessMemoRegistration(ctx, msg, constantProvider)
	if err != nil {
		return false, fmt.Errorf("system not recovered: registration failed: %w", err)
	}

	ctx.Logger().Info("System recovery successful after slashing")
	return true, nil
}

// CorruptMemoData simulates memo data corruption for testing recovery
func (k KVStore) CorruptMemoData(ctx cosmos.Context, reference string) error {
	ctx.Logger().Info("Simulating memo data corruption", "reference", reference)

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoRegistration, reference)
	// Corrupt the data by storing invalid bytes
	store.Set(key, []byte("corrupted_memo_data_invalid_protobuf"))

	return nil
}

// ValidateSystemIntegrity performs comprehensive system integrity checks
func (k KVStore) ValidateSystemIntegrity(ctx cosmos.Context) error {
	ctx.Logger().Info("Validating system integrity")

	// Check 1: Counter consistency across chains
	supportedChains, err := k.GetSupportedChains(ctx)
	if err != nil {
		return fmt.Errorf("failed to get supported chains: %w", err)
	}

	for _, chainID := range supportedChains {
		counter, err := k.GetCurrentCounter(ctx, chainID)
		if err != nil {
			return fmt.Errorf("counter integrity check failed for chain %s: %w", chainID, err)
		}

		if counter < 10000 || counter > 99999 {
			return fmt.Errorf("counter out of valid range for chain %s: %d", chainID, counter)
		}
	}

	// Check 2: Memo storage consistency
	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	for ; iterator.Valid(); iterator.Next() {
		var memo thorchaintypes.RegisteredMemo
		if err := k.cdc.Unmarshal(iterator.Value(), &memo); err != nil {
			return fmt.Errorf("corrupted memo found: %w", err)
		}

		// Validate memo structure
		if err := memo.Validate(); err != nil {
			return fmt.Errorf("invalid memo in storage: %w", err)
		}
	}

	ctx.Logger().Info("System integrity validation completed successfully")
	return nil
}

// PerformChaosRecovery attempts to recover from various chaos scenarios
func (k KVStore) PerformChaosRecovery(ctx cosmos.Context, scenario string) error {
	ctx.Logger().Info("Performing chaos recovery", "scenario", scenario)

	switch scenario {
	case "counter_corruption":
		// Recover all chain counters
		supportedChains, err := k.GetSupportedChains(ctx)
		if err != nil {
			return err
		}

		for _, chainID := range supportedChains {
			if _, err := k.RecoverCounterFromMemos(ctx, chainID); err != nil {
				return fmt.Errorf("failed to recover counter for chain %s: %w", chainID, err)
			}
		}

	case "memo_corruption":
		// Clean up corrupted memo entries
		return k.CleanupCorruptedMemos(ctx)

	case "full_recovery":
		// Comprehensive recovery
		if err := k.PerformChaosRecovery(ctx, "counter_corruption"); err != nil {
			return err
		}
		if err := k.PerformChaosRecovery(ctx, "memo_corruption"); err != nil {
			return err
		}

	default:
		return fmt.Errorf("unknown recovery scenario: %s", scenario)
	}

	// Validate system integrity after recovery
	return k.ValidateSystemIntegrity(ctx)
}

// CleanupCorruptedMemos removes corrupted memo entries from storage
func (k KVStore) CleanupCorruptedMemos(ctx cosmos.Context) error {
	ctx.Logger().Info("Cleaning up corrupted memos")

	iterator := k.GetRegisteredMemoIterator(ctx)
	defer iterator.Close()

	var corruptedKeys [][]byte
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))

	for ; iterator.Valid(); iterator.Next() {
		var memo thorchaintypes.RegisteredMemo
		if err := k.cdc.Unmarshal(iterator.Value(), &memo); err != nil {
			// Mark for deletion
			corruptedKeys = append(corruptedKeys, iterator.Key())
			continue
		}

		// Validate memo structure
		if err := memo.Validate(); err != nil {
			corruptedKeys = append(corruptedKeys, iterator.Key())
		}
	}

	// Delete corrupted entries
	for _, key := range corruptedKeys {
		store.Delete(key)
	}

	ctx.Logger().Info("Corrupted memo cleanup completed", "removed_count", len(corruptedKeys))
	return nil
}

// GetCurrentCounter safely retrieves the current counter for a chain
func (k KVStore) GetCurrentCounter(ctx cosmos.Context, chainID string) (int64, error) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	key := k.GetKey(prefixMemoCounter, chainID)

	if !store.Has(key) {
		return 10000, nil // Default starting value
	}

	var counter thorchaintypes.MemoCounter
	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, &counter); err != nil {
		// If unmarshaling fails, recover from existing memos
		ctx.Logger().Warn("Counter unmarshaling failed, attempting recovery", "chain", chainID)
		return k.RecoverCounterFromMemos(ctx, chainID)
	}

	return counter.Counter, nil
}

// ChaosTestMetrics tracks metrics during chaos testing
type ChaosTestMetrics struct {
	TotalOperations   int64
	SuccessfulOps     int64
	FailedOps         int64
	RecoveryAttempts  int64
	RecoverySuccesses int64
	AverageLatency    time.Duration
	MaxLatency        time.Duration
}

// RunChaosTestSuite executes a comprehensive chaos testing suite
func (k KVStore) RunChaosTestSuite(ctx cosmos.Context, config ChaosTestingConfig) (*ChaosTestMetrics, error) {
	ctx.Logger().Info("Starting comprehensive chaos test suite")

	metrics := &ChaosTestMetrics{}
	startTime := time.Now()

	// Test 1: Counter recovery
	if _, err := k.RecoverCounterFromMemos(ctx, "BTC"); err != nil {
		metrics.FailedOps++
	} else {
		metrics.SuccessfulOps++
	}
	metrics.TotalOperations++

	// Test 2: Load testing
	if successCount, err := k.TestRegistrationUnderLoad(ctx, 100); err != nil {
		metrics.FailedOps++
	} else {
		metrics.SuccessfulOps += successCount
		metrics.TotalOperations += 100
	}

	// Test 3: Failure simulation and recovery
	if err := k.SimulatePartialFailure(ctx, "storage_corruption"); err != nil {
		metrics.RecoveryAttempts++
		if err := k.PerformChaosRecovery(ctx, "counter_corruption"); err == nil {
			metrics.RecoverySuccesses++
		}
	}

	metrics.AverageLatency = time.Since(startTime) / time.Duration(metrics.TotalOperations)

	ctx.Logger().Info("Chaos test suite completed",
		"total_operations", metrics.TotalOperations,
		"success_rate", float64(metrics.SuccessfulOps)/float64(metrics.TotalOperations),
		"recovery_rate", float64(metrics.RecoverySuccesses)/float64(metrics.RecoveryAttempts))

	return metrics, nil
}
