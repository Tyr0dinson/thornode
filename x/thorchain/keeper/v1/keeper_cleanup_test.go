package keeperv1

import (
	"fmt"
	"time"

	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type BatchCleanupSuite struct{}

var _ = Suite(&BatchCleanupSuite{})

func (s *BatchCleanupSuite) TestBatchCleanup_PerformanceTarget(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup: Create 1000 expired memos
	expiredHeight := ctx.BlockHeight() - 100
	references := make([]string, 1000)

	for i := 0; i < 1000; i++ {
		ref := fmt.Sprintf("%05d", 10000+i)
		registration := createTestRegistrationWithExpiry(ref, "SWAP:BTC.BTC:addr", expiredHeight)
		err := keeper.SetRegisteredMemo(ctx, registration)
		c.Assert(err, IsNil)
		references[i] = ref
	}

	// Test: Batch cleanup performance
	start := time.Now()
	cleanupResult, err := keeper.CleanupExpiredMemosWithMetrics(ctx, 1000) // THIS WILL FAIL initially
	duration := time.Since(start)

	c.Assert(err, IsNil)
	c.Assert(duration < 100*time.Millisecond, Equals, true, Commentf("Cleanup took %v, expected <100ms", duration))
	c.Assert(cleanupResult.RemovedCount, Equals, int64(1000))

	// Verify: All expired memos removed
	for _, ref := range references {
		_, err := keeper.GetRegisteredMemo(ctx, ref)
		c.Assert(err, NotNil, Commentf("Memo %s should be removed", ref))
	}
}

func (s *BatchCleanupSuite) TestBatchCleanup_HandlesCleanupInterval(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Test: Check if cleanup should run based on interval
	shouldRun, err := keeper.ShouldRunCleanup(ctx) // THIS WILL FAIL initially
	c.Assert(err, IsNil)

	// Initially should run (no previous cleanup)
	c.Assert(shouldRun, Equals, true)

	// Run cleanup
	cleanupResult, err := keeper.CleanupExpiredMemosWithMetrics(ctx, 100)
	c.Assert(err, IsNil)
	_ = cleanupResult // Used for cleanup result

	// Should not run immediately after
	shouldRun, err = keeper.ShouldRunCleanup(ctx)
	c.Assert(err, IsNil)
	c.Assert(shouldRun, Equals, false)

	// Advance blocks beyond cleanup interval
	newCtx := ctx.WithBlockHeight(ctx.BlockHeight() + 101) // Beyond 100 block interval
	shouldRun, err = keeper.ShouldRunCleanup(newCtx)
	c.Assert(err, IsNil)
	c.Assert(shouldRun, Equals, true)
}

func (s *BatchCleanupSuite) TestBatchCleanup_HandlesEmptyCleanup(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Test: Cleanup with no expired memos
	cleanupResult, err := keeper.CleanupExpiredMemosWithMetrics(ctx, 100)
	c.Assert(err, IsNil)
	c.Assert(cleanupResult.RemovedCount, Equals, int64(0))
	c.Assert(cleanupResult.Duration < 10*time.Millisecond, Equals, true)
}

func (s *BatchCleanupSuite) TestBatchCleanup_PreservesActiveMemos(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup: Mix of expired and active memos
	currentHeight := ctx.BlockHeight()
	expiredHeight := currentHeight - 100
	futureHeight := currentHeight + 100

	// Create expired memos
	expiredRefs := []string{"10001", "10002", "10003"}
	for _, ref := range expiredRefs {
		registration := createTestRegistrationWithExpiry(ref, "SWAP:BTC.BTC:addr1", expiredHeight)
		err := keeper.SetRegisteredMemo(ctx, registration)
		c.Assert(err, IsNil)
	}

	// Create active memos
	activeRefs := []string{"20001", "20002", "20003"}
	for _, ref := range activeRefs {
		registration := createTestRegistrationWithExpiry(ref, "SWAP:ETH.ETH:addr2", futureHeight)
		err := keeper.SetRegisteredMemo(ctx, registration)
		c.Assert(err, IsNil)
	}

	// Test: Cleanup should remove only expired memos
	cleanupResult, err := keeper.CleanupExpiredMemosWithMetrics(ctx, 100)
	c.Assert(err, IsNil)
	c.Assert(cleanupResult.RemovedCount, Equals, int64(3)) // Only expired ones

	// Verify: Expired memos removed
	for _, ref := range expiredRefs {
		_, err := keeper.GetRegisteredMemo(ctx, ref)
		c.Assert(err, NotNil, Commentf("Expired memo %s should be removed", ref))
	}

	// Verify: Active memos preserved
	for _, ref := range activeRefs {
		_, err := keeper.GetRegisteredMemo(ctx, ref)
		c.Assert(err, IsNil, Commentf("Active memo %s should be preserved", ref))
	}
}

func (s *BatchCleanupSuite) TestBatchCleanup_EmitsMetrics(c *C) {
	ctx, keeper := setupKeeperForTest(c)

	// Setup: Create some expired memos
	expiredHeight := ctx.BlockHeight() - 50
	for i := 0; i < 5; i++ {
		ref := fmt.Sprintf("%05d", 30000+i)
		registration := createTestRegistrationWithExpiry(ref, "SWAP:BTC.BTC:addr", expiredHeight)
		err := keeper.SetRegisteredMemo(ctx, registration)
		c.Assert(err, IsNil)
	}

	// Test: Cleanup should emit metrics
	cleanupResult, err := keeper.CleanupExpiredMemosWithMetrics(ctx, 100)
	c.Assert(err, IsNil)
	c.Assert(cleanupResult.RemovedCount, Equals, int64(5))
	c.Assert(cleanupResult.Duration > 0, Equals, true)
	c.Assert(cleanupResult.ErrorCount, Equals, int64(0))
}

// Helper function to create test registration with specific expiry
func createTestRegistrationWithExpiry(reference string, memo string, expiresAt int64) thorchaintypes.RegisteredMemo {
	return thorchaintypes.RegisteredMemo{
		Reference:    reference,
		Memo:         memo,
		ChainId:      "BTC",
		RegisteredAt: expiresAt - 14400, // Registered 4 hours before expiry
		ExpiresAt:    expiresAt,
		Status:       "active",
	}
}
