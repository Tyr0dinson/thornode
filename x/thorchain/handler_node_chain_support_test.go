package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

var _ = Suite(&HandlerNodeChainSupportSuite{})

type HandlerNodeChainSupportSuite struct{}

func (s *HandlerNodeChainSupportSuite) TestRun(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Enable chain-aware consensus
	k.SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	handler := NewNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// Test valid MsgAddNodeChainSupport
	msgAdd := NewMsgAddNodeChainSupport(common.ETHChain.String(), node.NodeAddress)
	result, err := handler.Run(ctx, msgAdd)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Test valid MsgRemoveNodeChainSupport
	msgRemove := NewMsgRemoveNodeChainSupport(common.ETHChain.String(), node.NodeAddress)
	result, err = handler.Run(ctx, msgRemove)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Test invalid message type
	msgInvalid := NewMsgNetworkFee(1024, common.BTCChain, 1, 10000, GetRandomBech32Addr())
	result, err = handler.Run(ctx, msgInvalid)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerNodeChainSupportSuite) TestHandleAddChainSupport(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Enable chain-aware consensus
	k.SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)

	node := GetRandomValidatorNode(NodeActive)
	// Start with empty supported chains
	node.SupportedChains = []string{}
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	handler := NewNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// Happy path: add chain support
	msg := NewMsgAddNodeChainSupport(common.ETHChain.String(), node.NodeAddress)
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify chain was added
	updatedNode, err := k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNode.SupportedChains, HasLen, 1)
	c.Assert(updatedNode.SupportedChains[0], Equals, common.ETHChain.String())

	// Idempotent: adding same chain again should not error
	result, err = handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify chain list didn't duplicate
	updatedNode, err = k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNode.SupportedChains, HasLen, 1)

	// Add another chain
	msgBTC := NewMsgAddNodeChainSupport(common.BTCChain.String(), node.NodeAddress)
	result, err = handler.Run(ctx, msgBTC)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	updatedNode, err = k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNode.SupportedChains, HasLen, 2)
}

func (s *HandlerNodeChainSupportSuite) TestAddChainSupportValidation(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Enable chain-aware consensus
	k.SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)

	node := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	handler := NewNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// Empty signer
	msg := NewMsgAddNodeChainSupport(common.ETHChain.String(), cosmos.AccAddress{})
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// Empty chain
	msg = NewMsgAddNodeChainSupport("", node.NodeAddress)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// Invalid chain format
	msg = NewMsgAddNodeChainSupport("INVALIDCHAIN", node.NodeAddress)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// Node account not found
	nonExistentNode := GetRandomBech32Addr()
	msg = NewMsgAddNodeChainSupport(common.ETHChain.String(), nonExistentNode)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// Unsupported chain (not in common.AllChains)
	// Create a chain string that's valid format but not in AllChains
	msg = NewMsgAddNodeChainSupport("FAKE", node.NodeAddress)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerNodeChainSupportSuite) TestHandleRemoveChainSupport(c *C) {
	ctx, k := setupKeeperForTest(c)

	node := GetRandomValidatorNode(NodeActive)
	node.SupportedChains = []string{common.ETHChain.String(), common.BTCChain.String(), common.AVAXChain.String()}
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	handler := NewNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// Happy path: remove chain support
	msg := NewMsgRemoveNodeChainSupport(common.ETHChain.String(), node.NodeAddress)
	result, err := handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify chain was removed
	updatedNode, err := k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNode.SupportedChains, HasLen, 2)
	c.Assert(updatedNode.SupportedChains[0], Equals, common.BTCChain.String())
	c.Assert(updatedNode.SupportedChains[1], Equals, common.AVAXChain.String())

	// No-op: removing chain not in list should not error
	msgRemoveAgain := NewMsgRemoveNodeChainSupport(common.ETHChain.String(), node.NodeAddress)
	result, err = handler.Run(ctx, msgRemoveAgain)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify chain count didn't change
	updatedNode, err = k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNode.SupportedChains, HasLen, 2)
}

