package thorchain

import (
	. "gopkg.in/check.v1"
)

type QuerierSyncTestSuite struct{}

var _ = Suite(&QuerierSyncTestSuite{})

func (s *QuerierSyncTestSuite) TestErrNodeCatchingUp(c *C) {
	// Test that the error message is correct
	c.Assert(ErrNodeCatchingUp.Error(), Equals, "node is currently catching up to the tip of the blockchain, quotes may be stale")
}

func (s *QuerierSyncTestSuite) TestIsNodeCatchingUp(c *C) {
	ctx, _ := setupKeeperForTest(c)

	// Test the isNodeCatchingUp function
	// In test environment, this will likely fail due to no tendermint running
	// but we can test that it handles errors gracefully
	catchingUp, err := isNodeCatchingUp(ctx)

	// We expect an error in test environment since there's no tendermint running
	c.Assert(err, NotNil)
	c.Assert(catchingUp, Equals, false) // Should default to false on error
}
