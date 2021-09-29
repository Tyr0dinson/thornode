package types

import (
	. "gopkg.in/check.v1"
)

type MsgSetNodeTypeSuite struct{}

var _ = Suite(&MsgSetNodeTypeSuite{})

func (MsgSetNodeTypeSuite) TestMsgSetNodeType(c *C) {
	acc1 := GetRandomBech32Addr()
	c.Assert(acc1.Empty(), Equals, false)

	msgSetNodeType := NewMsgSetNodeType(NodeType_TypeValidator, acc1)
	c.Assert(msgSetNodeType.Route(), Equals, RouterKey)
	c.Assert(msgSetNodeType.Type(), Equals, "set_node_keys")
	c.Assert(msgSetNodeType.ValidateBasic(), IsNil)
	c.Assert(len(msgSetNodeType.GetSignBytes()) > 0, Equals, true)
	c.Assert(msgSetNodeType.GetSigners(), NotNil)
	c.Assert(msgSetNodeType.GetSigners()[0].String(), Equals, acc1.String())
}
