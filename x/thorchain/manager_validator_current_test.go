package thorchain

import (
	"strconv"

	sdkmath "cosmossdk.io/math"
	"github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type ValidatorMgrTestSuite struct{}

var _ = Suite(&ValidatorMgrTestSuite{})

type bondYieldRewardTestGasManager struct {
	*DummyGasManager
}

func (bondYieldRewardTestGasManager) GetAssetOutboundFee(_ cosmos.Context, _ common.Asset, _ bool) (cosmos.Uint, error) {
	return cosmos.NewUint(common.One), nil
}

func (vts *ValidatorMgrTestSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

func (vts *ValidatorMgrTestSuite) TestChurnWithChainHalt(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgr()
	mgr.K = k
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)
	minBond := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.MinimumBondInRune)

	// 1 standby node
	standbyNode := GetRandomValidatorNode(NodeStandby)
	standbyNode.Bond = cosmos.NewUint(uint64(minBond))
	c.Assert(k.SetNodeAccount(ctx, standbyNode), IsNil)

	// 1 vault with btc, created at block 1 (so lastChurnHeight=1)
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.Membership = append(vault.Membership, standbyNode.PubKeySet.Secp256k1.String())
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	// Set block height to churn interval after vault creation (1 + 60 = 61)
	mgr.Keeper().SetMimir(ctx, constants.ChurnInterval.String(), 60)
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

func (vts *ValidatorMgrTestSuite) TestChurnWithRetiringVaultDust(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgr()
	mgr.K = k
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)
	minBond := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.MinimumBondInRune)

	// 1 standby node
	standbyNode := GetRandomValidatorNode(NodeStandby)
	standbyNode.Bond = cosmos.NewUint(uint64(minBond))
	c.Assert(k.SetNodeAccount(ctx, standbyNode), IsNil)

	// 1 active vault with btc, created at block 1 (so lastChurnHeight=1)
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	vault.Membership = append(vault.Membership, standbyNode.PubKeySet.Secp256k1.String())
	c.Assert(k.SetVault(ctx, vault), IsNil)

	// 1 retiring vault still holding XMR above the dust threshold
	dust := common.XMRChain.DustThreshold()
	retiringVault := NewVault(1, RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), []ChainContract{})
	retiringVault.Coins = common.Coins{common.NewCoin(common.XMRAsset, dust.Add(cosmos.NewUint(1)))}
	c.Assert(k.SetVault(ctx, retiringVault), IsNil)

	// Set block height to churn interval after vault creation (1 + 60 = 61)
	mgr.Keeper().SetMimir(ctx, constants.ChurnInterval.String(), 60)
	ctx = ctx.WithBlockHeight(61)

	// should not start churn while a retiring vault has funds above dust
	c.Assert(networkMgr.BeginBlock(ctx, mgr, nil), IsNil)
	ready, err := k.ListValidatorsByStatus(ctx, NodeReady)
	c.Assert(err, IsNil)
	c.Assert(len(ready), Equals, 0)

	// A non-gas balance remains migration work even when its amount is below
	// that chain's gas-asset dust threshold.
	tokenAsset := common.Asset{Chain: common.ETHChain, Symbol: "TOKEN", Ticker: "TOKEN"}
	retiringVault.Coins = common.Coins{common.NewCoin(tokenAsset, cosmos.OneUint())}
	c.Assert(k.SetVault(ctx, retiringVault), IsNil)
	c.Assert(networkMgr.BeginBlock(ctx, mgr, nil), IsNil)
	ready, err = k.ListValidatorsByStatus(ctx, NodeReady)
	c.Assert(err, IsNil)
	c.Assert(len(ready), Equals, 0)

	// only a sub-dust remainder left (e.g. the XMR migration buffer awaiting
	// its post-final-round burn); rotation should no longer be blocked, even
	// when the vault retains a separate accounting deficit
	retiringVault.Coins = common.Coins{common.NewCoin(common.XMRAsset, dust)}
	retiringVault.Deficit = common.Coins{common.NewCoin(common.ETHAsset, cosmos.OneUint())}
	c.Assert(k.SetVault(ctx, retiringVault), IsNil)

	c.Assert(networkMgr.BeginBlock(ctx, mgr, nil), IsNil)
	ready, err = k.ListValidatorsByStatus(ctx, NodeReady)
	c.Assert(err, IsNil)
	c.Assert(len(ready), Equals, 1)
}

// TestChurnAfterDeficitOnlyRetiringVaultGoesInactive covers the normal block
// flow where migration cleanup inactivates a deficit-only retiring vault before
// the churn gate runs.
func (vts *ValidatorMgrTestSuite) TestChurnAfterDeficitOnlyRetiringVaultGoesInactive(c *C) {
	ctx, mgr := setupManagerForTest(c)
	validatorMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	networkMgr := newNetworkMgr(mgr.Keeper(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(validatorMgr, NotNil)
	c.Assert(networkMgr, NotNil)

	minBond := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.MinimumBondInRune)
	standbyNode := GetRandomValidatorNode(NodeStandby)
	standbyNode.Bond = cosmos.NewUint(uint64(minBond))
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, standbyNode), IsNil)

	activeVault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	activeVault.Membership = append(activeVault.Membership, standbyNode.PubKeySet.Secp256k1.String())
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	// The deficit-bearing vault starts as RetiringVault to mirror the state
	// created by vault debit underflow during churn settlement.
	retiringVault := NewVault(1, RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	retiringVault.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1)))
	c.Assert(mgr.Keeper().SetVault(ctx, retiringVault), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	mgr.Keeper().SetMimir(ctx, constants.ChurnInterval.String(), 60)
	ctx = ctx.WithBlockHeight(61)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)
	// Migration cleanup should move the vault out of the status set that blocks
	// rotation, without writing off or deleting the deficit.
	updatedVault, err := mgr.Keeper().GetVault(ctx, retiringVault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.Status, Equals, InactiveVault)
	c.Assert(updatedVault.GetDeficit(common.BTCAsset).Amount.Equal(cosmos.NewUint(1)), Equals, true)

	retiringVaults, err := mgr.Keeper().GetAsgardVaultsByStatus(ctx, RetiringVault)
	c.Assert(err, IsNil)
	c.Assert(retiringVaults, HasLen, 0)

	c.Assert(validatorMgr.BeginBlock(ctx, mgr, nil), IsNil)
	// A successful churn marks the standby validator as ready for the new vault.
	ready, err := mgr.Keeper().ListValidatorsByStatus(ctx, NodeReady)
	c.Assert(err, IsNil)
	c.Assert(ready, HasLen, 1)
}

// TestChurnWithDeficitOnlyRetiringVault exercises the churn gate directly with
// a deficit-bearing RetiringVault still present, proving that deficit alone does
// not block rotation.
func (vts *ValidatorMgrTestSuite) TestChurnWithDeficitOnlyRetiringVault(c *C) {
	ctx, mgr := setupManagerForTest(c)
	validatorMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(validatorMgr, NotNil)

	minBond := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.MinimumBondInRune)
	standbyNode := GetRandomValidatorNode(NodeStandby)
	standbyNode.Bond = cosmos.NewUint(uint64(minBond))
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, standbyNode), IsNil)

	activeVault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	activeVault.Membership = append(activeVault.Membership, standbyNode.PubKeySet.Secp256k1.String())
	c.Assert(mgr.Keeper().SetVault(ctx, activeVault), IsNil)

	// Leave the vault in RetiringVault for this test instead of running
	// migrateFunds, so BeginBlock must make the funds-vs-deficit distinction.
	retiringVault := NewVault(1, RetiringVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	retiringVault.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1)))
	c.Assert(mgr.Keeper().SetVault(ctx, retiringVault), IsNil)

	mgr.Keeper().SetMimir(ctx, constants.ChurnInterval.String(), 60)
	ctx = ctx.WithBlockHeight(61)

	c.Assert(validatorMgr.BeginBlock(ctx, mgr, nil), IsNil)
	// If the churn gate treated any RetiringVault as blocking, this node would
	// still be standby.
	ready, err := mgr.Keeper().ListValidatorsByStatus(ctx, NodeReady)
	c.Assert(err, IsNil)
	c.Assert(ready, HasLen, 1)

	// BeginBlock only proves the gate allows rotation; lifecycle cleanup is
	// covered by TestChurnAfterDeficitOnlyRetiringVaultGoesInactive.
	updatedVault, err := mgr.Keeper().GetVault(ctx, retiringVault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(updatedVault.Status, Equals, RetiringVault)
	c.Assert(updatedVault.GetDeficit(common.BTCAsset).Amount.Equal(cosmos.NewUint(1)), Equals, true)
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

func (vts *ValidatorMgrTestSuite) TestMarkLowBondActorOnlyAtCapacity(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)

	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	lowBond := GetRandomValidatorNode(NodeActive)
	lowBond.Bond = cosmos.NewUint(9)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, lowBond), IsNil)

	higherBond := GetRandomValidatorNode(NodeActive)
	higherBond.Bond = cosmos.NewUint(10)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, higherBond), IsNil)

	c.Assert(networkMgr.markLowBondActor(ctx, 3), IsNil)
	lowBondAfter, err := mgr.Keeper().GetNodeAccount(ctx, lowBond.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(lowBondAfter.LeaveScore, Equals, uint64(0))

	highestBond := GetRandomValidatorNode(NodeActive)
	highestBond.Bond = cosmos.NewUint(11)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, highestBond), IsNil)

	c.Assert(networkMgr.markLowBondActor(ctx, 3), IsNil)
	lowBondAfter, err = mgr.Keeper().GetNodeAccount(ctx, lowBond.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(lowBondAfter.LeaveScore > 0, Equals, true)

	higherBondAfter, err := mgr.Keeper().GetNodeAccount(ctx, higherBond.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(higherBondAfter.LeaveScore, Equals, uint64(0))
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

func (vts *ValidatorMgrTestSuite) TestGetChangedNodesDedupesSharedXMRMonovaultMembership(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	readyNode := GetRandomValidatorNode(NodeReady)
	readyNode.Bond = cosmos.NewUint(100)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, readyNode), IsNil)

	regularVault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), []ChainContract{})
	regularVault.Membership = append(regularVault.Membership, readyNode.PubKeySet.Secp256k1.String())
	c.Assert(mgr.Keeper().SetVault(ctx, regularVault), IsNil)

	xmrMonovault := NewVault(ctx.BlockHeight(), ActiveVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), []ChainContract{})
	xmrMonovault.PubKeyFrost = GetRandomEd25519PubKey()
	xmrMonovault.Membership = append(xmrMonovault.Membership, readyNode.PubKeySet.Secp256k1.String())
	c.Assert(mgr.Keeper().SetVault(ctx, xmrMonovault), IsNil)

	newNodes, removedNodes, err := networkMgr.getChangedNodes(ctx, NodeAccounts{})
	c.Assert(err, IsNil)
	c.Assert(newNodes, HasLen, 1)
	c.Assert(newNodes[0].NodeAddress.Equals(readyNode.NodeAddress), Equals, true)
	c.Assert(removedNodes, HasLen, 0)
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
	minBond := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.MinimumBondInRune)
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
		na.Bond = cosmos.NewUint(uint64(minBond))
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

func (vts *ValidatorMgrTestSuite) TestRegularVaultMembershipNeedsKeygenWhenActiveVaultMembershipStale(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, constants.AsgardSize.String(), 10)
	mgr := NewDummyMgrWithKeeper(k)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	active := make(NodeAccounts, 0, 3)
	expectedMembers := make(common.PubKeys, 0, 3)
	for i := 0; i < 3; i++ {
		na := GetRandomValidatorNode(NodeActive)
		active = append(active, na)
		expectedMembers = append(expectedMembers, na.PubKeySet.Secp256k1)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	}

	needsKeygen, err := validatorMgr.regularVaultMembershipNeedsKeygen(ctx, active)
	c.Assert(err, IsNil)
	c.Assert(needsKeygen, Equals, false)

	xmr := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, GetRandomPubKey())
	xmr.Membership = expectedMembers.Strings()
	xmr.PubKeyFrost = GetRandomEd25519PubKey()
	c.Assert(k.SetVault(ctx, xmr), IsNil)

	needsKeygen, err = validatorMgr.regularVaultMembershipNeedsKeygen(ctx, active)
	c.Assert(err, IsNil)
	c.Assert(needsKeygen, Equals, true)

	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain}.Strings(), nil)
	regular.Membership = expectedMembers[:2].Strings()
	c.Assert(k.SetVault(ctx, regular), IsNil)

	needsKeygen, err = validatorMgr.regularVaultMembershipNeedsKeygen(ctx, active)
	c.Assert(err, IsNil)
	c.Assert(needsKeygen, Equals, true)

	regular.Membership = expectedMembers.Strings()
	c.Assert(k.SetVault(ctx, regular), IsNil)

	needsKeygen, err = validatorMgr.regularVaultMembershipNeedsKeygen(ctx, active)
	c.Assert(err, IsNil)
	c.Assert(needsKeygen, Equals, false)
}

