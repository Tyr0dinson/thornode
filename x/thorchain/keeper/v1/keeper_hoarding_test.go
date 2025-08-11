package keeperv1

import (
	"testing"

	. "gopkg.in/check.v1"
)

func TestKeeperHoarding(t *testing.T) { TestingT(t) }

type KeeperHoardingSuite struct{}

var _ = Suite(&KeeperHoardingSuite{})

// RED Phase Test - This should FAIL initially to drive Task 5E implementation
func (s *KeeperHoardingSuite) TestDetectReferenceHoarding_ExceedsThreshold(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup node that will hoard references
	nodeAddr := GetRandomBech32Addr()
	timeWindow := int64(3600) // 1 hour window
	threshold := 5           // Default threshold: 5 references per hour
	
	// Simulate node registering many references within time window
	references := []string{"10001", "10002", "10003", "10004", "10005", "10006", "10007"}
	baseHeight := int64(1000)
	
	// Track multiple references for the same node within time window
	for i, ref := range references {
		blockHeight := baseHeight + int64(i) // Simulate close block heights
		err := keeper.TrackReferenceUsage(ctx, ref, nodeAddr.String(), blockHeight)
		c.Assert(err, IsNil)
	}
	
	// THIS WILL FAIL initially - method doesn't exist
	result, err := keeper.DetectReferenceHoarding(ctx, nodeAddr.String(), timeWindow)
	c.Assert(err, IsNil, Commentf("Hoarding detection should not error"))
	
	// Verify hoarding is detected when threshold is exceeded
	c.Assert(result.IsHoarding, Equals, true, 
		Commentf("Node with %d references should be detected as hoarding (threshold: %d)", 
			len(references), threshold))
	c.Assert(result.ReferenceCount >= threshold, Equals, true,
		Commentf("Reference count %d should exceed threshold %d", result.ReferenceCount, threshold))
	c.Assert(len(result.DetectedReferences), Equals, len(references),
		Commentf("Should detect all %d registered references", len(references)))
}

func (s *KeeperHoardingSuite) TestDetectReferenceHoarding_BelowThreshold(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup node with normal reference usage
	nodeAddr := GetRandomBech32Addr()
	timeWindow := int64(3600) // 1 hour window
	
	// Simulate normal usage: only 3 references within time window
	references := []string{"20001", "20002", "20003"}
	baseHeight := int64(2000)
	
	for i, ref := range references {
		blockHeight := baseHeight + int64(i)
		err := keeper.TrackReferenceUsage(ctx, ref, nodeAddr.String(), blockHeight)
		c.Assert(err, IsNil)
	}
	
	// THIS WILL FAIL initially - method doesn't exist
	result, err := keeper.DetectReferenceHoarding(ctx, nodeAddr.String(), timeWindow)
	c.Assert(err, IsNil, Commentf("Hoarding detection should not error"))
	
	// Verify hoarding is NOT detected when below threshold
	c.Assert(result.IsHoarding, Equals, false,
		Commentf("Node with %d references should NOT be detected as hoarding", len(references)))
	c.Assert(result.ReferenceCount, Equals, len(references),
		Commentf("Should accurately count %d references", len(references)))
}

func (s *KeeperHoardingSuite) TestDetectReferenceHoarding_TimeWindowFiltering(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup node with references spread over time
	nodeAddr := GetRandomBech32Addr()
	timeWindow := int64(1800) // 30 minute window
	
	// References within window (recent)
	recentRefs := []string{"30001", "30002", "30003", "30004", "30005", "30006"}
	recentHeight := int64(5000)
	
	// References outside window (old) - simulate much earlier blocks
	oldRefs := []string{"30101", "30102", "30103"}
	oldHeight := int64(1000) // Much earlier block height
	
	// Track recent references (within window)
	for i, ref := range recentRefs {
		blockHeight := recentHeight + int64(i)
		err := keeper.TrackReferenceUsage(ctx, ref, nodeAddr.String(), blockHeight)
		c.Assert(err, IsNil)
	}
	
	// Track old references (outside window)
	for i, ref := range oldRefs {
		blockHeight := oldHeight + int64(i)
		err := keeper.TrackReferenceUsage(ctx, ref, nodeAddr.String(), blockHeight)
		c.Assert(err, IsNil)
	}
	
	// THIS WILL FAIL initially - method doesn't exist
	result, err := keeper.DetectReferenceHoarding(ctx, nodeAddr.String(), timeWindow)
	c.Assert(err, IsNil, Commentf("Hoarding detection should not error"))
	
	// Should only count recent references within time window
	c.Assert(result.IsHoarding, Equals, true,
		Commentf("Should detect hoarding based on recent references only"))
	c.Assert(result.ReferenceCount, Equals, len(recentRefs),
		Commentf("Should count only recent references (%d), not old ones", len(recentRefs)))
}

