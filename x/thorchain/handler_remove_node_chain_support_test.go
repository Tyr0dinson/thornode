package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

var _ = Suite(&HandlerRemoveNodeChainSupportSuite{})

type HandlerRemoveNodeChainSupportSuite struct{}

func (s *HandlerRemoveNodeChainSupportSuite) TestValidate(c *C) {
	ctx, k := setupKeeperForTest(c)
	handler := NewRemoveNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// setup: active node with explicit chain support
	node := GetRandomValidatorNode(NodeActive)
	node.SupportedChains = []string{common.BTCChain.String(), common.ETHChain.String(), common.DOGEChain.String()}
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// happy path: remove a chain the node supports
	msg := NewMsgRemoveNodeChainSupport(common.BTCChain, node.NodeAddress)
	err := handler.validate(ctx, *msg)
	c.Assert(err, IsNil)

	// invalid: empty signer
	msg = &MsgRemoveNodeChainSupport{}
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// invalid: signer is not a node account
	msg = NewMsgRemoveNodeChainSupport(common.BTCChain, GetRandomBech32Addr())
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// invalid: chain not in node's supported chains
	msg = NewMsgRemoveNodeChainSupport(common.GAIAChain, node.NodeAddress)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// invalid: removing would drop below MinNodeChainSupport
	singleChainNode := GetRandomValidatorNode(NodeActive)
	singleChainNode.SupportedChains = []string{common.BTCChain.String()}
	c.Assert(k.SetNodeAccount(ctx, singleChainNode), IsNil)
	msg = NewMsgRemoveNodeChainSupport(common.BTCChain, singleChainNode.NodeAddress)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// invalid: cannot remove THORChain (native chain must always be present)
	thorNode := GetRandomValidatorNode(NodeActive)
	thorNode.SupportedChains = []string{
		common.THORChain.String(),
		common.BTCChain.String(),
		common.ETHChain.String(),
	}
	c.Assert(k.SetNodeAccount(ctx, thorNode), IsNil)
	msg = NewMsgRemoveNodeChainSupport(common.THORChain, thorNode.NodeAddress)
	err = handler.validate(ctx, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerRemoveNodeChainSupportSuite) TestValidateMinNodeChainSupport(c *C) {
	ctx, k := setupKeeperForTest(c)
	handler := NewRemoveNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// set MinNodeChainSupport to 2 via mimir
	k.SetMimir(ctx, constants.MinNodeChainSupport.String(), 2)

	node := GetRandomValidatorNode(NodeActive)
	node.SupportedChains = []string{common.BTCChain.String(), common.ETHChain.String()}
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// removing would drop from 2 to 1, below min of 2
	msg := NewMsgRemoveNodeChainSupport(common.BTCChain, node.NodeAddress)
	err := handler.validate(ctx, *msg)
	c.Assert(err, NotNil)

	// add a third chain, now removing is allowed
	node.SupportedChains = append(node.SupportedChains, common.DOGEChain.String())
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)
	msg = NewMsgRemoveNodeChainSupport(common.BTCChain, node.NodeAddress)
	err = handler.validate(ctx, *msg)
	c.Assert(err, IsNil)
}

func (s *HandlerRemoveNodeChainSupportSuite) TestHandle(c *C) {
	ctx, k := setupKeeperForTest(c)
	handler := NewRemoveNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	node := GetRandomValidatorNode(NodeActive)
	node.SupportedChains = []string{common.BTCChain.String(), common.ETHChain.String(), common.DOGEChain.String()}
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// remove BTC
	msg := NewMsgRemoveNodeChainSupport(common.BTCChain, node.NodeAddress)
	err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	na, err := k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(len(na.SupportedChains), Equals, 2)
	for _, ch := range na.SupportedChains {
		c.Assert(ch == common.BTCChain.String(), Equals, false)
	}

	// remove ETH
	msg = NewMsgRemoveNodeChainSupport(common.ETHChain, node.NodeAddress)
	err = handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	na, err = k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(len(na.SupportedChains), Equals, 1)
	c.Assert(na.SupportedChains[0], Equals, common.DOGEChain.String())
}

func (s *HandlerRemoveNodeChainSupportSuite) TestHandleMaterializeEmptyChains(c *C) {
	ctx, k := setupKeeperForTest(c)
	handler := NewRemoveNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// node with empty SupportedChains (means "all chains")
	node := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// should materialize all chains first, then remove BTC
	msg := NewMsgRemoveNodeChainSupport(common.BTCChain, node.NodeAddress)
	err := handler.handle(ctx, *msg)
	c.Assert(err, IsNil)

	na, err := k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(len(na.SupportedChains) > 0, Equals, true)
	for _, ch := range na.SupportedChains {
		c.Assert(ch == common.BTCChain.String(), Equals, false)
	}
}

func (s *HandlerRemoveNodeChainSupportSuite) TestRun(c *C) {
	ctx, k := setupKeeperForTest(c)
	handler := NewRemoveNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// wrong message type
	_, err := handler.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)

	node := GetRandomValidatorNode(NodeActive)
	node.SupportedChains = []string{common.BTCChain.String(), common.ETHChain.String()}
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// happy path through Run
	msg := NewMsgRemoveNodeChainSupport(common.BTCChain, node.NodeAddress)
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *HandlerRemoveNodeChainSupportSuite) TestAnteHandler(c *C) {
	ctx, k := setupKeeperForTest(c)

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// active node passes ante
	msg := NewMsgRemoveNodeChainSupport(common.BTCChain, node.NodeAddress)
	newCtx, err := RemoveNodeChainSupportAnteHandler(ctx, GetCurrentVersion(), k, *msg)
	c.Assert(err, IsNil)
	c.Assert(newCtx.Priority(), Equals, ActiveNodePriority)

	// standby node passes ante
	standbyNode := GetRandomValidatorNode(NodeStandby)
	c.Assert(k.SetNodeAccount(ctx, standbyNode), IsNil)
	msg = NewMsgRemoveNodeChainSupport(common.BTCChain, standbyNode.NodeAddress)
	_, err = RemoveNodeChainSupportAnteHandler(ctx, GetCurrentVersion(), k, *msg)
	c.Assert(err, IsNil)

	// non-node fails ante
	msg = NewMsgRemoveNodeChainSupport(common.BTCChain, GetRandomBech32Addr())
	_, err = RemoveNodeChainSupportAnteHandler(ctx, GetCurrentVersion(), k, *msg)
	c.Assert(err, NotNil)
}

func (s *HandlerRemoveNodeChainSupportSuite) TestRunValidationFail(c *C) {
	ctx, k := setupKeeperForTest(c)
	handler := NewRemoveNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// non-existent node
	msg := NewMsgRemoveNodeChainSupport(common.BTCChain, GetRandomBech32Addr())
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// empty signer
	badMsg := &MsgRemoveNodeChainSupport{Chain: common.BTCChain}
	result, err = handler.Run(ctx, badMsg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// standby node
	standbyNode := GetRandomValidatorNode(NodeStandby)
	standbyNode.SupportedChains = []string{common.BTCChain.String(), common.ETHChain.String()}
	c.Assert(k.SetNodeAccount(ctx, standbyNode), IsNil)
	msg = NewMsgRemoveNodeChainSupport(common.BTCChain, standbyNode.NodeAddress)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// verify signer field initialization
	c.Assert(len(badMsg.GetSigners()), Equals, 1)
	c.Assert(badMsg.GetSigners()[0], DeepEquals, cosmos.AccAddress(nil))
}