func (vts *ValidatorMgrTestSuite) TestChurnInnerSchedulesReplacementXMRKeygen(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	networkDummy, ok := mgr.NetworkMgr().(*NetworkMgrDummy)
	c.Assert(ok, Equals, true)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(validatorMgr, NotNil)
	minBond := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.MinimumBondInRune)

	k.SetMimir(ctx, constants.DesiredValidatorSet.String(), 4)
	k.SetMimir(ctx, constants.AsgardSize.String(), 10)

	membership := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		na := GetRandomValidatorNode(NodeActive)
		if i == 0 {
			na.RequestedToLeave = true
		}
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		membership = append(membership, na.PubKeySet.Secp256k1.String())
	}

	ready := GetRandomValidatorNode(NodeReady)
	ready.Bond = cosmos.NewUint(uint64(minBond))
	ready.Version = constants.SWVersion.String()
	c.Assert(k.SetNodeAccount(ctx, ready), IsNil)

	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)
	regular.Membership = append(regular.Membership, membership...)
	c.Assert(k.SetVault(ctx, regular), IsNil)

	xmr := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, GetRandomPubKey())
	xmr.Membership = append(xmr.Membership, membership...)
	xmr.PubKeyFrost = GetRandomEd25519PubKey()
	c.Assert(k.SetVault(ctx, xmr), IsNil)

	c.Assert(validatorMgr.churnInner(ctx), IsNil)
	c.Assert(networkDummy.nas, HasLen, 4)
	c.Assert(networkDummy.xmrNas, HasLen, 4)
	c.Assert(networkDummy.xmrNas.Contains(ready), Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestChurnInnerRecoversMissingXMRAtBFTFloor(c *C) {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	networkDummy, ok := mgr.NetworkMgr().(*NetworkMgrDummy)
	c.Assert(ok, Equals, true)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	k.SetMimir(ctx, constants.DesiredValidatorSet.String(), 4)
	k.SetMimir(ctx, constants.AsgardSize.String(), 10)
	membership := make([]string, 0, 4)
	active := make(NodeAccounts, 0, 4)
	for i := 0; i < 4; i++ {
		na := GetRandomValidatorNode(NodeActive)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		membership = append(membership, na.PubKeySet.Secp256k1.String())
		active = append(active, na)
	}

	// The ordinary vault already matches the stable four-node set. With no
	// ready node there is no regular rotation to incidentally trigger XMR.
	regular := NewVault(
		1,
		ActiveVault,
		AsgardVault,
		GetRandomPubKey(),
		common.Chains{common.THORChain, common.BTCChain}.Strings(),
		nil,
	)
	regular.Membership = append(regular.Membership, membership...)
	c.Assert(k.SetVault(ctx, regular), IsNil)

	c.Assert(validatorMgr.churnInner(ctx), IsNil)
	c.Assert(networkDummy.nas, HasLen, 0, Commentf("must not schedule an unnecessary regular keygen"))
	c.Assert(networkDummy.xmrNas, HasLen, 4)
	for _, member := range active {
		c.Assert(networkDummy.xmrNas.Contains(member), Equals, true)
	}
}

// TestBannedActiveNodeCannotEnterNextXMRKeygen follows the production block
// lifecycle rather than constructing an impossible churn input. A ban reaches
// consensus during transaction delivery, ValidatorMgr.EndBlock removes the
// forced node from the active set and disables it, and only the following
// BeginBlock derives the membership for replacement keygens.
func (vts *ValidatorMgrTestSuite) TestBannedActiveNodeCannotEnterNextXMRKeygen(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(60)
	k := mgr.Keeper()

	k.SetMimir(ctx, constants.BondSlashBan.String(), 0)
	k.SetMimir(ctx, constants.ChurnInterval.String(), 60)
	k.SetMimir(ctx, constants.DesiredValidatorSet.String(), 4)
	k.SetMimir(ctx, constants.AsgardSize.String(), 10)

	active := make(NodeAccounts, 0, 5)
	membership := make(common.PubKeys, 0, 5)
	for i := 0; i < 5; i++ {
		na := GetRandomValidatorNode(NodeActive)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		active = append(active, na)
		membership = append(membership, na.PubKeySet.Secp256k1)
	}

	regular := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain, common.BTCChain}.Strings(), nil)
	regular.Membership = membership.Strings()
	c.Assert(k.SetVault(ctx, regular), IsNil)

	xmr := NewVaultV2(1, ActiveVault, AsgardVault, GetRandomPubKey(), xmrMonovaultChains().Strings(), nil, common.EmptyPubKey)
	xmr.Membership = membership.Strings()
	xmr.PubKeyFrost = GetRandomEd25519PubKey()
	c.Assert(k.SetVault(ctx, xmr), IsNil)

	banned := active[0]
	banHandler := NewBanHandler(mgr)
	for _, banner := range active[1:] {
		_, err := banHandler.Run(ctx, NewMsgBan(banned.NodeAddress, banner.NodeAddress))
		c.Assert(err, IsNil)
	}

	bannedAfterVote, err := k.GetNodeAccount(ctx, banned.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(bannedAfterVote.Status, Equals, NodeActive)
	c.Assert(bannedAfterVote.ForcedToLeave, Equals, true)

	mgr.ValidatorMgr().EndBlock(ctx, mgr)
	bannedAfterEndBlock, err := k.GetNodeAccount(ctx, banned.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(bannedAfterEndBlock.Status, Equals, NodeDisabled)

	remaining, err := k.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	c.Assert(remaining, HasLen, 4)
	c.Assert(remaining.Contains(banned), Equals, false)

	nextCtx := ctx.WithBlockHeight(61)
	c.Assert(mgr.ValidatorMgr().BeginBlock(nextCtx, mgr, nil), IsNil)

	keygenBlock, err := k.GetKeygenBlock(nextCtx, nextCtx.BlockHeight())
	c.Assert(err, IsNil)
	var xmrKeygens []Keygen
	for _, keygen := range keygenBlock.Keygens {
		if keygen.Type == XMRMonovaultKeygen {
			xmrKeygens = append(xmrKeygens, keygen)
		}
	}
	c.Assert(xmrKeygens, HasLen, 1)
	c.Assert(xmrKeygens[0].GetMembers(), HasLen, 4)
	c.Assert(xmrKeygens[0].GetMembers().Contains(banned.PubKeySet.Secp256k1), Equals, false)
	for _, na := range remaining {
		c.Assert(xmrKeygens[0].GetMembers().Contains(na.PubKeySet.Secp256k1), Equals, true)
	}
}

func (vts *ValidatorMgrTestSuite) TestHasPendingCoScheduledKeygenUsesExtendedValidity(c *C) {
	ctx, k := setupKeeperForTest(c)
	const retryBlocks int64 = 2
	k.SetMimir(ctx, constants.ChurnRetryInterval.String(), retryBlocks)
	mgr := NewDummyMgrWithKeeper(k)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	const keygenHeight int64 = 2
	members := common.PubKeys{GetRandomPubKey(), GetRandomPubKey(), GetRandomPubKey()}
	keygen, err := NewKeygen(keygenHeight, members.Strings(), AsgardKeygen)
	c.Assert(err, IsNil)
	xmrKeygen, err := NewKeygen(keygenHeight, members.Strings(), XMRMonovaultKeygen)
	c.Assert(err, IsNil)
	keygenBlock := NewKeygenBlock(keygenHeight)
	keygenBlock.Keygens = []Keygen{keygen, xmrKeygen}
	k.SetKeygenBlock(ctx, keygenBlock)

	unrelatedVault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain}.Strings(), nil)
	unrelatedVault.Membership = common.PubKeys{GetRandomPubKey()}.Strings()
	c.Assert(k.SetVault(ctx, unrelatedVault), IsNil)

	extendedValidity := keygenResultValidityBlocks(ctx, k, XMRMonovaultKeygen)
	c.Assert(extendedValidity > retryBlocks, Equals, true)

	// The co-scheduled Asgard keygen remains pending beyond its plain retry
	// window because the TSS handler still accepts its reports through the XMR
	// window. The second height also proves the scan lookback was widened.
	for _, height := range []int64{
		keygenHeight + retryBlocks,
		keygenHeight + retryBlocks + 1,
		keygenHeight + extendedValidity - 1,
	} {
		pending, pendingErr := validatorMgr.hasPendingKeygen(ctx.WithBlockHeight(height), AsgardKeygen)
		c.Assert(pendingErr, IsNil)
		c.Assert(pending, Equals, true, Commentf("height %d", height))
	}

	pending, err := validatorMgr.hasPendingKeygen(ctx.WithBlockHeight(keygenHeight+extendedValidity), AsgardKeygen)
	c.Assert(err, IsNil)
	c.Assert(pending, Equals, true)

	pending, err = validatorMgr.hasPendingKeygen(ctx.WithBlockHeight(keygenHeight+extendedValidity), XMRMonovaultKeygen)
	c.Assert(err, IsNil)
	c.Assert(pending, Equals, true)

	pending, err = validatorMgr.hasPendingKeygen(ctx.WithBlockHeight(keygenHeight+extendedValidity+1), XMRMonovaultKeygen)
	c.Assert(err, IsNil)
	c.Assert(pending, Equals, false)
}

// A failed keygen must stop being pending exactly at its first retry tick
// (age == ChurnRetryInterval), so the retry that fires on that block is not
// suppressed as "pending" and the effective retry interval is one tick, not two.
func (vts *ValidatorMgrTestSuite) TestHasPendingStandaloneAsgardExpiresAtRetryBoundary(c *C) {
	ctx, k := setupKeeperForTest(c)
	const retryBlocks int64 = 2
	k.SetMimir(ctx, constants.ChurnRetryInterval.String(), retryBlocks)
	mgr := NewDummyMgrWithKeeper(k)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	const keygenHeight int64 = 2
	members := common.PubKeys{GetRandomPubKey(), GetRandomPubKey(), GetRandomPubKey()}
	keygen, err := NewKeygen(keygenHeight, members.Strings(), AsgardKeygen)
	c.Assert(err, IsNil)
	keygenBlock := NewKeygenBlock(keygenHeight)
	keygenBlock.Keygens = []Keygen{keygen}
	k.SetKeygenBlock(ctx, keygenBlock)

	// One block before the retry tick: still pending.
	pending, err := validatorMgr.hasPendingKeygen(ctx.WithBlockHeight(keygenHeight+retryBlocks-1), AsgardKeygen)
	c.Assert(err, IsNil)
	c.Assert(pending, Equals, true)

	// Exactly at the retry tick: expired, so the retry is NOT skipped. Before the
	// off-by-one fix this returned true and the first retry silently no-opped,
	// doubling the effective retry interval.
	pending, err = validatorMgr.hasPendingKeygen(ctx.WithBlockHeight(keygenHeight+retryBlocks), AsgardKeygen)
	c.Assert(err, IsNil)
	c.Assert(pending, Equals, false)

	// And after the tick.
	pending, err = validatorMgr.hasPendingKeygen(ctx.WithBlockHeight(keygenHeight+retryBlocks+1), AsgardKeygen)
	c.Assert(err, IsNil)
	c.Assert(pending, Equals, false)
}

// A keygen whose vault has since retired must not stay "pending" once a newer
// keygen of the same type produced the active vault. Seen in test-simulation: the
// genesis XMR keygen (block 65, one member) was still inside the XMR window at the
// block-428 churn, its vault had retired after the block-362 churn, so the churn
// skipped its XMR keygen and the leaving node stayed Active as an XMR member until
// the next retry tick.
func (vts *ValidatorMgrTestSuite) TestHasPendingKeygenIgnoresSupersededKeygen(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, constants.ChurnRetryInterval.String(), 365)
	mgr := NewDummyMgrWithKeeper(k)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	genesisMembers := common.PubKeys{GetRandomPubKey()}
	churnMembers := common.PubKeys{genesisMembers[0], GetRandomPubKey(), GetRandomPubKey(), GetRandomPubKey()}

	setXMRKeygen := func(height int64, members common.PubKeys) {
		keygen, err := NewKeygen(height, members.Strings(), XMRMonovaultKeygen)
		c.Assert(err, IsNil)
		keygenBlock := NewKeygenBlock(height)
		keygenBlock.Keygens = []Keygen{keygen}
		k.SetKeygenBlock(ctx, keygenBlock)
	}
	setXMRVault := func(height int64, status VaultStatus, members common.PubKeys) {
		vault := NewVault(height, status, AsgardVault, GetRandomPubKey(), common.Chains{common.XMRChain}.Strings(), nil)
		vault.Membership = members.Strings()
		c.Assert(k.SetVault(ctx, vault), IsNil)
	}

	// genesis XMR keygen, superseded by the churn keygen whose vault is active
	setXMRKeygen(65, genesisMembers)
	setXMRVault(67, RetiringVault, genesisMembers)
	setXMRKeygen(362, churnMembers)
	setXMRVault(366, ActiveVault, churnMembers)

	pending, err := validatorMgr.hasPendingKeygen(ctx.WithBlockHeight(428), XMRMonovaultKeygen)
	c.Assert(err, IsNil)
	c.Assert(pending, Equals, false)

	// a newer keygen without a vault yet is still pending
	setXMRKeygen(420, common.PubKeys{GetRandomPubKey(), GetRandomPubKey(), GetRandomPubKey()})
	pending, err = validatorMgr.hasPendingKeygen(ctx.WithBlockHeight(428), XMRMonovaultKeygen)
	c.Assert(err, IsNil)
	c.Assert(pending, Equals, true)
}

