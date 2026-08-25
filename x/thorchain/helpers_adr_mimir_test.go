package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type ADRMimirSuite struct{}

var _ = Suite(&ADRMimirSuite{})

func (s *ADRMimirSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

// addActiveNodes creates n active validators and returns them.
func addActiveNodes(c *C, ctx cosmos.Context, k keeper.Keeper, n int) NodeAccounts {
	nas := NodeAccounts{}
	for i := 0; i < n; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Bond = cosmos.NewUint(1000 * common.One)
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		nas = append(nas, na)
	}
	return nas
}

func (s *ADRMimirSuite) TestIsADRMimir(c *C) {
	c.Check(isADRMimir("ADR12FOO"), Equals, true)
	c.Check(isADRMimir("adr12foo"), Equals, true)
	c.Check(isADRMimir("ADR"), Equals, true)
	c.Check(isADRMimir("QUADRANT"), Equals, false)
	c.Check(isADRMimir("HALTBTCCHAIN"), Equals, false)
	c.Check(isADRMimir(""), Equals, false)
}

// ADR keys must never be classified operational, or their votes get purged at churn and
// they'd settle on a plurality instead of a supermajority.
func (s *ADRMimirSuite) TestADRIsNeverOperational(c *C) {
	c.Check(constants.IsOperationalMimir("ADR12HALTSOMETHING"), Equals, false)
	c.Check(constants.IsOperationalMimir("ADR99REVSHARE"), Equals, false)
	c.Check(constants.IsOperationalMimir("HALTBTCCHAIN"), Equals, true)
	// the kill switch itself must stay operational
	c.Check(constants.IsOperationalMimir(constants.RequiredVotingEnabled.String()), Equals, true)
}

// The pending window opens at 1/3+1 turnout and closes at supermajority or full turnout.
func (s *ADRMimirSuite) TestPendingADRVotesThresholds(c *C) {
	ctx, k := setupKeeperForTest(c)
	nas := addActiveNodes(c, ctx, k, 9)
	const key = "ADR42TEST"

	// 3 of 9 is exactly 1/3 - not enough
	for i := 0; i < 3; i++ {
		c.Assert(k.SetNodeMimir(ctx, key, 1, nas[i].NodeAddress), IsNil)
	}
	pending, err := pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Check(pending, HasLen, 0)

	// 4 of 9 crosses 1/3+1
	c.Assert(k.SetNodeMimir(ctx, key, 1, nas[3].NodeAddress), IsNil)
	pending, err = pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Assert(pending, HasLen, 1)
	c.Check(pending[0].Key, Equals, key)

	// 5 of 9 still short of the 2/3 (6) supermajority
	c.Assert(k.SetNodeMimir(ctx, key, 1, nas[4].NodeAddress), IsNil)
	pending, err = pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Check(pending, HasLen, 1)

	// 6 of 9 on the same value forms supermajority, clearing the requirement
	c.Assert(k.SetNodeMimir(ctx, key, 1, nas[5].NodeAddress), IsNil)
	pending, err = pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Check(pending, HasLen, 0)
}

// A fully-voted but split key clears too: nobody is left to require anything of.
func (s *ADRMimirSuite) TestPendingADRVotesFullTurnoutSplit(c *C) {
	ctx, k := setupKeeperForTest(c)
	nas := addActiveNodes(c, ctx, k, 6)
	const key = "ADR42SPLIT"

	for i, na := range nas {
		c.Assert(k.SetNodeMimir(ctx, key, int64(i%3), na.NodeAddress), IsNil)
	}
	pending, err := pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Check(pending, HasLen, 0)
}

func (s *ADRMimirSuite) TestPendingADRVotesIgnoresNonADRAndKillSwitch(c *C) {
	ctx, k := setupKeeperForTest(c)
	nas := addActiveNodes(c, ctx, k, 9)

	// non-ADR key with the same turnout must never be pending
	for i := 0; i < 4; i++ {
		c.Assert(k.SetNodeMimir(ctx, "SOMEECONOMICKEY", 1, nas[i].NodeAddress), IsNil)
	}
	pending, err := pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Check(pending, HasLen, 0)

	for i := 0; i < 4; i++ {
		c.Assert(k.SetNodeMimir(ctx, "ADR42TEST", 1, nas[i].NodeAddress), IsNil)
	}
	pending, err = pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Check(pending, HasLen, 1)

	// kill switch off disables enforcement entirely
	k.SetMimir(ctx, constants.RequiredVotingEnabled.String(), 0)
	pending, err = pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Check(pending, HasLen, 0)
}

