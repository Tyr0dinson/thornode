package thorchain

import (
	"errors"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/blang/semver"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerUpgradeCovCESuite struct{}

var _ = Suite(&HandlerUpgradeCovCESuite{})

// covCEUpgradeKeeper keeps the upgrade plan in memory (the test store has no
// x/upgrade store mounted) and can be told to fail selected calls.
type covCEUpgradeKeeper struct {
	keeper.Keeper
	plan            *upgradetypes.Plan
	planErr         error
	failSchedule    bool
	failClear       bool
	failGetProposed bool
	failPropose     bool
	failListActive  bool
}

func (k *covCEUpgradeKeeper) GetUpgradePlan(_ cosmos.Context) (upgradetypes.Plan, error) {
	if k.planErr != nil {
		return upgradetypes.Plan{}, k.planErr
	}
	if k.plan == nil {
		return upgradetypes.Plan{}, upgradetypes.ErrNoUpgradePlanFound
	}
	return *k.plan, nil
}

func (k *covCEUpgradeKeeper) ScheduleUpgrade(_ cosmos.Context, plan upgradetypes.Plan) error {
	if k.failSchedule {
		return errKaboom
	}
	k.plan = &plan
	return nil
}

func (k *covCEUpgradeKeeper) ClearUpgradePlan(_ cosmos.Context) error {
	if k.failClear {
		return errKaboom
	}
	k.plan = nil
	return nil
}

func (k *covCEUpgradeKeeper) GetProposedUpgrade(ctx cosmos.Context, name string) (*types.UpgradeProposal, error) {
	if k.failGetProposed {
		return nil, errKaboom
	}
	return k.Keeper.GetProposedUpgrade(ctx, name)
}

func (k *covCEUpgradeKeeper) ProposeUpgrade(ctx cosmos.Context, name string, p types.UpgradeProposal) error {
	if k.failPropose {
		return errKaboom
	}
	return k.Keeper.ProposeUpgrade(ctx, name, p)
}

func (k *covCEUpgradeKeeper) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	if k.failListActive {
		return nil, errKaboom
	}
	return k.Keeper.ListActiveValidators(ctx)
}

func covCEUpgradeSetup(c *C, n int) (cosmos.Context, *covCEUpgradeKeeper, []NodeAccount) {
	ctx, k := setupKeeperForTest(c)
	uk := &covCEUpgradeKeeper{Keeper: k}
	nodes := make([]NodeAccount, n)
	for i := range nodes {
		nodes[i] = GetRandomValidatorNode(NodeActive)
		nodes[i].Bond = cosmos.NewUint(100 * common.One)
		c.Assert(k.SetNodeAccount(ctx, nodes[i]), IsNil)
	}
	return ctx, uk, nodes
}

// covCECorruptProposal overwrites the stored bytes of the named proposal.
func covCECorruptProposal(c *C, ctx cosmos.Context, k keeper.Keeper, name string) {
	iter := k.GetUpgradeProposalIterator(ctx)
	var key []byte
	for ; iter.Valid(); iter.Next() {
		if string(iter.Key()[len(iter.Key())-len(name):]) == name {
			key = append([]byte(nil), iter.Key()...)
		}
	}
	iter.Close()
	c.Assert(key, NotNil)
	ctx.KVStore(keyThorchain).Set(key, []byte{0xff, 0xff, 0xff})
}

