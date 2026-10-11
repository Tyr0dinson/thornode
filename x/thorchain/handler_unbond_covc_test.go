package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type HandlerUnBondCovCESuite struct{}

var _ = Suite(&HandlerUnBondCovCESuite{})

type covCEUnbondFixture struct {
	na       NodeAccount
	opAcc    cosmos.AccAddress
	provAcc  cosmos.AccAddress
	zeroProv cosmos.AccAddress
}

// covCEUnbondSetup stores a node with an operator (60 RUNE), a bond provider
// (40 RUNE) and a whitelisted provider with zero bond.
func covCEUnbondSetup(c *C, ctx cosmos.Context, k keeper.Keeper, status NodeStatus) covCEUnbondFixture {
	na := GetRandomValidatorNode(status)
	na.Bond = cosmos.NewUint(100 * common.One)
	na.StatusSince = 0
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	opAcc, err := na.BondAddress.AccAddress()
	c.Assert(err, IsNil)
	provAcc := GetRandomBech32Addr()
	zeroProv := GetRandomBech32Addr()

	bp := NewBondProviders(na.NodeAddress)
	op := NewBondProvider(opAcc)
	op.Bond = cosmos.NewUint(60 * common.One)
	prov := NewBondProvider(provAcc)
	prov.Bond = cosmos.NewUint(40 * common.One)
	bp.Providers = []BondProvider{op, prov, NewBondProvider(zeroProv)}
	c.Assert(k.SetBondProviders(ctx, bp), IsNil)
	FundModule(c, ctx, k, BondName, 100*common.One)

	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), nil, []ChainContract{})
	c.Assert(k.SetVault(ctx, vault), IsNil)
	return covCEUnbondFixture{na: na, opAcc: opAcc, provAcc: provAcc, zeroProv: zeroProv}
}

func covCEUnbondTx(from common.Address) common.Tx {
	return common.NewTx(GetRandomTxHash(), from, GetRandomRUNEAddress(), common.Coins{}, common.Gas{}, "unbond")
}

func (s *HandlerUnBondCovCESuite) TestOperatorRemovesProvidersCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1_000_000)
	fx := covCEUnbondSetup(c, ctx, k, NodeActive)
	h := NewUnBondHandler(NewDummyMgrWithKeeper(k))

	// the operator may remove a zero balance provider even while active
	msg := NewMsgUnBond(covCEUnbondTx(fx.na.BondAddress), fx.na.NodeAddress, cosmos.ZeroUint(), fx.na.BondAddress, fx.zeroProv, fx.opAcc)
	_, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	bp, err := k.GetBondProviders(ctx, fx.na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(bp.Has(fx.zeroProv), Equals, false)
	c.Check(bp.Has(fx.provAcc), Equals, true)

	// but cannot pull funds out of an active node
	msg = NewMsgUnBond(covCEUnbondTx(fx.na.BondAddress), fx.na.NodeAddress, cosmos.NewUint(common.One), fx.na.BondAddress, fx.provAcc, fx.opAcc)
	_, err = h.Run(ctx, msg)
	c.Check(err, ErrorMatches, ".*cannot unbond while node is in active or ready status.*")
}

func (s *HandlerUnBondCovCESuite) TestOperatorUnbondsProviderCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1_000_000)
	fx := covCEUnbondSetup(c, ctx, k, NodeStandby)
	h := NewUnBondHandler(NewDummyMgrWithKeeper(k))

	// operator unbonds the provider's entire bond -> provider removed
	msg := NewMsgUnBond(covCEUnbondTx(fx.na.BondAddress), fx.na.NodeAddress, cosmos.NewUint(40*common.One), fx.na.BondAddress, fx.provAcc, fx.opAcc)
	_, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	na, err := k.GetNodeAccount(ctx, fx.na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(na.Bond.Uint64(), Equals, uint64(60*common.One))
	bp, err := k.GetBondProviders(ctx, fx.na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(bp.Has(fx.provAcc), Equals, false)

	// handle credits RUNE attached to the unbond tx back to the bond
	tx := covCEUnbondTx(fx.na.BondAddress)
	tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One))}
	msg = NewMsgUnBond(tx, fx.na.NodeAddress, cosmos.NewUint(common.One), fx.na.BondAddress, nil, fx.opAcc)
	c.Assert(h.handle(ctx, *msg), IsNil)
	na, err = k.GetNodeAccount(ctx, fx.na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(na.Bond.Uint64(), Equals, uint64(60*common.One))

	// coins on the unbond message are rejected by validate
	_, err = h.Run(ctx, msg)
	c.Check(err, ErrorMatches, ".*unbond message cannot have a non-zero coin amount.*")
}

