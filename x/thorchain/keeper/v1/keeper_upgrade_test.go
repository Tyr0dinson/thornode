package keeperv1

import (
	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type KeeperUpgradeSuite struct{}

var _ = Suite(&KeeperUpgradeSuite{})

func (s *KeeperUpgradeSuite) TestUpgrade(c *C) {
	ctx, k := setupKeeperForTest(c)

	// Add node accounts
	na1 := GetRandomValidatorNode(NodeActive)
	na1.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na1), IsNil)
	na2 := GetRandomValidatorNode(NodeActive)
	na2.Bond = cosmos.NewUint(200 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na2), IsNil)
	na3 := GetRandomValidatorNode(NodeActive)
	na3.Bond = cosmos.NewUint(300 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na3), IsNil)
	na4 := GetRandomValidatorNode(NodeActive)
	na4.Bond = cosmos.NewUint(400 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na4), IsNil)
	na5 := GetRandomValidatorNode(NodeActive)
	na5.Bond = cosmos.NewUint(500 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na5), IsNil)
	na6 := GetRandomValidatorNode(NodeActive)
	na6.Bond = cosmos.NewUint(600 * common.One)
	c.Assert(k.SetNodeAccount(ctx, na6), IsNil)

	const (
		upgradeName = "berserk"
		upgradeInfo = "scheduled upgrade"
	)

	upgradeHeight := ctx.BlockHeight() + 100

	// propose upgrade
	c.Assert(k.ProposeUpgrade(ctx, upgradeName, types.Upgrade{
		Height: upgradeHeight,
		Info:   upgradeInfo,
	}), IsNil)

	k.ApproveUpgrade(ctx, na1.NodeAddress, upgradeName)
	k.ApproveUpgrade(ctx, na2.NodeAddress, upgradeName)
	k.ApproveUpgrade(ctx, na3.NodeAddress, upgradeName)
	k.ApproveUpgrade(ctx, na4.NodeAddress, upgradeName)
	k.ApproveUpgrade(ctx, na5.NodeAddress, upgradeName)

	var proposals int
	var proposal *types.Upgrade
	var proposalKey []byte
	pIter := k.GetUpgradeProposalIterator(ctx)
	for ; pIter.Valid(); pIter.Next() {
		proposals++
		k.cdc.MustUnmarshal(pIter.Value(), proposal)
		proposalKey = pIter.Key()
	}

	c.Assert(proposals, Equals, 1)
	c.Assert(proposalKey, DeepEquals, []byte("upgrade_proposals/berserk"))
	c.Assert(proposal.Height, Equals, upgradeHeight)
	c.Assert(proposal.Info, Equals, upgradeInfo)

	proposal, err := k.GetProposedUpgrade(ctx, upgradeName)
	c.Assert(err, IsNil)
	c.Assert(proposal.Height, Equals, upgradeHeight)
	c.Assert(proposal.Info, Equals, upgradeInfo)

	var approves int
	vIter := k.GetUpgradeVoteIterator(ctx, upgradeName)
	for ; vIter.Valid(); vIter.Next() {
		c.Assert(vIter.Value(), DeepEquals, []byte{0x1})
		approves++
	}

	c.Assert(approves, Equals, 5)

	c.Assert(k.ScheduleUpgrade(ctx, upgradetypes.Plan{
		Name:   upgradeName,
		Height: upgradeHeight,
		Info:   upgradeInfo,
	}), IsNil)

	plan, ok := k.GetUpgradePlan(ctx)
	c.Assert(ok, Equals, true)
	c.Assert(plan.Name, Equals, upgradeName)
	c.Assert(plan.Height, Equals, upgradeHeight)
	c.Assert(plan.Info, Equals, upgradeInfo)

	approved, approvedPct, valsToQuorum, err := UpgradeApprovedByMajority(ctx, k, upgradeName)
	c.Assert(err, IsNil)
	c.Assert(approved, Equals, true)
	c.Assert(approvedPct, Equals, "0.8333333333333334")
	c.Assert(valsToQuorum, Equals, uint64(0))

	k.RejectUpgrade(ctx, na2.NodeAddress, upgradeName)

	approved, approvedPct, valsToQuorum, err = UpgradeApprovedByMajority(ctx, k, upgradeName)
	c.Assert(err, IsNil)
	c.Assert(approved, Equals, true)
	c.Assert(approvedPct, Equals, "0.6666666666666666")
	c.Assert(valsToQuorum, Equals, uint64(0))

	k.RejectUpgrade(ctx, na1.NodeAddress, upgradeName)

	k.ClearUpgradePlan(ctx)
	_, ok = k.GetUpgradePlan(ctx)
	c.Assert(ok, Equals, false)

	approved, approvedPct, valsToQuorum, err = UpgradeApprovedByMajority(ctx, k, upgradeName)
	c.Assert(err, IsNil)
	c.Assert(approved, Equals, false)
	c.Assert(approvedPct, Equals, "0.5")
	c.Assert(valsToQuorum, Equals, uint64(1))

	k.RejectUpgrade(ctx, na3.NodeAddress, upgradeName)
	k.RejectUpgrade(ctx, na4.NodeAddress, upgradeName)
	k.RejectUpgrade(ctx, na5.NodeAddress, upgradeName)
	k.RejectUpgrade(ctx, na6.NodeAddress, upgradeName)

	approved, approvedPct, valsToQuorum, err = UpgradeApprovedByMajority(ctx, k, upgradeName)
	c.Assert(err, IsNil)
	c.Assert(approved, Equals, false)
	c.Assert(approvedPct, Equals, "0")
	c.Assert(valsToQuorum, Equals, uint64(4))

	var rejects int
	vIter = k.GetUpgradeVoteIterator(ctx, upgradeName)
	for ; vIter.Valid(); vIter.Next() {
		c.Assert(vIter.Value(), DeepEquals, []byte{0xFF})
		rejects++
	}

	c.Assert(rejects, Equals, 6)

	ctx = ctx.WithBlockHeight(upgradeHeight + 1)

	c.Assert(k.RemoveExpiredUpgradeProposals(ctx), IsNil)

	_, err = k.GetProposedUpgrade(ctx, upgradeName)
	c.Assert(err, NotNil)

	proposals = 0
	pIter = k.GetUpgradeProposalIterator(ctx)
	for ; pIter.Valid(); pIter.Next() {
		proposals++
	}

	c.Assert(proposals, Equals, 0)

	var votes int
	vIter = k.GetUpgradeVoteIterator(ctx, upgradeName)
	for ; vIter.Valid(); vIter.Next() {
		votes++
	}

	c.Assert(votes, Equals, 0)
}
