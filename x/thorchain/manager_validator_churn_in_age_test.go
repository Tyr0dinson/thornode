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
	mgr.Keeper().SetMimir(ctx, constants.ChurnInterval.String(), 60)
	for i := 0; i < active; i++ {
		c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	}
	return churnInAgeTest{
		ctx: ctx,
		mgr: mgr,
		vm:  newValidatorMgr(mgr.Keeper(), mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr()),
	}
}

// ready stores a Ready node bonding the given RUNE whose age starts at the given
// height, or that has no age when it is negative.
func (t churnInAgeTest) ready(c *C, bond uint64, ageStart int64) NodeAccount {
	na := GetRandomValidatorNode(NodeReady)
	na.Bond = cosmos.NewUint(bond * common.One)
	na.StatusSince = max(ageStart, 0)
	c.Assert(t.mgr.Keeper().SetNodeAccount(t.ctx, na), IsNil)
	// as at the churn: a stored account with no age start counts from its StatusSince
	t.mgr.Keeper().SyncNodeAgeStart(t.ctx, na)
	if ageStart < 0 {
		t.mgr.Keeper().ResetNodeAgeStart(t.ctx, na.NodeAddress)
	}
	return na
}

// selected runs nextVaultNodeAccounts and reports, per given node, whether it is in
// the next set, and the size of that set.
func (t churnInAgeTest) selected(c *C, target int, nodes ...NodeAccount) ([]bool, int) {
	next, _, err := t.vm.nextVaultNodeAccounts(t.ctx, target)
	c.Assert(err, IsNil)
	return contains(next, nodes...), len(next)
}

// contains reports, per given node, whether it is in set.
func contains(set NodeAccounts, nodes ...NodeAccount) []bool {
	out := make([]bool, len(nodes))
	for i, na := range nodes {
		out[i] = set.Contains(na)
	}
	return out
}

// TestChurnInMinNodeAge asserts that with ChurnInMinNodeAgeBlocks set only Ready nodes
// at least that many blocks old are selected, that a node without an age is not, and
// that unset or 0 selects as before.
func (vts *ValidatorMgrTestSuite) TestChurnInMinNodeAge(c *C) {
	t := newChurnInAgeTest(c, 4)
	t.mgr.Keeper().SetMimir(t.ctx, constants.NumberOfNewNodesPerChurn.String(), 10)
	young := t.ready(c, 300, 990)
	underAge := t.ready(c, 250, 951) // 49 blocks old
	atAge := t.ready(c, 200, 950)    // 50 blocks old
	old := t.ready(c, 150, 100)
	noAge := t.ready(c, 100, -1)
	nodes := []NodeAccount{young, underAge, atAge, old, noAge}

	got, size := t.selected(c, 20, nodes...)
	c.Check(got, DeepEquals, []bool{true, true, true, true, true}, Commentf("unset must select every Ready node"))
	c.Check(size, Equals, 9)

	t.mgr.Keeper().SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 50)
	got, size = t.selected(c, 20, nodes...)
	c.Check(got, DeepEquals, []bool{false, false, true, true, false})
	c.Check(size, Equals, 6)

	t.mgr.Keeper().SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 0)
	got, _ = t.selected(c, 20, nodes...)
	c.Check(got, DeepEquals, []bool{true, true, true, true, true}, Commentf("0 must select every Ready node"))
}

// TestChurnInMinNodeAgeCap asserts ChurnInMinNodeAgeBlocks counts for at most
// churnInMinNodeAgeMaxChurns churn intervals.
func (vts *ValidatorMgrTestSuite) TestChurnInMinNodeAgeCap(c *C) {
	t := newChurnInAgeTest(c, 4)
	t.mgr.Keeper().SetMimir(t.ctx, constants.NumberOfNewNodesPerChurn.String(), 10)
	t.mgr.Keeper().SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 1_000_000_000)
	atCap := t.ready(c, 200, 820)    // 180 blocks old: 3 churn intervals of 60
	underCap := t.ready(c, 100, 821) // 179 blocks old

	got, _ := t.selected(c, 20, atCap, underCap)
	c.Check(got, DeepEquals, []bool{true, false})

	// the cap follows ChurnInterval
	t.mgr.Keeper().SetMimir(t.ctx, constants.ChurnInterval.String(), 100)
	got, _ = t.selected(c, 20, atCap, underCap)
	c.Check(got, DeepEquals, []bool{false, false})
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

