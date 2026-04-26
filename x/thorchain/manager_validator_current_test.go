package thorchain

import (
	"strconv"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type ValidatorMgrTestSuite struct{}

var _ = Suite(&ValidatorMgrTestSuite{})

func (vts *ValidatorMgrTestSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func (vts *ValidatorMgrTestSuite) TestChurnWithChainHalt(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgr()
	mgr.K = k
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	// 1 standby node
	standbyNode := GetRandomValidatorNode(NodeStandby)
	c.Assert(k.SetNodeAccount(ctx, standbyNode), IsNil)

	// 1 vault with btc, created at block 1 (so lastChurnHeight=1)
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.Membership = append(vault.Membership, standbyNode.PubKeySet.Secp256k1.String())
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Set block height to churn interval after vault creation (1 + 60 = 61)
	ctx = ctx.WithBlockHeight(61)

	// set mimir to halt btc chain
	mgr.Keeper().SetMimir(ctx, "HaltBTCChain", 1)
	mgr.Keeper().SetMimir(ctx, "HaltETHChain", 1) // ignored since not in active vault

	// should not start churn with chain halted
	c.Assert(networkMgr.BeginBlock(ctx, mgr, nil), IsNil)
	ready, err := k.ListValidatorsByStatus(ctx, NodeReady)
	c.Assert(err, IsNil)
	c.Assert(len(ready), Equals, 0) // standby node should not be ready if churn did not start

	// set mimir to halt btc chain
	mgr.Keeper().SetMimir(ctx, "HaltBTCChain", -1)

	// should start churn with chain unhalted
	c.Assert(networkMgr.BeginBlock(ctx, mgr, nil), IsNil)
	ready, err = k.ListValidatorsByStatus(ctx, NodeReady)
	c.Assert(err, IsNil)
	c.Assert(len(ready), Equals, 1) // standby node should be ready if churn started
}

func (vts *ValidatorMgrTestSuite) TestSetupValidatorNodes(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1)
	mgr := NewDummyMgr()
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(validatorMgr, NotNil)
	err := validatorMgr.setupValidatorNodes(ctx, 0)
	c.Assert(err, IsNil)

	// no node accounts at all
	err = validatorMgr.setupValidatorNodes(ctx, 1)
	c.Assert(err, NotNil)

	activeNode := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, activeNode), IsNil)

	err = validatorMgr.setupValidatorNodes(ctx, 1)
	c.Assert(err, IsNil)

	readyNode := GetRandomValidatorNode(NodeReady)
	c.Assert(k.SetNodeAccount(ctx, readyNode), IsNil)

	// one active node and one ready node on start up
	// it should take both of the node as active
	validatorMgr1 := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	c.Assert(validatorMgr1.BeginBlock(ctx, mgr, nil), IsNil)
	activeNodes, err := k.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(activeNodes) == 2, Equals, true)

	activeNode1 := GetRandomValidatorNode(NodeActive)
	activeNode2 := GetRandomValidatorNode(NodeActive)
	c.Assert(k.SetNodeAccount(ctx, activeNode1), IsNil)
	c.Assert(k.SetNodeAccount(ctx, activeNode2), IsNil)

	// three active nodes and 1 ready nodes, it should take them all
	validatorMgr2 := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(validatorMgr2.BeginBlock(ctx, mgr, nil), IsNil)

	activeNodes1, err := k.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	c.Assert(len(activeNodes1) == 4, Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestRagnarokForMainnet(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	mgr.Keeper().SetMimir(ctx, constants.DesiredValidatorSet.String(), 12)
	mgr.Keeper().SetMimir(ctx, constants.ArtificialRagnarokBlockHeight.String(), 1024)
	mgr.Keeper().SetMimir(ctx, constants.ChurnInterval.String(), 256)
	mgr.Keeper().SetMimir(ctx, constants.AsgardSize.String(), 30)
	mgr.Keeper().SetMimir(ctx, constants.MaxNodeToChurnOutForLowVersion.String(), 3)

	for i := 0; i < 12; i++ {
		node := GetRandomValidatorNode(NodeReady)
		node.Bond = cosmos.NewUint(common.One * uint64(i+1))
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, node), IsNil)
	}
	c.Assert(networkMgr.setupValidatorNodes(ctx, 1), IsNil)
	nodeAccounts, err := mgr.Keeper().ListValidatorsByStatus(ctx, NodeActive)
	c.Assert(err, IsNil)
	c.Assert(len(nodeAccounts), Equals, 12)

	// trigger ragnarok
	ctx = ctx.WithBlockHeight(1024)
	c.Assert(networkMgr.BeginBlock(ctx, mgr, nil), IsNil)
	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	for _, item := range nodeAccounts {
		vault.Membership = append(vault.Membership, item.PubKeySet.Secp256k1.String())
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)
	updates := networkMgr.EndBlock(ctx, mgr)
	// ragnarok , no one leaves
	c.Assert(updates, IsNil)
	ragnarokHeight, err := mgr.Keeper().GetRagnarokBlockHeight(ctx)
	c.Assert(err, IsNil)
	c.Assert(ragnarokHeight == 1024, Equals, true, Commentf("%d == %d", ragnarokHeight, 1024))
}

func (vts *ValidatorMgrTestSuite) TestLowerVersion(c *C) {
	ctx, mgr := setupManagerForTest(c)

	mgr.Keeper().SetMimir(ctx, constants.DesiredValidatorSet.String(), 12)
	mgr.Keeper().SetMimir(ctx, constants.ArtificialRagnarokBlockHeight.String(), 1024)
	mgr.Keeper().SetMimir(ctx, constants.ChurnInterval.String(), 256)
	mgr.Keeper().SetMimir(ctx, constants.AsgardSize.String(), 30)
	mgr.Keeper().SetMimir(ctx, constants.MaxNodeToChurnOutForLowVersion.String(), 3)

	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)
	c.Assert(networkMgr.markLowVersionValidators(ctx), IsNil)

	for i := 0; i < 12; i++ {
		activeNode := GetRandomValidatorNode(NodeActive)
		activeNode.Version = "0.5.0"
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode), IsNil)
	}

	// Add 4 low version nodes
	activeNode1 := GetRandomValidatorNode(NodeActive)
	activeNode1.Version = "0.4.0"
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode1), IsNil)

	activeNode2 := GetRandomValidatorNode(NodeActive)
	activeNode2.Version = "0.4.0"
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode2), IsNil)

	activeNode3 := GetRandomValidatorNode(NodeActive)
	activeNode3.Version = "0.4.0"
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode3), IsNil)

	activeNode4 := GetRandomValidatorNode(NodeActive)
	activeNode4.Version = "0.4.0"
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode4), IsNil)

	lowVersionAddresses := []common.Address{activeNode1.BondAddress, activeNode2.BondAddress, activeNode3.BondAddress, activeNode4.BondAddress}
	markedCount := 0

	// Not more than ChurnOutForLowVersionBlocks (default 21600), so should mark 0 of the low version validators
	c.Assert(networkMgr.markLowVersionValidators(ctx), IsNil)
	activeNas, err := networkMgr.k.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	for _, na := range activeNas {

		isCorrectNode := false

		for _, addr := range lowVersionAddresses {
			if addr == na.BondAddress {
				isCorrectNode = true
				break
			}
		}

		if na.LeaveScore > 0 && isCorrectNode {
			markedCount++
		}
	}
	c.Assert(markedCount, Equals, 0)

	ctx = ctx.WithBlockHeight(30000)

	// Now more than ChurnOutForLowVersionBlocks (default 21600), so should mark 3 of the low version validators
	c.Assert(networkMgr.markLowVersionValidators(ctx), IsNil)
	activeNas, err = networkMgr.k.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	for _, na := range activeNas {

		isCorrectNode := false

		for _, addr := range lowVersionAddresses {
			if addr == na.BondAddress {
				isCorrectNode = true
				break
			}
		}

		if na.LeaveScore > 0 && isCorrectNode {
			markedCount++
		}
	}
	c.Assert(markedCount, Equals, 3)
}