func (s *HandlerUpgradeCovCESuite) TestInvalidMessagesCovCE(c *C) {
	ctx, uk, nodes := covCEUpgradeSetup(c, 1)
	mgr := NewDummyMgrWithKeeper(uk)
	for _, h := range []MsgHandler{NewProposeUpgradeHandler(mgr), NewApproveUpgradeHandler(mgr), NewRejectUpgradeHandler(mgr)} {
		_, err := h.Run(ctx, &types.MsgMimir{})
		c.Check(errors.Is(err, errInvalidMessage), Equals, true)
	}

	// approve / reject: basic validation, auth, missing proposal
	_, err := NewApproveUpgradeHandler(mgr).Run(ctx, &MsgApproveUpgrade{})
	c.Check(err, NotNil)
	_, err = NewRejectUpgradeHandler(mgr).Run(ctx, &MsgRejectUpgrade{})
	c.Check(err, NotNil)
	_, err = NewApproveUpgradeHandler(mgr).Run(ctx, NewMsgApproveUpgrade("9.9.9", GetRandomBech32Addr()))
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)
	_, err = NewRejectUpgradeHandler(mgr).Run(ctx, NewMsgRejectUpgrade("9.9.9", GetRandomBech32Addr()))
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)
	_, err = NewRejectUpgradeHandler(mgr).Run(ctx, NewMsgRejectUpgrade("9.9.9", nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, ".*upgrade proposal does not exist: 9.9.9.*")

	uk.failGetProposed = true
	_, err = NewProposeUpgradeHandler(mgr).Run(ctx, NewMsgProposeUpgrade("9.9.9", ctx.BlockHeight()+10, "info", nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, ".*failed to get proposed upgrade: 9.9.9.*")
	_, err = NewApproveUpgradeHandler(mgr).Run(ctx, NewMsgApproveUpgrade("9.9.9", nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, ".*failed to get proposed upgrade: 9.9.9.*")
	_, err = NewRejectUpgradeHandler(mgr).Run(ctx, NewMsgRejectUpgrade("9.9.9", nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, ".*failed to get proposed upgrade: 9.9.9.*")
	uk.failGetProposed = false

	uk.failPropose = true
	_, err = NewProposeUpgradeHandler(mgr).Run(ctx, NewMsgProposeUpgrade("9.9.9", ctx.BlockHeight()+10, "info", nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, "failed to propose upgrade: kaboom")
}

func (s *HandlerUpgradeCovCESuite) TestProposeValidationCovCE(c *C) {
	ctx, uk, nodes := covCEUpgradeSetup(c, 2)
	mgr := NewDummyMgrWithKeeper(uk)
	h := NewProposeUpgradeHandler(mgr)
	height := ctx.BlockHeight() + 100

	// proposals from another node do not count towards this node's limit
	for _, name := range []string{"1.0.1", "1.0.2", "1.0.3"} {
		_, err := h.Run(ctx, NewMsgProposeUpgrade(name, height, "info", nodes[1].NodeAddress))
		c.Assert(err, IsNil)
	}
	for _, name := range []string{"2.0.1", "2.0.2", "2.0.3"} {
		_, err := h.Run(ctx, NewMsgProposeUpgrade(name, height, "info", nodes[0].NodeAddress))
		c.Assert(err, IsNil)
	}
	_, err := h.Run(ctx, NewMsgProposeUpgrade("2.0.4", height, "info", nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, ".*exceeded maximum number of upgrade proposals: 3.*")

	// a corrupt stored proposal fails validation of new proposals
	covCECorruptProposal(c, ctx, uk.Keeper, "1.0.1")
	_, err = h.Run(ctx, NewMsgProposeUpgrade("3.0.0", height, "info", nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, ".*failed to unmarshal upgrade proposal.*")
	c.Check(scheduleAnyApprovedUpgrade(ctx, uk), ErrorMatches, "failed to unmarshal proposed upgrade.*")
}

func (s *HandlerUpgradeCovCESuite) TestScheduleAndClearCovCE(c *C) {
	ctx, uk, nodes := covCEUpgradeSetup(c, 3)
	mgr := NewDummyMgrWithKeeper(uk)
	height := ctx.BlockHeight() + 100

	for _, name := range []string{"9.0.0", "9.1.0"} {
		_, err := NewProposeUpgradeHandler(mgr).Run(ctx, NewMsgProposeUpgrade(name, height, "info-"+name, nodes[0].NodeAddress))
		c.Assert(err, IsNil)
	}

	// unknown proposal
	c.Check(scheduleUpgradeIfNecessary(ctx, uk, "missing"), ErrorMatches, "upgrade proposal not found: missing")
	// store errors
	uk.planErr = errKaboom
	c.Check(scheduleUpgradeIfNecessary(ctx, uk, "9.0.0"), ErrorMatches, "failed to get upgrade plan: kaboom")
	c.Check(clearUpgradeIfNecessary(ctx, uk, "9.0.0"), ErrorMatches, "failed to get upgrade plan: kaboom")
	uk.planErr = nil
	uk.failGetProposed = true
	c.Check(scheduleUpgradeIfNecessary(ctx, uk, "9.0.0"), ErrorMatches, "failed to get proposed upgrade: kaboom")
	uk.failGetProposed = false
	uk.failListActive = true
	c.Check(scheduleUpgradeIfNecessary(ctx, uk, "9.0.0"), ErrorMatches, "failed to determine if upgrade is approved.*kaboom")
	uk.failListActive = false

	// the proposer auto-approves; one more approval reaches 2/3 -> scheduled
	approve := NewApproveUpgradeHandler(mgr)
	for _, na := range nodes[1:2] {
		_, err := approve.Run(ctx, NewMsgApproveUpgrade("9.0.0", na.NodeAddress))
		c.Assert(err, IsNil)
	}
	c.Assert(uk.plan, NotNil)
	c.Check(uk.plan.Name, Equals, "9.0.0")
	c.Check(uk.plan.Info, Equals, "info-9.0.0")
	// already scheduled
	c.Check(scheduleUpgradeIfNecessary(ctx, uk, "9.0.0"), IsNil)
	// approving twice is rejected
	_, err := approve.Run(ctx, NewMsgApproveUpgrade("9.0.0", nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, ".*upgrade already approved: 9.0.0.*")

	// B reaches majority while A is scheduled: not scheduled yet
	for _, na := range nodes[1:2] {
		_, err = approve.Run(ctx, NewMsgApproveUpgrade("9.1.0", na.NodeAddress))
		c.Assert(err, IsNil)
	}
	c.Check(uk.plan.Name, Equals, "9.0.0")

	// clearing a different / still approved plan is a no-op
	c.Check(clearUpgradeIfNecessary(ctx, uk, "9.1.0"), IsNil)
	c.Check(clearUpgradeIfNecessary(ctx, uk, "9.0.0"), IsNil)
	uk.failListActive = true
	c.Check(clearUpgradeIfNecessary(ctx, uk, "9.0.0"), ErrorMatches, "failed to determine if upgrade is approved.*kaboom")
	c.Check(scheduleAnyApprovedUpgrade(ctx, uk), ErrorMatches, "failed to check upgrade quorum for 9.0.0.*kaboom")
	uk.failListActive = false

	// one rejection drops A under the majority; clearing fails
	reject := NewRejectUpgradeHandler(mgr)
	uk.failClear = true
	_, err = reject.Run(ctx, NewMsgRejectUpgrade("9.0.0", nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, "failed to clear upgrade plan: kaboom")
	c.Check(uk.plan.Name, Equals, "9.0.0")
	uk.failClear = false

	// rejecting twice is refused
	_, err = reject.Run(ctx, NewMsgRejectUpgrade("9.0.0", nodes[0].NodeAddress))
	c.Check(err, ErrorMatches, ".*upgrade already rejected: 9.0.0.*")

	// re-run the clear: A is cleared and the already approved B is scheduled
	c.Assert(clearUpgradeIfNecessary(ctx, uk, "9.0.0"), IsNil)
	c.Assert(uk.plan, NotNil)
	c.Check(uk.plan.Name, Equals, "9.1.0")
	c.Check(uk.plan.Info, Equals, "info-9.1.0")
}

func (s *HandlerUpgradeCovCESuite) TestActiveValidatorAnteHandlerCovCE(c *C) {
	ctx, uk, nodes := covCEUpgradeSetup(c, 1)
	FundModule(c, ctx, uk.Keeper, BondName, 100*common.One)

	_, err := ActiveValidatorAnteHandler(ctx, semver.MustParse("1.0.0"), uk, nodes[0].NodeAddress)
	c.Assert(err, IsNil)
	got, err := uk.GetNodeAccount(ctx, nodes[0].NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Bond.LT(cosmos.NewUint(100*common.One)), Equals, true)

	_, err = ActiveValidatorAnteHandler(ctx, semver.MustParse("1.0.0"), uk, GetRandomBech32Addr())
	c.Check(err, NotNil)
}
