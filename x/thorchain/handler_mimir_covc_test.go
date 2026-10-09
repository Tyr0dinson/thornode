package thorchain

import (
	"errors"
	"strings"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type HandlerMimirCovCESuite struct{}

var _ = Suite(&HandlerMimirCovCESuite{})

func covCEMimirNodes(c *C, ctx cosmos.Context, mgr *Mgrs, n int) []NodeAccount {
	nodes := make([]NodeAccount, n)
	for i := range nodes {
		nodes[i] = GetRandomValidatorNode(NodeActive)
		nodes[i].Bond = cosmos.NewUint(100 * common.One)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, nodes[i]), IsNil)
	}
	FundModule(c, ctx, mgr.Keeper(), BondName, uint64(n)*100*common.One)
	return nodes
}

func (s *HandlerMimirCovCESuite) TestValidateCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nodes := covCEMimirNodes(c, ctx, mgr, 1)
	h := NewMimirHandler(mgr)

	// invalid key characters
	err := h.validate(ctx, *NewMsgMimir("bad_key!", 1, nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, "invalid mimir key.*")
	// too long
	err = h.validate(ctx, *NewMsgMimir(strings.Repeat("A", constants.MaxMimirLength+1), 1, nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, "invalid mimir key.*")

	// min slip mimir is capped at SlipMinBpsMax
	maxBps := mgr.Keeper().GetConfigInt64(ctx, constants.SlipMinBpsMax)
	c.Check(h.validate(ctx, *NewMsgMimir("L1SlipMinBps", maxBps, nodes[0].NodeAddress)), IsNil)
	err = h.validate(ctx, *NewMsgMimir("L1SlipMinBps", maxBps+1, nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, "min slip mimir exceeds SlipMinBpsMax.*")

	// ante handler
	newCtx, err := MimirAnteHandler(ctx, mgr.GetVersion(), mgr.Keeper(), *NewMsgMimir("Foo", 1, nodes[0].NodeAddress))
	c.Assert(err, IsNil)
	c.Check(newCtx.Priority(), Equals, ActiveNodePriority)
	_, err = MimirAnteHandler(ctx, mgr.GetVersion(), mgr.Keeper(), *NewMsgMimir("Foo", 1, GetRandomBech32Addr()))
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)
}

func (s *HandlerMimirCovCESuite) TestHandleCostAndConsensusCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nodes := covCEMimirNodes(c, ctx, mgr, 4)
	k := mgr.Keeper()
	h := NewMimirHandler(mgr)

	// a node with less bond than the fee pays its whole bond
	poor := GetRandomValidatorNode(NodeStandby)
	poor.Bond = cosmos.NewUint(1)
	c.Assert(k.SetNodeAccount(ctx, poor), IsNil)
	c.Assert(h.handle(ctx, *NewMsgMimir("CovCEKey", 3, poor.NodeAddress)), IsNil)
	got, err := k.GetNodeAccount(ctx, poor.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Bond.IsZero(), Equals, true)
	// zero bond node pays nothing and still records the vote
	c.Assert(h.handle(ctx, *NewMsgMimir("CovCEKey", 3, poor.NodeAddress)), IsNil)

	// three of four active nodes reach supermajority for 5
	for _, na := range nodes[:3] {
		_, err = h.Run(ctx, NewMsgMimir("CovCEKey", 5, na.NodeAddress))
		c.Assert(err, IsNil)
	}
	val, err := k.GetMimir(ctx, "CovCEKey")
	c.Assert(err, IsNil)
	c.Check(val, Equals, int64(5))
	bonded, err := k.GetNodeAccount(ctx, nodes[0].NodeAddress)
	c.Assert(err, IsNil)
	c.Check(bonded.Bond.LT(cosmos.NewUint(100*common.One)), Equals, true)

	// admin override to 9; a lone vote for 7 does not change it because the
	// effective value (5) differs from the voted value
	k.SetMimir(ctx, "CovCEKey", 9)
	_, err = h.Run(ctx, NewMsgMimir("CovCEKey", 7, nodes[3].NodeAddress))
	c.Assert(err, IsNil)
	val, err = k.GetMimir(ctx, "CovCEKey")
	c.Assert(err, IsNil)
	c.Check(val, Equals, int64(9))
}

func (s *HandlerMimirCovCESuite) TestHandleErrorsCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	nodes := covCEMimirNodes(c, ctx, mgr, 1)
	k := mgr.Keeper()
	msg := NewMsgMimir("CovCEErrKey", 4, nodes[0].NodeAddress)

	for _, tc := range []struct {
		method string
		msg    string
	}{
		{"GetNodeAccount", ".*is not authorized.*"},
		{"SetNodeAccount", "fail to save node account: kaboom"},
		{"SendFromModuleToModule", "kaboom"},
		{"SetNodeMimir", "kaboom"},
		{"GetNodeMimirs", "kaboom"},
		{"ListActiveValidators", "kaboom"},
	} {
		mgr.K = newCovCEFailKeeper(k, tc.method)
		err := NewMimirHandler(mgr).handle(ctx, *msg)
		c.Check(err, ErrorMatches, tc.msg, Commentf("method %s", tc.method))
	}
	mgr.K = k

	// event emission failures: set_node_mimir, bond, set_mimir
	origEvt := mgr.eventMgr
	for i, expected := range []string{"kaboom", "kaboom", "fail to emit set_mimir event: kaboom"} {
		mgr.eventMgr = &covCEFailEventMgr{EventManager: origEvt, failOn: i + 1}
		key := "CovCEEvtKey" + string(rune('A'+i))
		_, err := NewMimirHandler(mgr).Run(ctx, NewMsgMimir(key, 4, nodes[0].NodeAddress))
		c.Check(err, ErrorMatches, expected, Commentf("emit #%d", i+1))
	}
	mgr.eventMgr = origEvt
}
