package thorchain

import (
	"sync"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
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
	asgard := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	asgard.Membership = []string{na.PubKeySet.Secp256k1.String()}
	c.Assert(loaded.Keeper().SetVault(ctx, asgard), IsNil)

	fresh := NewManagers(loaded.K, loaded.cdc, loaded.storeService, loaded.coinKeeper, loaded.accountKeeper, loaded.upgradeKeeper, loaded.wasmKeeper)
	c.Assert(fresh.GetConstants(), IsNil) // as on a node that has not run BeginBlock
	qs := &queryServer{mgr: fresh, initOnce: &sync.Once{}}

	_, err := qs.Network(ctx, &types.QueryNetworkRequest{})
	c.Assert(err, IsNil)
	_, err = qs.Pools(ctx, &types.QueryPoolsRequest{})
	c.Assert(err, IsNil)
	nodes, err := qs.Nodes(ctx, &types.QueryNodesRequest{})
	c.Assert(err, IsNil)
	c.Assert(nodes.Nodes, HasLen, 1)
	c.Check(nodes.Nodes[0].NodeAddress, Equals, na.NodeAddress.String())
	c.Assert(fresh.GetConstants(), NotNil)
}

// A query must never replace a manager set consensus already built, even when its
// context (e.g. a historical height) resolves to a different version.
func (s *QueryUnloadedManagersSuite) TestQueryLoadNeverReplacesManagers(c *C) {
	ctx, mgr := setupManagerForTest(c)
	version := mgr.GetVersion()
	gasMgr := mgr.GasMgr()

	// a context whose lowest active version is older than the loaded one
	oldCtx, _ := ctx.CacheContext()
	na := GetRandomValidatorNode(NodeActive)
	na.Version = "3.0.0"
	c.Assert(mgr.Keeper().SetNodeAccount(oldCtx, na), IsNil)
	c.Assert(mgr.Keeper().GetLowestActiveVersion(oldCtx).LT(version), Equals, true)

	c.Assert(mgr.LoadManagerForQuery(oldCtx), IsNil)
	c.Check(mgr.GetVersion().String(), Equals, version.String())
	c.Check(mgr.GasMgr() == gasMgr, Equals, true)
}
