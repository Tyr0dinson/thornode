package thorchain

import (
	"encoding/json"

	gateway "github.com/cosmos/gogogateway"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
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

	// Operator address must be populated from the node account's BondAddress.
	c.Check(resp.Blames[0].NodeOperatorAddress, Equals, high.BondAddress.String())
	c.Check(resp.Blames[1].NodeOperatorAddress, Equals, mid.BondAddress.String())
	c.Check(resp.Blames[2].NodeOperatorAddress, Equals, low.BondAddress.String())
}

func (s *QuerierNodeBlameSuite) TestNodeBlame(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	na := setupBlameRecord(c, ctx, mgr, 7, 3, 2, 1)

	resp, err := qs.queryNodeBlame(ctx, &types.QueryNodeBlameRequest{
		Address: na.NodeAddress.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)
	c.Check(resp.NodeAddress.Equals(na.NodeAddress), Equals, true)
	c.Check(resp.NodeOperatorAddress, Equals, na.BondAddress.String())
	c.Check(resp.KeygenBlameCountTotal, Equals, int64(7))
	c.Check(resp.KeysignBlameCountTotal, Equals, int64(3))
	c.Check(resp.KeygenBlameCountSinceChurn, Equals, int64(2))
	c.Check(resp.KeysignBlameCountSinceChurn, Equals, int64(1))
}

func (s *QuerierNodeBlameSuite) TestNodeBlameOpenAPIConformance(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}
	// Same options as the REST gateway marshaler in CustomGRPCGatewayRouter.
	marshaler := &gateway.JSONPb{EmitDefaults: true, OrigName: true}

	na := setupBlameRecord(c, ctx, mgr, 7, 3, 2, 1)
	other := setupBlameRecord(c, ctx, mgr, 1, 1, 9, 4)

	// Single node: counters must be JSON numbers, not proto-style strings.
	blameResp, err := qs.queryNodeBlame(ctx, &types.QueryNodeBlameRequest{
		Address: na.NodeAddress.String(),
	})
	c.Assert(err, IsNil)
	body, err := marshaler.Marshal(blameResp)
	c.Assert(err, IsNil)
	var blame openapi.NodeBlameResponse
	c.Assert(json.Unmarshal(body, &blame), IsNil)
	c.Check(blame.NodeAddress, Equals, na.NodeAddress.String())
	c.Check(blame.NodeOperatorAddress, Equals, na.BondAddress.String())
	c.Check(blame.KeygenBlameCountTotal, Equals, int64(7))
	c.Check(blame.KeysignBlameCountTotal, Equals, int64(3))
	c.Check(blame.KeygenBlameCountSinceChurn, Equals, int64(2))
	c.Check(blame.KeysignBlameCountSinceChurn, Equals, int64(1))

	// List: must be a bare array, not {"blames": [...]}.
	blamesResp, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{})
	c.Assert(err, IsNil)
	body, err = marshaler.Marshal(blamesResp)
	c.Assert(err, IsNil)
	var blames []openapi.NodeBlameResponse
	c.Assert(json.Unmarshal(body, &blames), IsNil)
	c.Assert(len(blames), Equals, 2)
	c.Check(blames[0].NodeAddress, Equals, other.NodeAddress.String())
	c.Check(blames[0].NodeOperatorAddress, Equals, other.BondAddress.String())
	c.Check(blames[0].KeygenBlameCountSinceChurn, Equals, int64(9))
	c.Check(blames[0].KeysignBlameCountSinceChurn, Equals, int64(4))
	c.Check(blames[1].NodeAddress, Equals, na.NodeAddress.String())
	c.Check(blames[1].KeygenBlameCountTotal, Equals, int64(7))
}

func (s *QuerierNodeBlameSuite) TestNodeBlamesEmptyIsJSONArray(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	resp, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{})
	c.Assert(err, IsNil)
	body, err := resp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	c.Check(string(body), Equals, "[]")
}