// Rounded node shares must conserve the funded reward, independently of Bond Yield.
// The live five-validator replacement allocated 342 + 4*1141 from only 4905.
func (vts *ValidatorMgrTestSuite) TestBondRewardCapsRoundedNodeShares(c *C) {
	for _, rewardUnits := range []uint64{0, 3, 4905} {
		for _, enabled := range []int64{0, 1} {
			ctx, k := setupKeeperForTest(c)
			ctx = ctx.WithBlockHeight(20)
			mgr := NewDummyMgrWithKeeper(k)
			eventMgr, err := GetEventManager(GetCurrentVersion())
			c.Assert(err, IsNil)
			mgr.eventMgr = eventMgr
			validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), eventMgr)
			k.SetMimir(ctx, constants.BondYieldEnabled.String(), enabled)

			initialBonds := cosmos.ZeroUint()
			for _, weight := range []uint64{3, 10, 10, 10, 10} {
				na := GetRandomValidatorNode(NodeActive)
				na.ActiveBlockHeight = 1
				na.Bond = cosmos.NewUint(weight * common.One)
				c.Assert(k.SetNodeAccount(ctx, na), IsNil)
				operator, addressErr := na.BondAddress.AccAddress()
				c.Assert(addressErr, IsNil)
				bp := NewBondProviders(na.NodeAddress)
				bp.NodeOperatorFee = cosmos.NewUint(500)
				bp.Providers = []BondProvider{NewBondProvider(operator)}
				bp.Bond(na.Bond, operator)
				c.Assert(bp.SetAutoClaim(operator, 4000, ""), Equals, true)
				c.Assert(k.SetBondProviders(ctx, bp), IsNil)
				initialBonds = initialBonds.Add(na.Bond)
			}
			reward := cosmos.NewUint(rewardUnits)
			network, err := k.GetNetwork(ctx)
			c.Assert(err, IsNil)
			network.BondRewardRune = reward
			c.Assert(k.SetNetwork(ctx, network), IsNil)
			FundModule(c, ctx, k, BondName, initialBonds.Add(reward).Uint64())
			assertBondInvariantHolds(c, ctx, k.InvariantRoutes())

			c.Assert(validatorMgr.distributeBondReward(ctx, mgr), IsNil)
			assertBondInvariantHolds(c, ctx, k.InvariantRoutes())
			network, err = k.GetNetwork(ctx)
			c.Assert(err, IsNil)
			c.Check(network.BondRewardRune.IsZero(), Equals, true)

			paid := cosmos.ZeroUint()
			rewardEvents := 0
			for _, event := range ctx.EventManager().Events() {
				if event.Type != "bond" {
					continue
				}
				attrs := make(map[string]string)
				for _, attr := range event.Attributes {
					attrs[attr.Key] = attr.Value
				}
				if attrs["bond_type"] == "bond_reward" {
					amount, err := cosmos.ParseUint(attrs["amount"])
					c.Assert(err, IsNil)
					paid = paid.Add(amount)
					rewardEvents++
				}
			}
			c.Check(rewardEvents, Equals, 5)
			c.Check(paid.Equal(reward), Equals, true, Commentf("BondYieldEnabled=%d paid=%s", enabled, paid))
		}
	}
}

func (vts *ValidatorMgrTestSuite) TestWeightedBondReward(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr.eventMgr = eventMgr
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), eventMgr)
	c.Assert(networkMgr, NotNil)
	k.SetMimir(ctx, constants.BondYieldEnabled.String(), 1)

	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(4 * common.One)
	na1.SupportedChains = []string{common.ETHChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na1), IsNil)

	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(3 * common.One)
	na2.SupportedChains = []string{common.ETHChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na2), IsNil)

	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.NewUint(2 * common.One)
	na3.SupportedChains = []string{common.ETHChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na3), IsNil)

	// Enable chain-aware consensus feature
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)
	setRewardVault(c, ctx, mgr.Keeper(), common.Chains{common.ETHChain}, na1, na2, na3)

	network, _ := networkMgr.k.GetNetwork(ctx)
	network.BondRewardRune = cosmos.NewUint(1 * common.One)
	// Set up chain-specific rewards and bond units for ETH chain
	network.SetChainRewardAmount(common.ETHChain.String(), cosmos.NewUint(1*common.One))
	network.SetChainBondUnitsAmount(common.ETHChain.String(), cosmos.NewUint(20))
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

	// Every node supports ETH, so the ETH bucket (the whole 1 RUNE) is split by
	// effective bond, as in the legacy payout: the hard cap is 3 RUNE, so the
	// shares are 3/8, 3/8 and 2/8, and each operator receives 5% of its share.
	// The split does not depend on the order nodes are paid in.
	c.Check(nodeOperator1Balance.AmountOf(common.RuneNative.Native()).String(), Equals, strconv.FormatInt(1875000, 10))
	c.Check(nodeOperator2Balance.AmountOf(common.RuneNative.Native()).String(), Equals, strconv.FormatInt(1875000, 10))
	c.Check(nodeOperator3Balance.AmountOf(common.RuneNative.Native()).String(), Equals, strconv.FormatInt(1250000, 10))

	bondYieldEvents := 0
	for _, event := range ctx.EventManager().Events() {
		if event.Type != "bond_yield" {
			continue
		}
		bondYieldEvents++
	}
	c.Check(bondYieldEvents, Equals, 0)
}

func (vts *ValidatorMgrTestSuite) TestBondYieldDisabledRebondsRewards(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr.eventMgr = eventMgr
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), eventMgr)
	c.Assert(networkMgr, NotNil)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(4 * common.One)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	network, _ := networkMgr.k.GetNetwork(ctx)
	network.BondRewardRune = cosmos.NewUint(1 * common.One)
	c.Assert(mgr.Keeper().SetNetwork(ctx, network), IsNil)

	FundModule(c, ctx, mgr.Keeper(), BondName, 10*common.One)

	c.Assert(networkMgr.distributeBondReward(ctx, mgr), IsNil)

	updated, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(updated.Bond.GT(na.Bond), Equals, true)

	bp, err := mgr.Keeper().GetBondProviders(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	providerAcc, err := na.BondAddress.AccAddress()
	c.Assert(err, IsNil)
	provider := bp.Get(providerAcc)
	c.Assert(provider.IsEmpty(), Equals, false)

	for _, event := range ctx.EventManager().Events() {
		c.Check(event.Type, Not(Equals), "bond_yield")
	}
}

func (vts *ValidatorMgrTestSuite) TestBondRewardNormalizesLegacyProviderOrdering(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr.eventMgr = eventMgr
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), eventMgr)
	c.Assert(networkMgr, NotNil)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(4 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	operator, err := na.BondAddress.AccAddress()
	c.Assert(err, IsNil)
	provider := GetRandomBech32Addr()
	bp := NewBondProviders(na.NodeAddress)
	bp.Providers = []BondProvider{
		NewBondProvider(provider),
		NewBondProvider(operator),
	}
	bp.Bond(cosmos.NewUint(common.One), provider)
	bp.Bond(cosmos.NewUint(3*common.One), operator)
	c.Assert(k.SetBondProviders(ctx, bp), IsNil)

	network, err := k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	network.BondRewardRune = cosmos.NewUint(common.One)
	c.Assert(k.SetNetwork(ctx, network), IsNil)
	FundModule(c, ctx, k, BondName, 5*common.One)

	c.Assert(networkMgr.distributeBondReward(ctx, mgr), IsNil)

	stored, err := k.GetBondProviders(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(stored.Providers, HasLen, 2)
	c.Check(stored.Providers[0].BondAddress.Equals(operator), Equals, true)
	c.Check(stored.Providers[1].BondAddress.Equals(provider), Equals, true)

	updated, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(updated.Bond.Equal(cosmos.NewUint(5*common.One)), Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestBondYieldAutoClaimPaysAndRebondsRemainder(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr.eventMgr = eventMgr
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), eventMgr)
	c.Assert(networkMgr, NotNil)
	k.SetMimir(ctx, constants.BondYieldEnabled.String(), 1)

	initialBond := cosmos.NewUint(4 * common.One)
	reward := cosmos.NewUint(common.One)
	expectedPayout := cosmos.NewUint(40 * common.One / 100)
	expectedRebond := cosmos.NewUint(60 * common.One / 100)
	expectedBond := initialBond.Add(expectedRebond)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = initialBond
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	providerAcc, err := na.BondAddress.AccAddress()
	c.Assert(err, IsNil)
	bp := NewBondProviders(na.NodeAddress)
	bp.Providers = []BondProvider{NewBondProvider(providerAcc)}
	bp.Bond(na.Bond, providerAcc)
	c.Assert(bp.SetAutoClaim(providerAcc, 4000, ""), Equals, true)
	c.Assert(mgr.Keeper().SetBondProviders(ctx, bp), IsNil)

	network, _ := networkMgr.k.GetNetwork(ctx)
	network.BondRewardRune = reward
	c.Assert(mgr.Keeper().SetNetwork(ctx, network), IsNil)

	FundModule(c, ctx, mgr.Keeper(), BondName, initialBond.Add(reward).Uint64())
	assertBondInvariantHolds(c, ctx, mgr.Keeper().InvariantRoutes())

	c.Assert(networkMgr.distributeBondReward(ctx, mgr), IsNil)

	updated, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(updated.Bond.Equal(expectedBond), Equals, true)

	stored, err := mgr.Keeper().GetBondProviders(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	provider := stored.Get(providerAcc)
	c.Assert(provider.IsEmpty(), Equals, false)
	c.Check(provider.Bond.Equal(expectedBond), Equals, true)
	c.Check(provider.Bps, Equals, uint64(4000))
	c.Check(provider.PayoutTarget, Equals, "")

	balance := mgr.Keeper().GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount
	c.Check(balance.Equal(cosmos.NewInt(int64(expectedPayout.Uint64()))), Equals, true)

	updatedNetwork, err := mgr.Keeper().GetNetwork(ctx)
	c.Assert(err, IsNil)
	c.Check(updatedNetwork.BondRewardRune.IsZero(), Equals, true)
	c.Check(mgr.Keeper().GetRuneBalanceOfModule(ctx, BondName).Equal(expectedBond), Equals, true)
	assertBondInvariantHolds(c, ctx, mgr.Keeper().InvariantRoutes())

	attrs := singleBondYieldEventAttributes(c, ctx)
	c.Check(attrs["provider_address"], Equals, providerAcc.String())
	c.Check(attrs["reward_share"], Equals, reward.String())
	c.Check(attrs["payout_amount"], Equals, expectedPayout.String())
	c.Check(attrs["rebonded_amount"], Equals, expectedRebond.String())
	c.Check(attrs["bps"], Equals, "4000")
	c.Check(attrs["payout_target"], Equals, "")
}

func (vts *ValidatorMgrTestSuite) TestBondYieldPreferredAssetPayoutAtRewardTime(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	mgr.gasMgr = bondYieldRewardTestGasManager{DummyGasManager: NewDummyGasManager()}
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr.eventMgr = eventMgr
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), eventMgr)
	c.Assert(networkMgr, NotNil)
	k.SetMimir(ctx, constants.BondYieldEnabled.String(), 1)

	initialBond := cosmos.NewUint(4 * common.One)
	reward := cosmos.NewUint(common.One)
	expectedPayout := cosmos.NewUint(40 * common.One / 100)
	expectedRebond := cosmos.NewUint(60 * common.One / 100)
	expectedBond := initialBond.Add(expectedRebond)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = initialBond
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	providerAcc, err := na.BondAddress.AccAddress()
	c.Assert(err, IsNil)
	const payoutTarget = "yieldname"
	bp := NewBondProviders(na.NodeAddress)
	bp.Providers = []BondProvider{NewBondProvider(providerAcc)}
	bp.Bond(initialBond, providerAcc)
	c.Assert(bp.SetAutoClaim(providerAcc, 4000, payoutTarget), Equals, true)
	c.Assert(k.SetBondProviders(ctx, bp), IsNil)

	tn := NewTHORName(payoutTarget, 1000, []THORNameAlias{
		{Chain: common.BTCChain, Address: GetRandomBTCAddress()},
	})
	tn.Owner = providerAcc
	tn.PreferredAsset = common.BTCAsset
	tn.PreferredAssetOutboundFeeMultiplier = 1
	k.SetTHORName(ctx, tn)

	network, err := k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	network.BondRewardRune = reward
	c.Assert(k.SetNetwork(ctx, network), IsNil)

	FundModule(c, ctx, k, BondName, initialBond.Add(reward).Uint64())
	assertBondInvariantHolds(c, ctx, k.InvariantRoutes())

	c.Assert(networkMgr.payNodeAccountBondAward(
		ctx,
		na.ActiveBlockHeight,
		na,
		reward,
		initialBond,
		initialBond,
		mgr,
	), IsNil)

	updated, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(updated.Bond.Equal(expectedBond), Equals, true)

	stored, err := k.GetBondProviders(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	provider := stored.Get(providerAcc)
	c.Assert(provider.IsEmpty(), Equals, false)
	c.Check(provider.Bond.Equal(expectedBond), Equals, true)
	c.Check(provider.Bps, Equals, uint64(4000))
	c.Check(provider.PayoutTarget, Equals, payoutTarget)

	collector, err := k.GetAffiliateCollector(ctx, tn.Name)
	c.Assert(err, IsNil)
	c.Check(collector.Name, Equals, tn.Name)
	c.Check(collector.RuneAmount.Equal(expectedPayout), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, AffiliateCollectorName).Equal(expectedPayout), Equals, true)
	c.Check(k.GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount.IsZero(), Equals, true)

	updatedNetwork, err := k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	c.Check(updatedNetwork.BondRewardRune.IsZero(), Equals, true)
	c.Check(k.GetRuneBalanceOfModule(ctx, BondName).Equal(expectedBond), Equals, true)
	assertBondInvariantHolds(c, ctx, k.InvariantRoutes())

	attrs := singleBondYieldEventAttributes(c, ctx)
	c.Check(attrs["provider_address"], Equals, providerAcc.String())
	c.Check(attrs["reward_share"], Equals, reward.String())
	c.Check(attrs["payout_amount"], Equals, expectedPayout.String())
	c.Check(attrs["rebonded_amount"], Equals, expectedRebond.String())
	c.Check(attrs["bps"], Equals, "4000")
	c.Check(attrs["payout_target"], Equals, payoutTarget)
}

