package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type QuerierNodeBlameSuite struct{}

var _ = Suite(&QuerierNodeBlameSuite{})

// setupBlameRecord creates an active node and stores a blame record with the
// given counters.
func setupBlameRecord(c *C, ctx cosmos.Context, mgr *Mgrs, keygenTotal, keysignTotal, keygenSinceChurn, keysignSinceChurn int64) types.NodeAccount {
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	record := types.NewNodeBlameRecord(na.NodeAddress)
	record.KeygenBlameCountTotal = keygenTotal
	record.KeysignBlameCountTotal = keysignTotal
	record.KeygenBlameCountSinceChurn = keygenSinceChurn
	record.KeysignBlameCountSinceChurn = keysignSinceChurn
	c.Assert(mgr.Keeper().SetNodeBlameRecord(ctx, record), IsNil)
	return na
}

func (s *QuerierNodeBlameSuite) TestNodeBlamesDefaultSort(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	// Three active nodes with different keygen_since_churn counts.
	low := setupBlameRecord(c, ctx, mgr, 10, 1, 1, 1)
	high := setupBlameRecord(c, ctx, mgr, 5, 1, 9, 1)
	mid := setupBlameRecord(c, ctx, mgr, 20, 1, 5, 1)

	resp, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Blames), Equals, 3)

	// Default: keygen_since_churn descending.
	c.Check(resp.Blames[0].NodeAddress.Equals(high.NodeAddress), Equals, true)
	c.Check(resp.Blames[1].NodeAddress.Equals(mid.NodeAddress), Equals, true)
	c.Check(resp.Blames[2].NodeAddress.Equals(low.NodeAddress), Equals, true)
}

func (s *QuerierNodeBlameSuite) TestNodeBlamesSortByKeysignTotal(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	a := setupBlameRecord(c, ctx, mgr, 1, 100, 1, 1)
	b := setupBlameRecord(c, ctx, mgr, 1, 50, 1, 1)
	d := setupBlameRecord(c, ctx, mgr, 1, 200, 1, 1)

	resp, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{
		SortBy: "keysign_total",
	})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Blames), Equals, 3)
	c.Check(resp.Blames[0].NodeAddress.Equals(d.NodeAddress), Equals, true)
	c.Check(resp.Blames[1].NodeAddress.Equals(a.NodeAddress), Equals, true)
	c.Check(resp.Blames[2].NodeAddress.Equals(b.NodeAddress), Equals, true)
}

func (s *QuerierNodeBlameSuite) TestNodeBlamesAscendingOrder(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	low := setupBlameRecord(c, ctx, mgr, 1, 1, 1, 1)
	high := setupBlameRecord(c, ctx, mgr, 1, 1, 9, 1)
	mid := setupBlameRecord(c, ctx, mgr, 1, 1, 5, 1)

	resp, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{
		SortOrder: "asc",
	})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Blames), Equals, 3)
	c.Check(resp.Blames[0].NodeAddress.Equals(low.NodeAddress), Equals, true)
	c.Check(resp.Blames[1].NodeAddress.Equals(mid.NodeAddress), Equals, true)
	c.Check(resp.Blames[2].NodeAddress.Equals(high.NodeAddress), Equals, true)
}

func (s *QuerierNodeBlameSuite) TestNodeBlamesInvalidSortBy(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	resp, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{
		SortBy: "bogus",
	})
	c.Check(resp, IsNil)
	c.Assert(err, NotNil)
}

func (s *QuerierNodeBlameSuite) TestNodeBlamesInvalidSortOrder(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	resp, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{
		SortOrder: "sideways",
	})
	c.Check(resp, IsNil)
	c.Assert(err, NotNil)
}

func (s *QuerierNodeBlameSuite) TestNodeBlamesStableOrderOnTies(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	// All three nodes have the same keygen_since_churn count.
	setupBlameRecord(c, ctx, mgr, 1, 1, 5, 1)
	setupBlameRecord(c, ctx, mgr, 2, 2, 5, 2)
	setupBlameRecord(c, ctx, mgr, 3, 3, 5, 3)

	// Two consecutive calls should return the same ordering (deterministic on ties).
	first, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{})
	c.Assert(err, IsNil)
	second, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(first.Blames), Equals, 3)
	c.Assert(len(second.Blames), Equals, 3)
	for i := range first.Blames {
		c.Check(first.Blames[i].NodeAddress.Equals(second.Blames[i].NodeAddress), Equals, true)
	}
}
