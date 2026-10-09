package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerReBondCovCESuite struct{}

var _ = Suite(&HandlerReBondCovCESuite{})

func covCERebondTx(from cosmos.AccAddress) common.Tx {
	return common.NewTx(GetRandomTxHash(), common.Address(from.String()), GetRandomRUNEAddress(), common.Coins{}, common.Gas{}, "rebond")
}

func (s *HandlerReBondCovCESuite) TestRunCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	fx := covCEUnbondSetup(c, ctx, k, NodeActive)
	mgr := NewDummyMgrWithKeeper(k)
	h := NewReBondHandler(mgr)

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	// partial move of 10 RUNE from provider to the zero-bond provider
	msg := NewMsgReBond(covCERebondTx(fx.provAcc), fx.na.NodeAddress, fx.zeroProv, cosmos.NewUint(10*common.One), fx.provAcc)
	_, err = h.Run(ctx, msg)
	c.Assert(err, IsNil)
	bp, err := k.GetBondProviders(ctx, fx.na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(bp.Get(fx.provAcc).Bond.Uint64(), Equals, uint64(30*common.One))
	c.Check(bp.Get(fx.zeroProv).Bond.Uint64(), Equals, uint64(10*common.One))

	// zero amount moves the whole remaining bond and removes the old provider
	msg = NewMsgReBond(covCERebondTx(fx.provAcc), fx.na.NodeAddress, fx.zeroProv, cosmos.ZeroUint(), fx.provAcc)
	_, err = h.Run(ctx, msg)
	c.Assert(err, IsNil)
	bp, err = k.GetBondProviders(ctx, fx.na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(bp.Has(fx.provAcc), Equals, false)
	c.Check(bp.Get(fx.zeroProv).Bond.Uint64(), Equals, uint64(40*common.One))
	na, err := k.GetNodeAccount(ctx, fx.na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(na.Bond.Uint64(), Equals, uint64(100*common.One))

	// the removed provider can no longer rebond
	_, err = h.Run(ctx, msg)
	c.Check(err, ErrorMatches, ".*is not bonded with.*")
}

func (s *HandlerReBondCovCESuite) TestValidateCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	fx := covCEUnbondSetup(c, ctx, k, NodeActive)
	h := NewReBondHandler(NewDummyMgrWithKeeper(k))
	good := NewMsgReBond(covCERebondTx(fx.provAcc), fx.na.NodeAddress, fx.zeroProv, cosmos.ZeroUint(), fx.provAcc)
	c.Assert(h.validate(ctx, *good), IsNil)

	c.Check(h.validate(ctx, MsgReBond{}), NotNil)

	withCoins := *good
	withCoins.TxIn.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(1))}
	c.Check(h.validate(ctx, withCoins), ErrorMatches, ".*rebond message cannot have a non-zero coin amount.*")

	unknown := *good
	unknown.NodeAddress = GetRandomBech32Addr()
	c.Check(h.validate(ctx, unknown), ErrorMatches, "(?s).*node account status is unknown.*")

	// operator cannot rebond
	op := *good
	op.Signer = fx.opAcc
	c.Check(h.validate(ctx, op), ErrorMatches, ".*node account is not allowed to rebond.*")

	notListed := *good
	notListed.NewBondProviderAddress = GetRandomBech32Addr()
	c.Check(h.validate(ctx, notListed), ErrorMatches, ".*new bond address is not whitelisted.*")

	for _, m := range []string{"GetNodeAccount", "GetBondProviders"} {
		hf := NewReBondHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, m)))
		c.Check(errors.Is(hf.validate(ctx, *good), errInternal), Equals, true, Commentf("method %s", m))
	}

	badBond := GetRandomValidatorNode(NodeActive)
	badBond.BondAddress = common.Address("bogus")
	c.Assert(k.SetNodeAccount(ctx, badBond), IsNil)
	bb := *good
	bb.NodeAddress = badBond.NodeAddress
	c.Check(h.validate(ctx, bb), ErrorMatches, "(?s).*fail to get node account address.*")
}

func (s *HandlerReBondCovCESuite) TestHandleErrorsCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	fx := covCEUnbondSetup(c, ctx, k, NodeActive)
	mgr := NewDummyMgrWithKeeper(k)
	good := NewMsgReBond(covCERebondTx(fx.provAcc), fx.na.NodeAddress, fx.zeroProv, cosmos.NewUint(common.One), fx.provAcc)

	k.SetMimir(ctx, constants.HaltRebond.String(), 1)
	c.Check(NewReBondHandler(mgr).handle(ctx, *good), ErrorMatches, "rebond has been disabled by mimir")
	k.SetMimir(ctx, constants.HaltRebond.String(), 0)

	for _, m := range []string{"GetNodeAccount", "GetBondProviders", "SetNodeAccount", "SetBondProviders"} {
		hf := NewReBondHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, m)))
		c.Check(errors.Is(hf.handle(ctx, *good), errInternal), Equals, true, Commentf("method %s", m))
	}

	oldMissing := *good
	oldMissing.Signer = GetRandomBech32Addr()
	c.Check(NewReBondHandler(mgr).handle(ctx, oldMissing), ErrorMatches, ".*old bond provider not found.*")

	zero := *good
	zero.Signer = fx.zeroProv
	zero.NewBondProviderAddress = fx.provAcc
	c.Check(NewReBondHandler(mgr).handle(ctx, zero), ErrorMatches, ".*bond is zero.*")

	newMissing := *good
	newMissing.NewBondProviderAddress = GetRandomBech32Addr()
	c.Check(NewReBondHandler(mgr).handle(ctx, newMissing), ErrorMatches, ".*new bond provider not found.*")

	// a failing event emission is only logged; the rebond is still applied
	mgr.eventMgr = &covCEFailEventMgr{EventManager: mgr.eventMgr, failOn: 1}
	c.Assert(NewReBondHandler(mgr).handle(ctx, *good), IsNil)
	bp, err := k.GetBondProviders(ctx, fx.na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(bp.Get(fx.zeroProv).Bond.Uint64(), Equals, uint64(common.One))
}