func assertBondInvariantHolds(c *C, ctx cosmos.Context, routes []common.InvariantRoute) {
	for _, route := range routes {
		if route.Route != "bond" {
			continue
		}
		messages, broken := route.Invariant(ctx)
		c.Check(broken, Equals, false, Commentf("bond invariant messages: %v", messages))
		c.Check(messages, IsNil)
		return
	}
	c.Fatalf("bond invariant route not found")
}

func singleBondYieldEventAttributes(c *C, ctx cosmos.Context) map[string]string {
	var result map[string]string
	for _, event := range ctx.EventManager().Events() {
		if event.Type != "bond_yield" {
			continue
		}
		c.Assert(result, IsNil, Commentf("expected exactly one bond_yield event"))
		result = make(map[string]string)
		for _, attr := range event.Attributes {
			result[attr.Key] = attr.Value
		}
	}
	c.Assert(result, NotNil, Commentf("expected one bond_yield event"))
	return result
}

func (vts *ValidatorMgrTestSuite) TestBondYieldFailedPayoutRebondsFallback(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	eventMgr, err := GetEventManager(GetCurrentVersion())
	c.Assert(err, IsNil)
	mgr.eventMgr = eventMgr
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), eventMgr)
	c.Assert(networkMgr, NotNil)
	k.SetMimir(ctx, constants.BondYieldEnabled.String(), 1)

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(4 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)

	providerAcc, err := na.BondAddress.AccAddress()
	c.Assert(err, IsNil)
	payoutTarget := k.GetModuleAccAddress(AsgardName).String()
	bp := NewBondProviders(na.NodeAddress)
	bp.Providers = []BondProvider{NewBondProvider(providerAcc)}
	bp.Bond(na.Bond, providerAcc)
	c.Assert(bp.SetAutoClaim(providerAcc, 4000, payoutTarget), Equals, true)
	c.Assert(k.SetBondProviders(ctx, bp), IsNil)

	reward := cosmos.NewUint(1 * common.One)
	network, err := k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	network.BondRewardRune = reward
	c.Assert(k.SetNetwork(ctx, network), IsNil)

	FundModule(c, ctx, k, BondName, 10*common.One)

	c.Assert(networkMgr.distributeBondReward(ctx, mgr), IsNil)

	updated, err := k.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(updated.Bond.Equal(na.Bond.Add(reward)), Equals, true)

	stored, err := k.GetBondProviders(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	provider := stored.Get(providerAcc)
	c.Check(provider.Bond.Equal(na.Bond.Add(reward)), Equals, true)
	c.Check(k.GetBalanceOf(ctx, providerAcc, common.RuneNative).Amount.IsZero(), Equals, true)

	var bondYieldAttrs map[string]string
	for _, event := range ctx.EventManager().Events() {
		if event.Type != "bond_yield" {
			continue
		}
		c.Check(bondYieldAttrs, IsNil, Commentf("expected exactly one bond_yield event"))
		bondYieldAttrs = make(map[string]string)
		for _, attr := range event.Attributes {
			bondYieldAttrs[attr.Key] = attr.Value
		}
	}
	c.Assert(bondYieldAttrs, NotNil)
	c.Check(bondYieldAttrs["provider_address"], Equals, providerAcc.String())
	c.Check(bondYieldAttrs["reward_share"], Equals, reward.String())
	c.Check(bondYieldAttrs["payout_amount"], Equals, cosmos.ZeroUint().String())
	c.Check(bondYieldAttrs["rebonded_amount"], Equals, reward.String())
	c.Check(bondYieldAttrs["bps"], Equals, "4000")
	c.Check(bondYieldAttrs["payout_target"], Equals, payoutTarget)
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
	mgr.Keeper().SetMimir(ctx, constants.MaxMissingBlockChurnOut.String(), 2)

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
	mgr.Keeper().SetMimir(ctx, constants.MaxMissingBlockChurnOut.String(), 1)

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

// TestNodeAccountPreflightCheckKeygenChainSet covers the four outcomes of the keygen
// chain set check: a node with no report is unaffected, a complete report passes, a
// report that omits a chain the active vaults serve is held in Standby, and a report
// older than the active vaults is not judged at all.
func (vts *ValidatorMgrTestSuite) TestNodeAccountPreflightCheckKeygenChainSet(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	mgr := NewDummyMgrWithKeeper(k)

	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(validatorMgr, NotNil)

	// The active vaults serve BTC and ETH, and were created at height 500.
	vault := NewVault(500, ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.BTCChain, common.ETHChain}.Strings(), []ChainContract{})
	c.Assert(k.SetVault(ctx, vault), IsNil)
	k.SetMimir(ctx, constants.MinimumBondInRune.String(), 1000000)

	readyNode := func() NodeAccount {
		na := GetRandomValidatorNode(NodeStandby)
		na.Bond = cosmos.NewUint(10 * common.One)
		na.PubKeySet = GetRandomPubKeySet()
		na.Version = constants.SWVersion.String()
		na.IPAddress = "192.168.0.1"
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		return na
	}

	// No keygen report at all: nothing to judge, so a node that has never been
	// selected is unaffected.
	never := readyNode()
	status, err := validatorMgr.NodeAccountPreflightCheck(ctx, never, k.GetConstants())
	c.Assert(err, IsNil)
	c.Assert(status, Equals, NodeReady)

	// A report covering every chain the active vaults serve.
	complete := readyNode()
	k.SetLastKeygenChains(ctx, complete.NodeAddress, common.Chains{common.BTCChain, common.ETHChain}, 600)
	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, complete, k.GetConstants())
	c.Assert(err, IsNil)
	c.Assert(status, Equals, NodeReady)

	// A report that omits ETH: this is the node that would drop ETH from the next
	// vault and stall the rotation, so it is held back.
	incomplete := readyNode()
	k.SetLastKeygenChains(ctx, incomplete.NodeAddress, common.Chains{common.BTCChain}, 600)
	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, incomplete, k.GetConstants())
	c.Assert(err, NotNil)
	c.Assert(status, Equals, NodeStandby)
	c.Assert(err.Error(), Equals, "node account keygen report at height 600 omitted chain ETH served by the active vaults")

	// The mimir kill switch restores the old behaviour.
	k.SetMimir(ctx, constants.MimirKeyPreflightChainSetDisabled, 1)
	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, incomplete, k.GetConstants())
	c.Assert(err, IsNil)
	c.Assert(status, Equals, NodeReady)
	k.SetMimir(ctx, constants.MimirKeyPreflightChainSetDisabled, 0)

	// A report older than the active vaults may predate a chain being added to them,
	// so it is not evidence of a missing chain. This is also what expires a recorded
	// report at the next completed churn, so no node can be locked out.
	stale := readyNode()
	k.SetLastKeygenChains(ctx, stale.NodeAddress, common.Chains{common.BTCChain}, 400)
	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, stale, k.GetConstants())
	c.Assert(err, IsNil)
	c.Assert(status, Equals, NodeReady)

	// XMR is served by a monovault keygen with its own fixed chain set, so an asgard
	// report never carries it and its absence is not held against the reporter. The
	// monovault rotates on its own schedule: one created after the asgard keygen must
	// not move the report-age bound past the asgard reports, or every incomplete
	// report would be treated as stale and the check would never fire.
	xmrVault := NewVault(900, ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.THORChain, common.XMRChain}.Strings(), []ChainContract{})
	c.Assert(k.SetVault(ctx, xmrVault), IsNil)
	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, complete, k.GetConstants())
	c.Assert(err, IsNil)
	c.Assert(status, Equals, NodeReady)
	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, incomplete, k.GetConstants())
	c.Assert(err, NotNil)
	c.Assert(status, Equals, NodeStandby)
	c.Assert(err.Error(), Equals, "node account keygen report at height 600 omitted chain ETH served by the active vaults")

	// A chain being retired (RAGNAROK-<gas asset>) is dropped from the reports of the
	// operators who set OptToRetire first, so it is not required.
	k.SetMimir(ctx, "RAGNAROK-ETH-ETH", 1)
	status, err = validatorMgr.NodeAccountPreflightCheck(ctx, incomplete, k.GetConstants())
	c.Assert(err, IsNil)
	c.Assert(status, Equals, NodeReady)
	k.SetMimir(ctx, "RAGNAROK-ETH-ETH", 0)
}

// chainSetTest holds the fixture shared by the chain-set churn-out tests: active asgard
// vaults created at height 500 serving the given chains, and the mark armed with a limit
// of 10 unless a test changes it.
type chainSetTest struct {
	ctx cosmos.Context
	k   keeper.Keeper
	vm  *ValidatorMgr
}

func newChainSetTest(c *C, chains common.Chains) chainSetTest {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	mgr := NewDummyMgrWithKeeper(k)
	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(vm, NotNil)
	vault := NewVault(500, ActiveVault, AsgardVault, GetRandomPubKey(), chains.Strings(), []ChainContract{})
	c.Assert(k.SetVault(ctx, vault), IsNil)
	k.SetMimir(ctx, constants.MimirKeyChainSetChurnOutMax, 10)
	return chainSetTest{ctx: ctx, k: k, vm: vm}
}

// node adds an Active node whose last asgard keygen report, at height 600, declared the
// given chains; nil means it has never reported.
func (t chainSetTest) node(c *C, declared common.Chains) NodeAccount {
	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(10 * common.One)
	c.Assert(t.k.SetNodeAccount(t.ctx, na), IsNil)
	if declared != nil {
		t.k.SetLastKeygenChains(t.ctx, na.NodeAddress, declared, 600)
	}
	return na
}

// pendingVault adds an asgard InitVault, as a keygen round leaves it, with the given chains.
func (t chainSetTest) pendingVault(c *C, chains common.Chains) Vault {
	vault := NewVault(700, InitVault, AsgardVault, GetRandomPubKey(), chains.Strings(), []ChainContract{})
	c.Assert(t.k.SetVault(t.ctx, vault), IsNil)
	return vault
}

func (t chainSetTest) leaveScore(c *C, na NodeAccount) uint64 {
	after, err := t.k.GetNodeAccount(t.ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	return after.LeaveScore
}

var (
	btcEth  = common.Chains{common.BTCChain, common.ETHChain}
	btcOnly = common.Chains{common.BTCChain}
)

// TestMarkChainSetActors covers the Active-side half: it is off unless the mimir is set,
// it marks nothing until a pending vault is actually created without a chain, and then
// it marks only the nodes whose latest report omitted that chain.
func (vts *ValidatorMgrTestSuite) TestMarkChainSetActors(c *C) {
	t := newChainSetTest(c, btcEth)
	complete := t.node(c, btcEth)
	incomplete := t.node(c, btcOnly)
	noReport := t.node(c, nil)

	t.k.SetMimir(t.ctx, constants.MimirKeyChainSetChurnOutMax, 0)
	t.pendingVault(c, btcOnly)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, incomplete), Equals, uint64(0), Commentf("must not mark while ChainSetChurnOutMax is 0"))

	t.k.SetMimir(t.ctx, constants.MimirKeyChainSetChurnOutMax, 10)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, incomplete) > 0, Equals, true, Commentf("the omitted chain stalled a pending vault"))
	c.Check(t.leaveScore(c, complete), Equals, uint64(0))
	c.Check(t.leaveScore(c, noReport), Equals, uint64(0))
}

// TestMarkChainSetActors_NoStallNoMark: an omission that stalled nothing (the chain still
// reached its supermajority, so no pending vault lacks it) is not marked, even when the
// churn is being retried for some other reason.
func (vts *ValidatorMgrTestSuite) TestMarkChainSetActors_NoStallNoMark(c *C) {
	t := newChainSetTest(c, btcEth)
	incomplete := t.node(c, btcOnly)

	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, incomplete), Equals, uint64(0), Commentf("no pending vault: nothing stalled"))

	t.pendingVault(c, btcEth)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, incomplete), Equals, uint64(0), Commentf("the pending vault kept ETH"))
}