// rotationTest is a churn at height 1000 over nine Active nodes at capacity, in which
// the node with the lowest bond and the oldest node are two different nodes.
type rotationTest struct {
	churnInAgeTest
	network *NetworkMgrDummy
	lowBond NodeAccount
	oldest  NodeAccount
	leaving NodeAccount
	actives NodeAccounts
}

func newRotationTest(c *C, requestLeave bool) rotationTest {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	mgr := NewDummyMgrWithKeeper(k)
	network, ok := mgr.NetworkMgr().(*NetworkMgrDummy)
	c.Assert(ok, Equals, true)
	k.SetMimir(ctx, constants.DesiredValidatorSet.String(), 9)
	k.SetMimir(ctx, constants.AsgardSize.String(), 10)
	k.SetMimir(ctx, constants.ChurnInterval.String(), 60)

	t := rotationTest{network: network}
	membership := make([]string, 0, 9)
	for i := 0; i < 9; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.StatusSince = int64(100 + i)
		switch i {
		case 0:
			t.oldest = na
		case 1:
			na.Bond = cosmos.NewUint(500 * common.One)
			t.lowBond = na
		case 2:
			if requestLeave {
				na.RequestedToLeave = true
				na.LeaveScore = 1
				t.leaving = na
			}
		}
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
		membership = append(membership, na.PubKeySet.Secp256k1.String())
		t.actives = append(t.actives, na)
	}
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.THORChain}.Strings(), nil)
	vault.Membership = membership
	c.Assert(k.SetVault(ctx, vault), IsNil)

	t.churnInAgeTest = churnInAgeTest{
		ctx: ctx,
		vm:  newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr()),
	}
	return t
}

// readyNode stores a Ready node whose age starts at the given height.
func (t rotationTest) readyNode(c *C, ageStart int64) NodeAccount {
	na := GetRandomValidatorNode(NodeReady)
	na.Bond = cosmos.NewUint(2000 * common.One)
	na.StatusSince = ageStart
	c.Assert(t.vm.k.SetNodeAccount(t.ctx, na), IsNil)
	t.vm.k.SyncNodeAgeStart(t.ctx, na)
	return na
}

// next runs churnInner and returns the set it selected, which the XMR keygen takes
// (no XMR vault exists, so it runs every churn).
func (t rotationTest) next(c *C) NodeAccounts {
	c.Assert(t.vm.churnInner(t.ctx), IsNil)
	return t.network.xmrNas
}