func (vts *ValidatorMgrTestSuite) TestBadActors(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	// no bad actors with active node accounts
	nas, err := networkMgr.findBadActors(ctx, 0, 3)
	c.Assert(err, IsNil)
	c.Assert(nas, HasLen, 0)

	activeNode := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode), IsNil)

	// no bad actors with active node accounts with no slash points
	nas, err = networkMgr.findBadActors(ctx, 0, 3)
	c.Assert(err, IsNil)
	c.Assert(nas, HasLen, 0)

	activeNode = GetRandomValidatorNode(NodeActive)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, activeNode.NodeAddress, 250)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode), IsNil)
	activeNode = GetRandomValidatorNode(NodeActive)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, activeNode.NodeAddress, 500)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode), IsNil)

	// finds the worse actor
	nas, err = networkMgr.findBadActors(ctx, 0, 3)
	c.Assert(err, IsNil)
	c.Assert(nas, HasLen, 1)
	c.Check(nas[0].NodeAddress.Equals(activeNode.NodeAddress), Equals, true)

	// create really bad actors (crossing the redline)
	bad1 := GetRandomValidatorNode(NodeActive)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, bad1.NodeAddress, 5000)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, bad1), IsNil)
	bad2 := GetRandomValidatorNode(NodeActive)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, bad2.NodeAddress, 5000)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, bad2), IsNil)

	nas, err = networkMgr.findBadActors(ctx, 0, 3)
	c.Assert(err, IsNil)
	c.Assert(nas, HasLen, 2, Commentf("%d", len(nas)))

	// inconsistent order, workaround
	var count int
	for _, bad := range nas {
		if bad.Equals(bad1) || bad.Equals(bad2) {
			count++
		}
	}
	c.Check(count, Equals, 2)
}

func (vts *ValidatorMgrTestSuite) TestFindBadActors(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	activeNode := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode), IsNil)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, activeNode.NodeAddress, 50)
	nodeAccounts, err := networkMgr.findBadActors(ctx, 100, 3)
	c.Assert(err, IsNil)
	c.Assert(nodeAccounts, HasLen, 0)

	activeNode1 := GetRandomValidatorNode(NodeActive)
	activeNode1.StatusSince = 900
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode1), IsNil)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, activeNode1.NodeAddress, 200)

	// findBadActor assumes it is being called during a churn now,
	// so this should now mark this node as bad.
	nodeAccounts, err = networkMgr.findBadActors(ctx, 100, 3)
	c.Assert(err, IsNil)
	c.Assert(nodeAccounts, HasLen, 1)
	c.Assert(nodeAccounts.Contains(activeNode1), Equals, true)

	activeNode2 := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode2), IsNil)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, activeNode2.NodeAddress, 2000)

	activeNode3 := GetRandomValidatorNode(NodeActive)
	activeNode3.StatusSince = 1000
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode3), IsNil)
	mgr.Keeper().SetNodeAccountSlashPoints(ctx, activeNode3.NodeAddress, 2000)
	ctx = ctx.WithBlockHeight(2000)
	// node 3 and node 2 should both be marked even though node 3 is newer
	// (this is because we're not favoring older nodes anymore)
	nodeAccounts, err = networkMgr.findBadActors(ctx, 100, 3)
	c.Assert(err, IsNil)
	c.Assert(nodeAccounts, HasLen, 2)
	c.Assert(nodeAccounts.Contains(activeNode2), Equals, true)
	c.Assert(nodeAccounts.Contains(activeNode3), Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestFindLowBondActor(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(10)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	na, err := networkMgr.findLowBondActor(ctx)
	c.Assert(err, IsNil)
	c.Assert(na.IsEmpty(), Equals, false)
	c.Assert(na.Bond.Uint64(), Equals, uint64(10))

	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(9)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na2), IsNil)

	na, err = networkMgr.findLowBondActor(ctx)
	c.Assert(err, IsNil)
	c.Assert(na.Bond.Uint64(), Equals, uint64(9))

	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.ZeroUint()
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na3), IsNil)

	na, err = networkMgr.findLowBondActor(ctx)
	c.Assert(err, IsNil)
	c.Assert(na.Bond.IsZero(), Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestRagnarokBond(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	err := networkMgr.setupValidatorNodes(ctx, 0)
	c.Assert(err, IsNil)

	activeNode := GetRandomValidatorNode(NodeActive)
	activeNode.Bond = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode), IsNil)

	disabledNode := GetRandomValidatorNode(NodeDisabled)
	disabledNode.Bond = cosmos.ZeroUint()
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, disabledNode), IsNil)

	// no unbonding for first 10
	c.Assert(networkMgr.ragnarokBond(ctx, 1, mgr), IsNil)
	activeNode, err = mgr.Keeper().GetNodeAccount(ctx, activeNode.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(activeNode.Bond.Equal(cosmos.NewUint(100)), Equals, true)

	c.Assert(networkMgr.ragnarokBond(ctx, 11, mgr), IsNil)
	activeNode, err = mgr.Keeper().GetNodeAccount(ctx, activeNode.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(activeNode.Bond.Equal(cosmos.NewUint(90)), Equals, true)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0, Commentf("Len %d", items))
	mgr.TxOutStore().ClearOutboundItems(ctx)

	c.Assert(networkMgr.ragnarokBond(ctx, 12, mgr), IsNil)
	activeNode, err = mgr.Keeper().GetNodeAccount(ctx, activeNode.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(activeNode.Bond.Equal(cosmos.NewUint(72)), Equals, true)
	items, err = mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0, Commentf("Len %d", items))
}

func (vts *ValidatorMgrTestSuite) TestGetChangedNodes(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	err := networkMgr.setupValidatorNodes(ctx, 0)
	c.Assert(err, IsNil)

	activeNode := GetRandomValidatorNode(NodeActive)
	activeNode.Bond = cosmos.NewUint(100)
	activeNode.ForcedToLeave = true
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode), IsNil)

	disabledNode := GetRandomValidatorNode(NodeDisabled)
	disabledNode.Bond = cosmos.ZeroUint()
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, disabledNode), IsNil)

	vault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	vault.Membership = append(vault.Membership, activeNode.PubKeySet.Secp256k1.String())
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	newNodes, removedNodes, err := networkMgr.getChangedNodes(ctx, NodeAccounts{activeNode})
	c.Assert(err, IsNil)
	c.Assert(newNodes, HasLen, 0)
	c.Assert(removedNodes, HasLen, 1)
}

