package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// churnInAgeTest is a validator manager at height 1000 over a set of Active nodes,
// none of which is marked to leave.
type churnInAgeTest struct {
	ctx cosmos.Context
	mgr *Mgrs
	vm  *ValidatorMgr
}

func newChurnInAgeTest(c *C, active int) churnInAgeTest {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	for i := 0; i < active; i++ {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	}
	return churnInAgeTest{
		ctx: ctx,
		mgr: mgr,
		vm:  newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr()),
	}
}

// ready stores a Ready node bonding the given RUNE, first seen at the given height
// (none recorded when 0).
func (t churnInAgeTest) ready(c *C, bond uint64, firstSeen int64) NodeAccount {
	na := GetRandomValidatorNode(NodeReady)
	na.Bond = cosmos.NewUint(bond * common.One)
	c.Assert(t.mgr.Keeper().SetNodeAccount(t.ctx, na), IsNil)
	if firstSeen > 0 {
		t.mgr.Keeper().SetNodeFirstSeen(t.ctx, na.NodeAddress, firstSeen)
	}
	return na
}

// selected runs nextVaultNodeAccounts and reports, per given node, whether it is in
// the next set, and the size of that set.
func (t churnInAgeTest) selected(c *C, target int, nodes ...NodeAccount) ([]bool, int) {
	next, _, err := t.vm.nextVaultNodeAccounts(t.ctx, target)
	c.Assert(err, IsNil)
	out := make([]bool, len(nodes))
	for i, na := range nodes {
		for _, n := range next {
			if n.NodeAddress.Equals(na.NodeAddress) {
				out[i] = true
			}
		}
	}
	return out, len(next)
}

// TestChurnInMinNodeAge asserts that with ChurnInMinNodeAgeBlocks set only Ready nodes
// at least that many blocks old are selected, and that unset or 0 selects as before.
func (vts *ValidatorMgrTestSuite) TestChurnInMinNodeAge(c *C) {
	t := newChurnInAgeTest(c, 4)
	t.mgr.Keeper().SetMimir(t.ctx, constants.NumberOfNewNodesPerChurn.String(), 10)
	young := t.ready(c, 300, 990)
	underAge := t.ready(c, 250, 951) // 49 blocks old
	atAge := t.ready(c, 200, 950)    // 50 blocks old
	unrecorded := t.ready(c, 150, 0) // judged by StatusSince 1
	old := t.ready(c, 100, 100)
	unrecordedYoung := GetRandomValidatorNode(NodeReady) // judged by StatusSince 990
	unrecordedYoung.StatusSince = 990
	c.Assert(t.mgr.Keeper().SetNodeAccount(t.ctx, unrecordedYoung), IsNil)
	nodes := []NodeAccount{young, underAge, atAge, unrecorded, old, unrecordedYoung}

	got, size := t.selected(c, 20, nodes...)
	c.Check(got, DeepEquals, []bool{true, true, true, true, true, true}, Commentf("unset must select every Ready node"))
	c.Check(size, Equals, 10)

	t.mgr.Keeper().SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 50)
	got, size = t.selected(c, 20, nodes...)
	c.Check(got, DeepEquals, []bool{false, false, true, true, true, false})
	c.Check(size, Equals, 7)

	t.mgr.Keeper().SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 0)
	got, _ = t.selected(c, 20, nodes...)
	c.Check(got, DeepEquals, []bool{true, true, true, true, true, true}, Commentf("0 must select every Ready node"))
}

// TestChurnInMinNodeAgeKeepsBFTFloor asserts the age filter alone never leaves the next
// set below MinimumNodesForBFT, which would start a ragnarok: held-back nodes fill the
// gap, largest bond first, after every node old enough.
func (vts *ValidatorMgrTestSuite) TestChurnInMinNodeAgeKeepsBFTFloor(c *C) {
	t := newChurnInAgeTest(c, 2)
	minBFT := t.mgr.Keeper().GetConstants().GetInt64Value(constants.MinimumNodesForBFT)
	c.Assert(minBFT, Equals, int64(4))
	t.mgr.Keeper().SetMimir(t.ctx, constants.NumberOfNewNodesPerChurn.String(), 4)
	t.mgr.Keeper().SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 50)
	old := t.ready(c, 50, 1)
	young1 := t.ready(c, 300, 999)
	young2 := t.ready(c, 200, 999)
	young3 := t.ready(c, 100, 999)

	got, size := t.selected(c, 10, old, young1, young2, young3)
	c.Check(got, DeepEquals, []bool{true, true, false, false})
	c.Check(size, Equals, 4)

	// Without the filter all four join, so it cost two nodes above the floor.
	t.mgr.Keeper().SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 0)
	_, size = t.selected(c, 10)
	c.Check(size, Equals, 6)
}