// TestChurnInMinNodeAgeLimitsRotation asserts that with the age filter on, the low
// bond and age marks take no more nodes than the Ready nodes old enough to churn in
// that the other removals leave over, and that the other removals still happen.
func (vts *ValidatorMgrTestSuite) TestChurnInMinNodeAgeLimitsRotation(c *C) {
	// Off: the low bond and oldest nodes leave, both Ready nodes join.
	t := newRotationTest(c, false)
	young1, young2 := t.readyNode(c, 990), t.readyNode(c, 990)
	next := t.next(c)
	c.Check(next, HasLen, 9)
	c.Check(contains(next, t.lowBond, t.oldest, young1, young2), DeepEquals, []bool{false, false, true, true})

	// On, no Ready node old enough: nobody is marked, the set keeps its size.
	t = newRotationTest(c, false)
	t.vm.k.SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 50)
	young1, young2 = t.readyNode(c, 990), t.readyNode(c, 990)
	next = t.next(c)
	c.Check(next, HasLen, 9)
	c.Check(contains(next, t.lowBond, t.oldest, young1, young2), DeepEquals, []bool{true, true, false, false})
	for _, na := range t.actives {
		got, err := t.vm.k.GetNodeAccount(t.ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Check(got.LeaveScore, Equals, uint64(0), Commentf("no node may be marked to make room"))
	}

	// On, one Ready node old enough: only the low bond node makes room for it.
	t = newRotationTest(c, false)
	t.vm.k.SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 50)
	old := t.readyNode(c, 100)
	young1 = t.readyNode(c, 990)
	next = t.next(c)
	c.Check(next, HasLen, 9)
	c.Check(contains(next, t.lowBond, t.oldest, old, young1), DeepEquals, []bool{false, true, true, false})

	// On, one Ready node old enough, which replaces a node that asked to leave: no node
	// is marked to make room, the set keeps its size.
	t = newRotationTest(c, true)
	t.vm.k.SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 50)
	old = t.readyNode(c, 100)
	next = t.next(c)
	c.Check(next, HasLen, 9)
	c.Check(contains(next, t.leaving, t.lowBond, t.oldest, old), DeepEquals, []bool{false, true, true, true})

	// On, no Ready node old enough: a node that asked to leave still leaves.
	t = newRotationTest(c, true)
	t.vm.k.SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 50)
	young1 = t.readyNode(c, 990)
	next = t.next(c)
	c.Check(next, HasLen, 8)
	c.Check(contains(next, t.leaving, t.lowBond, t.oldest, young1), DeepEquals, []bool{false, true, true, false})
}

// TestSyncNodeAgeStarts asserts the churn judges node accounts stored without an age
// start, applies a change of MinimumBondInRune, leaves Disabled nodes alone, and that
// the age start survives markReadyActors resetting StatusSince.
func (vts *ValidatorMgrTestSuite) TestSyncNodeAgeStarts(c *C) {
	t := newChurnInAgeTest(c, 0)
	k := t.mgr.Keeper()
	ageStart := func(na NodeAccount) (int64, bool) {
		height, ok, err := k.GetNodeAgeStart(t.ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		return height, ok
	}

	// written as at genesis: not stored before, so not judged yet
	standby := GetRandomValidatorNode(NodeStandby)
	standby.StatusSince = 700
	c.Assert(k.SetNodeAccount(t.ctx, standby), IsNil)
	disabled := GetRandomValidatorNode(NodeDisabled)
	c.Assert(k.SetNodeAccount(t.ctx, disabled), IsNil)
	_, ok := ageStart(standby)
	c.Check(ok, Equals, false)

	c.Assert(t.vm.syncNodeAgeStarts(t.ctx), IsNil)
	height, ok := ageStart(standby)
	c.Check(ok, Equals, true)
	c.Check(height, Equals, int64(700))
	_, ok = ageStart(disabled)
	c.Check(ok, Equals, false, Commentf("a Disabled node never churns in again"))

	// raised above the bond (100 RUNE): the node loses its age
	k.SetMimir(t.ctx, constants.MinimumBondInRune.String(), 150*common.One)
	c.Assert(t.vm.syncNodeAgeStarts(t.ctx), IsNil)
	_, ok = ageStart(standby)
	c.Check(ok, Equals, false)

	// lowered below it: its age starts at this churn
	k.SetMimir(t.ctx, constants.MinimumBondInRune.String(), 50*common.One)
	c.Assert(t.vm.syncNodeAgeStarts(t.ctx.WithBlockHeight(900)), IsNil)
	height, _ = ageStart(standby)
	c.Check(height, Equals, int64(900))

	// markReadyActors moves the standby node to Ready at 1000; its age still counts
	// from 900.
	c.Assert(t.vm.markReadyActors(t.ctx), IsNil)
	na, err := k.GetNodeAccount(t.ctx, standby.NodeAddress)
	c.Assert(err, IsNil)
	c.Assert(na.Status, Equals, NodeReady)
	c.Assert(na.StatusSince, Equals, int64(1000))
	height, _ = ageStart(standby)
	c.Check(height, Equals, int64(900))
	k.SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 100)
	got, _ := t.selected(c, 10, standby)
	c.Check(got, DeepEquals, []bool{true})
}