func (vts *ValidatorMgrTestSuite) TestSplitNext(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgr()
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	nas := make(NodeAccounts, 0)
	for i := 0; i < 90; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(uint64(i))
		nas = append(nas, na)
	}
	sets := networkMgr.splitNext(ctx, nas, 30)
	c.Assert(sets, HasLen, 3)
	c.Assert(sets[0], HasLen, 30)
	c.Assert(sets[1], HasLen, 30)
	c.Assert(sets[2], HasLen, 30)

	nas = make(NodeAccounts, 0)
	for i := 0; i < 100; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(uint64(i))
		nas = append(nas, na)
	}
	sets = networkMgr.splitNext(ctx, nas, 30)
	c.Assert(sets, HasLen, 4)
	c.Assert(sets[0], HasLen, 25)
	c.Assert(sets[1], HasLen, 25)
	c.Assert(sets[2], HasLen, 25)
	c.Assert(sets[3], HasLen, 25)

	nas = make(NodeAccounts, 0)
	for i := 0; i < 3; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(uint64(i))
		nas = append(nas, na)
	}
	sets = networkMgr.splitNext(ctx, nas, 30)
	c.Assert(sets, HasLen, 1)
	c.Assert(sets[0], HasLen, 3)
}

func (s *ValidatorMgrTestSuite) TestSplitNext_EmptyNodeAccounts(c *C) {
	var vm ValidatorMgr
	ctx, _ := setupKeeperForTest(c)

	groups := vm.splitNext(ctx, NodeAccounts{}, 2)

	c.Assert(len(groups), Equals, 0)
}

func (s *ValidatorMgrTestSuite) TestSplitNext_SingleNodeAccount(c *C) {
	var vm ValidatorMgr
	ctx, _ := setupKeeperForTest(c)

	nodeAcc := NodeAccount{NodeAddress: types.AccAddress("addr1"), Bond: sdkmath.NewUint(50), BondAddress: common.Address("bondAddr1")}

	groups := vm.splitNext(ctx, NodeAccounts{nodeAcc}, 1)

	// no asgard should have a length of 1
	c.Assert(len(groups), Equals, 0)
}

func (s *ValidatorMgrTestSuite) TestSplitNext_AllSameBondAddress(c *C) {
	var vm ValidatorMgr
	ctx, _ := setupKeeperForTest(c)

	nodeAcc1 := NodeAccount{NodeAddress: types.AccAddress("addr1"), Bond: sdkmath.NewUint(50), BondAddress: common.Address("bondAddr1")}
	nodeAcc2 := NodeAccount{NodeAddress: types.AccAddress("addr2"), Bond: sdkmath.NewUint(60), BondAddress: common.Address("bondAddr1")}
	nodeAcc3 := NodeAccount{NodeAddress: types.AccAddress("addr3"), Bond: sdkmath.NewUint(70), BondAddress: common.Address("bondAddr1")}
	nodeAcc4 := NodeAccount{NodeAddress: types.AccAddress("addr4"), Bond: sdkmath.NewUint(80), BondAddress: common.Address("bondAddr1")}

	groups := vm.splitNext(ctx, NodeAccounts{nodeAcc1, nodeAcc2, nodeAcc3, nodeAcc4}, 2)

	c.Assert(len(groups), Equals, 2)
	c.Assert(len(groups[0]), Equals, 2)
	c.Assert(len(groups[1]), Equals, 2)

	expectedGroup1 := NodeAccounts{nodeAcc4, nodeAcc2}
	expectedGroup2 := NodeAccounts{nodeAcc3, nodeAcc1}

	c.Assert(groups[0], DeepEquals, expectedGroup1)
	c.Assert(groups[1], DeepEquals, expectedGroup2)
}

func (s *ValidatorMgrTestSuite) TestSplitNext_DifferentBondAddresses(c *C) {
	var vm ValidatorMgr
	ctx, _ := setupKeeperForTest(c)

	nodeAcc1 := NodeAccount{NodeAddress: types.AccAddress("addr1"), Bond: sdkmath.NewUint(50), BondAddress: common.Address("bondAddr1")}
	nodeAcc2 := NodeAccount{NodeAddress: types.AccAddress("addr2"), Bond: sdkmath.NewUint(60), BondAddress: common.Address("bondAddr2")}
	nodeAcc3 := NodeAccount{NodeAddress: types.AccAddress("addr3"), Bond: sdkmath.NewUint(70), BondAddress: common.Address("bondAddr3")}
	nodeAcc4 := NodeAccount{NodeAddress: types.AccAddress("addr4"), Bond: sdkmath.NewUint(80), BondAddress: common.Address("bondAddr4")}

	groups := vm.splitNext(ctx, NodeAccounts{nodeAcc1, nodeAcc2, nodeAcc3, nodeAcc4}, 2)

	c.Assert(len(groups), Equals, 2)
	c.Assert(len(groups[0]), Equals, 2)
	c.Assert(len(groups[1]), Equals, 2)

	expectedGroup1 := NodeAccounts{nodeAcc1, nodeAcc3}
	expectedGroup2 := NodeAccounts{nodeAcc2, nodeAcc4}

	c.Assert(groups[0], DeepEquals, expectedGroup1)
	c.Assert(groups[1], DeepEquals, expectedGroup2)
}

func (s *ValidatorMgrTestSuite) TestSplitNext_SameBondSameAddress(c *C) {
	var vm ValidatorMgr
	ctx, _ := setupKeeperForTest(c)

	nodeAcc1 := NodeAccount{NodeAddress: types.AccAddress("addr1"), Bond: sdkmath.NewUint(50), BondAddress: common.Address("bondAddr1")}
	nodeAcc2 := NodeAccount{NodeAddress: types.AccAddress("addr2"), Bond: sdkmath.NewUint(50), BondAddress: common.Address("bondAddr1")}

	groups := vm.splitNext(ctx, NodeAccounts{nodeAcc1, nodeAcc2}, 2)

	c.Assert(len(groups), Equals, 1)
	c.Assert(len(groups[0]), Equals, 2)

	expectedGroup1 := NodeAccounts{nodeAcc1, nodeAcc2}

	c.Assert(groups[0], DeepEquals, expectedGroup1)
}

func (s *ValidatorMgrTestSuite) TestSplitNext_ZeroAsgardSize(c *C) {
	var vm ValidatorMgr
	ctx, _ := setupKeeperForTest(c)

	nodeAcc := NodeAccount{NodeAddress: types.AccAddress("addr1"), Bond: sdkmath.NewUint(50), BondAddress: common.Address("bondAddr1")}

	groups := vm.splitNext(ctx, NodeAccounts{nodeAcc}, 0)

	c.Assert(len(groups), Equals, 0)
}

func (s *ValidatorMgrTestSuite) TestSplitNext_NegativeAsgardSize(c *C) {
	var vm ValidatorMgr
	ctx, _ := setupKeeperForTest(c)

	nodeAcc := NodeAccount{NodeAddress: types.AccAddress("addr1"), Bond: sdkmath.NewUint(50), BondAddress: common.Address("bondAddr1")}

	groups := vm.splitNext(ctx, NodeAccounts{nodeAcc}, -1)

	c.Assert(len(groups), Equals, 0)
}

func (s *ValidatorMgrTestSuite) TestSplitNext_AsgardSizeLargerThanLen(c *C) {
	var vm ValidatorMgr
	ctx, _ := setupKeeperForTest(c)

	nodeAcc1 := NodeAccount{NodeAddress: types.AccAddress("addr1"), Bond: sdkmath.NewUint(50), BondAddress: common.Address("bondAddr1")}
	nodeAcc2 := NodeAccount{NodeAddress: types.AccAddress("addr2"), Bond: sdkmath.NewUint(60), BondAddress: common.Address("bondAddr1")}

	groups := vm.splitNext(ctx, NodeAccounts{nodeAcc1, nodeAcc2}, 3)

	c.Assert(len(groups), Equals, 1)

	expectedGroup1 := NodeAccounts{nodeAcc2, nodeAcc1}

	c.Assert(groups[0], DeepEquals, expectedGroup1)
}

