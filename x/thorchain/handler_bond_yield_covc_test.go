package thorchain

import (
	"errors"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerBondYieldCovCESuite struct{}

var _ = Suite(&HandlerBondYieldCovCESuite{})

func covCEBondYieldSetup(c *C) (cosmos.Context, *Mgrs, NodeAccount, cosmos.AccAddress) {
	ctx, mgr := setupManagerForTest(c)
	mgr.Keeper().SetMimir(ctx, constants.BondYieldEnabled.String(), 1)
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	acc, err := na.BondAddress.AccAddress()
	c.Assert(err, IsNil)
	return ctx, mgr, na, acc
}

func covCEBondYieldMsg(na NodeAccount, from cosmos.AccAddress, bps uint64, target string) *MsgBondYield {
	tx := common.Tx{ID: GetRandomTxHash(), FromAddress: common.Address(from.String())}
	return NewMsgBondYield(tx, na.NodeAddress, bps, target, from)
}

func (s *HandlerBondYieldCovCESuite) TestRunCovCE(c *C) {
	ctx, mgr, na, acc := covCEBondYieldSetup(c)
	h := NewBondYieldHandler(mgr)

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	// invalid basic
	_, err = h.Run(ctx, &MsgBondYield{})
	c.Check(err, NotNil)

	// happy path with a THORChain payout address: the legacy node gets its
	// provider list backfilled and the auto-claim settings stored
	target := GetRandomBech32Addr()
	res, err := h.Run(ctx, covCEBondYieldMsg(na, acc, 2500, target.String()))
	c.Assert(err, IsNil)
	c.Assert(res, NotNil)
	bp, err := mgr.Keeper().GetBondProviders(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	p := bp.Get(acc)
	c.Check(p.Bps, Equals, uint64(2500))
	c.Check(p.PayoutTarget, Equals, target.String())

	// non provider
	_, err = h.Run(ctx, covCEBondYieldMsg(na, GetRandomBech32Addr(), 100, ""))
	c.Check(err, ErrorMatches, "(?s).*bond provider address is not valid for node account.*")

	// payout target on another chain is rejected by ValidateBasic
	_, err = h.Run(ctx, covCEBondYieldMsg(na, acc, 100, GetRandomBTCAddress().String()))
	c.Check(err, ErrorMatches, "(?s).*must be a THORChain address.*")

	// a network module payout target is rejected by the stateful validation
	reserve := authtypes.NewModuleAddress(ReserveName)
	_, err = h.Run(ctx, covCEBondYieldMsg(na, acc, 100, reserve.String()))
	c.Check(err, ErrorMatches, "(?s).*invalid bond yield payout target.*cannot be a network module.*")

	// from address that is not a THORChain address
	msg := covCEBondYieldMsg(na, acc, 100, "")
	msg.TxIn.FromAddress = GetRandomBTCAddress()
	_, err = h.Run(ctx, msg)
	c.Check(errors.Is(err, se.ErrInvalidAddress), Equals, true)

	// disabled
	mgr.Keeper().SetMimir(ctx, constants.BondYieldEnabled.String(), 0)
	_, err = h.Run(ctx, covCEBondYieldMsg(na, acc, 100, ""))
	c.Check(err, ErrorMatches, "(?s).*bond yield is not enabled.*")
}

func (s *HandlerBondYieldCovCESuite) TestKeeperErrorsCovCE(c *C) {
	ctx, mgr, na, acc := covCEBondYieldSetup(c)
	msg := covCEBondYieldMsg(na, acc, 100, "")
	k := mgr.Keeper()

	for _, m := range []string{"GetNodeAccount", "GetBondProviders"} {
		mgr.K = newCovCEFailKeeper(k, m)
		h := NewBondYieldHandler(mgr)
		c.Check(h.validate(ctx, *msg), ErrorMatches, "(?s).*kaboom.*", Commentf("validate %s", m))
		c.Check(h.handle(ctx, *msg), ErrorMatches, "(?s).*kaboom.*", Commentf("handle %s", m))
	}

	mgr.K = newCovCEFailKeeper(k, "SetBondProviders")
	_, err := NewBondYieldHandler(mgr).Run(ctx, msg)
	c.Check(err, ErrorMatches, "kaboom")
	mgr.K = k

	// handle with a non-THORChain from address
	bad := covCEBondYieldMsg(na, acc, 100, "")
	bad.TxIn.FromAddress = GetRandomBTCAddress()
	c.Check(NewBondYieldHandler(mgr).handle(ctx, *bad), ErrorMatches, "(?s).*fail to parse bond provider address.*")

	// handle with a non provider
	c.Check(NewBondYieldHandler(mgr).handle(ctx, *covCEBondYieldMsg(na, GetRandomBech32Addr(), 100, "")),
		ErrorMatches, "(?s).*bond provider address is not valid for node account.*")

	// a node with an invalid bond address cannot be backfilled
	na2 := GetRandomValidatorNode(NodeActive)
	na2.BondAddress = common.Address("bogus")
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	msg2 := covCEBondYieldMsg(na2, acc, 100, "")
	c.Check(NewBondYieldHandler(mgr).validate(ctx, *msg2), ErrorMatches, "(?s).*fail to parse bond address.*")
	c.Check(NewBondYieldHandler(mgr).handle(ctx, *msg2), ErrorMatches, "(?s).*fail to parse bond address.*")
}