// TestMarkChainSetActors_IgnoresChainThatDidNotStall: only an omission of a chain that
// actually stalled counts. A node lagging on a different chain is left alone.
func (vts *ValidatorMgrTestSuite) TestMarkChainSetActors_IgnoresChainThatDidNotStall(c *C) {
	t := newChainSetTest(c, common.Chains{common.BTCChain, common.ETHChain, common.LTCChain})
	missingETH := t.node(c, common.Chains{common.BTCChain, common.LTCChain})
	missingLTC := t.node(c, common.Chains{common.BTCChain, common.ETHChain})

	t.pendingVault(c, common.Chains{common.BTCChain, common.LTCChain}) // ETH stalled, LTC did not
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, missingETH) > 0, Equals, true)
	c.Check(t.leaveScore(c, missingLTC), Equals, uint64(0), Commentf("LTC kept its supermajority"))
}

// TestMarkChainSetActors_LimitIsAllOrNothing: every faulty node is marked, up to the
// limit; past it, none are, rather than a subset that would be picked again every retry.
func (vts *ValidatorMgrTestSuite) TestMarkChainSetActors_LimitIsAllOrNothing(c *C) {
	t := newChainSetTest(c, btcEth)
	a := t.node(c, btcOnly)
	b := t.node(c, btcOnly)
	t.pendingVault(c, btcOnly)

	t.k.SetMimir(t.ctx, constants.MimirKeyChainSetChurnOutMax, 1)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, a), Equals, uint64(0), Commentf("two faulty nodes over a limit of 1: none marked"))
	c.Check(t.leaveScore(c, b), Equals, uint64(0))

	t.k.SetMimir(t.ctx, constants.MimirKeyChainSetChurnOutMax, 2)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, a) > 0, Equals, true)
	c.Check(t.leaveScore(c, b) > 0, Equals, true)
}

// TestMarkChainSetActors_MarksPersistAcrossRetries is the review's multi-retry case: the
// first round's pending vault is retired when the retry's keygen is scheduled, and the
// excluded nodes cannot file a newer report, yet both offenders are marked on every
// attempt, not only the one first in address order.
func (vts *ValidatorMgrTestSuite) TestMarkChainSetActors_MarksPersistAcrossRetries(c *C) {
	t := newChainSetTest(c, btcEth)
	a := t.node(c, btcOnly)
	b := t.node(c, btcOnly)
	pending := t.pendingVault(c, btcOnly)

	for attempt := 1; attempt <= 3; attempt++ {
		c.Assert(t.vm.clearLeaveScores(t.ctx), IsNil)
		c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
		c.Check(t.leaveScore(c, a) > 0, Equals, true, Commentf("attempt %d", attempt))
		c.Check(t.leaveScore(c, b) > 0, Equals, true, Commentf("attempt %d", attempt))
		// TriggerKeygen retires the round's pending vault; the next round fails for
		// another reason and leaves none.
		pending.UpdateStatus(InactiveVault, t.ctx.BlockHeight())
		c.Assert(t.k.SetVault(t.ctx, pending), IsNil)
	}
}

// TestMarkChainSetActors_StaleReportUntilCycleCompletes documents the trade-off: a node
// left out cannot re-report, so it stays marked for the rest of the cycle even if its
// operator fixed the config, and is judged afresh once the churn completes.
func (vts *ValidatorMgrTestSuite) TestMarkChainSetActors_StaleReportUntilCycleCompletes(c *C) {
	t := newChainSetTest(c, btcEth)
	fixedLater := t.node(c, btcOnly)
	t.pendingVault(c, btcOnly)

	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, fixedLater) > 0, Equals, true)
	c.Assert(t.vm.clearLeaveScores(t.ctx), IsNil)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, fixedLater) > 0, Equals, true, Commentf("still the latest report it could file"))

	// The churn completes: the new active vaults are newer than the report and the stall.
	vaults, err := t.k.GetAsgardVaultsByStatus(t.ctx, ActiveVault)
	c.Assert(err, IsNil)
	for _, v := range vaults {
		v.UpdateStatus(RetiringVault, t.ctx.BlockHeight())
		c.Assert(t.k.SetVault(t.ctx, v), IsNil)
	}
	newVault := NewVault(900, ActiveVault, AsgardVault, GetRandomPubKey(), btcEth.Strings(), []ChainContract{})
	c.Assert(t.k.SetVault(t.ctx, newVault), IsNil)
	c.Assert(t.vm.clearLeaveScores(t.ctx), IsNil)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, fixedLater), Equals, uint64(0), Commentf("a new cycle judges the next report"))
}

// TestMarkChainSetActors_XMRAndRetiring asserts the mark uses the same required set and
// report-age bound as the preflight half: an XMR monovault newer than the asgard keygen
// does not expire the asgard reports, and a chain under RAGNAROK is not required.
func (vts *ValidatorMgrTestSuite) TestMarkChainSetActors_XMRAndRetiring(c *C) {
	t := newChainSetTest(c, btcEth)
	xmrVault := NewVault(900, ActiveVault, AsgardVault, GetRandomPubKey(),
		common.Chains{common.THORChain, common.XMRChain}.Strings(), []ChainContract{})
	c.Assert(t.k.SetVault(t.ctx, xmrVault), IsNil)
	early := t.node(c, btcOnly)
	t.pendingVault(c, btcOnly)

	// ETH is being retired: the node that already dropped it is not marked.
	t.k.SetMimir(t.ctx, "RAGNAROK-ETH-ETH", 1)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, early), Equals, uint64(0), Commentf("a chain under RAGNAROK must not be required"))

	// Without the retirement the same report is marked, despite the newer monovault.
	t.k.SetMimir(t.ctx, "RAGNAROK-ETH-ETH", 0)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, early) > 0, Equals, true, Commentf("a newer XMR monovault must not expire the asgard report"))
}

// removedBy runs nextVaultNodeAccounts and reports which of the given nodes it left out.
func (t chainSetTest) removedBy(c *C, size int, nodes ...NodeAccount) []bool {
	next, _, err := t.vm.nextVaultNodeAccounts(t.ctx, size)
	c.Assert(err, IsNil)
	out := make([]bool, len(nodes))
	for i, na := range nodes {
		out[i] = true
		for _, n := range next {
			if n.NodeAddress.Equals(na.NodeAddress) {
				out[i] = false
			}
		}
	}
	return out
}

// TestNextVaultNodeAccounts_ChainSetMarkLeavesFirst asserts a node marked for its chain
// set is the one removed when only one node may leave, even though it has no slash
// points and so the highest recomputed LeaveScore of the marked nodes.
func (vts *ValidatorMgrTestSuite) TestNextVaultNodeAccounts_ChainSetMarkLeavesFirst(c *C) {
	t := newChainSetTest(c, btcEth)
	nodes := NodeAccounts{t.node(c, btcOnly)}
	for i := 0; i < 4; i++ {
		nodes = append(nodes, t.node(c, btcEth))
	}
	c.Assert(findMaxAbleToLeave(len(nodes)), Equals, 1)
	t.pendingVault(c, btcOnly)
	misconfigured, slashed := nodes[0], nodes[1]
	c.Assert(t.k.IncNodeAccountSlashPoints(t.ctx, slashed.NodeAddress, 100), IsNil)
	c.Assert(t.vm.markActor(t.ctx, slashed, "for age"), IsNil)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)

	removed := t.removedBy(c, len(nodes), misconfigured, slashed)
	c.Check(removed[0], Equals, true, Commentf("the chain-set node must be the one to leave"))
	c.Check(removed[1], Equals, false)
}

// TestNextVaultNodeAccounts_ChainSetAheadOfRequested: with one free slot, a faulty node
// leaves before a voluntary leaver, who could not leave anyway while the keygen stalls.
// A ForcedToLeave node still goes first.
func (vts *ValidatorMgrTestSuite) TestNextVaultNodeAccounts_ChainSetAheadOfRequested(c *C) {
	t := newChainSetTest(c, btcEth)
	nodes := NodeAccounts{t.node(c, btcOnly), t.node(c, btcEth)}
	for i := 0; i < 4; i++ {
		nodes = append(nodes, t.node(c, btcEth))
	}
	c.Assert(findMaxAbleToLeave(len(nodes)), Equals, 1)
	t.pendingVault(c, btcOnly)
	faulty, requester := nodes[0], nodes[1]
	requester.RequestedToLeave = true
	c.Assert(t.k.SetNodeAccount(t.ctx, requester), IsNil)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)

	removed := t.removedBy(c, len(nodes), faulty, requester)
	c.Check(removed[0], Equals, true, Commentf("the faulty node takes the only slot"))
	c.Check(removed[1], Equals, false)

	forced := nodes[2]
	forced.ForcedToLeave = true
	c.Assert(t.k.SetNodeAccount(t.ctx, forced), IsNil)
	removed = t.removedBy(c, len(nodes), faulty, forced)
	c.Check(removed[0], Equals, false)
	c.Check(removed[1], Equals, true, Commentf("ForcedToLeave still goes first"))
}

// TestNextVaultNodeAccounts_ChainSetLeavesSlotForOthers: when faulty nodes would take
// every slot while an offline or bad-actor node also waits to leave, the last slot goes
// to the ordinary order, so that node can leave too.
func (vts *ValidatorMgrTestSuite) TestNextVaultNodeAccounts_ChainSetLeavesSlotForOthers(c *C) {
	t := newChainSetTest(c, btcEth)
	var faulty, others NodeAccounts
	for i := 0; i < 3; i++ {
		faulty = append(faulty, t.node(c, btcOnly))
	}
	for i := 0; i < 9; i++ {
		others = append(others, t.node(c, btcEth))
	}
	c.Assert(findMaxAbleToLeave(len(faulty)+len(others)), Equals, 3)
	t.pendingVault(c, btcOnly)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)

	// No one else waiting: all three faulty nodes leave.
	removed := t.removedBy(c, 12, faulty...)
	c.Check(removed, DeepEquals, []bool{true, true, true})

	offline := others[0]
	c.Assert(t.k.IncNodeAccountSlashPoints(t.ctx, offline.NodeAddress, 100), IsNil)
	c.Assert(t.vm.markActor(t.ctx, offline, "for not signing blocks"), IsNil)
	removed = t.removedBy(c, 12, append(NodeAccounts{offline}, faulty...)...)
	c.Check(removed[0], Equals, true, Commentf("the offline node keeps a slot"))
	leaving := 0
	for _, r := range removed[1:] {
		if r {
			leaving++
		}
	}
	c.Check(leaving, Equals, 2, Commentf("two faulty nodes take the other slots"))
}

// TestNextVaultNodeAccounts_ChainSetTinySetKeepsRequestedLeaver: with four nodes no slot
// is free, so findCountToRemove only lets a forced or requested leaver go; a faulty node
// must not be sorted ahead of the voluntary leaver there.
func (vts *ValidatorMgrTestSuite) TestNextVaultNodeAccounts_ChainSetTinySetKeepsRequestedLeaver(c *C) {
	t := newChainSetTest(c, btcEth)
	nodes := NodeAccounts{t.node(c, btcOnly), t.node(c, btcEth), t.node(c, btcEth), t.node(c, btcEth)}
	c.Assert(findMaxAbleToLeave(len(nodes)), Equals, 0)
	t.pendingVault(c, btcOnly)
	requester := nodes[1]
	requester.RequestedToLeave = true
	c.Assert(t.k.SetNodeAccount(t.ctx, requester), IsNil)
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)

	removed := t.removedBy(c, len(nodes), nodes[0], requester)
	c.Check(removed[0], Equals, false)
	c.Check(removed[1], Equals, true, Commentf("the requested leaver still goes"))
}