func (s *HandlerNodeChainSupportSuite) TestChainSupportStoresCanonicalChain(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	handler := NewNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	node := GetRandomValidatorNode(NodeActive)
	node.SupportedChains = []string{common.ETHChain.String()}
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	// A lower-case add is stored as the canonical upper-case chain.
	_, err := handler.Run(ctx, NewMsgAddNodeChainSupport("btc", node.NodeAddress))
	c.Assert(err, IsNil)
	na, err := k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(na.SupportedChains, DeepEquals, []string{"ETH", "BTC"})

	// Any case variant of a chain already present is a no-op.
	for _, variant := range []string{"BTC", "Btc", "bTC"} {
		_, err = handler.Run(ctx, NewMsgAddNodeChainSupport(variant, node.NodeAddress))
		c.Assert(err, IsNil)
	}
	na, err = k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(na.SupportedChains, DeepEquals, []string{"ETH", "BTC"})

	// State written before normalization: remove drops every case variant.
	na.SupportedChains = []string{"ETH", "btc", "BTC", "Btc"}
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	_, err = handler.Run(ctx, NewMsgRemoveNodeChainSupport("bTc", node.NodeAddress))
	c.Assert(err, IsNil)
	na, err = k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(na.SupportedChains, DeepEquals, []string{"ETH"})

	// Add on top of a legacy lower-case entry does not create a duplicate.
	na.SupportedChains = []string{"ETH", "btc"}
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	_, err = handler.Run(ctx, NewMsgAddNodeChainSupport("BTC", node.NodeAddress))
	c.Assert(err, IsNil)
	na, err = k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(na.SupportedChains, DeepEquals, []string{"ETH", "btc"})
}

func (s *HandlerNodeChainSupportSuite) TestGetNodeSupportedChainsNormalizes(c *C) {
	ctx, k := setupKeeperForTest(c)

	node := GetRandomValidatorNode(NodeActive)
	// Legacy state: case variants, a duplicate and an unparsable entry.
	node.SupportedChains = []string{"btc", "ETH", "BTC", "Btc", "eth", "not a chain!"}
	c.Assert(getNodeSupportedChains(ctx, k, node), DeepEquals, []string{"BTC", "ETH"})

	// The read path feeds chain grouping: one node declaring "btc" next to a
	// node declaring "BTC" lands in the same group, not a group of its own.
	a := GetRandomValidatorNode(NodeActive)
	a.SupportedChains = []string{"btc"}
	b := GetRandomValidatorNode(NodeActive)
	b.SupportedChains = []string{"BTC"}
	vm := newNetworkMgr(k, NewTxStoreDummy(), NewDummyEventMgr())
	groups, keys := vm.groupChainsByNodeSets(ctx, NodeAccounts{a, b})
	c.Assert(keys, HasLen, 1)
	c.Assert(groups[keys[0]].Chains, DeepEquals, []string{"BTC"})
	c.Assert(groups[keys[0]].Members, HasLen, 2)
}

func (s *HandlerNodeChainSupportSuite) TestRemoveChainSupportMinimumConstraint(c *C) {
	ctx, k := setupKeeperForTest(c)

	node := GetRandomValidatorNode(NodeActive)
	node.SupportedChains = []string{common.ETHChain.String()}
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	handler := NewNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// Get the MinNodeChainSupport constant (should be 1 in test environment)
	minChainSupport := k.GetConfigInt64(ctx, constants.MinNodeChainSupport)
	c.Assert(minChainSupport, Equals, int64(1))

	// Try to remove the only chain - should fail
	msg := NewMsgRemoveNodeChainSupport(common.ETHChain.String(), node.NodeAddress)
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// Verify chain was not removed
	updatedNode, err := k.GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNode.SupportedChains, HasLen, 1)
	c.Assert(updatedNode.SupportedChains[0], Equals, common.ETHChain.String())
}

