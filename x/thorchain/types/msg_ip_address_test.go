package types

import (
	"errors"
	"strings"

	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type MsgSetIPAddressSuite struct{}

var _ = Suite(&MsgSetIPAddressSuite{})

func (MsgSetIPAddressSuite) TestMsgSetIPAddressSuite(c *C) {
	acc1 := GetRandomBech32Addr()
	c.Assert(acc1.Empty(), Equals, false)
	msg := NewMsgSetIPAddress("192.168.0.1", acc1)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), NotNil)
	c.Assert(msg.GetSigners()[0].String(), Equals, acc1.String())

	msgIPv6 := NewMsgSetIPAddress("2001:db8::1", acc1)
	c.Assert(msgIPv6.ValidateBasic(), IsNil)

	onionAddr := strings.Repeat("a", 56) + ".onion"
	msgOnion := NewMsgSetIPAddress(onionAddr, acc1)
	c.Assert(msgOnion.ValidateBasic(), IsNil)
	c.Assert(IsValidOnionV3(onionAddr), Equals, true)

	msg1 := NewMsgSetIPAddress("192.168.0.1", cosmos.AccAddress{})
	err1 := msg1.ValidateBasic()
	c.Assert(err1, NotNil)
	c.Assert(errors.Is(err1, se.ErrInvalidAddress), Equals, true)

	msg2 := NewMsgSetIPAddress("whatever", acc1)
	err2 := msg2.ValidateBasic()
	c.Assert(err2, NotNil)
	c.Assert(errors.Is(err2, se.ErrUnknownRequest), Equals, true)

	invalidOnions := []string{
		strings.Repeat("a", 16) + ".onion",
		strings.Repeat("a", 55) + ".onion",
		strings.Repeat("a", 57) + ".onion",
		strings.Repeat("A", 56) + ".onion",
		strings.Repeat("0", 56) + ".onion",
	}
	for _, addr := range invalidOnions {
		msg := NewMsgSetIPAddress(addr, acc1)
		err := msg.ValidateBasic()
		c.Assert(err, NotNil)
		c.Assert(errors.Is(err, se.ErrUnknownRequest), Equals, true)
		c.Assert(IsValidOnionV3(addr), Equals, false)
	}
}