// Standby votes are stored but must not count toward turnout or consensus.
func (s *ADRMimirSuite) TestStandbyVotesDoNotCount(c *C) {
	ctx, k := setupKeeperForTest(c)
	nas := addActiveNodes(c, ctx, k, 9)
	const key = "ADR42TEST"

	standby := GetRandomValidatorNode(NodeStandby)
	c.Assert(k.SetNodeAccount(ctx, standby), IsNil)

	for i := 0; i < 3; i++ {
		c.Assert(k.SetNodeMimir(ctx, key, 1, nas[i].NodeAddress), IsNil)
	}
	c.Assert(k.SetNodeMimir(ctx, key, 1, standby.NodeAddress), IsNil)

	// 3 active + 1 standby is still only 3 of 9 - below the 1/3+1 trigger
	pending, err := pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Check(pending, HasLen, 0)

	mimirs, err := k.GetNodeMimirs(ctx, key)
	c.Assert(err, IsNil)
	c.Check(mimirs.VotersOf(key, nas.GetNodeAddresses()), Equals, 3)
	c.Check(mimirs.Has(key, standby.NodeAddress), Equals, true)
}

func (s *ADRMimirSuite) TestNodeHasPendingADRVote(c *C) {
	ctx, k := setupKeeperForTest(c)
	nas := addActiveNodes(c, ctx, k, 9)
	const key = "ADR42TEST"

	standby := GetRandomValidatorNode(NodeStandby)
	c.Assert(k.SetNodeAccount(ctx, standby), IsNil)

	for i := 0; i < 4; i++ {
		c.Assert(k.SetNodeMimir(ctx, key, 1, nas[i].NodeAddress), IsNil)
	}

	pending, err := pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)

	got, ok := nodeHasPendingADRVote(pending, standby.NodeAddress)
	c.Check(ok, Equals, true)
	c.Check(got, Equals, key)

	// a node that has voted is clear
	_, ok = nodeHasPendingADRVote(pending, nas[0].NodeAddress)
	c.Check(ok, Equals, false)

	// once it votes, the standby node is clear too
	c.Assert(k.SetNodeMimir(ctx, key, 1, standby.NodeAddress), IsNil)
	pending, err = pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	_, ok = nodeHasPendingADRVote(pending, standby.NodeAddress)
	c.Check(ok, Equals, false)
}

// A standby node that hasn't voted must not be promoted to Ready, and the reason has to
// be visible on the preflight status (which is what the node API surfaces).
func (s *ADRMimirSuite) TestPreflightBlocksNonVotingStandby(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	nas := addActiveNodes(c, ctx, k, 9)
	const key = "ADR42TEST"

	minBond := mgr.Keeper().GetConfigInt64(ctx, constants.MinimumBondInRune)
	standby := GetRandomValidatorNode(NodeStandby)
	standby.Bond = cosmos.NewUint(uint64(minBond))
	standby.Version = GetCurrentVersion().String()
	c.Assert(k.SetNodeAccount(ctx, standby), IsNil)

	// the pending set is recomputed each time, as the real callers do once per loop
	preflight := func() (NodeStatus, error) {
		pending, err := pendingADRVotes(ctx, k)
		c.Assert(err, IsNil)
		return vm.NodeAccountPreflightCheck(ctx, standby, pending)
	}

	// no pending ADR - the node is ready
	status, err := preflight()
	c.Assert(err, IsNil)
	c.Check(status, Equals, NodeReady)

	for i := 0; i < 4; i++ {
		c.Assert(k.SetNodeMimir(ctx, key, 1, nas[i].NodeAddress), IsNil)
	}

	status, err = preflight()
	c.Assert(err, NotNil)
	c.Check(status, Equals, NodeStandby)
	c.Check(err.Error(), Equals, "node account has not voted on required mimir: "+key)

	// voting clears it
	c.Assert(k.SetNodeMimir(ctx, key, 1, standby.NodeAddress), IsNil)
	status, err = preflight()
	c.Assert(err, IsNil)
	c.Check(status, Equals, NodeReady)
}