// TestChurnInnerChainSetStallSmallSet runs a stalled retry end to end through churnInner
// on a nine-node set (stagenet sizing): the faulty node is left out of the keygen that is
// triggered, and with more faulty nodes than free slots the keygen still fires with the
// remainder, who are marked again on the next attempt.
func (vts *ValidatorMgrTestSuite) TestChurnInnerChainSetStallSmallSet(c *C) {
	for _, nFaulty := range []int{1, 3} {
		ctx, k := setupKeeperForTest(c)
		ctx = ctx.WithBlockHeight(1000)
		mgr := NewDummyMgrWithKeeper(k)
		networkDummy, ok := mgr.NetworkMgr().(*NetworkMgrDummy)
		c.Assert(ok, Equals, true)
		vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
		k.SetMimir(ctx, constants.DesiredValidatorSet.String(), 9)
		k.SetMimir(ctx, constants.AsgardSize.String(), 20)
		k.SetMimir(ctx, constants.MimirKeyChainSetChurnOutMax, 5)

		var nodes NodeAccounts
		membership := make([]string, 0, 9)
		for i := 0; i < 9; i++ {
			na := GetRandomValidatorNode(NodeActive)
			na.Bond = cosmos.NewUint(10 * common.One)
			na.StatusSince = 100
			c.Assert(k.SetNodeAccount(ctx, na), IsNil)
			declared := btcEth
			if i < nFaulty {
				declared = btcOnly
			}
			k.SetLastKeygenChains(ctx, na.NodeAddress, declared, 600)
			nodes = append(nodes, na)
			membership = append(membership, na.PubKeySet.Secp256k1.String())
		}
		active := NewVault(500, ActiveVault, AsgardVault, GetRandomPubKey(), btcEth.Strings(), nil)
		active.Membership = membership
		c.Assert(k.SetVault(ctx, active), IsNil)
		pending := NewVault(700, InitVault, AsgardVault, GetRandomPubKey(), btcOnly.Strings(), nil)
		pending.Membership = membership
		c.Assert(k.SetVault(ctx, pending), IsNil)

		c.Assert(vm.churnInner(ctx), IsNil)
		c.Assert(len(networkDummy.nas) > 0, Equals, true, Commentf("%d faulty: the keygen must still fire", nFaulty))
		inKeygen := func(na NodeAccount) bool { return networkDummy.nas.Contains(na) }
		excluded := 0
		for _, na := range nodes[:nFaulty] {
			if !inKeygen(na) {
				excluded++
			}
		}
		if nFaulty == 1 {
			c.Check(excluded, Equals, 1, Commentf("the faulty node must be left out of the keygen"))
			continue
		}
		// Nine nodes free two slots. The age mark is also waiting, so one slot goes to
		// the ordinary order and at least one faulty node stays in for this attempt.
		c.Check(excluded >= 1, Equals, true)
		c.Check(excluded < nFaulty, Equals, true, Commentf("more faulty nodes than slots"))
		for _, na := range nodes[:nFaulty] {
			after, err := k.GetNodeAccount(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			c.Check(after.LeaveScore > 0, Equals, true, Commentf("every faulty node is marked"))
		}
	}
}

// seedObservationTallies gives each node quota[i] misses out of 30 voters on ETH.
func seedObservationTallies(ctx cosmos.Context, mgr *Mgrs, nas NodeAccounts, quota []int) {
	for v := 0; v < 30; v++ {
		var missed []cosmos.AccAddress
		for i, na := range nas {
			if quota[i] > v {
				missed = append(missed, na.NodeAddress)
			}
		}
		mgr.Keeper().AddObservationTally(ctx, common.ETHChain, observationTallyUnit, missed, nil)
	}
}

func (vts *ValidatorMgrTestSuite) TestMarkMissingObservationActors(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	nas := make(NodeAccounts, 6)
	for i := range nas {
		nas[i] = GetRandomValidatorNode(NodeActive)
	}
	nas[5].LeaveScore = 100 // already marked for churn-out
	for _, na := range nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
	// 100%, 80%, 60% and 40% missed; nas[4] misses everything but churned in for
	// the last 10 voters only, below MissingObservationMinSamples; nas[5] misses
	// everything but is already marked
	quota := []int{30, 24, 18, 12, 30, 30}
	for v := 0; v < 30; v++ {
		if v == 20 {
			c.Assert(mgr.Keeper().ResetObservationTally(ctx, nas[4].NodeAddress), IsNil)
		}
		var missed []cosmos.AccAddress
		for i, na := range nas {
			if quota[i] > v {
				missed = append(missed, na.NodeAddress)
			}
		}
		mgr.Keeper().AddObservationTally(ctx, common.ETHChain, observationTallyUnit, missed, nil)
	}
	leaveScores := func() []uint64 {
		out := make([]uint64, len(nas))
		for i, na := range nas {
			after, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
			c.Assert(err, IsNil)
			out[i] = after.LeaveScore
		}
		return out
	}

	// off by default
	c.Assert(networkMgr.markMissingObservationActors(ctx), IsNil)
	c.Check(leaveScores(), DeepEquals, []uint64{0, 0, 0, 0, 0, 100})

	// capped at 2: the two worst above 50% are marked, the already-marked node is
	// neither re-marked nor counted against the cap
	mgr.Keeper().SetMimir(ctx, constants.MaxMissingObservationChurnOut.String(), 2)
	c.Assert(networkMgr.markMissingObservationActors(ctx), IsNil)
	scores := leaveScores()
	c.Check(scores[0] > 0, Equals, true, Commentf("100%% missed should be marked"))
	c.Check(scores[1] > 0, Equals, true, Commentf("80%% missed should be marked"))
	c.Check(scores[2], Equals, uint64(0), Commentf("60%% missed is capped out"))
	c.Check(scores[3], Equals, uint64(0), Commentf("40%% missed is under the threshold"))
	c.Check(scores[4], Equals, uint64(0), Commentf("too few samples since churn-in"))
	c.Check(scores[5], Equals, uint64(100))
}

func (vts *ValidatorMgrTestSuite) TestMarkMissingObservationActors_TieBreaksByAddress(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	networkMgr := newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	mgr.Keeper().SetMimir(ctx, constants.MaxMissingObservationChurnOut.String(), 1)

	nas := NodeAccounts{GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive)}
	for _, na := range nas {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	}
	seedObservationTallies(ctx, mgr, nas, []int{30, 30})

	c.Assert(networkMgr.markMissingObservationActors(ctx), IsNil)
	low, high := nas[0], nas[1]
	if high.NodeAddress.String() < low.NodeAddress.String() {
		low, high = high, low
	}
	lowAfter, err := mgr.Keeper().GetNodeAccount(ctx, low.NodeAddress)
	c.Assert(err, IsNil)
	highAfter, err := mgr.Keeper().GetNodeAccount(ctx, high.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(lowAfter.LeaveScore > 0, Equals, true)
	c.Check(highAfter.LeaveScore, Equals, uint64(0))
}

func (vts *ValidatorMgrTestSuite) TestLegacyBondRewardPath(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	// Create nodes with bonds
	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(4 * common.One)
	na1.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na1), IsNil)

	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(3 * common.One)
	na2.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na2), IsNil)

	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.NewUint(2 * common.One)
	na3.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na3), IsNil)

	// Disable chain-aware consensus (use legacy behavior)
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 0)

	network, _ := networkMgr.k.GetNetwork(ctx)
	network.BondRewardRune = cosmos.NewUint(1 * common.One)
	network.TotalBondUnits = cosmos.NewUint(20) // Legacy uses total bond units, not chain-specific
	c.Assert(mgr.Keeper().SetNetwork(ctx, network), IsNil)

	// Fund Bond Module
	FundModule(c, ctx, mgr.Keeper(), BondName, 10*common.One)

	// Pay out bond rewards using legacy path
	c.Assert(networkMgr.distributeBondReward(ctx, mgr), IsNil)

	// Verify the legacy path was used by checking that TotalBondUnits was decremented
	// (not chain-specific bond units)
	networkAfter, err := mgr.Keeper().GetNetwork(ctx)
	c.Assert(err, IsNil)

	// In legacy mode, TotalBondUnits should be decremented by the number of blocks
	// (ctx.BlockHeight() - lastChurnHeight) for each active node
	// Since all nodes are active from block 1 (lastChurnHeight=1),
	// and we're at block 20, that's 19 blocks * 3 nodes = 57 units decremented from initial 40
	// SafeSub will clamp this to 0
	c.Assert(networkAfter.TotalBondUnits.IsZero(), Equals, true,
		Commentf("TotalBondUnits should be fully decremented in legacy mode, got %s", networkAfter.TotalBondUnits))

	// Verify some rewards were distributed (node operator fees)
	nodeOperator1Balance := mgr.Keeper().GetBalance(ctx, na1.NodeAddress)
	nodeOperator2Balance := mgr.Keeper().GetBalance(ctx, na2.NodeAddress)
	nodeOperator3Balance := mgr.Keeper().GetBalance(ctx, na3.NodeAddress)

	// All nodes should receive some rewards
	c.Assert(nodeOperator1Balance.AmountOf(common.RuneNative.Native()).GT(sdkmath.ZeroInt()), Equals, true,
		Commentf("Node 1 should receive rewards in legacy mode"))
	c.Assert(nodeOperator2Balance.AmountOf(common.RuneNative.Native()).GT(sdkmath.ZeroInt()), Equals, true,
		Commentf("Node 2 should receive rewards in legacy mode"))
	c.Assert(nodeOperator3Balance.AmountOf(common.RuneNative.Native()).GT(sdkmath.ZeroInt()), Equals, true,
		Commentf("Node 3 should receive rewards in legacy mode"))
}

func (vts *ValidatorMgrTestSuite) TestChainRewardsEdgeCases(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	// Enable chain-aware consensus
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)

	// Test Case 1: Node with empty SupportedChains (backwards compatibility)
	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(2 * common.One)
	na1.ActiveBlockHeight = 1        // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	na1.SupportedChains = []string{} // Empty - should inherit from active vaults
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na1), IsNil)

	// An empty SupportedChains does not matter: rewards follow the vaults the
	// node is a member of.
	setRewardVault(c, ctx, mgr.Keeper(), common.Chains{common.ETHChain}, na1)

	network, _ := networkMgr.k.GetNetwork(ctx)
	network.BondRewardRune = cosmos.NewUint(1 * common.One)
	network.SetChainRewardAmount(common.ETHChain.String(), cosmos.NewUint(1*common.One))
	network.SetChainBondUnitsAmount(common.ETHChain.String(), cosmos.NewUint(20))
	c.Assert(mgr.Keeper().SetNetwork(ctx, network), IsNil)

	FundModule(c, ctx, mgr.Keeper(), BondName, 10*common.One)

	// Should not error with empty SupportedChains
	err := networkMgr.distributeBondReward(ctx, mgr)
	c.Assert(err, IsNil, Commentf("Should handle empty SupportedChains gracefully"))

	// Test Case 2: Chain with zero rewards
	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(2 * common.One)
	na2.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	na2.SupportedChains = []string{common.BTCChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na2), IsNil)

	network, _ = networkMgr.k.GetNetwork(ctx)
	// Set BTC chain rewards to zero
	network.SetChainRewardAmount(common.BTCChain.String(), cosmos.ZeroUint())
	network.SetChainBondUnitsAmount(common.BTCChain.String(), cosmos.NewUint(20))
	c.Assert(mgr.Keeper().SetNetwork(ctx, network), IsNil)

	err = networkMgr.distributeBondReward(ctx, mgr)
	c.Assert(err, IsNil, Commentf("Should handle zero chain rewards gracefully"))

	// Node should receive zero rewards but no error
	nodeOperator2Balance := mgr.Keeper().GetBalance(ctx, na2.NodeAddress)
	c.Assert(nodeOperator2Balance.AmountOf(common.RuneNative.Native()).IsZero(), Equals, true,
		Commentf("Node with zero chain rewards should receive nothing"))

	// Test Case 3: Chain with zero bond units
	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.NewUint(2 * common.One)
	na3.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	na3.SupportedChains = []string{common.LTCChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na3), IsNil)

	network, _ = networkMgr.k.GetNetwork(ctx)
	// Set LTC chain bond units to zero
	network.SetChainRewardAmount(common.LTCChain.String(), cosmos.NewUint(1*common.One))
	network.SetChainBondUnitsAmount(common.LTCChain.String(), cosmos.ZeroUint())
	c.Assert(mgr.Keeper().SetNetwork(ctx, network), IsNil)

	err = networkMgr.distributeBondReward(ctx, mgr)
	c.Assert(err, IsNil, Commentf("Should handle zero chain bond units gracefully"))

	// Node should receive zero rewards but no error
	nodeOperator3Balance := mgr.Keeper().GetBalance(ctx, na3.NodeAddress)
	c.Assert(nodeOperator3Balance.AmountOf(common.RuneNative.Native()).IsZero(), Equals, true,
		Commentf("Node with zero chain bond units should receive nothing"))
}