// churnFailKeeper fails the churn after its first writes: clearLeaveScores lists the
// active nodes once markReadyActors has updated the node statuses in the churn's cache
// context.
type churnFailKeeper struct {
	keeper.Keeper
}

func (k churnFailKeeper) ListActiveValidators(_ cosmos.Context) (NodeAccounts, error) {
	return nil, errKaboom
}

// TestChurnKeepsAgeStartsWhenItFails asserts that when a churn fails after its first
// writes, those writes roll back while the age starts it synced before them remain.
func (vts *ValidatorMgrTestSuite) TestChurnKeepsAgeStartsWhenItFails(c *C) {
	t := newChurnInAgeTest(c, 0)
	k := t.mgr.Keeper()
	low := GetRandomValidatorNode(NodeStandby)
	low.Bond = cosmos.NewUint(100 * common.One)
	high := GetRandomValidatorNode(NodeStandby)
	high.Bond = cosmos.NewUint(200 * common.One)
	for _, na := range []NodeAccount{low, high} {
		c.Assert(k.SetNodeAccount(t.ctx, na), IsNil)
		k.SyncNodeAgeStart(t.ctx, na)
		_, ok, err := k.GetNodeAgeStart(t.ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Assert(ok, Equals, true)
	}
	// raised above the low node's bond: the churn's sync clears its age
	k.SetMimir(t.ctx, constants.MinimumBondInRune.String(), 150*common.One)

	vm := newValidatorMgr(churnFailKeeper{k}, t.mgr.NetworkMgr(), t.mgr.TxOutStore(), t.mgr.EventMgr())
	c.Assert(vm.churn(t.ctx), NotNil)

	// markReadyActors moved the high node to Ready in the cache context; the failure
	// dropped that.
	na, err := k.GetNodeAccount(t.ctx, high.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(na.Status, Equals, NodeStandby)
	// The sync ran before the cache context, so its result stays.
	_, ok, err := k.GetNodeAgeStart(t.ctx, low.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, false)
	_, ok, err = k.GetNodeAgeStart(t.ctx, high.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(ok, Equals, true)

	// The same churn without the failure moves the high node to Ready, so the check above
	// is not vacuous.
	c.Assert(t.vm.markReadyActors(t.ctx), IsNil)
	na, err = k.GetNodeAccount(t.ctx, high.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(na.Status, Equals, NodeReady)
}

// ageStartFailKeeper fails to read the age start of one node.
type ageStartFailKeeper struct {
	keeper.Keeper
	bad cosmos.AccAddress
}

func (k ageStartFailKeeper) GetNodeAgeStart(ctx cosmos.Context, addr cosmos.AccAddress) (int64, bool, error) {
	if addr.Equals(k.bad) {
		return 0, false, errKaboom
	}
	return k.Keeper.GetNodeAgeStart(ctx, addr)
}

// TestChurnInMinNodeAgeUnreadableRecord asserts an unreadable age start holds back
// only its own node and fails neither the sync nor the selection.
func (vts *ValidatorMgrTestSuite) TestChurnInMinNodeAgeUnreadableRecord(c *C) {
	t := newChurnInAgeTest(c, 4)
	t.mgr.Keeper().SetMimir(t.ctx, constants.MimirKeyChurnInMinNodeAgeBlocks, 50)
	good := t.ready(c, 200, 1)
	bad := t.ready(c, 300, 1)
	vm := newValidatorMgr(ageStartFailKeeper{t.mgr.Keeper(), bad.NodeAddress}, t.mgr.NetworkMgr(), t.mgr.TxOutStore(), t.mgr.EventMgr())

	c.Assert(vm.syncNodeAgeStarts(t.ctx), IsNil)
	next, _, err := vm.nextVaultNodeAccounts(t.ctx, 20)
	c.Assert(err, IsNil)
	c.Check(contains(next, good, bad), DeepEquals, []bool{true, false})
}