func (s *ValidatorMgrTestSuite) TestSplitNext_MultipleDuplicates(c *C) {
	var vm ValidatorMgr
	ctx, _ := setupKeeperForTest(c)

	nodeAcc1 := NodeAccount{NodeAddress: types.AccAddress("addr1"), Bond: sdkmath.NewUint(50), BondAddress: common.Address("bondAddr1")}
	nodeAcc2 := NodeAccount{NodeAddress: types.AccAddress("addr2"), Bond: sdkmath.NewUint(60), BondAddress: common.Address("bondAddr1")}
	nodeAcc3 := NodeAccount{NodeAddress: types.AccAddress("addr3"), Bond: sdkmath.NewUint(70), BondAddress: common.Address("bondAddr2")}
	nodeAcc4 := NodeAccount{NodeAddress: types.AccAddress("addr4"), Bond: sdkmath.NewUint(80), BondAddress: common.Address("bondAddr2")}

	groups := vm.splitNext(ctx, NodeAccounts{nodeAcc1, nodeAcc2, nodeAcc3, nodeAcc4}, 2)

	c.Assert(len(groups), Equals, 2)
	c.Assert(len(groups[0]), Equals, 2)
	c.Assert(len(groups[1]), Equals, 2)

	expectedGroup1 := NodeAccounts{nodeAcc2, nodeAcc4}
	expectedGroup2 := NodeAccounts{nodeAcc1, nodeAcc3}

	c.Assert(groups[0], DeepEquals, expectedGroup1)
	c.Assert(groups[1], DeepEquals, expectedGroup2)
}

func (s *ValidatorMgrTestSuite) TestSplitNext_MultipleDuplicatesOfDiffSizes(c *C) {
	var vm ValidatorMgr
	ctx, _ := setupKeeperForTest(c)

	nodeAcc1 := NodeAccount{NodeAddress: types.AccAddress("addr1"), Bond: sdkmath.NewUint(200), BondAddress: common.Address("bondAddr0")}
	nodeAcc2 := NodeAccount{NodeAddress: types.AccAddress("addr2"), Bond: sdkmath.NewUint(60), BondAddress: common.Address("bondAddr1")}
	nodeAcc3 := NodeAccount{NodeAddress: types.AccAddress("addr3"), Bond: sdkmath.NewUint(70), BondAddress: common.Address("bondAddr2")}
	nodeAcc4 := NodeAccount{NodeAddress: types.AccAddress("addr4"), Bond: sdkmath.NewUint(80), BondAddress: common.Address("bondAddr2")}
	nodeAcc5 := NodeAccount{NodeAddress: types.AccAddress("addr5"), Bond: sdkmath.NewUint(85), BondAddress: common.Address("bondAddr3")}
	nodeAcc6 := NodeAccount{NodeAddress: types.AccAddress("addr6"), Bond: sdkmath.NewUint(100), BondAddress: common.Address("bondAddr3")}
	nodeAcc7 := NodeAccount{NodeAddress: types.AccAddress("addr7"), Bond: sdkmath.NewUint(90), BondAddress: common.Address("bondAddr3")}

	groups := vm.splitNext(ctx, NodeAccounts{nodeAcc7, nodeAcc3, nodeAcc2, nodeAcc4, nodeAcc5, nodeAcc6, nodeAcc1}, 4)

	c.Assert(len(groups), Equals, 2)
	c.Assert(len(groups[0]), Equals, 4)
	c.Assert(len(groups[1]), Equals, 3)

	expectedGroup1 := NodeAccounts{nodeAcc6, nodeAcc5, nodeAcc3, nodeAcc1}
	expectedGroup2 := NodeAccounts{nodeAcc7, nodeAcc4, nodeAcc2}

	c.Assert(groups[0], DeepEquals, expectedGroup1)
	c.Assert(groups[1], DeepEquals, expectedGroup2)
}

func (vts *ValidatorMgrTestSuite) TestFindCounToRemove(c *C) {
	// remove one
	c.Check(findCountToRemove(NodeAccounts{
		NodeAccount{LeaveScore: 12},
		NodeAccount{},
		NodeAccount{},
		NodeAccount{},
		NodeAccount{},
	}), Equals, 1)

	// don't remove one
	c.Check(findCountToRemove(NodeAccounts{
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{},
		NodeAccount{},
	}), Equals, 0)

	// remove one because of request to leave
	c.Check(findCountToRemove(NodeAccounts{
		NodeAccount{LeaveScore: 12, RequestedToLeave: true},
		NodeAccount{},
		NodeAccount{},
		NodeAccount{},
	}), Equals, 1)

	// remove one because of banned
	c.Check(findCountToRemove(NodeAccounts{
		NodeAccount{LeaveScore: 12, ForcedToLeave: true},
		NodeAccount{},
		NodeAccount{},
		NodeAccount{},
	}), Equals, 1)

	// don't remove more than 1/3rd of node accounts
	c.Check(findCountToRemove(NodeAccounts{
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
		NodeAccount{LeaveScore: 12},
	}), Equals, 3)
}

func (vts *ValidatorMgrTestSuite) TestFindMaxAbleToLeave(c *C) {
	c.Check(findMaxAbleToLeave(-1), Equals, 0)
	c.Check(findMaxAbleToLeave(0), Equals, 0)
	c.Check(findMaxAbleToLeave(1), Equals, 0)
	c.Check(findMaxAbleToLeave(2), Equals, 0)
	c.Check(findMaxAbleToLeave(3), Equals, 0)
	c.Check(findMaxAbleToLeave(4), Equals, 0)

	c.Check(findMaxAbleToLeave(5), Equals, 1)
	c.Check(findMaxAbleToLeave(6), Equals, 1)
	c.Check(findMaxAbleToLeave(7), Equals, 2)
	c.Check(findMaxAbleToLeave(8), Equals, 2)
	c.Check(findMaxAbleToLeave(9), Equals, 2)
	c.Check(findMaxAbleToLeave(10), Equals, 3)
	c.Check(findMaxAbleToLeave(11), Equals, 3)
	c.Check(findMaxAbleToLeave(12), Equals, 3)
}

func (vts *ValidatorMgrTestSuite) TestFindNextVaultNodeAccounts(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(validatorMgr, NotNil)
	nas := NodeAccounts{}
	for i := 0; i < 12; i++ {
		na := GetRandomValidatorNode(NodeActive)
		nas = append(nas, na)
	}
	nas[0].LeaveScore = 1024
	k.SetNodeAccountSlashPoints(ctx, nas[0].NodeAddress, 50)
	nas[1].LeaveScore = 1025
	k.SetNodeAccountSlashPoints(ctx, nas[1].NodeAddress, 200)
	nas[2].ForcedToLeave = true
	nas[3].RequestedToLeave = true
	for _, item := range nas {
		c.Assert(k.SetNodeAccount(ctx, item), IsNil)
	}
	err := validatorMgr.markReadyActors(ctx)
	c.Assert(err, IsNil)
	nasAfter, rotate, err := validatorMgr.nextVaultNodeAccounts(ctx, 12)
	c.Assert(err, IsNil)
	c.Assert(rotate, Equals, true)
	c.Assert(nasAfter, HasLen, 10)
}

