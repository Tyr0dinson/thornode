package thorchain

import (
	"errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerMaintCovCESuite struct{}

var _ = Suite(&HandlerMaintCovCESuite{})

func covCEMaintEvent(c *C, ctx cosmos.Context) string {
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != "toggle_maintenance" {
			continue
		}
		for _, attr := range evt.Attributes {
			if attr.Key == "maintenance" {
				return attr.Value
			}
		}
	}
	c.Fatal("toggle_maintenance event not found")
	return ""
}

func (s *HandlerMaintCovCESuite) TestRunToggleCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	bondAcc, err := na.BondAddress.AccAddress()
	c.Assert(err, IsNil)

	h := NewMaintHandler(NewDummyMgrWithKeeper(k))

	// wrong message type
	_, err = h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)

	// toggle on
	ctx1 := ctx.WithEventManager(sdk.NewEventManager())
	res, err := h.Run(ctx1, types.NewMsgMaint(na.NodeAddress, bondAcc))
	c.Assert(err, IsNil)
	c.Assert(res, NotNil)
	got, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Maintenance, Equals, true)
	c.Check(covCEMaintEvent(c, ctx1), Equals, "on")

	// toggle off
	ctx2 := ctx.WithEventManager(sdk.NewEventManager())
	_, err = h.Run(ctx2, types.NewMsgMaint(na.NodeAddress, bondAcc))
	c.Assert(err, IsNil)
	got, err = k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Maintenance, Equals, false)
	c.Check(covCEMaintEvent(c, ctx2), Equals, "off")

	// signer not the bond address is rejected by Run
	_, err = h.Run(ctx, types.NewMsgMaint(na.NodeAddress, GetRandomBech32Addr()))
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)
}

func (s *HandlerMaintCovCESuite) TestValidateMaintAuthCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)

	// unknown node -> empty account -> unauthorized
	err := validateMaintAuth(ctx, k, GetRandomBech32Addr(), GetRandomBech32Addr())
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)

	// keeper error -> unauthorized
	fk := newCovCEFailKeeper(k, "GetNodeAccount")
	err = validateMaintAuth(ctx, fk, GetRandomBech32Addr(), GetRandomBech32Addr())
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)

	// invalid bond address on the node account -> parse error
	na := GetRandomValidatorNode(NodeActive)
	na.BondAddress = common.Address("not-a-thor-address")
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	err = validateMaintAuth(ctx, k, na.NodeAddress, GetRandomBech32Addr())
	c.Check(err, NotNil)
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, false)
}

func (s *HandlerMaintCovCESuite) TestHandleErrorsCovCE(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	bondAcc, err := na.BondAddress.AccAddress()
	c.Assert(err, IsNil)
	msg := types.NewMsgMaint(na.NodeAddress, bondAcc)

	h := NewMaintHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "GetNodeAccount")))
	err = h.handle(ctx, *msg)
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)

	h = NewMaintHandler(NewDummyMgrWithKeeper(newCovCEFailKeeper(k, "SetNodeAccount")))
	_, err = h.Run(ctx, msg)
	c.Check(err, ErrorMatches, "fail to save node account: kaboom")
	got, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Maintenance, Equals, false)
}
