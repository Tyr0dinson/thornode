package keeperv1

import (
	"testing"

	. "gopkg.in/check.v1"
)

func TestKeeperCollusion(t *testing.T) { TestingT(t) }

type KeeperCollusionSuite struct{}

var _ = Suite(&KeeperCollusionSuite{})

// RED Phase Test - This should FAIL initially to drive Task 5A implementation
func (s *KeeperCollusionSuite) TestDetectDuplicateReference_ReturnsTrueForDuplicates(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup: Node1 already used reference "12345"
	nodeAddr1 := GetRandomBech32Addr()
	nodeAddr2 := GetRandomBech32Addr()
	reference := "12345"
	blockHeight := int64(100)
	
	// Node1 uses the reference first
	err := keeper.TrackReferenceUsage(ctx, reference, nodeAddr1.String(), blockHeight)
	c.Assert(err, IsNil)
	
	// Test: Node2 attempts same reference - should detect duplicate
	isDuplicate := keeper.DetectDuplicateReference(ctx, reference, nodeAddr2.String())
	
	// This should return true - different node using same reference = duplicate
	c.Assert(isDuplicate, Equals, true, Commentf("Reference %s used by node1, should be duplicate for node2", reference))
}

func (s *KeeperCollusionSuite) TestDetectDuplicateReference_ReturnsFalseForSameNode(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	// Setup: Node1 already used reference "12345"
	nodeAddr1 := GetRandomBech32Addr()
	reference := "12345"
	blockHeight := int64(100)
	
	// Node1 uses the reference
	err := keeper.TrackReferenceUsage(ctx, reference, nodeAddr1.String(), blockHeight)
	c.Assert(err, IsNil)
	
	// Test: Same node1 checking same reference - should NOT be duplicate
	isDuplicate := keeper.DetectDuplicateReference(ctx, reference, nodeAddr1.String())
	
	// Same node using same reference is not a duplicate
	c.Assert(isDuplicate, Equals, false, Commentf("Same node using same reference should not be duplicate"))
}

func (s *KeeperCollusionSuite) TestDetectDuplicateReference_ReturnsFalseForUniqueReference(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	nodeAddr1 := GetRandomBech32Addr()
	reference := "67890"
	
	// Test: Node1 uses completely new reference - should NOT be duplicate
	isDuplicate := keeper.DetectDuplicateReference(ctx, reference, nodeAddr1.String())
	
	// Unique reference should not be duplicate
	c.Assert(isDuplicate, Equals, false, Commentf("Unique reference should not be duplicate"))
}

func (s *KeeperCollusionSuite) TestTrackReferenceUsage_StoresCorrectData(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	nodeAddr := GetRandomBech32Addr()
	reference := "12345"
	blockHeight := int64(100)
	
	// Track reference usage
	err := keeper.TrackReferenceUsage(ctx, reference, nodeAddr.String(), blockHeight)
	c.Assert(err, IsNil)
	
	// Verify data was stored correctly
	users := keeper.GetReferenceUsers(ctx, reference)
	c.Assert(len(users), Equals, 1, Commentf("Should have exactly 1 user for reference"))
	c.Assert(users[0], Equals, nodeAddr.String(), Commentf("Stored user should match"))
	
	// Verify block height was stored correctly
	storedHeight, found := keeper.GetReferenceBlockHeight(ctx, reference, nodeAddr.String())
	c.Assert(found, Equals, true, Commentf("Should find stored block height"))
	c.Assert(storedHeight, Equals, blockHeight, Commentf("Stored block height should match"))
}

func (s *KeeperCollusionSuite) TestGetReferenceUsers_ReturnsMultipleUsers(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	nodeAddr1 := GetRandomBech32Addr()
	nodeAddr2 := GetRandomBech32Addr()
	reference := "12345"
	blockHeight := int64(100)
	
	// Both nodes track same reference (simulating duplicate detection scenario)
	err := keeper.TrackReferenceUsage(ctx, reference, nodeAddr1.String(), blockHeight)
	c.Assert(err, IsNil)
	err = keeper.TrackReferenceUsage(ctx, reference, nodeAddr2.String(), blockHeight+1)
	c.Assert(err, IsNil)
	
	// Get all users of the reference
	users := keeper.GetReferenceUsers(ctx, reference)
	c.Assert(len(users), Equals, 2, Commentf("Should have exactly 2 users for reference"))
	
	// Users should be in the list (order not guaranteed)
	userSet := make(map[string]bool)
	for _, user := range users {
		userSet[user] = true
	}
	c.Assert(userSet[nodeAddr1.String()], Equals, true, Commentf("Node1 should be in users list"))
	c.Assert(userSet[nodeAddr2.String()], Equals, true, Commentf("Node2 should be in users list"))
}

func (s *KeeperCollusionSuite) TestGetNodeReferenceCount_TracksNodeActivity(c *C) {
	ctx, keeper := setupKeeperForTest(c)
	
	nodeAddr := GetRandomBech32Addr()
	
	// Initially should have zero references
	count := keeper.GetNodeReferenceCount(ctx, nodeAddr.String())
	c.Assert(count, Equals, int64(0), Commentf("New node should have zero reference count"))
	
	// Track multiple references for same node
	err := keeper.TrackReferenceUsage(ctx, "12345", nodeAddr.String(), 100)
	c.Assert(err, IsNil)
	err = keeper.TrackReferenceUsage(ctx, "67890", nodeAddr.String(), 101)
	c.Assert(err, IsNil)
	
	// Count should increment
	count = keeper.GetNodeReferenceCount(ctx, nodeAddr.String())
	c.Assert(count, Equals, int64(2), Commentf("Node should have 2 tracked references"))
}

// Use existing setupKeeperForTest function from keeper_test.go