func (s *HandlerUnBondCovCESuite) TestUnbondGuardsCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1_000_000)
	fx := covCEUnbondSetup(c, ctx, k, NodeStandby)
	h := NewUnBondHandler(NewDummyMgrWithKeeper(k))
	msg := NewMsgUnBond(covCEUnbondTx(fx.na.BondAddress), fx.na.NodeAddress, cosmos.NewUint(common.One), fx.na.BondAddress, nil, fx.opAcc)

	// paused
	k.SetMimir(ctx, constants.PauseUnbond.String(), 1)
	_, err := h.Run(ctx, msg)
	c.Check(err, ErrorMatches, "unbonding has been paused")
	k.SetMimir(ctx, constants.PauseUnbond.String(), 0)

	// bond lockup period
	k.SetMimir(ctx, constants.BondLockupPeriod.String(), 2_000_000)
	_, err = h.Run(ctx, msg)
	c.Check(err, ErrorMatches, "node can not unbond before 2000000")
	k.SetMimir(ctx, constants.BondLockupPeriod.String(), 0)

	// still a member of a retiring vault
	retiring := NewVault(1, RetiringVault, AsgardVault, GetRandomPubKey(), nil, []ChainContract{})
	retiring.Membership = []string{fx.na.PubKeySet.Secp256k1.String()}
	c.Assert(k.SetVault(ctx, retiring), IsNil)
	_, err = h.Run(ctx, msg)
	c.Check(err, ErrorMatches, "fail to unbond, still part of the retiring vault")
	retiring.Status = InactiveVault
	c.Assert(k.SetVault(ctx, retiring), IsNil)

	// keeper failures
	for _, tc := range []struct {
		method string
		run    bool
	}{
		{"GetBondProviders", true},
		{"GetNodeAccount", false},
		{"GetAsgardVaultsByStatus", false},
	} {
		hf := NewUnBondHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, tc.method)))
		if tc.run {
			_, err = hf.Run(ctx, msg)
		} else {
			err = hf.handle(ctx, *msg)
		}
		c.Check(errors.Is(err, errInternal), Equals, true, Commentf("method %s", tc.method))
	}

	// handle for the operator removing a provider fails when providers can't be read
	pmsg := NewMsgUnBond(covCEUnbondTx(fx.na.BondAddress), fx.na.NodeAddress, cosmos.ZeroUint(), fx.na.BondAddress, fx.zeroProv, fx.opAcc)
	err = NewUnBondHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "GetBondProviders"))).handle(ctx, *pmsg)
	c.Check(errors.Is(err, errInternal), Equals, true)

	// saving the providers after removal fails: refundBond's own
	// SetBondProviders succeeds, the handler's second one fails. Run in a
	// throwaway cache context so the refund does not leak into later cases.
	fk := newCovCEFailKeeper(k, "SetBondProviders")
	fk.skip["SetBondProviders"] = 1
	cctx, _ := ctx.CacheContext()
	err = NewUnBondHandler(NewDummyMgrWithKeeper(fk)).handle(cctx, *pmsg)
	c.Check(err, ErrorMatches, "(?s).*fail to save bond providers.*")
	c.Check(fk.calls["SetBondProviders"], Equals, 2)

	// saving the node after crediting attached RUNE fails: refundBond's own
	// SetNodeAccount succeeds, the handler's second one fails
	tx := covCEUnbondTx(fx.na.BondAddress)
	tx.Coins = common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One))}
	cmsg := NewMsgUnBond(tx, fx.na.NodeAddress, cosmos.NewUint(common.One), fx.na.BondAddress, nil, fx.opAcc)
	fk = newCovCEFailKeeper(k, "SetNodeAccount")
	fk.skip["SetNodeAccount"] = 1
	cctx, _ = ctx.CacheContext()
	err = NewUnBondHandler(NewDummyMgrWithKeeper(fk)).handle(cctx, *cmsg)
	c.Check(errors.Is(err, errInternal), Equals, true)
	c.Check(err, ErrorMatches, "(?s).*fail to save node account to key value store.*")
	c.Check(fk.calls["SetNodeAccount"], Equals, 2)
}