func (vts *ValidatorMgrTestSuite) TestFindNextVaultNodeAccountsMax(c *C) {
	// test that we don't exceed the targetCount
	ctx, mgr := setupManagerForTest(c)
	validatorMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(validatorMgr, NotNil)
	// create active nodes
	for i := 0; i < 12; i++ {
		na := GetRandomValidatorNode(NodeActive)
		if i < 3 {
			na.LeaveScore = 1024
		}
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
	// create standby nodes
	for i := 0; i < 12; i++ {
		na := GetRandomValidatorNode(NodeStandby)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
	err := validatorMgr.markReadyActors(ctx)
	c.Assert(err, IsNil)
	nasAfter, rotate, err := validatorMgr.nextVaultNodeAccounts(ctx, 12)
	c.Assert(err, IsNil)
	c.Assert(rotate, Equals, true)
	c.Assert(nasAfter, HasLen, 12, Commentf("%d", len(nasAfter)))
}

func (vts *ValidatorMgrTestSuite) TestFindNextVaultNodeAccountsBFT(c *C) {
	// ensure that we don't lose BFT when more nodes will churn out that nodes
	// that can churn in
	ctx, mgr := setupManagerForTest(c)
	validatorMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(validatorMgr, NotNil)
	// create active nodes
	for i := 0; i < 4; i++ {
		na := GetRandomValidatorNode(NodeActive)
		if i < 3 {
			na.LeaveScore = 1024
		}
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
	// create standby nodes
	for i := 0; i < 2; i++ {
		na := GetRandomValidatorNode(NodeReady)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
	err := validatorMgr.markReadyActors(ctx)
	c.Assert(err, IsNil)
	nasAfter, rotate, err := validatorMgr.nextVaultNodeAccounts(ctx, 4)
	c.Assert(err, IsNil)
	c.Assert(rotate, Equals, false)
	c.Assert(nasAfter, HasLen, 4, Commentf("%d", len(nasAfter)))
}

func (vts *ValidatorMgrTestSuite) TestWeightedBondReward(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(4 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na1), IsNil)

	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(3 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na2), IsNil)

	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.NewUint(2 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na3), IsNil)

	network, _ := networkMgr.k.GetNetwork(ctx)
	network.BondRewardRune = cosmos.NewUint(1 * common.One)
	c.Assert(mgr.Keeper().SetNetwork(ctx, network), IsNil)

	// Fund Bond Module so there is enough reward to transfer to the node operators
	FundModule(c, ctx, mgr.Keeper(), BondName, 10*common.One)

	// pay out bond rewards
	c.Assert(networkMgr.distributeBondReward(ctx, mgr), IsNil)

	// As the node operators are the only bond providers, the NodeOperatorFee (5%)
	// portion of the full reward is transferred to their addresses
	nodeOperator1Balance := mgr.Keeper().GetBalance(ctx, na1.NodeAddress)
	nodeOperator2Balance := mgr.Keeper().GetBalance(ctx, na2.NodeAddress)
	nodeOperator3Balance := mgr.Keeper().GetBalance(ctx, na3.NodeAddress)

	// The bond hard cap in the test environment is 3 * common.One, both na1 and na2 should have the same reward (3/8ths each)
	// (5% of 37500000 is 1875000)
	c.Check(nodeOperator1Balance.AmountOf(common.RuneNative.Native()).String(), Equals, strconv.FormatInt(1875000, 10))
	c.Check(nodeOperator2Balance.AmountOf(common.RuneNative.Native()).String(), Equals, strconv.FormatInt(1875000, 10))

	// na3.Bond is below the hard cap, it should have a smaller reward accordingly (2/8ths)
	// (5% of 25000000 is 1250000)
	c.Check(nodeOperator3Balance.AmountOf(common.RuneNative.Native()).String(), Equals, strconv.FormatInt(1250000, 10))
}

func (vts *ValidatorMgrTestSuite) TestNodeAccountPreflightCheckMaintenance(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	mgr := NewDummyMgrWithKeeper(k)

	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(validatorMgr, NotNil)

	// Test with a valid node account
	nodeAccount := GetRandomValidatorNode(NodeActive)
	nodeAccount.Bond = cosmos.NewUint(10 * common.One) // well above min bond
	nodeAccount.PubKeySet = GetRandomPubKeySet()
	nodeAccount.Version = constants.SWVersion.String()
	nodeAccount.IPAddress = "192.168.0.1"

	// Add the node account to the store
	c.Assert(k.SetNodeAccount(ctx, nodeAccount), IsNil)

	// Set minimum bond
	k.SetMimir(ctx, constants.MinimumBondInRune.String(), 1000000)

	// Test normal operation first (should be Ready)
	status, err := validatorMgr.NodeAccountPreflightCheck(ctx, nodeAccount, k.GetConstants())
	c.Assert(err, IsNil)
	c.Assert(status, Equals, NodeReady)

	// Test with maintenance mode enabled
	nodeAccount.Maintenance = true
	c.Assert(k.SetNodeAccount(ctx, nodeAccount), IsNil)

	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, nodeAccount, k.GetConstants())
	c.Assert(status, Equals, NodeStandby)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "node account is in maintenance mode")

	// Test with maintenance mode disabled again
	nodeAccount.Maintenance = false
	c.Assert(k.SetNodeAccount(ctx, nodeAccount), IsNil)

	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, nodeAccount, k.GetConstants())
	c.Assert(err, IsNil)
	c.Assert(status, Equals, NodeReady)

	// Test with other failure conditions while in maintenance

	// Test with maintenance mode + forced to leave
	nodeAccount.Maintenance = true
	nodeAccount.ForcedToLeave = true
	c.Assert(k.SetNodeAccount(ctx, nodeAccount), IsNil)

	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, nodeAccount, k.GetConstants())
	c.Assert(status, Equals, NodeDisabled)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "node account has been banned")

	// Reset forced to leave
	nodeAccount.ForcedToLeave = false
	c.Assert(k.SetNodeAccount(ctx, nodeAccount), IsNil)

	// Test with maintenance mode + requested to leave
	nodeAccount.RequestedToLeave = true
	c.Assert(k.SetNodeAccount(ctx, nodeAccount), IsNil)

	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, nodeAccount, k.GetConstants())
	c.Assert(status, Equals, NodeStandby)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "node account has requested to leave")

	// Maintenance mode error should be masked by more severe errors
}

func (vts *ValidatorMgrTestSuite) TestActiveNodeRequestToLeaveShouldBeStandby(c *C) {
	var err error
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(10)

	// create active asgard vault
	asgard := GetRandomVault()
	c.Assert(mgr.Keeper().SetVault(ctx, asgard), IsNil)

	// Add bonders/validators
	bonderCount := 4
	for i := 1; i <= bonderCount; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.ActiveBlockHeight = 5
		na.Bond = cosmos.NewUint(1_000_000 * uint64(i) * common.One)
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

		// Add bond to asgard
		asgard.AddFunds(common.Coins{
			common.NewCoin(common.RuneAsset(), na.Bond),
		})
		asgard.Membership = append(asgard.Membership, na.PubKeySet.Secp256k1.String())
		c.Assert(mgr.Keeper().SetVault(ctx, asgard), IsNil)
	}
	// set one node request to leave
	nodeKey := asgard.Membership[0]
	nodePubKey, err := common.NewPubKey(nodeKey)
	c.Assert(err, IsNil)
	na, err := mgr.Keeper().GetNodeAccountByPubKey(ctx, nodePubKey)
	c.Assert(err, IsNil)
	na.RequestedToLeave = true
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	newAsgard := GetRandomVault()
	newAsgard.Type = AsgardVault
	newAsgard.Membership = asgard.Membership[1:]
	c.Assert(mgr.Keeper().SetVault(ctx, newAsgard), IsNil)
	c.Assert(mgr.NetworkMgr().RotateVault(ctx, newAsgard), IsNil)

	updates := mgr.ValidatorMgr().EndBlock(ctx, mgr)
	c.Assert(updates, NotNil)

	naAfter, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(naAfter.RequestedToLeave, Equals, false)
}

