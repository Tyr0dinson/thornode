package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerBondCovCESuite struct{}

var _ = Suite(&HandlerBondCovCESuite{})

func covCEBondTx(amt uint64) common.Tx {
	return common.NewTx(GetRandomTxHash(), GetRandomTHORAddress(), GetRandomTHORAddress(),
		common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(amt))}, common.Gas{}, "bond")
}

func (s *HandlerBondCovCESuite) TestValidateCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	h := NewBondHandler(mgr)

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	ready := GetRandomValidatorNode(NodeReady)
	c.Assert(k.SetNodeAccount(ctx, ready), IsNil)
	msg := NewMsgBond(covCEBondTx(common.One), ready.NodeAddress, cosmos.NewUint(common.One), ready.BondAddress, nil, GetRandomBech32Addr(), -1)
	c.Check(h.validate(ctx, *msg), ErrorMatches, "(?s).*cannot add bond while node is ready status.*")

	standby := GetRandomValidatorNode(NodeStandby)
	standby.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetNodeAccount(ctx, standby), IsNil)
	msg = NewMsgBond(covCEBondTx(common.One), standby.NodeAddress, cosmos.NewUint(common.One), standby.BondAddress, nil, GetRandomBech32Addr(), -1)
	c.Assert(h.validate(ctx, *msg), IsNil)

	k.SetMimir(ctx, constants.PauseBond.String(), 1)
	c.Check(h.validate(ctx, *msg), ErrorMatches, "(?s).*bonding has been paused.*")
	k.SetMimir(ctx, constants.PauseBond.String(), 0)

	k.SetMimir(ctx, "MaximumBondInRune", int64(100*common.One))
	c.Check(h.validate(ctx, *msg), ErrorMatches, ".*too much bond, max validator bond.*")
	k.SetMimir(ctx, "MaximumBondInRune", int64(1000*common.One))
	c.Check(h.validate(ctx, *msg), IsNil)

	// mimir lookup failure is only logged
	c.Check(NewBondHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "GetMimir"))).validate(ctx, *msg), IsNil)

	for _, m := range []string{"GetNodeAccount", "GetBondProviders"} {
		hf := NewBondHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, m)))
		c.Check(errors.Is(hf.validate(ctx, *msg), errInternal), Equals, true, Commentf("method %s", m))
	}
}

func (s *HandlerBondCovCESuite) TestHandleNewNodeCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	FundModule(c, ctx, k, BondName, 1000*common.One)
	mgr := NewDummyMgrWithKeeper(k)

	nodeAddr := GetRandomBech32Addr()
	bondAddr := GetRandomTHORAddress()
	msg := NewMsgBond(covCEBondTx(10*common.One), nodeAddr, cosmos.NewUint(10*common.One), bondAddr, nil, GetRandomBech32Addr(), 2000)

	// failure to fund the new node account
	err := NewBondHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "SendFromModuleToAccount"))).handle(ctx, *msg)
	c.Check(err, ErrorMatches, "(?s).*fail to send one RUNE to node address.*")

	// bond events failing to emit are only logged
	mgr.eventMgr = &covCEFailEventMgr{EventManager: mgr.eventMgr, failOn: 1}
	_, err = NewBondHandler(mgr).Run(ctx, msg)
	c.Assert(err, IsNil)
	mgr.eventMgr = &covCEFailEventMgr{EventManager: mgr.eventMgr, failOn: 1}

	na, err := k.GetNodeAccount(ctx, nodeAddr)
	c.Assert(err, IsNil)
	c.Check(na.Status, Equals, NodeWhiteListed)
	c.Check(na.Bond.Uint64(), Equals, uint64(9*common.One))
	bp, err := k.GetBondProviders(ctx, nodeAddr)
	c.Assert(err, IsNil)
	c.Check(bp.NodeOperatorFee.Uint64(), Equals, uint64(2000))

	// the operator adds bond providers until the maximum is reached
	opAcc, err := bondAddr.AccAddress()
	c.Assert(err, IsNil)
	k.SetMimir(ctx, constants.MaxBondProviders.String(), 2)
	add := NewMsgBond(covCEBondTx(common.One), nodeAddr, cosmos.NewUint(common.One), bondAddr, GetRandomBech32Addr(), opAcc, -1)
	_, err = NewBondHandler(mgr).Run(ctx, add)
	c.Assert(err, IsNil)
	add = NewMsgBond(covCEBondTx(common.One), nodeAddr, cosmos.NewUint(common.One), bondAddr, GetRandomBech32Addr(), opAcc, -1)
	_, err = NewBondHandler(mgr).Run(ctx, add)
	c.Check(err, ErrorMatches, "additional bond providers are not allowed, maximum reached")
	bp, err = k.GetBondProviders(ctx, nodeAddr)
	c.Assert(err, IsNil)
	c.Check(bp.Providers, HasLen, 2)

	// keeper failures in handle
	more := NewMsgBond(covCEBondTx(common.One), nodeAddr, cosmos.NewUint(common.One), bondAddr, nil, opAcc, -1)
	for _, m := range []string{"GetNodeAccount", "GetBondProviders", "SetNodeAccount", "SetBondProviders"} {
		hf := NewBondHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, m)))
		c.Check(errors.Is(hf.handle(ctx, *more), errInternal), Equals, true, Commentf("method %s", m))
	}
	_, err = NewBondHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "SetNodeAccount"))).Run(ctx, more)
	c.Check(errors.Is(err, errInternal), Equals, true)
}