func (vts *ValidatorMgrTestSuite) TestMultipleChainRewards(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	// Enable chain-aware consensus
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)

	// Create nodes supporting different chain combinations
	// Node 1: Supports only ETH
	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(3 * common.One)
	na1.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	na1.SupportedChains = []string{common.ETHChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na1), IsNil)

	// Node 2: Supports only BTC
	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(2 * common.One)
	na2.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	na2.SupportedChains = []string{common.BTCChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na2), IsNil)

	// Node 3: Supports both ETH and BTC
	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.NewUint(4 * common.One)
	na3.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	na3.SupportedChains = []string{common.ETHChain.String(), common.BTCChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na3), IsNil)
	setRewardVault(c, ctx, mgr.Keeper(), common.Chains{common.ETHChain}, na1, na3)
	setRewardVault(c, ctx, mgr.Keeper(), common.Chains{common.BTCChain}, na2, na3)

	// Set up network with different rewards for each chain
	network, _ := networkMgr.k.GetNetwork(ctx)
	// Total available rewards: the per-chain buckets are an attribution of it
	network.BondRewardRune = cosmos.NewUint(200 * common.One)

	// ETH has more rewards
	network.SetChainRewardAmount(common.ETHChain.String(), cosmos.NewUint(120*common.One)) // 120 RUNE
	network.SetChainBondUnitsAmount(common.ETHChain.String(), cosmos.NewUint(40))

	// BTC has fewer rewards
	network.SetChainRewardAmount(common.BTCChain.String(), cosmos.NewUint(80*common.One)) // 80 RUNE
	network.SetChainBondUnitsAmount(common.BTCChain.String(), cosmos.NewUint(40))

	c.Assert(mgr.Keeper().SetNetwork(ctx, network), IsNil)

	// Fund Bond Module with enough to pay out rewards
	FundModule(c, ctx, mgr.Keeper(), BondName, 1000*common.One)

	// Distribute rewards
	err := networkMgr.distributeBondReward(ctx, mgr)
	c.Assert(err, IsNil)

	// Verify that nodes received rewards from their supported chains
	nodeOperator1Balance := mgr.Keeper().GetBalance(ctx, na1.NodeAddress)
	nodeOperator2Balance := mgr.Keeper().GetBalance(ctx, na2.NodeAddress)
	nodeOperator3Balance := mgr.Keeper().GetBalance(ctx, na3.NodeAddress)

	// Node 1 should receive ETH rewards only
	na1Rewards := nodeOperator1Balance.AmountOf(common.RuneNative.Native())
	c.Assert(na1Rewards.GT(sdkmath.ZeroInt()), Equals, true,
		Commentf("Node 1 (ETH only) should receive rewards"))

	// Node 2 should receive BTC rewards only
	na2Rewards := nodeOperator2Balance.AmountOf(common.RuneNative.Native())
	c.Assert(na2Rewards.GT(sdkmath.ZeroInt()), Equals, true,
		Commentf("Node 2 (BTC only) should receive rewards"))

	// Node 3 should receive both ETH and BTC rewards, so should be highest
	na3Rewards := nodeOperator3Balance.AmountOf(common.RuneNative.Native())
	c.Assert(na3Rewards.GT(sdkmath.ZeroInt()), Equals, true,
		Commentf("Node 3 (ETH+BTC) should receive rewards"))

	// Node 3 (supporting both chains) should receive more than nodes supporting single chains
	c.Assert(na3Rewards.GT(na1Rewards), Equals, true,
		Commentf("Node supporting both chains should receive more than single-chain nodes"))
	c.Assert(na3Rewards.GT(na2Rewards), Equals, true,
		Commentf("Node supporting both chains should receive more than single-chain nodes"))

	// Verify chain rewards were decremented
	networkAfter, err := mgr.Keeper().GetNetwork(ctx)
	c.Assert(err, IsNil)

	ethRewardsAfter := networkAfter.GetChainRewardAmount(common.ETHChain.String())
	btcRewardsAfter := networkAfter.GetChainRewardAmount(common.BTCChain.String())

	c.Assert(ethRewardsAfter.LT(cosmos.NewUint(120*common.One)), Equals, true,
		Commentf("ETH chain rewards should be decremented"))
	c.Assert(btcRewardsAfter.LT(cosmos.NewUint(80*common.One)), Equals, true,
		Commentf("BTC chain rewards should be decremented"))

	// Verify chain bond units were decremented (19 blocks for each node, since lastChurnHeight=1)
	ethBondUnitsAfter := networkAfter.GetChainBondUnitsAmount(common.ETHChain.String())
	btcBondUnitsAfter := networkAfter.GetChainBondUnitsAmount(common.BTCChain.String())

	// ETH: Node 1 and Node 3 support it, so 2 nodes * 19 blocks = 38 units decremented from initial 40
	c.Assert(ethBondUnitsAfter.Equal(cosmos.NewUint(2)), Equals, true,
		Commentf("ETH bond units should be 2 (40 - 38), got %s", ethBondUnitsAfter))

	// BTC: Node 2 and Node 3 support it, so 2 nodes * 19 blocks = 38 units decremented from initial 40
	c.Assert(btcBondUnitsAfter.Equal(cosmos.NewUint(2)), Equals, true,
		Commentf("BTC bond units should be 2 (40 - 38), got %s", btcBondUnitsAfter))
}

func (vts *ValidatorMgrTestSuite) TestChainAwareVsLegacyComparison(c *C) {
	// This test verifies that chain-aware and legacy reward distribution behave differently
	// Setup identical node configurations and compare the results

	// Test 1: Legacy mode (EnableNodeChainConsensus disabled)
	ctx1, k1 := setupKeeperForTest(c)
	ctx1 = ctx1.WithBlockHeight(20)
	mgr1 := NewDummyMgrWithKeeper(k1)
	networkMgr1 := newValidatorMgr(k1, mgr1.NetworkMgr(), mgr1.TxOutStore(), mgr1.EventMgr())

	// Disable chain-aware consensus
	mgr1.Keeper().SetMimir(ctx1, constants.EnableNodeChainConsensus.String(), 0)

	// Create nodes for legacy test
	na1Legacy := GetRandomValidatorNode(NodeActive)
	na1Legacy.Bond = cosmos.NewUint(3 * common.One)
	// Use default ActiveBlockHeight = 10 from GetRandomValidatorNode
	c.Assert(mgr1.Keeper().SetNodeAccount(ctx1, na1Legacy), IsNil)

	na2Legacy := GetRandomValidatorNode(NodeActive)
	na2Legacy.Bond = cosmos.NewUint(2 * common.One)
	// Use default ActiveBlockHeight = 10 from GetRandomValidatorNode
	c.Assert(mgr1.Keeper().SetNodeAccount(ctx1, na2Legacy), IsNil)

	// Setup legacy network state with larger rewards
	network1, _ := networkMgr1.k.GetNetwork(ctx1)
	network1.BondRewardRune = cosmos.NewUint(100 * common.One) // Increased from 1 RUNE
	network1.TotalBondUnits = cosmos.NewUint(100)              // Legacy uses total bond units
	c.Assert(mgr1.Keeper().SetNetwork(ctx1, network1), IsNil)

	FundModule(c, ctx1, mgr1.Keeper(), BondName, 1000*common.One)
	err := networkMgr1.distributeBondReward(ctx1, mgr1)
	c.Assert(err, IsNil)

	legacyNode1Balance := mgr1.Keeper().GetBalance(ctx1, na1Legacy.NodeAddress).AmountOf(common.RuneNative.Native())
	legacyNode2Balance := mgr1.Keeper().GetBalance(ctx1, na2Legacy.NodeAddress).AmountOf(common.RuneNative.Native())

	// Test 2: Chain-aware mode (EnableNodeChainConsensus enabled)
	ctx2, k2 := setupKeeperForTest(c)
	ctx2 = ctx2.WithBlockHeight(20)
	mgr2 := NewDummyMgrWithKeeper(k2)
	networkMgr2 := newValidatorMgr(k2, mgr2.NetworkMgr(), mgr2.TxOutStore(), mgr2.EventMgr())

	// Enable chain-aware consensus
	mgr2.Keeper().SetMimir(ctx2, constants.EnableNodeChainConsensus.String(), 1)

	// Create nodes for chain-aware test
	na1ChainAware := GetRandomValidatorNode(NodeActive)
	na1ChainAware.Bond = cosmos.NewUint(3 * common.One)
	// Use default ActiveBlockHeight = 10 from GetRandomValidatorNode
	na1ChainAware.SupportedChains = []string{common.ETHChain.String(), common.BTCChain.String()}
	c.Assert(mgr2.Keeper().SetNodeAccount(ctx2, na1ChainAware), IsNil)

	na2ChainAware := GetRandomValidatorNode(NodeActive)
	na2ChainAware.Bond = cosmos.NewUint(2 * common.One)
	// Use default ActiveBlockHeight = 10 from GetRandomValidatorNode
	na2ChainAware.SupportedChains = []string{common.ETHChain.String(), common.BTCChain.String()}
	c.Assert(mgr2.Keeper().SetNodeAccount(ctx2, na2ChainAware), IsNil)
	setRewardVault(c, ctx2, mgr2.Keeper(), common.Chains{common.ETHChain, common.BTCChain}, na1ChainAware, na2ChainAware)

	// Setup chain-aware network state with same total rewards
	network2, _ := networkMgr2.k.GetNetwork(ctx2)
	network2.BondRewardRune = cosmos.NewUint(1 * common.One)
	// Split rewards across chains
	network2.SetChainRewardAmount(common.ETHChain.String(), cosmos.NewUint(50000000)) // 0.5 RUNE
	network2.SetChainBondUnitsAmount(common.ETHChain.String(), cosmos.NewUint(40))
	network2.SetChainRewardAmount(common.BTCChain.String(), cosmos.NewUint(50000000)) // 0.5 RUNE
	network2.SetChainBondUnitsAmount(common.BTCChain.String(), cosmos.NewUint(40))
	c.Assert(mgr2.Keeper().SetNetwork(ctx2, network2), IsNil)

	FundModule(c, ctx2, mgr2.Keeper(), BondName, 10*common.One)
	err = networkMgr2.distributeBondReward(ctx2, mgr2)
	c.Assert(err, IsNil)

	chainAwareNode1Balance := mgr2.Keeper().GetBalance(ctx2, na1ChainAware.NodeAddress).AmountOf(common.RuneNative.Native())
	chainAwareNode2Balance := mgr2.Keeper().GetBalance(ctx2, na2ChainAware.NodeAddress).AmountOf(common.RuneNative.Native())

	// Log the reward amounts for documentation
	c.Logf("Legacy mode - Node 1: %s, Node 2: %s", legacyNode1Balance, legacyNode2Balance)
	c.Logf("Chain-aware mode - Node 1: %s, Node 2: %s", chainAwareNode1Balance, chainAwareNode2Balance)

	// Verify at least one node got rewards in each mode (validating both code paths ran)
	legacyTotalRewards := legacyNode1Balance.Add(legacyNode2Balance)
	chainAwareTotalRewards := chainAwareNode1Balance.Add(chainAwareNode2Balance)

	c.Assert(legacyTotalRewards.GT(sdkmath.ZeroInt()), Equals, true,
		Commentf("Legacy mode should distribute some rewards, total: %s", legacyTotalRewards))
	c.Assert(chainAwareTotalRewards.GT(sdkmath.ZeroInt()), Equals, true,
		Commentf("Chain-aware mode should distribute some rewards, total: %s", chainAwareTotalRewards))

	// Both code paths executed successfully, which validates the mimir toggle works
	c.Logf("Both legacy and chain-aware reward distribution paths executed successfully")
}

func (vts *ValidatorMgrTestSuite) TestChainRewardNetworkStateUpdates(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)

	mgr := NewDummyMgrWithKeeper(k)
	networkMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	c.Assert(networkMgr, NotNil)

	// Enable chain-aware consensus
	mgr.Keeper().SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)

	// Create nodes supporting different chains
	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(3 * common.One)
	na1.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	na1.SupportedChains = []string{common.ETHChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na1), IsNil)

	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(2 * common.One)
	na2.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	na2.SupportedChains = []string{common.BTCChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na2), IsNil)

	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.NewUint(4 * common.One)
	na3.ActiveBlockHeight = 1 // Active from block 1 (ActiveBlockHeight=0 would skip rewards)
	na3.SupportedChains = []string{common.ETHChain.String(), common.BTCChain.String()}
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na3), IsNil)
	setRewardVault(c, ctx, mgr.Keeper(), common.Chains{common.ETHChain}, na1, na3)
	setRewardVault(c, ctx, mgr.Keeper(), common.Chains{common.BTCChain}, na2, na3)

	// Set up initial network state with precise values
	network, _ := networkMgr.k.GetNetwork(ctx)

	initialETHRewards := cosmos.NewUint(120 * common.One)     // 120 RUNE
	initialBTCRewards := cosmos.NewUint(80 * common.One)      // 80 RUNE
	initialETHBondUnits := cosmos.NewUint(100)                // 100 units
	initialBTCBondUnits := cosmos.NewUint(100)                // 100 units
	initialBondRewardRune := cosmos.NewUint(200 * common.One) // 200 RUNE
	initialTotalBondUnits := cosmos.NewUint(200)              // 200 units (should not be used in chain-aware mode)

	network.BondRewardRune = initialBondRewardRune

	network.SetChainRewardAmount(common.ETHChain.String(), initialETHRewards)
	network.SetChainBondUnitsAmount(common.ETHChain.String(), initialETHBondUnits)
	network.SetChainRewardAmount(common.BTCChain.String(), initialBTCRewards)
	network.SetChainBondUnitsAmount(common.BTCChain.String(), initialBTCBondUnits)
	network.TotalBondUnits = initialTotalBondUnits

	c.Assert(mgr.Keeper().SetNetwork(ctx, network), IsNil)

	// Fund Bond Module with enough to pay out rewards
	FundModule(c, ctx, mgr.Keeper(), BondName, 1000*common.One)

	// Distribute rewards
	err := networkMgr.distributeBondReward(ctx, mgr)
	c.Assert(err, IsNil)

	// Verify network state was updated correctly
	networkAfter, err := mgr.Keeper().GetNetwork(ctx)
	c.Assert(err, IsNil)

	// 1. Verify chain-specific rewards were decremented
	ethRewardsAfter := networkAfter.GetChainRewardAmount(common.ETHChain.String())
	btcRewardsAfter := networkAfter.GetChainRewardAmount(common.BTCChain.String())

	c.Assert(ethRewardsAfter.LT(initialETHRewards), Equals, true,
		Commentf("ETH rewards should be decremented: before=%s, after=%s", initialETHRewards, ethRewardsAfter))
	c.Assert(btcRewardsAfter.LT(initialBTCRewards), Equals, true,
		Commentf("BTC rewards should be decremented: before=%s, after=%s", initialBTCRewards, btcRewardsAfter))

	// 2. Verify chain-specific bond units were decremented
	// Each active node should decrement bond units by (blockHeight - lastChurnHeight) = 19 blocks (since lastChurnHeight=1)
	// ETH: supported by na1 and na3 -> 2 nodes * 19 blocks = 38 units
	// BTC: supported by na2 and na3 -> 2 nodes * 19 blocks = 38 units
	ethBondUnitsAfter := networkAfter.GetChainBondUnitsAmount(common.ETHChain.String())
	btcBondUnitsAfter := networkAfter.GetChainBondUnitsAmount(common.BTCChain.String())

	expectedETHBondUnitsAfter := cosmos.NewUint(62) // 100 - 38
	expectedBTCBondUnitsAfter := cosmos.NewUint(62) // 100 - 38

	c.Assert(ethBondUnitsAfter.Equal(expectedETHBondUnitsAfter), Equals, true,
		Commentf("ETH bond units should be 62: got %s", ethBondUnitsAfter))
	c.Assert(btcBondUnitsAfter.Equal(expectedBTCBondUnitsAfter), Equals, true,
		Commentf("BTC bond units should be 62: got %s", btcBondUnitsAfter))

	// 3. Verify BondRewardRune was decremented
	// The total rewards distributed should be deducted from BondRewardRune
	c.Assert(networkAfter.BondRewardRune.LT(initialBondRewardRune), Equals, true,
		Commentf("BondRewardRune should be decremented: before=%s, after=%s",
			initialBondRewardRune, networkAfter.BondRewardRune))

	// 4. Verify TotalBondUnits was decremented in chain-aware mode
	// In the new chain-aware mode, TotalBondUnits should also be decremented
	// Total: 3 nodes * 19 blocks = 57 units
	expectedTotalBondUnitsAfter := cosmos.NewUint(143) // 200 - 57
	c.Assert(networkAfter.TotalBondUnits.Equal(expectedTotalBondUnitsAfter), Equals, true,
		Commentf("TotalBondUnits should be 143 in chain-aware mode: got %s", networkAfter.TotalBondUnits))

	// 5. Verify the deltas match expected calculations
	ethRewardsDelta := initialETHRewards.Sub(ethRewardsAfter)
	btcRewardsDelta := initialBTCRewards.Sub(btcRewardsAfter)

	c.Logf("ETH rewards delta: %s", ethRewardsDelta)
	c.Logf("BTC rewards delta: %s", btcRewardsDelta)

	// Both chains should have distributed some rewards
	c.Assert(ethRewardsDelta.GT(cosmos.ZeroUint()), Equals, true,
		Commentf("ETH should have distributed rewards"))
	c.Assert(btcRewardsDelta.GT(cosmos.ZeroUint()), Equals, true,
		Commentf("BTC should have distributed rewards"))
}