func (vts *ValidatorMgrTestSuite) TestMarkMissingActors(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	// Set MissingBlockChurnOut mimir
	missingBlocksThreshold := int64(10)
	mgr.Keeper().SetMimir(ctx, constants.MissingBlockChurnOut.String(), missingBlocksThreshold)

	// Create active nodes with different missing blocks counts
	activeNode1 := GetRandomValidatorNode(NodeActive)
	activeNode1.MissingBlocks = 5 // Below threshold
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode1), IsNil)

	activeNode2 := GetRandomValidatorNode(NodeActive)
	activeNode2.MissingBlocks = 15 // Above threshold
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode2), IsNil)

	activeNode3 := GetRandomValidatorNode(NodeActive)
	activeNode3.MissingBlocks = 20 // Above threshold but already marked for churn
	activeNode3.LeaveScore = 100
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode3), IsNil)

	// Run the function being tested
	err := networkMgr.markMissingActors(ctx)
	c.Assert(err, IsNil)

	// Check results
	node1After, err := mgr.Keeper().GetNodeAccount(ctx, activeNode1.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(node1After.LeaveScore, Equals, uint64(0), Commentf("Node with missing blocks below threshold should not be marked"))

	node2After, err := mgr.Keeper().GetNodeAccount(ctx, activeNode2.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(node2After.LeaveScore, Equals, uint64(100000000000), Commentf("Node with missing blocks above threshold should be marked, %d", node2After.LeaveScore))

	node3After, err := mgr.Keeper().GetNodeAccount(ctx, activeNode3.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(node3After.LeaveScore, Equals, uint64(100), Commentf("Node already marked for churn should keep its leave score"))
}

func (vts *ValidatorMgrTestSuite) TestMarkMissingActors_NoActiveNodes(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	// Set MissingBlockChurnOut mimir
	mgr.Keeper().SetMimir(ctx, constants.MissingBlockChurnOut.String(), 10)

	// Run with no active nodes
	err := networkMgr.markMissingActors(ctx)
	c.Assert(err, IsNil, Commentf("Should handle empty node list gracefully"))
}

func (vts *ValidatorMgrTestSuite) TestMarkMissingActors_EdgeCases(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	// Set MissingBlockChurnOut mimir to exactly match a node's missing blocks
	missingBlocksThreshold := int64(10)
	mgr.Keeper().SetMimir(ctx, constants.MissingBlockChurnOut.String(), missingBlocksThreshold)

	// Create node with exactly the threshold number of missing blocks
	activeNode := GetRandomValidatorNode(NodeActive)
	activeNode.MissingBlocks = uint64(missingBlocksThreshold)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode), IsNil)

	// Run the function
	err := networkMgr.markMissingActors(ctx)
	c.Assert(err, IsNil)

	// Check result - node should not be marked as the condition is > threshold, not >=
	nodeAfter, err := mgr.Keeper().GetNodeAccount(ctx, activeNode.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(nodeAfter.LeaveScore, Equals, uint64(0), Commentf("Node with missing blocks equal to threshold should not be marked"))
}

func (vts *ValidatorMgrTestSuite) TestMarkMissingActors_MimirDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	// Set MissingBlockChurnOut mimir to 0 (disabled)
	mgr.Keeper().SetMimir(ctx, constants.MissingBlockChurnOut.String(), 0)

	// Create node with missing blocks
	activeNode := GetRandomValidatorNode(NodeActive)
	activeNode.MissingBlocks = 100 // High number of missing blocks
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, activeNode), IsNil)

	// Run the function
	err := networkMgr.markMissingActors(ctx)
	c.Assert(err, IsNil)

	// Check result - node should not be marked when mimir is disabled
	nodeAfter, err := mgr.Keeper().GetNodeAccount(ctx, activeNode.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(nodeAfter.LeaveScore, Equals, uint64(0), Commentf("Node should not be marked when MissingBlockChurnOut is disabled"))
}

func (vts *ValidatorMgrTestSuite) TestPartitionByChainSupport_SinglePartition(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	networkMgrDummy := NewNetworkMgrDummy()
	vm := newValidatorMgr(k, networkMgrDummy, mgr.TxOutStore(), mgr.EventMgr())

	// All nodes with empty SupportedChains (legacy behavior) should partition into one group
	nas := make(NodeAccounts, 5)
	for i := 0; i < 5; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(uint64(100+i) * common.One)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		nas[i] = na
	}

	// Create an active vault so getNodeSupportedChains resolves chains from vaults
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain, common.ETHChain}.Strings(), []ChainContract{})
	for _, na := range nas {
		vault.Membership = append(vault.Membership, na.PubKeySet.Secp256k1.String())
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	partitions := vm.partitionByChainSupport(ctx, mgr, nas)
	c.Assert(len(partitions), Equals, 1)
	c.Assert(len(partitions[0].nodes), Equals, 5)
	// Vault chains (BTC, ETH) plus defensive THORChain inclusion.
	c.Assert(len(partitions[0].chains), Equals, 3)
	hasTHOR := false
	for _, ch := range partitions[0].chains {
		if ch.Equals(common.THORChain) {
			hasTHOR = true
			break
		}
	}
	c.Assert(hasTHOR, Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestPartitionByChainSupport_TwoPartitions(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	networkMgrDummy := NewNetworkMgrDummy()
	vm := newValidatorMgr(k, networkMgrDummy, mgr.TxOutStore(), mgr.EventMgr())

	// Group A: 3 nodes with BTC+ETH
	groupA := make(NodeAccounts, 3)
	for i := 0; i < 3; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.SupportedChains = []string{common.BTCChain.String(), common.ETHChain.String()}
		na.Bond = cosmos.NewUint(uint64(100+i) * common.One)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		groupA[i] = na
	}

	// Group B: 2 nodes with BTC only
	groupB := make(NodeAccounts, 2)
	for i := 0; i < 2; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.SupportedChains = []string{common.BTCChain.String()}
		na.Bond = cosmos.NewUint(uint64(200+i) * common.One)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		groupB[i] = na
	}

	all := make(NodeAccounts, 0, len(groupA)+len(groupB))
	all = append(all, groupA...)
	all = append(all, groupB...)
	partitions := vm.partitionByChainSupport(ctx, mgr, all)
	c.Assert(len(partitions), Equals, 2)

	// THORChain is force-included in every partition. Sort order by
	// comma-joined chain string: "BTC,ETH,THOR" < "BTC,THOR", so
	// the BTC+ETH partition sorts first.
	c.Assert(len(partitions[0].nodes), Equals, 3) // BTC+ETH
	c.Assert(len(partitions[0].chains), Equals, 3)

	c.Assert(len(partitions[1].nodes), Equals, 2) // BTC only (+ THORChain)
	c.Assert(len(partitions[1].chains), Equals, 2)
}