// TestRecordNodeFirstSeen asserts a node without a first-seen height gets the earliest
// height its account holds, that a recorded one keeps its height, and that the record
// taken before markReadyActors survives it resetting StatusSince.
func (vts *ValidatorMgrTestSuite) TestRecordNodeFirstSeen(c *C) {
	t := newChurnInAgeTest(c, 0)
	k := t.mgr.Keeper()

	active := GetRandomValidatorNode(NodeActive)
	active.StatusSince = 400
	active.ActiveBlockHeight = 10
	c.Assert(k.SetNodeAccount(t.ctx, active), IsNil)
	standby := GetRandomValidatorNode(NodeStandby)
	standby.StatusSince = 700
	c.Assert(k.SetNodeAccount(t.ctx, standby), IsNil)
	recorded := GetRandomValidatorNode(NodeStandby)
	recorded.StatusSince = 800
	c.Assert(k.SetNodeAccount(t.ctx, recorded), IsNil)
	k.SetNodeFirstSeen(t.ctx, recorded.NodeAddress, 5)
	disabled := GetRandomValidatorNode(NodeDisabled)
	c.Assert(k.SetNodeAccount(t.ctx, disabled), IsNil)

	c.Assert(t.vm.recordNodeFirstSeen(t.ctx), IsNil)
	c.Assert(t.vm.markReadyActors(t.ctx), IsNil)

	_, ok, err := k.GetNodeFirstSeen(t.ctx, disabled.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, false, Commentf("a Disabled node never churns in again"))

	for _, tc := range []struct {
		na   NodeAccount
		want int64
	}{
		{active, 10},
		{standby, 700},
		{recorded, 5},
	} {
		height, found, getErr := k.GetNodeFirstSeen(t.ctx, tc.na.NodeAddress)
		c.Assert(getErr, IsNil)
		c.Check(found, Equals, true)
		c.Check(height, Equals, tc.want)
	}

	// markReadyActors moved the standby node to Ready at 1000; its age still counts
	// from 700.
	na, err := k.GetNodeAccount(t.ctx, standby.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(na.Status, Equals, NodeReady)
	c.Assert(na.StatusSince, Equals, int64(1000))
	k.SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 300)
	got, _ := t.selected(c, 10, standby)
	c.Check(got, DeepEquals, []bool{true})
}

// churnFailKeeper fails the churn right after it starts.
type churnFailKeeper struct {
	keeper.Keeper
}

func (k churnFailKeeper) ListValidatorsByStatus(_ cosmos.Context, _ NodeStatus) (NodeAccounts, error) {
	return nil, errKaboom
}

// TestChurnKeepsFirstSeenWhenItFails asserts the first-seen records of a churn survive
// that churn failing, so a later churn cannot record a newer StatusSince instead.
func (vts *ValidatorMgrTestSuite) TestChurnKeepsFirstSeenWhenItFails(c *C) {
	t := newChurnInAgeTest(c, 0)
	standby := GetRandomValidatorNode(NodeStandby)
	standby.StatusSince = 700
	c.Assert(t.mgr.Keeper().SetNodeAccount(t.ctx, standby), IsNil)

	vm := newValidatorMgr(churnFailKeeper{t.mgr.Keeper()}, t.mgr.NetworkMgr(), t.mgr.TxOutStore(), t.mgr.EventMgr())
	c.Assert(vm.churn(t.ctx), NotNil)

	height, ok, err := t.mgr.Keeper().GetNodeFirstSeen(t.ctx, standby.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, true)
	c.Check(height, Equals, int64(700))
}

// firstSeenFailKeeper fails to read the first-seen record of one node.
type firstSeenFailKeeper struct {
	keeper.Keeper
	bad cosmos.AccAddress
}

func (k firstSeenFailKeeper) GetNodeFirstSeen(ctx cosmos.Context, addr cosmos.AccAddress) (int64, bool, error) {
	if addr.Equals(k.bad) {
		return 0, false, errKaboom
	}
	return k.Keeper.GetNodeFirstSeen(ctx, addr)
}

// TestChurnInMinNodeAgeUnreadableRecord asserts an unreadable first-seen record holds
// back only its own node and fails neither the backfill nor the selection.
func (vts *ValidatorMgrTestSuite) TestChurnInMinNodeAgeUnreadableRecord(c *C) {
	t := newChurnInAgeTest(c, 4)
	t.mgr.Keeper().SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 50)
	good := t.ready(c, 200, 1)
	bad := t.ready(c, 300, 1)
	vm := newValidatorMgr(firstSeenFailKeeper{t.mgr.Keeper(), bad.NodeAddress}, t.mgr.NetworkMgr(), t.mgr.TxOutStore(), t.mgr.EventMgr())

	c.Assert(vm.recordNodeFirstSeen(t.ctx), IsNil)
	next, _, err := vm.nextVaultNodeAccounts(t.ctx, 20)
	c.Assert(err, IsNil)
	var hasGood, hasBad bool
	for _, na := range next {
		hasGood = hasGood || na.NodeAddress.Equals(good.NodeAddress)
		hasBad = hasBad || na.NodeAddress.Equals(bad.NodeAddress)
	}
	c.Check(hasGood, Equals, true)
	c.Check(hasBad, Equals, false)
}