func (s *QuerierNodeBlameSuite) TestNodeBlameUnknownAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	// Address with no NodeAccount and no blame record: zero counters,
	// empty operator address, no error.
	stranger := GetRandomValidatorNode(NodeActive).NodeAddress
	resp, err := qs.queryNodeBlame(ctx, &types.QueryNodeBlameRequest{
		Address: stranger.String(),
	})
	c.Assert(err, IsNil)
	c.Assert(resp, NotNil)
	c.Check(resp.NodeOperatorAddress, Equals, "")
	c.Check(resp.KeygenBlameCountTotal, Equals, int64(0))
	c.Check(resp.KeysignBlameCountTotal, Equals, int64(0))
}

func (s *QuerierNodeBlameSuite) TestNodeBlameMissingAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	resp, err := qs.queryNodeBlame(ctx, &types.QueryNodeBlameRequest{})
	c.Check(resp, IsNil)
	c.Assert(err, NotNil)
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

func (s *QuerierNodeBlameSuite) TestNodeBlamesTiesOrderedByNodeAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	for i := 0; i < 5; i++ {
		setupBlameRecord(c, ctx, mgr, int64(i), int64(i), 5, int64(i))
	}

	for _, order := range []string{"desc", "asc"} {
		resp, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{SortOrder: order})
		c.Assert(err, IsNil)
		c.Assert(len(resp.Blames), Equals, 5)
		for i := 1; i < len(resp.Blames); i++ {
			c.Check(resp.Blames[i-1].NodeAddress.String() < resp.Blames[i].NodeAddress.String(), Equals, true,
				Commentf("sort_order=%s: ties at index %d not ordered by node address", order, i))
		}
	}
}

// Active nodes that have never been blamed have no stored record but must
// still be listed, with zero counters; records of non-active nodes must not.
func (s *QuerierNodeBlameSuite) TestNodeBlamesIncludesUnblamedActiveNodes(c *C) {
	ctx, mgr := setupManagerForTest(c)
	qs := &queryServer{mgr: mgr}

	blamed := setupBlameRecord(c, ctx, mgr, 3, 1, 2, 1)

	clean := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, clean), IsNil)

	standby := GetRandomValidatorNode(NodeStandby)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, standby), IsNil)
	record := types.NewNodeBlameRecord(standby.NodeAddress)
	record.KeygenBlameCountTotal = 9
	record.KeygenBlameCountSinceChurn = 9
	c.Assert(mgr.Keeper().SetNodeBlameRecord(ctx, record), IsNil)

	resp, err := qs.queryNodeBlames(ctx, &types.QueryNodeBlamesRequest{})
	c.Assert(err, IsNil)
	c.Assert(len(resp.Blames), Equals, 2)

	// Default sort is keygen_since_churn descending: blamed (2) before clean (0).
	c.Check(resp.Blames[0].NodeAddress.Equals(blamed.NodeAddress), Equals, true)
	c.Check(resp.Blames[0].KeygenBlameCountTotal, Equals, int64(3))

	got := resp.Blames[1]
	c.Check(got.NodeAddress.Equals(clean.NodeAddress), Equals, true)
	c.Check(got.NodeOperatorAddress, Equals, clean.BondAddress.String())
	c.Check(got.KeygenBlameCountTotal, Equals, int64(0))
	c.Check(got.KeysignBlameCountTotal, Equals, int64(0))
	c.Check(got.KeygenBlameCountSinceChurn, Equals, int64(0))
	c.Check(got.KeysignBlameCountSinceChurn, Equals, int64(0))

	// The unblamed node's JSON entry still carries its address and counters.
	body, err := resp.MarshalJSONPB(nil)
	c.Assert(err, IsNil)
	var blames []openapi.NodeBlameResponse
	c.Assert(json.Unmarshal(body, &blames), IsNil)
	c.Assert(len(blames), Equals, 2)
	c.Check(blames[1].NodeAddress, Equals, clean.NodeAddress.String())
}