func (s *HandlerNodeChainSupportSuite) TestRemoveChainSupportValidation(c *C) {
	ctx, k := setupKeeperForTest(c)

	node := GetRandomValidatorNode(NodeActive)
	node.SupportedChains = []string{common.ETHChain.String(), common.BTCChain.String()}
	c.Assert(k.SetNodeAccount(ctx, node), IsNil)

	handler := NewNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// Empty signer
	msg := NewMsgRemoveNodeChainSupport(common.ETHChain.String(), cosmos.AccAddress{})
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// Empty chain
	msg = NewMsgRemoveNodeChainSupport("", node.NodeAddress)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// Invalid chain format
	msg = NewMsgRemoveNodeChainSupport("INVALIDCHAIN", node.NodeAddress)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// Node account not found
	nonExistentNode := GetRandomBech32Addr()
	msg = NewMsgRemoveNodeChainSupport(common.ETHChain.String(), nonExistentNode)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

// NodeChainSupportTestHelper is a test helper that wraps the keeper to simulate failures
type NodeChainSupportTestHelper struct {
	keeper.Keeper
	failGetNodeAccount bool
	failSetNodeAccount bool
}

func NewNodeChainSupportTestHelper(k keeper.Keeper) *NodeChainSupportTestHelper {
	return &NodeChainSupportTestHelper{
		Keeper: k,
	}
}

func (h *NodeChainSupportTestHelper) GetNodeAccount(ctx cosmos.Context, addr cosmos.AccAddress) (NodeAccount, error) {
	if h.failGetNodeAccount {
		return NodeAccount{}, errKaboom
	}
	return h.Keeper.GetNodeAccount(ctx, addr)
}

func (h *NodeChainSupportTestHelper) SetNodeAccount(ctx cosmos.Context, na NodeAccount) error {
	if h.failSetNodeAccount {
		return errKaboom
	}
	return h.Keeper.SetNodeAccount(ctx, na)
}

func (s *HandlerNodeChainSupportSuite) TestHandleAddChainSupportKeeperErrors(c *C) {
	testCases := []struct {
		name            string
		messageProvider func(ctx cosmos.Context, helper *NodeChainSupportTestHelper) cosmos.Msg
		validator       func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *NodeChainSupportTestHelper, name string)
	}{
		{
			name: "fail to get node account should return an error",
			messageProvider: func(ctx cosmos.Context, helper *NodeChainSupportTestHelper) cosmos.Msg {
				helper.failGetNodeAccount = true
				return NewMsgAddNodeChainSupport(common.ETHChain.String(), GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *NodeChainSupportTestHelper, name string) {
				c.Check(err, NotNil, Commentf(name))
				c.Check(result, IsNil, Commentf(name))
			},
		},
		{
			name: "fail to save node account should return an error",
			messageProvider: func(ctx cosmos.Context, helper *NodeChainSupportTestHelper) cosmos.Msg {
				nodeAccount := GetRandomValidatorNode(NodeActive)
				// Start with empty chains so we're actually adding a new one
				nodeAccount.SupportedChains = []string{}
				c.Assert(helper.Keeper.SetNodeAccount(ctx, nodeAccount), IsNil)
				helper.failSetNodeAccount = true
				return NewMsgAddNodeChainSupport(common.ETHChain.String(), nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *NodeChainSupportTestHelper, name string) {
				c.Check(err, NotNil, Commentf(name))
				c.Check(result, IsNil, Commentf(name))
			},
		},
	}

	for _, tc := range testCases {
		ctx, mgr := setupManagerForTest(c)
		// Enable chain-aware consensus
		mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
		helper := NewNodeChainSupportTestHelper(mgr.Keeper())
		mgr.K = helper
		handler := NewNodeChainSupportHandler(mgr)
		msg := tc.messageProvider(ctx, helper)
		result, err := handler.Run(ctx, msg)
		tc.validator(c, ctx, result, err, helper, tc.name)
	}
}

func (s *HandlerNodeChainSupportSuite) TestHandleRemoveChainSupportKeeperErrors(c *C) {
	testCases := []struct {
		name            string
		messageProvider func(ctx cosmos.Context, helper *NodeChainSupportTestHelper) cosmos.Msg
		validator       func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *NodeChainSupportTestHelper, name string)
	}{
		{
			name: "fail to get node account should return an error",
			messageProvider: func(ctx cosmos.Context, helper *NodeChainSupportTestHelper) cosmos.Msg {
				helper.failGetNodeAccount = true
				return NewMsgRemoveNodeChainSupport(common.ETHChain.String(), GetRandomBech32Addr())
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *NodeChainSupportTestHelper, name string) {
				c.Check(err, NotNil, Commentf(name))
				c.Check(result, IsNil, Commentf(name))
			},
		},
		{
			name: "fail to save node account should return an error",
			messageProvider: func(ctx cosmos.Context, helper *NodeChainSupportTestHelper) cosmos.Msg {
				nodeAccount := GetRandomValidatorNode(NodeActive)
				nodeAccount.SupportedChains = []string{common.ETHChain.String(), common.BTCChain.String()}
				c.Assert(helper.Keeper.SetNodeAccount(ctx, nodeAccount), IsNil)
				helper.failSetNodeAccount = true
				return NewMsgRemoveNodeChainSupport(common.ETHChain.String(), nodeAccount.NodeAddress)
			},
			validator: func(c *C, ctx cosmos.Context, result *cosmos.Result, err error, helper *NodeChainSupportTestHelper, name string) {
				c.Check(err, NotNil, Commentf(name))
				c.Check(result, IsNil, Commentf(name))
			},
		},
	}

	for _, tc := range testCases {
		ctx, mgr := setupManagerForTest(c)
		helper := NewNodeChainSupportTestHelper(mgr.Keeper())
		mgr.K = helper
		handler := NewNodeChainSupportHandler(mgr)
		msg := tc.messageProvider(ctx, helper)
		result, err := handler.Run(ctx, msg)
		tc.validator(c, ctx, result, err, helper, tc.name)
	}
}

func (s *HandlerNodeChainSupportSuite) TestNodeChainSupportAnteHandler(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	FundModule(c, ctx, k, BondName, 100*common.One)

	fee := k.GetNativeTxFee(ctx)
	c.Assert(fee.IsZero(), Equals, false)

	activeNode := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, activeNode), IsNil)
	standbyNode := GetRandomValidatorNode(NodeStandby)
	c.Assert(k.SetNodeAccount(ctx, standbyNode), IsNil)

	bondOf := func(addr cosmos.AccAddress) cosmos.Uint {
		na, err := k.GetNodeAccount(ctx, addr)
		c.Assert(err, IsNil)
		return na.Bond
	}

	// Add and remove from any node status are admitted, each charged the
	// native tx fee from bond, with no elevated mempool priority.
	for _, na := range []NodeAccount{activeNode, standbyNode} {
		before := bondOf(na.NodeAddress)
		reserveBefore := k.GetRuneBalanceOfModule(ctx, ReserveName)

		newCtx, err := NodeChainSupportAnteHandler(ctx, k, NewMsgAddNodeChainSupport(common.ETHChain.String(), na.NodeAddress))
		c.Assert(err, IsNil)
		c.Check(newCtx.Priority(), Equals, int64(0))
		newCtx, err = NodeChainSupportAnteHandler(ctx, k, NewMsgRemoveNodeChainSupport(common.ETHChain.String(), na.NodeAddress))
		c.Assert(err, IsNil)
		c.Check(newCtx.Priority(), Equals, int64(0))

		c.Check(bondOf(na.NodeAddress).String(), Equals, common.SafeSub(before, fee.MulUint64(2)).String())
		c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).String(), Equals, reserveBefore.Add(fee.MulUint64(2)).String())
	}

	// A signer that is not a node account is rejected.
	_, err := NodeChainSupportAnteHandler(ctx, k, NewMsgAddNodeChainSupport(common.ETHChain.String(), GetRandomBech32Addr()))
	c.Assert(err, NotNil)

	// A node without bond to cover the fee is rejected.
	broke := GetRandomValidatorNode(NodeStandby)
	broke.Bond = cosmos.ZeroUint()
	c.Assert(k.SetNodeAccount(ctx, broke), IsNil)
	_, err = NodeChainSupportAnteHandler(ctx, k, NewMsgRemoveNodeChainSupport(common.ETHChain.String(), broke.NodeAddress))
	c.Assert(err, NotNil)

	// With the mimir off, add is rejected before any fee is charged; remove
	// stays available so a node can still shed a chain.
	k.SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 0)
	before := bondOf(activeNode.NodeAddress)
	_, err = NodeChainSupportAnteHandler(ctx, k, NewMsgAddNodeChainSupport(common.ETHChain.String(), activeNode.NodeAddress))
	c.Assert(err, NotNil)
	c.Check(bondOf(activeNode.NodeAddress).String(), Equals, before.String())
	_, err = NodeChainSupportAnteHandler(ctx, k, NewMsgRemoveNodeChainSupport(common.ETHChain.String(), activeNode.NodeAddress))
	c.Assert(err, IsNil)

	// Test with invalid message type
	msgInvalid := NewMsgNetworkFee(1024, common.BTCChain, 1, 10000, GetRandomBech32Addr())
	newCtx, err := NodeChainSupportAnteHandler(ctx, k, msgInvalid)
	c.Assert(err, NotNil)
	c.Assert(newCtx, NotNil)
}

