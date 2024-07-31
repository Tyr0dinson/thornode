package types

import (
	"errors"
	"strings"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

type MsgProposeUpgradeSuite struct{}

var _ = Suite(&MsgProposeUpgradeSuite{})

func (MsgProposeUpgradeSuite) TestMsgProposeUpgradeSuite(c *C) {
	acc := GetRandomBech32Addr()
	c.Assert(acc.Empty(), Equals, false)
	msg := NewMsgProposeUpgrade("berserk", 100000, "proposed upgrade", acc)
	c.Assert(msg.Route(), Equals, RouterKey)
	c.Assert(msg.Type(), Equals, "propose_upgrade")
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(len(msg.GetSignBytes()) > 0, Equals, true)
	c.Assert(msg.GetSigners(), NotNil)
	c.Assert(msg.GetSigners()[0].String(), Equals, acc.String())

	msg = NewMsgProposeUpgrade("berserk", 10000, "proposed upgrade", cosmos.AccAddress{})
	err := msg.ValidateBasic()
	c.Check(err, NotNil)
	c.Check(errors.Is(err, se.ErrInvalidAddress), Equals, true)

	msg = NewMsgProposeUpgrade("", 10000, "proposed upgrade", acc)
	err = msg.ValidateBasic()
	c.Check(err, NotNil)
	c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true)
	c.Assert(strings.Contains(err.Error(), "name cannot be empty"), Equals, true)

	msg = NewMsgProposeUpgrade("berserk", 0, "proposed upgrade", acc)
	err = msg.ValidateBasic()
	c.Check(err, NotNil)
	c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true)
	c.Assert(strings.Contains(err.Error(), "height cannot be zero"), Equals, true)
}

type MsgApproveUpgradeSuite struct{}

var _ = Suite(&MsgApproveUpgradeSuite{})

func (MsgApproveUpgradeSuite) TestMsgApproveUpgradeSuite(c *C) {
	acc := GetRandomBech32Addr()
	c.Assert(acc.Empty(), Equals, false)
	msg := NewMsgApproveUpgrade("berserk", acc)
	c.Assert(msg.Route(), Equals, RouterKey)
	c.Assert(msg.Type(), Equals, "approve_upgrade")
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(len(msg.GetSignBytes()) > 0, Equals, true)
	c.Assert(msg.GetSigners(), NotNil)
	c.Assert(msg.GetSigners()[0].String(), Equals, acc.String())

	msg = NewMsgApproveUpgrade("berserk", cosmos.AccAddress{})
	err := msg.ValidateBasic()
	c.Check(err, NotNil)
	c.Check(errors.Is(err, se.ErrInvalidAddress), Equals, true)

	msg = NewMsgApproveUpgrade("", acc)
	err = msg.ValidateBasic()
	c.Check(err, NotNil)
	c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true)
	c.Assert(strings.Contains(err.Error(), "name cannot be empty"), Equals, true)
}

type MsgRejectUpgradeSuite struct{}

var _ = Suite(&MsgRejectUpgradeSuite{})

func (MsgRejectUpgradeSuite) TestMsgRejectUpgradeSuite(c *C) {
	acc := GetRandomBech32Addr()
	c.Assert(acc.Empty(), Equals, false)
	msg := NewMsgRejectUpgrade("berserk", acc)
	c.Assert(msg.Route(), Equals, RouterKey)
	c.Assert(msg.Type(), Equals, "reject_upgrade")
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(len(msg.GetSignBytes()) > 0, Equals, true)
	c.Assert(msg.GetSigners(), NotNil)
	c.Assert(msg.GetSigners()[0].String(), Equals, acc.String())

	msg = NewMsgRejectUpgrade("berserk", cosmos.AccAddress{})
	err := msg.ValidateBasic()
	c.Check(err, NotNil)
	c.Check(errors.Is(err, se.ErrInvalidAddress), Equals, true)

	msg = NewMsgRejectUpgrade("", acc)
	err = msg.ValidateBasic()
	c.Check(err, NotNil)
	c.Check(errors.Is(err, se.ErrUnknownRequest), Equals, true)
	c.Assert(strings.Contains(err.Error(), "name cannot be empty"), Equals, true)
}
