package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gopkg.in/check.v1"
)

var _ = Suite(&HandlerAddNodeChainSupportSuite{})

type HandlerAddNodeChainSupportSuite struct{}

func (s *HandlerAddNodeChainSupportSuite) TestValidate(c *C) {
	ctx, k := setupKeeperForTest(c)
	handler := NewAddNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// happy path: active node, valid chain
	node := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)
	msg := NewMsgAddNodeChainSupport(common.BTCChain, node.NodeAddress)
	err := handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// standby node should also be allowed
	standbyNode := GetRandomValidatorNode(NodeStandby)
	c.Assert(k.SetNodeAccount(ctx, standbyNode), IsNil)
	msg = NewMsgAddNodeChainSupport(common.ETHChain, standbyNode.NodeAddress)
	err = handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// invalid: empty signer
	msg = &MsgAddNodeChainSupport{}
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// invalid: signer is not a node account
	msg = NewMsgAddNodeChainSupport(common.BTCChain, GetRandomBech32Addr())
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// invalid: unsupported chain
	msg = NewMsgAddNodeChainSupport(common.Chain("FAKECHAINFOO"), node.NodeAddress)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerAddNodeChainSupportSuite) TestHandle(c *C) {
	ctx, k := setupKeeperForTest(c)
	handler := NewAddNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// adding a chain when SupportedChains is empty materializes full list then appends
	msg := NewMsgAddNodeChainSupport(common.BTCChain, node.NodeAddress)
	err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	na, err := k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(len(na.SupportedChains) > 0, Equals, true)
	found := false
	for _, ch := range na.SupportedChains {
		if ch == common.BTCChain.String() {
			found = true
		}
	}
	c.Assert(found, Equals, true)

	// idempotent: adding the same chain again is a no-op
	prevLen := len(na.SupportedChains)
	err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	na, err = k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(len(na.SupportedChains), Equals, prevLen)

	// adding a different chain appends it
	msg = NewMsgAddNodeChainSupport(common.ETHChain, node.NodeAddress)
	err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)
	na, err = k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	foundETH := false
	for _, ch := range na.SupportedChains {
		if ch == common.ETHChain.String() {
			foundETH = true
		}
	}
	c.Assert(foundETH, Equals, true)
}

func (s *HandlerAddNodeChainSupportSuite) TestRun(c *C) {
	ctx, k := setupKeeperForTest(c)
	handler := NewAddNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// wrong message type
	_, err := handler.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// happy path through Run
	msg := NewMsgAddNodeChainSupport(common.BTCChain, node.NodeAddress)
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *HandlerAddNodeChainSupportSuite) TestAnteHandler(c *C) {
	ctx, k := setupKeeperForTest(c)

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// active node passes ante
	msg := NewMsgAddNodeChainSupport(common.BTCChain, node.NodeAddress)
	newCtx, err := AddNodeChainSupportAnteHandler(ctx, GetCurrentVersion(), k, *msg)
	c.Assert(err, IsNil)
	c.Assert(newCtx.Priority(), Equals, int64(ActiveNodePriority))

	// standby node passes ante
	standbyNode := GetRandomValidatorNode(NodeStandby)
	c.Assert(k.SetNodeAccount(ctx, standbyNode), IsNil)
	msg = NewMsgAddNodeChainSupport(common.BTCChain, standbyNode.NodeAddress)
	_, err = AddNodeChainSupportAnteHandler(ctx, GetCurrentVersion(), k, *msg)
	c.Assert(err, IsNil)

	// non-node fails ante
	msg = NewMsgAddNodeChainSupport(common.BTCChain, GetRandomBech32Addr())
	_, err = AddNodeChainSupportAnteHandler(ctx, GetCurrentVersion(), k, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerAddNodeChainSupportSuite) TestNodeAccountSignerPriority(c *C) {
	ctx, k := setupKeeperForTest(c)

	// empty signers
	_, err := nodeAccountSignerPriority(ctx, k, []cosmos.AccAddress{})
	c.Assert(err, IsNil)

	// valid node
	node := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)
	newCtx, err := nodeAccountSignerPriority(ctx, k, []cosmos.AccAddress{node.NodeAddress})
	c.Assert(err, IsNil)
	c.Assert(newCtx.Priority(), Equals, int64(ActiveNodePriority))
}