func (s *ADRMimirSuite) TestMarkNonVotingActors(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	nas := addActiveNodes(c, ctx, k, 9)
	const key = "ADR42TEST"

	markedCount := func() int {
		active, err := k.ListActiveValidators(ctx)
		c.Assert(err, IsNil)
		n := 0
		for _, na := range active {
			if na.LeaveScore > 0 {
				n++
			}
		}
		return n
	}
	markedAddr := func() string {
		active, err := k.ListActiveValidators(ctx)
		c.Assert(err, IsNil)
		for _, na := range active {
			if na.LeaveScore > 0 {
				return na.NodeAddress.String()
			}
		}
		return ""
	}
	clearMarks := func() {
		active, err := k.ListActiveValidators(ctx)
		c.Assert(err, IsNil)
		for _, na := range active {
			na.LeaveScore = 0
			c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		}
	}

	// WithBlockHeader replaces the whole header, so keep the height: markActor derives
	// LeaveScore from (blockHeight - lastChurnHeight) and a zero score reads as unmarked.
	withAppHash := func(ctx cosmos.Context, hash []byte) cosmos.Context {
		header := ctx.BlockHeader()
		header.AppHash = hash
		return ctx.WithBlockHeader(header)
	}
	ctx = withAppHash(ctx.WithBlockHeight(100), []byte{0x01, 0x02, 0x03, 0x04})

	// below the trigger, nothing is marked
	for i := 0; i < 3; i++ {
		c.Assert(k.SetNodeMimir(ctx, key, 1, nas[i].NodeAddress), IsNil)
	}
	c.Assert(vm.markNonVotingActors(ctx), IsNil)
	c.Check(markedCount(), Equals, 0)

	// above the trigger, exactly one node is marked
	c.Assert(k.SetNodeMimir(ctx, key, 1, nas[3].NodeAddress), IsNil)
	c.Assert(vm.markNonVotingActors(ctx), IsNil)
	c.Check(markedCount(), Equals, 1)
	first := markedAddr()

	// the marked node must be one that hasn't voted
	mimirs, err := k.GetNodeMimirs(ctx, key)
	c.Assert(err, IsNil)
	addr, err := cosmos.AccAddressFromBech32(first)
	c.Assert(err, IsNil)
	c.Check(mimirs.Has(key, addr), Equals, false)

	// same app hash selects the same node - the pick must be deterministic
	clearMarks()
	c.Assert(vm.markNonVotingActors(ctx), IsNil)
	c.Check(markedAddr(), Equals, first)

	// a different app hash generally selects a different node; walk a few hashes and
	// assert the selection is not pinned to one address
	clearMarks()
	differs := false
	for i := byte(0); i < 20 && !differs; i++ {
		clearMarks()
		hCtx := withAppHash(ctx, []byte{i, 0xAA})
		c.Assert(vm.markNonVotingActors(hCtx), IsNil)
		if markedAddr() != first {
			differs = true
		}
	}
	c.Check(differs, Equals, true)

	// kill switch off marks nobody
	clearMarks()
	k.SetMimir(ctx, constants.RequiredVotingEnabled.String(), 0)
	c.Assert(vm.markNonVotingActors(ctx), IsNil)
	c.Check(markedCount(), Equals, 0)
}

// Abstaining is how a node clears the requirement without taking a side: the vote is stored
// (unlike -1, which removes it) and counts as turnout, but never toward a value.
func (s *ADRMimirSuite) TestAbstainClearsRequirement(c *C) {
	ctx, k := setupKeeperForTest(c)
	nas := addActiveNodes(c, ctx, k, 9)
	const key = "ADR42ABSTAIN"

	for i := 0; i < 4; i++ {
		c.Assert(k.SetNodeMimir(ctx, key, 1, nas[i].NodeAddress), IsNil)
	}
	pending, err := pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Assert(pending, HasLen, 1)

	// abstaining stores the vote, so this node is no longer required to vote
	c.Assert(k.SetNodeMimir(ctx, key, constants.MimirAbstainValue, nas[4].NodeAddress), IsNil)
	pending, err = pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	_, ok := nodeHasPendingADRVote(pending, nas[4].NodeAddress)
	c.Check(ok, Equals, false)

	// ...but it does not help form consensus - 4 of 9 is still short of the 6 needed
	pending, err = pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Check(pending, HasLen, 1)

	// once the rest abstain too, everyone has voted and nothing is pending
	for i := 5; i < 9; i++ {
		c.Assert(k.SetNodeMimir(ctx, key, constants.MimirAbstainValue, nas[i].NodeAddress), IsNil)
	}
	pending, err = pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Check(pending, HasLen, 0)

	// and no value was ever chosen
	mimirs, err := k.GetNodeMimirs(ctx, key)
	c.Assert(err, IsNil)
	_, ok = mimirs.HasSuperMajority(key, nas.GetNodeAddresses())
	c.Check(ok, Equals, false)

	// unvoting with -1 removes the record and puts the node back on the hook
	c.Assert(k.SetNodeMimir(ctx, key, -1, nas[4].NodeAddress), IsNil)
	mimirs, err = k.GetNodeMimirs(ctx, key)
	c.Assert(err, IsNil)
	c.Check(mimirs.Has(key, nas[4].NodeAddress), Equals, false)
	pending, err = pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	got, ok := nodeHasPendingADRVote(pending, nas[4].NodeAddress)
	c.Check(ok, Equals, true)
	c.Check(got, Equals, key)
}

// Abstains count toward the 1/3+1 turnout, so they can open the required-voting window for
// everyone else. One notion of participation - deliberate, see docs/mimir.md.
func (s *ADRMimirSuite) TestAbstainOpensWindow(c *C) {
	ctx, k := setupKeeperForTest(c)
	nas := addActiveNodes(c, ctx, k, 9)
	const key = "ADR42OPEN"

	c.Assert(k.SetNodeMimir(ctx, key, 1, nas[0].NodeAddress), IsNil)
	for i := 1; i < 4; i++ {
		c.Assert(k.SetNodeMimir(ctx, key, constants.MimirAbstainValue, nas[i].NodeAddress), IsNil)
	}

	pending, err := pendingADRVotes(ctx, k)
	c.Assert(err, IsNil)
	c.Assert(pending, HasLen, 1)
	c.Check(pending[0].Key, Equals, key)

	got, ok := nodeHasPendingADRVote(pending, nas[8].NodeAddress)
	c.Check(ok, Equals, true)
	c.Check(got, Equals, key)
}