func (s *HandlerNodeChainSupportSuite) TestNodeAccountStatuses(c *C) {
	ctx, k := setupKeeperForTest(c)

	handler := NewNodeChainSupportHandler(NewDummyMgrWithKeeper(k))

	// Enable chain-aware consensus for this test
	k.SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)

	// Test with different node statuses
	statuses := []NodeStatus{NodeActive, NodeStandby, NodeReady, NodeDisabled}
	for _, status := range statuses {
		node := GetRandomValidatorNode(status)
		// Start with empty chains
		node.SupportedChains = []string{}
		c.Assert(k.SetNodeAccount(ctx, node), IsNil)

		// All node statuses should be able to add/remove chain support
		msg := NewMsgAddNodeChainSupport(common.ETHChain.String(), node.NodeAddress)
		result, err := handler.Run(ctx, msg)
		c.Assert(err, IsNil, Commentf("status: %s", status))
		c.Assert(result, NotNil, Commentf("status: %s", status))

		updatedNode, err := k.GetNodeAccount(ctx, node.NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(updatedNode.SupportedChains, HasLen, 1)
	}
}

func (s *HandlerNodeChainSupportSuite) TestAddChainSupportMimirValidation(c *C) {
	ctx, mgr := setupManagerForTest(c)

	node := GetRandomValidatorNode(NodeActive)
	node.SupportedChains = []string{}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)

	handler := NewNodeChainSupportHandler(mgr)

	// Test 1: MsgAddNodeChainSupport should fail when mimir is explicitly disabled
	// Note: In mocknet, EnableNodeChainConsensus defaults to 1, so we must explicitly set to 0
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 0)
	msg := NewMsgAddNodeChainSupport(common.ETHChain.String(), node.NodeAddress)
	result, err := handler.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "chain-aware consensus feature is not enabled")

	// Verify chain was not added
	updatedNode, err := mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNode.SupportedChains, HasLen, 0)

	// Test 2: MsgAddNodeChainSupport should succeed when mimir is enabled
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	result, err = handler.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify chain was added
	updatedNode, err = mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNode.SupportedChains, HasLen, 1)
	c.Assert(updatedNode.SupportedChains[0], Equals, common.ETHChain.String())

	// Test 3: MsgRemoveNodeChainSupport should work regardless of mimir setting
	// Disable mimir
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 0)

	// Remove should still work
	msgRemove := NewMsgRemoveNodeChainSupport(common.ETHChain.String(), node.NodeAddress)
	result, err = handler.Run(ctx, msgRemove)
	c.Assert(err, NotNil) // Should fail due to MinNodeChainSupport constraint
	c.Assert(result, IsNil)

	// Add another chain first (with mimir enabled)
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	msgAdd := NewMsgAddNodeChainSupport(common.BTCChain.String(), node.NodeAddress)
	result, err = handler.Run(ctx, msgAdd)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Now disable mimir and remove should work
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 0)
	result, err = handler.Run(ctx, msgRemove)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)

	// Verify chain was removed
	updatedNode, err = mgr.Keeper().GetNodeAccount(ctx, node.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(updatedNode.SupportedChains, HasLen, 1)
	c.Assert(updatedNode.SupportedChains[0], Equals, common.BTCChain.String())
}