func (vts *ValidatorMgrTestSuite) TestChurnPartitionBelowMinimumExcluded(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	networkMgrDummy := NewNetworkMgrDummy()
	vm := newValidatorMgr(k, networkMgrDummy, mgr.TxOutStore(), mgr.EventMgr())

	// Set MinNodeChainSupport to 4 via mimir so partitions below this size are skipped.
	k.SetMimir(ctx, constants.MinNodeChainSupport.String(), 4)

	// Group A: 5 nodes with BTC+ETH — viable
	groupA := make(NodeAccounts, 5)
	for i := 0; i < 5; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.SupportedChains = []string{common.BTCChain.String(), common.ETHChain.String()}
		na.Bond = cosmos.NewUint(uint64(100+i) * common.One)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		groupA[i] = na
	}

	// Group B: 2 nodes with DOGE only — below MinNodeChainSupport
	groupB := make(NodeAccounts, 2)
	for i := 0; i < 2; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.SupportedChains = []string{common.DOGEChain.String()}
		na.Bond = cosmos.NewUint(uint64(200+i) * common.One)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		groupB[i] = na
	}

	all := make(NodeAccounts, 0, len(groupA)+len(groupB))
	all = append(all, groupA...)
	all = append(all, groupB...)
	minNodeChainSupport := k.GetConfigInt64(ctx, constants.MinNodeChainSupport)
	c.Assert(minNodeChainSupport, Equals, int64(4))

	partitions := vm.partitionByChainSupport(ctx, mgr, all)
	c.Assert(len(partitions), Equals, 2)

	// Simulate what churnInner does: filter out partitions below MinNodeChainSupport
	asgardSize := int64(40)
	triggeredCount := 0
	for _, p := range partitions {
		if int64(len(p.nodes)) < minNodeChainSupport {
			continue
		}
		groups := vm.splitNext(ctx, p.nodes, asgardSize)
		for _, group := range groups {
			_ = networkMgrDummy.TriggerKeygen(ctx, group, p.chains)
			triggeredCount++
		}
	}

	// Only the viable partition (5 nodes) should have triggered keygen
	c.Assert(triggeredCount, Equals, 1)
	c.Assert(len(networkMgrDummy.keygens), Equals, 1)
	c.Assert(len(networkMgrDummy.keygens[0]), Equals, 5)
	// BTC+ETH + defensive THORChain inclusion.
	c.Assert(len(networkMgrDummy.keygenChains[0]), Equals, 3)
}

func (vts *ValidatorMgrTestSuite) TestAutoRagnarokChainLostToOptOuts(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	eventMgr := newEventMgr()
	networkMgrDummy := NewNetworkMgrDummy()
	vm := newValidatorMgr(k, networkMgrDummy, mgr.TxOutStore(), eventMgr)

	// Create an active vault that includes DOGE chain, simulating a chain that
	// was previously supported but will lose support due to opt-outs.
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain, common.ETHChain, common.DOGEChain}.Strings(),
		[]ChainContract{})
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// Create a DOGE pool so we can verify it gets staged for ragnarok.
	dogePool := NewPool()
	dogePool.Asset = common.DOGEAsset
	dogePool.Status = PoolAvailable
	dogePool.BalanceRune = cosmos.NewUint(1000 * common.One)
	dogePool.BalanceAsset = cosmos.NewUint(5000 * common.One)
	c.Assert(k.SetPool(ctx, dogePool), IsNil)

	// 5 nodes all opt into BTC+ETH only (no DOGE support).
	nodes := make(NodeAccounts, 5)
	for i := 0; i < 5; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.SupportedChains = []string{common.BTCChain.String(), common.ETHChain.String()}
		na.Bond = cosmos.NewUint(uint64(100+i) * common.One)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		nodes[i] = na
	}

	ctx = ctx.WithBlockHeight(100)
	coveredChains := make(map[common.Chain]struct{})
	coveredChains[common.BTCChain] = struct{}{}
	coveredChains[common.ETHChain] = struct{}{}

	err := vm.autoRagnarokUncoveredChains(ctx, mgr, coveredChains)
	c.Assert(err, IsNil)

	// Verify halt mimir was set for DOGE.
	haltVal, err := k.GetMimir(ctx, "HaltDOGEChain")
	c.Assert(err, IsNil)
	c.Assert(haltVal, Equals, int64(100))

	// Verify pool was staged.
	pool, err := k.GetPool(ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.Status, Equals, PoolStaged)

	// Verify pool ragnarok start was set.
	ragStart, err := k.GetPoolRagnarokStart(ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Assert(ragStart, Equals, int64(100))

	// Verify chain_ragnarok_initiated event was emitted.
	found := false
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type == "chain_ragnarok_initiated" {
			found = true
			for _, attr := range evt.Attributes {
				if attr.Key == "chain" {
					c.Assert(attr.Value, Equals, common.DOGEChain.String())
				}
				if attr.Key == "reason" {
					c.Assert(attr.Value, Equals, "insufficient validators")
				}
			}
		}
	}
	c.Assert(found, Equals, true)

	// Verify BTC/ETH halt mimirs were NOT set.
	btcHalt, err := k.GetMimir(ctx, "HaltBTCChain")
	c.Assert(err, IsNil)
	c.Assert(btcHalt, Equals, int64(-1))
	ethHalt, err := k.GetMimir(ctx, "HaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(ethHalt, Equals, int64(-1))
}

