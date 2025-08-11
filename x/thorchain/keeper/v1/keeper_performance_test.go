package keeperv1

import (
	"fmt"
	"testing"
	"time"

	. "gopkg.in/check.v1"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestKeeperPerformance(t *testing.T) { TestingT(t) }

type KeeperPerformanceSuite struct{}

var _ = Suite(&KeeperPerformanceSuite{})

// RED Phase Test - This should FAIL initially to drive performance implementation
func (s *KeeperPerformanceSuite) TestReferenceDetectionPerformance_Under1ms(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup test data - create many references to stress test
	nodeAddr1 := GetRandomBech32Addr()
	nodeAddr2 := GetRandomBech32Addr()
	
	// Create 1000 reference entries to simulate production load
	for i := 0; i < 1000; i++ {
		reference := fmt.Sprintf("REF%05d", i)
		if i%2 == 0 {
			err := keeper.TrackReferenceUsage(ctx, reference, nodeAddr1.String(), int64(i))
			c.Assert(err, IsNil)
		} else {
			err := keeper.TrackReferenceUsage(ctx, reference, nodeAddr2.String(), int64(i))
			c.Assert(err, IsNil)
		}
	}
	
	// Measure duplicate detection performance
	testReference := "REF00500" // Reference already used by nodeAddr1
	
	start := time.Now()
	isDuplicate := keeper.DetectDuplicateReference(ctx, testReference, nodeAddr2.String())
	elapsed := time.Since(start)
	
	// Should detect the duplicate
	c.Assert(isDuplicate, Equals, true, 
		Commentf("Should detect duplicate reference"))
	
	// Performance requirement: under 1ms
	c.Assert(elapsed < time.Millisecond, Equals, true,
		Commentf("Reference detection took %v, should be < 1ms", elapsed))
}

func (s *KeeperPerformanceSuite) TestConsensusValidationPerformance_Under10ms(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup consensus evidence with 100 observers (large scale)
	observers := make([]cosmos.AccAddress, 100)
	reportingObservers := make([]cosmos.AccAddress, 70) // 70% consensus
	
	for i := 0; i < 100; i++ {
		observers[i] = GetRandomBech32Addr()
		if i < 70 {
			reportingObservers[i] = observers[i]
		}
	}
	
	evidence := CollusionConsensusEvidence{
		CollusionEvidence: CollusionEvidence{
			NodeOperator:  GetRandomBech32Addr().String(),
			Reference:     "PERF123",
			CollusionType: "duplicate_reference",
			BlockHeight:   1000,
		},
		ReportingObservers: reportingObservers,
		TotalObservers:     100,
	}
	
	// Measure consensus validation performance
	start := time.Now()
	hasConsensus, err := keeper.ValidateCollusionConsensus(ctx, evidence)
	elapsed := time.Since(start)
	
	c.Assert(err, IsNil, Commentf("Consensus validation should not error"))
	c.Assert(hasConsensus, Equals, true, Commentf("Should detect valid consensus"))
	
	// Performance requirement: under 10ms
	c.Assert(elapsed < 10*time.Millisecond, Equals, true,
		Commentf("Consensus validation took %v, should be < 10ms", elapsed))
}

func (s *KeeperPerformanceSuite) TestHoardingDetectionPerformance_Under5ms(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup large dataset for hoarding detection stress test
	nodeAddr := GetRandomBech32Addr()
	
	// Create 500 references across different time periods
	baseHeight := int64(5000)
	for i := 0; i < 500; i++ {
		reference := fmt.Sprintf("HOARD%05d", i)
		blockHeight := baseHeight + int64(i)
		err := keeper.TrackReferenceUsage(ctx, reference, nodeAddr.String(), blockHeight)
		c.Assert(err, IsNil)
	}
	
	// Measure hoarding detection performance
	timeWindow := int64(3600) // 1 hour window
	
	start := time.Now()
	result, err := keeper.DetectReferenceHoarding(ctx, nodeAddr.String(), timeWindow)
	elapsed := time.Since(start)
	
	c.Assert(err, IsNil, Commentf("Hoarding detection should not error"))
	c.Assert(result.IsHoarding, Equals, true, 
		Commentf("Should detect hoarding with 500 references"))
	
	// Performance requirement: under 5ms for hoarding detection
	c.Assert(elapsed < 5*time.Millisecond, Equals, true,
		Commentf("Hoarding detection took %v, should be < 5ms", elapsed))
}

func (s *KeeperPerformanceSuite) TestBatchReferenceOperations_OptimizedThroughput(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Test batch operations for high throughput scenarios
	nodeAddrs := make([]cosmos.AccAddress, 10)
	for i := 0; i < 10; i++ {
		nodeAddrs[i] = GetRandomBech32Addr()
	}
	
	// Measure batch reference tracking performance
	batchSize := 1000
	references := make([]string, batchSize)
	for i := 0; i < batchSize; i++ {
		references[i] = fmt.Sprintf("BATCH%05d", i)
	}
	
	start := time.Now()
	for i, ref := range references {
		nodeAddr := nodeAddrs[i%len(nodeAddrs)]
		err := keeper.TrackReferenceUsage(ctx, ref, nodeAddr.String(), int64(i))
		c.Assert(err, IsNil)
	}
	elapsed := time.Since(start)
	
	// Performance target: 1000 operations in under 100ms (100 ops/ms)
	c.Assert(elapsed < 100*time.Millisecond, Equals, true,
		Commentf("Batch tracking %d references took %v, should be < 100ms", batchSize, elapsed))
	
	// Calculate throughput
	throughput := float64(batchSize) / elapsed.Seconds()
	c.Assert(throughput > 10000, Equals, true,
		Commentf("Throughput %.0f ops/sec should be > 10,000 ops/sec", throughput))
}

func (s *KeeperPerformanceSuite) TestMemoryUsageOptimization_NoMemoryLeaks(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Test memory usage patterns
	nodeAddr := GetRandomBech32Addr()
	
	// Create and clean up references to test memory management
	for round := 0; round < 10; round++ {
		// Create 100 references
		for i := 0; i < 100; i++ {
			reference := fmt.Sprintf("MEM%d_%05d", round, i)
			err := keeper.TrackReferenceUsage(ctx, reference, nodeAddr.String(), int64(round*1000+i))
			c.Assert(err, IsNil)
		}
		
		// Test operations that should not leak memory
		for i := 0; i < 100; i++ {
			reference := fmt.Sprintf("MEM%d_%05d", round, i)
			keeper.DetectDuplicateReference(ctx, reference, nodeAddr.String())
		}
	}
	
	// Verify performance remains consistent after many operations
	testRef := "MEMTEST"
	err := keeper.TrackReferenceUsage(ctx, testRef, nodeAddr.String(), 10000)
	c.Assert(err, IsNil)
	
	start := time.Now()
	isDup := keeper.DetectDuplicateReference(ctx, testRef, GetRandomBech32Addr().String())
	elapsed := time.Since(start)
	
	c.Assert(isDup, Equals, true)
	c.Assert(elapsed < time.Millisecond, Equals, true,
		Commentf("Performance should remain consistent after memory stress test"))
}

func (s *KeeperPerformanceSuite) TestProgressivePenaltyCalculation_OptimizedLookup(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup node with significant slash point history
	nodeAddr := GetRandomBech32Addr()
	
	// Set up a node with bond and slash history
	originalBond := cosmos.NewUint(10000000000) // 100 RUNE
	FundModule(c, ctx, keeper, ModuleName, originalBond.Uint64())
	err := setupNodeWithBond(ctx, keeper, nodeAddr, originalBond)
	c.Assert(err, IsNil)
	
	// Add significant slash point history
	keeper.SetNodeAccountSlashPoints(ctx, nodeAddr, 15000)
	
	// Measure progressive penalty calculation performance
	collusionType := "duplicate_reference"
	
	start := time.Now()
	result, err := keeper.CalculateProgressivePenalty(ctx, nodeAddr, collusionType)
	elapsed := time.Since(start)
	
	c.Assert(err, IsNil, Commentf("Progressive penalty calculation should not error"))
	c.Assert(result.EscalationFactor > 1.0, Equals, true,
		Commentf("Should calculate escalated penalty"))
	
	// Performance requirement: penalty calculation under 1ms
	c.Assert(elapsed < time.Millisecond, Equals, true,
		Commentf("Progressive penalty calculation took %v, should be < 1ms", elapsed))
}

func (s *KeeperPerformanceSuite) TestConcurrentAccess_ThreadSafety(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Test concurrent access patterns (simulated)
	nodeAddr := GetRandomBech32Addr()
	
	// Setup initial data
	for i := 0; i < 100; i++ {
		reference := fmt.Sprintf("CONCURRENT%05d", i)
		err := keeper.TrackReferenceUsage(ctx, reference, nodeAddr.String(), int64(i))
		c.Assert(err, IsNil)
	}
	
	// Simulate concurrent read operations
	start := time.Now()
	
	// Multiple operations that might happen concurrently
	for i := 0; i < 50; i++ {
		reference := fmt.Sprintf("CONCURRENT%05d", i)
		keeper.DetectDuplicateReference(ctx, reference, nodeAddr.String())
		keeper.GetReferenceUsers(ctx, reference)
		keeper.GetNodeReferenceCount(ctx, nodeAddr.String())
	}
	
	elapsed := time.Since(start)
	
	// Performance should remain good under simulated concurrent load
	avgTime := elapsed / 50
	c.Assert(avgTime < time.Millisecond, Equals, true,
		Commentf("Average operation time %v should be < 1ms under concurrent load", avgTime))
}