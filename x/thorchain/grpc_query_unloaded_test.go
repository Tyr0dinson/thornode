package thorchain

import (
	"sync"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type QueryUnloadedManagersSuite struct{}

var _ = Suite(&QueryUnloadedManagersSuite{})

// A node that has not executed a block on this binary yet (started at an upgrade
// height, waiting for the network) has managers that BeginBlock never built.
// Queries must still be served, not panic on nil managers.
func (s *QueryUnloadedManagersSuite) TestQueriesBeforeFirstBlock(c *C) {
	ctx, loaded := setupManagerForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(loaded.Keeper().SetNodeAccount(ctx, na), IsNil)

	fresh := NewManagers(loaded.K, loaded.cdc, loaded.storeService, loaded.coinKeeper, loaded.accountKeeper, loaded.upgradeKeeper, loaded.wasmKeeper)
	c.Assert(fresh.GetConstants(), IsNil) // as on a node that has not run BeginBlock
	qs := &queryServer{mgr: fresh, initOnce: &sync.Once{}}

	_, err := qs.Network(ctx, &types.QueryNetworkRequest{})
	c.Assert(err, IsNil)
	_, err = qs.Pools(ctx, &types.QueryPoolsRequest{})
	c.Assert(err, IsNil)
	// This fixture has no vaults, so the nodes query errors cleanly; it must not panic.
	_, _ = qs.Nodes(ctx, &types.QueryNodesRequest{})
	c.Assert(fresh.GetConstants(), NotNil)
}