func (s *KeeperHoardingSuite) TestDetectReferenceHoarding_ConfigurableThreshold(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	nodeAddr := GetRandomBech32Addr()
	timeWindow := int64(3600)
	
	// Register exactly 4 references
	references := []string{"40001", "40002", "40003", "40004"}
	baseHeight := int64(4000)
	
	for i, ref := range references {
		blockHeight := baseHeight + int64(i)
		err := keeper.TrackReferenceUsage(ctx, ref, nodeAddr.String(), blockHeight)
		c.Assert(err, IsNil)
	}
	
	// Test with different thresholds
	testCases := []struct {
		threshold       int
		expectedHoarding bool
		description      string
	}{
		{3, true, "4 references should trigger hoarding with threshold 3"},
		{5, false, "4 references should NOT trigger hoarding with threshold 5"},
		{4, false, "4 references should NOT trigger hoarding with exact threshold 4 (need >threshold)"},
	}
	
	for _, tc := range testCases {
		// THIS WILL FAIL initially - method doesn't exist
		result, err := keeper.DetectReferenceHoardingWithThreshold(ctx, nodeAddr.String(), timeWindow, tc.threshold)
		c.Assert(err, IsNil, Commentf("Hoarding detection should not error for case: %s", tc.description))
		
		c.Assert(result.IsHoarding, Equals, tc.expectedHoarding,
			Commentf("Case: %s", tc.description))
		c.Assert(result.HoardingThreshold, Equals, tc.threshold,
			Commentf("Should record the threshold used"))
	}
}

func (s *KeeperHoardingSuite) TestGetHoardingThreshold_ReturnsConfiguredValue(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// THIS WILL FAIL initially - method doesn't exist
	threshold := keeper.GetHoardingThreshold(ctx)
	
	// Should return a reasonable default threshold
	c.Assert(threshold > 0, Equals, true, Commentf("Threshold should be positive"))
	c.Assert(threshold <= 10, Equals, true, Commentf("Default threshold should be reasonable (<= 10)"))
}

func (s *KeeperHoardingSuite) TestRecordHoardingOffense_TracksForProgression(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	nodeAddr := GetRandomBech32Addr()
	
	// Create hoarding result
	hoardingResult := ReferenceHoardingResult{
		IsHoarding:         true,
		NodeOperator:       nodeAddr.String(),
		ReferenceCount:     8,
		TimeWindowSeconds:  3600,
		DetectedReferences: []string{"50001", "50002", "50003", "50004", "50005", "50006", "50007", "50008"},
		HoardingThreshold:  5,
	}
	
	// THIS WILL FAIL initially - method doesn't exist
	err := keeper.RecordHoardingOffense(ctx, hoardingResult)
	c.Assert(err, IsNil, Commentf("Recording hoarding offense should not error"))
	
	// Verify the offense was recorded (this enables progressive penalties)
	// Check that node's slash points increased
	slashPoints, err := keeper.GetNodeAccountSlashPoints(ctx, nodeAddr)
	c.Assert(err, IsNil)
	c.Assert(slashPoints > 0, Equals, true,
		Commentf("Node should have slash points added for hoarding offense"))
}

func (s *KeeperHoardingSuite) TestGetReferenceCountInWindow_AccurateCount(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	nodeAddr := GetRandomBech32Addr()
	
	// Setup references at different block heights
	testData := []struct {
		reference   string
		blockHeight int64
	}{
		{"60001", 6000}, // Within window
		{"60002", 6010}, // Within window
		{"60003", 6020}, // Within window
		{"60004", 5000}, // Outside window (too old)
		{"60005", 6030}, // Within window
	}
	
	for _, data := range testData {
		err := keeper.TrackReferenceUsage(ctx, data.reference, nodeAddr.String(), data.blockHeight)
		c.Assert(err, IsNil)
	}
	
	// Count references within a specific window
	currentHeight := int64(6035)
	windowBlocks := int64(50) // 50 blocks window
	
	// THIS WILL FAIL initially - method doesn't exist
	count, references, err := keeper.GetReferenceCountInWindow(ctx, nodeAddr.String(), currentHeight, windowBlocks)
	c.Assert(err, IsNil, Commentf("Getting reference count should not error"))
	
	// Should find 4 references within window (60001, 60002, 60003, 60005)
	expectedCount := 4
	c.Assert(count, Equals, expectedCount,
		Commentf("Should count %d references within block window", expectedCount))
	c.Assert(len(references), Equals, expectedCount,
		Commentf("Should return %d reference strings", expectedCount))
}