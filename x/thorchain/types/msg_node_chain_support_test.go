package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type MsgNodeChainSupportTestSuite struct{}

var _ = Suite(&MsgNodeChainSupportTestSuite{})

func (MsgNodeChainSupportTestSuite) TestMsgAddNodeChainSupport(c *C) {
	acc := GetRandomBech32Addr()

	// Valid message
	msg := NewMsgAddNodeChainSupport(common.ETHChain.String(), acc)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), NotNil)
	c.Assert(msg.GetSigners(), HasLen, 1)
	c.Assert(msg.GetSigners()[0].String(), Equals, acc.String())
	c.Assert(msg.Chain, Equals, common.ETHChain.String())

	// Invalid: empty signer
	msg = NewMsgAddNodeChainSupport(common.BTCChain.String(), nil)
	c.Assert(msg.ValidateBasic(), NotNil)

	// Invalid: empty chain
	msg = NewMsgAddNodeChainSupport("", acc)
	c.Assert(msg.ValidateBasic(), NotNil)
}

func (MsgNodeChainSupportTestSuite) TestMsgRemoveNodeChainSupport(c *C) {
	acc := GetRandomBech32Addr()

	// Valid message
	msg := NewMsgRemoveNodeChainSupport(common.AVAXChain.String(), acc)
	c.Assert(msg.ValidateBasic(), IsNil)
	c.Assert(msg.GetSigners(), NotNil)
	c.Assert(msg.GetSigners(), HasLen, 1)
	c.Assert(msg.GetSigners()[0].String(), Equals, acc.String())
	c.Assert(msg.Chain, Equals, common.AVAXChain.String())

	// Invalid: empty signer
	msg = NewMsgRemoveNodeChainSupport(common.BSCChain.String(), nil)
	c.Assert(msg.ValidateBasic(), NotNil)

	// Invalid: empty chain
	msg = NewMsgRemoveNodeChainSupport("", acc)
	c.Assert(msg.ValidateBasic(), NotNil)
}
