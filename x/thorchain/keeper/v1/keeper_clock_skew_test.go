package keeperv1

import (
	"time"

	. "gopkg.in/check.v1"
)

type ClockSkewToleranceSuite struct{}

var _ = Suite(&ClockSkewToleranceSuite{})

func (s *ClockSkewToleranceSuite) TestClockSkewTolerance_HandlesNodeTimeDifferences(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup: Nodes with different clock times (±60 seconds)
	baseTime := time.Now()
	nodeTimestamps := []time.Time{
		baseTime.Add(-60 * time.Second), // Node 1: -60s
		baseTime,                        // Node 2: baseline
		baseTime.Add(30 * time.Second),  // Node 3: +30s
		baseTime.Add(-45 * time.Second), // Node 4: -45s
		baseTime.Add(60 * time.Second),  // Node 5: +60s
	}

	// Test: Calculate consensus time
	consensusTime, err := keeper.CalculateConsensusTime(ctx, nodeTimestamps)
	c.Assert(err, IsNil) // THIS WILL FAIL initially

	// Verify: Consensus time is median of timestamps
	// Sorted timestamps: [-60s, -45s, 0s, +30s, +60s] -> median is 0s (baseTime)
	expectedConsensus := baseTime // Should be the exact middle value
	c.Assert(consensusTime.Unix(), Equals, expectedConsensus.Unix())
}

func (s *ClockSkewToleranceSuite) TestClockSkewTolerance_CalculatesEffectiveTTL(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup: Registration time with clock skew tolerance
	registrationTime := time.Now()
	ttl := int64(14400) // 4 hours

	// Test: Calculate effective TTL with skew buffer
	effectiveTTL, err := keeper.CalculateExpiryWithSkew(ctx, registrationTime, ttl)
	c.Assert(err, IsNil) // THIS WILL FAIL initially

	// Verify: Effective TTL accounts for clock skew buffer
	// Formula: TTL_effective = TTL_registered - 2δ - processing_buffer
	expectedBuffer := int64(240) // 2 * 60 seconds + 120 second processing buffer = 240 blocks
	expectedEffective := ttl - expectedBuffer
	c.Assert(effectiveTTL, Equals, expectedEffective)
}

func (s *ClockSkewToleranceSuite) TestClockSkewTolerance_RejectsExcessiveSkew(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup: Timestamps with excessive clock skew (>60 seconds)
	baseTime := time.Now()
	excessiveTimestamps := []time.Time{
		baseTime.Add(-120 * time.Second), // Node 1: -120s (excessive)
		baseTime,                         // Node 2: baseline
		baseTime.Add(90 * time.Second),   // Node 3: +90s (excessive)
	}

	// Test: Attempt consensus with excessive skew
	_, err := keeper.CalculateConsensusTime(ctx, excessiveTimestamps)

	c.Assert(err, NotNil) // THIS WILL FAIL initially
	c.Check(err.Error(), Matches, ".*excessive clock skew.*")
}

func (s *ClockSkewToleranceSuite) TestClockSkewTolerance_HandlesOddNumberOfNodes(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup: Odd number of nodes (5 nodes)
	baseTime := time.Now()
	nodeTimestamps := []time.Time{
		baseTime.Add(-30 * time.Second), // Node 1: -30s
		baseTime.Add(-10 * time.Second), // Node 2: -10s
		baseTime,                        // Node 3: baseline (median)
		baseTime.Add(20 * time.Second),  // Node 4: +20s
		baseTime.Add(40 * time.Second),  // Node 5: +40s
	}

	// Test: Calculate consensus time with odd number
	consensusTime, err := keeper.CalculateConsensusTime(ctx, nodeTimestamps)
	c.Assert(err, IsNil)

	// Verify: Consensus time is the exact median (middle value)
	expectedConsensus := baseTime // Should be the exact middle value
	c.Assert(consensusTime.Unix(), Equals, expectedConsensus.Unix())
}

func (s *ClockSkewToleranceSuite) TestClockSkewTolerance_HandlesEvenNumberOfNodes(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup: Even number of nodes (4 nodes)
	baseTime := time.Now()
	nodeTimestamps := []time.Time{
		baseTime.Add(-20 * time.Second), // Node 1: -20s
		baseTime.Add(-10 * time.Second), // Node 2: -10s (middle 1)
		baseTime.Add(10 * time.Second),  // Node 3: +10s (middle 2)
		baseTime.Add(30 * time.Second),  // Node 4: +30s
	}

	// Test: Calculate consensus time with even number
	consensusTime, err := keeper.CalculateConsensusTime(ctx, nodeTimestamps)
	c.Assert(err, IsNil)

	// Verify: Consensus time is average of two middle values
	// Average of -10s and +10s = 0s (baseTime)
	expectedConsensus := baseTime
	c.Assert(consensusTime.Unix(), Equals, expectedConsensus.Unix())
}