func (vts *ValidatorMgrTestSuite) TestAutoRagnarokChainLostToNodeRemovals(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	eventMgr := newEventMgr()
	networkMgrDummy := NewNetworkMgrDummy()
	vm := newValidatorMgr(k, networkMgrDummy, mgr.TxOutStore(), eventMgr)

	// Active vault with BTC, ETH, and DOGE.
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain, common.ETHChain, common.DOGEChain}.Strings(),
		[]ChainContract{})
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// DOGE pool.
	dogePool := NewPool()
	dogePool.Asset = common.DOGEAsset
	dogePool.Status = PoolAvailable
	dogePool.BalanceRune = cosmos.NewUint(500 * common.One)
	dogePool.BalanceAsset = cosmos.NewUint(2500 * common.One)
	c.Assert(k.SetPool(ctx, dogePool), IsNil)

	// Scenario: only 2 nodes support DOGE (below MinNodeChainSupport when raised to 4),
	// while 5 nodes support BTC+ETH. The partition loop would have already
	// filtered out the DOGE partition, so coveredChains won't include DOGE.
	ctx = ctx.WithBlockHeight(200)
	coveredChains := make(map[common.Chain]struct{})
	coveredChains[common.BTCChain] = struct{}{}
	coveredChains[common.ETHChain] = struct{}{}

	err := vm.autoRagnarokUncoveredChains(ctx, mgr, coveredChains)
	c.Assert(err, IsNil)

	// Verify DOGE halt.
	haltVal, err := k.GetMimir(ctx, "HaltDOGEChain")
	c.Assert(err, IsNil)
	c.Assert(haltVal, Equals, int64(200))

	// Verify pool ragnarok.
	pool, err := k.GetPool(ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.Status, Equals, PoolStaged)

	ragStart, err := k.GetPoolRagnarokStart(ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Assert(ragStart, Equals, int64(200))

	// Verify event.
	found := false
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type == "chain_ragnarok_initiated" {
			found = true
		}
	}
	c.Assert(found, Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestDistributeChainBondRewardsSingleChain(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)
	mgr := NewDummyMgrWithKeeper(k)
	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)

	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.Membership = []string{na1.PubKeySet.Secp256k1.String()}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	FundModule(c, ctx, k, BondName, 100*common.One)

	c.Assert(k.AddChainIncome(ctx, common.BTCChain, cosmos.NewUint(10*common.One)), IsNil)

	c.Assert(vm.distributeChainBondRewards(ctx, mgr), IsNil)

	naAfter, err := k.GetNodeAccount(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	expectedBond := na1.Bond.Add(cosmos.NewUint(10 * common.One))
	nodeOperatorFee := common.GetSafeShare(cosmos.NewUint(500), cosmos.NewUint(10000), cosmos.NewUint(10*common.One))
	expectedBond = common.SafeSub(expectedBond, nodeOperatorFee)
	c.Assert(naAfter.Bond.String(), Equals, expectedBond.String())

	income, err := k.GetChainIncome(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(income.IsZero(), Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestDistributeChainBondRewardsMultiChain(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)
	mgr := NewDummyMgrWithKeeper(k)
	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)

	btcVault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	btcVault.Membership = []string{na1.PubKeySet.Secp256k1.String()}
	c.Assert(k.SetVault(ctx, btcVault), IsNil)

	ethVault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.ETHChain}.Strings(), []ChainContract{})
	ethVault.Membership = []string{na1.PubKeySet.Secp256k1.String()}
	c.Assert(k.SetVault(ctx, ethVault), IsNil)

	FundModule(c, ctx, k, BondName, 200*common.One)

	c.Assert(k.AddChainIncome(ctx, common.BTCChain, cosmos.NewUint(10*common.One)), IsNil)
	c.Assert(k.AddChainIncome(ctx, common.ETHChain, cosmos.NewUint(20*common.One)), IsNil)

	c.Assert(vm.distributeChainBondRewards(ctx, mgr), IsNil)

	naAfter, err := k.GetNodeAccount(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	totalChainReward := cosmos.NewUint(30 * common.One)
	expectedBond := na1.Bond.Add(totalChainReward)
	nodeOperatorFee := common.GetSafeShare(cosmos.NewUint(500), cosmos.NewUint(10000), totalChainReward)
	expectedBond = common.SafeSub(expectedBond, nodeOperatorFee)
	c.Assert(naAfter.Bond.String(), Equals, expectedBond.String())

	btcIncome, err := k.GetChainIncome(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(btcIncome.IsZero(), Equals, true)

	ethIncome, err := k.GetChainIncome(ctx, common.ETHChain)
	c.Assert(err, IsNil)
	c.Assert(ethIncome.IsZero(), Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestDistributeChainBondRewardsBondOverCap(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)
	mgr := NewDummyMgrWithKeeper(k)
	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(4 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)

	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(3 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)

	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.NewUint(2 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na3), IsNil)

	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.Membership = []string{
		na1.PubKeySet.Secp256k1.String(),
		na2.PubKeySet.Secp256k1.String(),
		na3.PubKeySet.Secp256k1.String(),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	FundModule(c, ctx, k, BondName, 100*common.One)

	chainReward := cosmos.NewUint(1 * common.One)
	c.Assert(k.AddChainIncome(ctx, common.BTCChain, chainReward), IsNil)

	c.Assert(vm.distributeChainBondRewards(ctx, mgr), IsNil)

	// Bond hard cap for 3 nodes: sort [2,3,4], 2/3*3=2, index 1 → cap=3
	// Effective bonds: min(4,3)=3, min(3,3)=3, min(2,3)=2. Total=8
	// na1 share: 3/8 * 1e8, na2 share: 3/8 * 1e8, na3 share: remainder (2/8)

	na1After, _ := k.GetNodeAccount(ctx, na1.NodeAddress)
	na2After, _ := k.GetNodeAccount(ctx, na2.NodeAddress)
	na3After, _ := k.GetNodeAccount(ctx, na3.NodeAddress)

	reward1 := common.GetUncappedShare(cosmos.NewUint(3), cosmos.NewUint(8), chainReward)
	reward2 := common.GetUncappedShare(cosmos.NewUint(3), cosmos.NewUint(8), chainReward)
	// na3 gets the remainder
	reward3 := common.SafeSub(chainReward, reward1.Add(reward2))

	fee1 := common.GetSafeShare(cosmos.NewUint(500), cosmos.NewUint(10000), reward1)
	fee2 := common.GetSafeShare(cosmos.NewUint(500), cosmos.NewUint(10000), reward2)
	fee3 := common.GetSafeShare(cosmos.NewUint(500), cosmos.NewUint(10000), reward3)

	c.Assert(na1After.Bond.String(), Equals, na1.Bond.Add(reward1).Sub(fee1).String())
	c.Assert(na2After.Bond.String(), Equals, na2.Bond.Add(reward2).Sub(fee2).String())
	c.Assert(na3After.Bond.String(), Equals, na3.Bond.Add(reward3).Sub(fee3).String())

	// na1 and na2 should have equal rewards (both capped at 3)
	c.Assert(reward1.String(), Equals, reward2.String())
	// na3 should have less
	c.Assert(reward3.LT(reward1), Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestDistributeChainBondRewardsPartialFailure(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)
	mgr := NewDummyMgrWithKeeper(k)
	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)

	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)

	// Pre-populate na2's bond providers with an unrelated address.
	// passiveBackfill skips when providers are non-empty, and payChainReward
	// fails because na2.BondAddress is not listed as a provider.
	otherAddr := GetRandomBech32Addr()
	bp2 := NewBondProviders(na2.NodeAddress)
	bp2.Providers = []BondProvider{NewBondProvider(otherAddr)}
	c.Assert(k.SetBondProviders(ctx, bp2), IsNil)

	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.Membership = []string{
		na1.PubKeySet.Secp256k1.String(),
		na2.PubKeySet.Secp256k1.String(),
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)

	FundModule(c, ctx, k, BondName, 200*common.One)

	chainReward := cosmos.NewUint(10 * common.One)
	c.Assert(k.AddChainIncome(ctx, common.BTCChain, chainReward), IsNil)

	networkBefore, err := k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	initialBondReward := networkBefore.BondRewardRune

	// With equal bonds, each node is allocated chainReward/2 = 5*common.One.
	// The failure on na2 leaves half the reward undistributed.
	expectedUndistributed := cosmos.NewUint(5 * common.One)

	c.Assert(vm.distributeChainBondRewards(ctx, mgr), IsNil)

	// na1 should have grown (received its share); na2 should be untouched.
	na1After, err := k.GetNodeAccount(ctx, na1.NodeAddress)
	c.Assert(err, IsNil)
	na2After, err := k.GetNodeAccount(ctx, na2.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(na1After.Bond.GT(na1.Bond), Equals, true, Commentf("na1 bond should have grown"))
	c.Assert(na2After.Bond.String(), Equals, na2.Bond.String(), Commentf("na2 bond should be unchanged"))

	// The undistributed share must be swept into the global BondRewardRune,
	// not silently lost.
	networkAfter, err := k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	c.Assert(
		networkAfter.BondRewardRune.String(),
		Equals,
		initialBondReward.Add(expectedUndistributed).String(),
	)

	// chain income is zeroed regardless of the partial failure
	income, err := k.GetChainIncome(ctx, common.BTCChain)
	c.Assert(err, IsNil)
	c.Assert(income.IsZero(), Equals, true)
}