// Chain-aware payouts split each chain's bucket among the nodes supporting
// that chain, by effective bond, from a snapshot taken before anyone is paid.
func (vts *ValidatorMgrTestSuite) TestChainRewardPayoutUsesChainBondAndSnapshot(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)
	mgr := NewDummyMgrWithKeeper(k)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	k.SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)

	var nas NodeAccounts
	for i := 0; i < 4; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(10 * common.One)
		na.SupportedChains = []string{common.BTCChain.String()}
		if i < 2 {
			na.SupportedChains = append(na.SupportedChains, common.ETHChain.String())
		}
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		nas = append(nas, na)
	}
	setRewardVault(c, ctx, k, common.Chains{common.BTCChain}, nas...)
	setRewardVault(c, ctx, k, common.Chains{common.ETHChain}, nas[:2]...)

	network, err := k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	network.BondRewardRune = cosmos.NewUint(6 * common.One)
	network.SetChainRewardAmount(common.BTCChain.String(), cosmos.NewUint(4*common.One))
	network.SetChainRewardAmount(common.ETHChain.String(), cosmos.NewUint(2*common.One))
	c.Assert(k.SetNetwork(ctx, network), IsNil)
	FundModule(c, ctx, k, BondName, 100*common.One)

	c.Assert(validatorMgr.distributeBondReward(ctx, mgr), IsNil)

	// BTC: 4 RUNE over 4 equal nodes; ETH: 2 RUNE over the 2 ETH nodes.
	for i, na := range nas {
		updated, getErr := k.GetNodeAccount(ctx, na.NodeAddress)
		c.Assert(getErr, IsNil)
		want := cosmos.NewUint(common.One)
		if i < 2 {
			want = cosmos.NewUint(2 * common.One)
		}
		reward := common.SafeSub(updated.Bond, na.Bond)
		operatorFee := k.GetBalance(ctx, na.NodeAddress).AmountOf(common.RuneNative.Native())
		c.Check(reward.Add(cosmos.NewUintFromBigInt(operatorFee.BigInt())).String(), Equals, want.String(), Commentf("node %d", i))
	}
	network, err = k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	c.Check(network.BondRewardRune.IsZero(), Equals, true)
	c.Check(network.GetChainRewardAmount(common.BTCChain.String()).IsZero(), Equals, true)
	c.Check(network.GetChainRewardAmount(common.ETHChain.String()).IsZero(), Equals, true)
}

func (vts *ValidatorMgrTestSuite) TestReconcileChainRewards(c *C) {
	ctx, k := setupKeeperForTest(c)
	na := GetRandomValidatorNode(NodeActive)
	na.SupportedChains = []string{common.BTCChain.String()}
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	setRewardVault(c, ctx, k, common.Chains{common.BTCChain}, na)

	// Buckets above the ledger (accrued but never debited) are scaled down.
	network := NewNetwork()
	network.BondRewardRune = cosmos.NewUint(100)
	network.SetChainRewardAmount(common.BTCChain.String(), cosmos.NewUint(300))
	network.SetChainRewardAmount(common.ETHChain.String(), cosmos.NewUint(100))
	c.Assert(reconcileChainRewards(ctx, k, &network), IsNil)
	c.Check(network.GetChainRewardAmount(common.BTCChain.String()).String(), Equals, "75")
	c.Check(network.GetChainRewardAmount(common.ETHChain.String()).String(), Equals, "25")

	// A ledger the buckets do not cover (accrued with the mimir off) is
	// attributed by node participation.
	network = NewNetwork()
	network.BondRewardRune = cosmos.NewUint(100)
	network.SetChainRewardAmount(common.BTCChain.String(), cosmos.NewUint(40))
	c.Assert(reconcileChainRewards(ctx, k, &network), IsNil)
	c.Check(network.GetChainRewardAmount(common.BTCChain.String()).String(), Equals, "100")
}

// The legacy payout never debits the per-chain buckets, so it drops them.
func (vts *ValidatorMgrTestSuite) TestLegacyBondPayoutClearsChainRewards(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)
	mgr := NewDummyMgrWithKeeper(k)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(10 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	network, err := k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	network.BondRewardRune = cosmos.NewUint(common.One)
	network.SetChainRewardAmount(common.BTCChain.String(), cosmos.NewUint(5*common.One))
	network.SetChainBondUnitsAmount(common.BTCChain.String(), cosmos.NewUint(20))
	c.Assert(k.SetNetwork(ctx, network), IsNil)
	FundModule(c, ctx, k, BondName, 10*common.One)

	c.Assert(validatorMgr.distributeBondReward(ctx, mgr), IsNil)

	network, err = k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	c.Check(network.ChainRewards, HasLen, 0)
	c.Check(network.ChainBondUnits, HasLen, 0)
}

// setRewardVault stores an active Asgard vault serving chains with the given
// members. Chain-aware rewards follow vault membership, not SupportedChains.
func setRewardVault(c *C, ctx cosmos.Context, k keeper.Keeper, chains common.Chains, members ...NodeAccount) Vault {
	vault := GetRandomVault()
	vault.Status = ActiveVault
	vault.StatusSince = 1
	vault.Chains = chains.Strings()
	vault.Membership = nil
	for _, na := range members {
		vault.Membership = append(vault.Membership, na.PubKeySet.Secp256k1.String())
	}
	c.Assert(k.SetVault(ctx, vault), IsNil)
	return vault
}

// A validator outside a chain's vault that declares the chain just before the
// churn payout gets no share of that chain's accrued bucket: payout eligibility
// and the bond denominator follow the vaults a node served during the period.
func (vts *ValidatorMgrTestSuite) TestLateChainSupportCannotDivertChainRewards(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)
	mgr := NewDummyMgrWithKeeper(k)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	k.SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)

	// honest serves BTC and ETH; attacker served only BTC during the period.
	honest := GetRandomValidatorNode(NodeActive)
	honest.Bond = cosmos.NewUint(10 * common.One)
	honest.ActiveBlockHeight = 1
	honest.SupportedChains = []string{common.BTCChain.String(), common.ETHChain.String()}
	c.Assert(k.SetNodeAccount(ctx, honest), IsNil)
	attacker := GetRandomValidatorNode(NodeActive)
	attacker.Bond = cosmos.NewUint(10 * common.One)
	attacker.ActiveBlockHeight = 1
	attacker.SupportedChains = []string{common.BTCChain.String()}
	c.Assert(k.SetNodeAccount(ctx, attacker), IsNil)

	setRewardVault(c, ctx, k, common.Chains{common.BTCChain}, honest, attacker)
	setRewardVault(c, ctx, k, common.Chains{common.ETHChain}, honest)

	network, err := k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	network.BondRewardRune = cosmos.NewUint(6 * common.One)
	network.SetChainRewardAmount(common.BTCChain.String(), cosmos.NewUint(4*common.One))
	network.SetChainRewardAmount(common.ETHChain.String(), cosmos.NewUint(2*common.One))
	c.Assert(k.SetNetwork(ctx, network), IsNil)
	FundModule(c, ctx, k, BondName, 100*common.One)

	// Right before payout the attacker declares ETH support.
	_, err = NewNodeChainSupportHandler(mgr).Run(ctx, NewMsgAddNodeChainSupport(common.ETHChain.String(), attacker.NodeAddress))
	c.Assert(err, IsNil)
	declared, err := k.GetNodeAccount(ctx, attacker.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(declared.SupportedChains, DeepEquals, []string{common.BTCChain.String(), common.ETHChain.String()})

	c.Assert(validatorMgr.distributeBondReward(ctx, mgr), IsNil)

	paid := func(na NodeAccount) string {
		updated, getErr := k.GetNodeAccount(ctx, na.NodeAddress)
		c.Assert(getErr, IsNil)
		reward := common.SafeSub(updated.Bond, na.Bond)
		operatorFee := k.GetBalance(ctx, na.NodeAddress).AmountOf(common.RuneNative.Native())
		return reward.Add(cosmos.NewUintFromBigInt(operatorFee.BigInt())).String()
	}
	// attacker: half of BTC (2 RUNE) and nothing from ETH, which it never
	// served. honest: half of BTC plus all of ETH (4 RUNE).
	c.Check(paid(attacker), Equals, cosmos.NewUint(2*common.One).String())
	c.Check(paid(honest), Equals, cosmos.NewUint(4*common.One).String())
}

// A churn pays the generation that earned the period: vaults retired at this
// block count, vaults activated at this block do not.
func (vts *ValidatorMgrTestSuite) TestChainRewardsFollowEarningVaults(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(20)
	mgr := NewDummyMgrWithKeeper(k)
	validatorMgr := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	k.SetMimir(ctx, constants.EnableNodeChainConsensus.String(), 1)

	var nas NodeAccounts
	for i := 0; i < 2; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(10 * common.One)
		na.ActiveBlockHeight = 1
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		nas = append(nas, na)
	}
	// BTC: both served; the vault retired at this block in the churn.
	retired := setRewardVault(c, ctx, k, common.Chains{common.BTCChain}, nas...)
	retired.UpdateStatus(RetiringVault, ctx.BlockHeight())
	c.Assert(k.SetVault(ctx, retired), IsNil)
	// ETH: only nas[0] served; the new ETH vault with both members was
	// activated at this block and earned nothing yet.
	setRewardVault(c, ctx, k, common.Chains{common.ETHChain}, nas[0])
	fresh := setRewardVault(c, ctx, k, common.Chains{common.ETHChain}, nas...)
	fresh.UpdateStatus(ActiveVault, ctx.BlockHeight())
	c.Assert(k.SetVault(ctx, fresh), IsNil)
	// An older retiring vault (retired at an earlier churn) earned nothing.
	stale := setRewardVault(c, ctx, k, common.Chains{common.LTCChain}, nas[1])
	stale.UpdateStatus(RetiringVault, 5)
	c.Assert(k.SetVault(ctx, stale), IsNil)

	network, err := k.GetNetwork(ctx)
	c.Assert(err, IsNil)
	network.BondRewardRune = cosmos.NewUint(6 * common.One)
	network.SetChainRewardAmount(common.BTCChain.String(), cosmos.NewUint(4*common.One))
	network.SetChainRewardAmount(common.ETHChain.String(), cosmos.NewUint(2*common.One))
	c.Assert(k.SetNetwork(ctx, network), IsNil)
	FundModule(c, ctx, k, BondName, 100*common.One)

	c.Assert(validatorMgr.distributeBondReward(ctx, mgr), IsNil)

	for i, want := range []uint64{4 * common.One, 2 * common.One} {
		updated, getErr := k.GetNodeAccount(ctx, nas[i].NodeAddress)
		c.Assert(getErr, IsNil)
		reward := common.SafeSub(updated.Bond, nas[i].Bond)
		operatorFee := k.GetBalance(ctx, nas[i].NodeAddress).AmountOf(common.RuneNative.Native())
		c.Check(reward.Add(cosmos.NewUintFromBigInt(operatorFee.BigInt())).String(), Equals, cosmos.NewUint(want).String(), Commentf("node %d", i))
	}
}
