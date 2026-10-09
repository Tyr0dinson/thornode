package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type ValidatorMgrCovCDSuite struct{}

var _ = Suite(&ValidatorMgrCovCDSuite{})

func covCDValSetup(c *C) (cosmos.Context, *Mgrs, covCDKeeper, *ValidatorMgr) {
	ctx, mgr := setupManagerForTest(c)
	ek := newCovCDKeeper(mgr.Keeper())
	mgr.K = ek
	return ctx, mgr, ek, newValidatorMgr(ek, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
}

func (s *ValidatorMgrCovCDSuite) TestBeginBlockChurnGatesCovCD(c *C) {
	ctx, mgr, ek, vm := covCDValSetup(c)
	ctx = ctx.WithBlockHeight(1000)
	// no active vaults => last churn height 0; a 1000 block interval makes this a churn block
	ek.SetMimir(ctx, constants.ChurnInterval.String(), 1000)

	ek.SetMimir(ctx, "HaltChurning", 500)
	c.Check(vm.BeginBlock(ctx, mgr, nil), IsNil)
	ek.SetMimir(ctx, "HaltChurning", 0)

	ek.fail["GetAsgardVaultsByStatus:"+ActiveVault.String()] = true
	c.Check(vm.BeginBlock(ctx, mgr, nil), Equals, errKaboom)
	delete(ek.fail, "GetAsgardVaultsByStatus:"+ActiveVault.String())

	ek.fail["ListActiveValidators"] = true
	c.Check(vm.BeginBlock(ctx, mgr, nil), Equals, errKaboom)
	delete(ek.fail, "ListActiveValidators")

	ek.SetMimir(ctx, constants.AsgardSize.String(), 0)
	c.Check(vm.BeginBlock(ctx, mgr, nil), IsNil)
	ek.SetMimir(ctx, constants.AsgardSize.String(), 20)

	ek.fail["GetAsgardVaultsByStatus:"+RetiringVault.String()] = true
	c.Check(vm.BeginBlock(ctx, mgr, nil), Equals, errKaboom)
	delete(ek.fail, "GetAsgardVaultsByStatus:"+RetiringVault.String())

	// retry tick after an overdue churn: churn is attempted and its error surfaces
	ek.SetMimir(ctx, constants.ChurnRetryInterval.String(), 10)
	retryCtx := ctx.WithBlockHeight(1020)
	ek.fail["ListValidatorsByStatus"] = true
	c.Check(vm.BeginBlock(retryCtx, mgr, nil), Equals, errKaboom)
	delete(ek.fail, "ListValidatorsByStatus")
	// off the retry tick nothing happens
	c.Check(vm.BeginBlock(ctx.WithBlockHeight(1021), mgr, nil), IsNil)

	// no keygen was ever scheduled
	for _, h := range []int64{1000, 1020} {
		kb, err := ek.GetKeygenBlock(ctx, h)
		c.Assert(err, IsNil)
		c.Check(kb.Keygens, HasLen, 0)
	}
}

func (s *ValidatorMgrCovCDSuite) TestKeygenResolvedByVaultsCovCD(c *C) {
	members := common.PubKeys{GetRandomPubKey(), GetRandomPubKey()}
	asgard := GetRandomVault()
	asgard.Membership = members.Strings()
	xmr := GetRandomVault()
	xmr.Membership = members.Strings()
	xmr.Chains = []string{common.XMRChain.String()}
	other := GetRandomVault()

	asgardKeygen, err := NewKeygen(10, members.Strings(), AsgardKeygen)
	c.Assert(err, IsNil)
	xmrKeygen, err := NewKeygen(10, members.Strings(), XMRMonovaultKeygen)
	c.Assert(err, IsNil)

	c.Check(keygenResolvedByVaults(asgardKeygen, Vaults{other, asgard}), Equals, true)
	c.Check(keygenResolvedByVaults(asgardKeygen, Vaults{other, xmr}), Equals, false)
	c.Check(keygenResolvedByVaults(xmrKeygen, Vaults{xmr}), Equals, true)
	c.Check(keygenResolvedByVaults(xmrKeygen, Vaults{asgard}), Equals, false)
	c.Check(keygenResolvedByVaults(xmrKeygen, Vaults{}), Equals, false)
}

func (s *ValidatorMgrCovCDSuite) TestHasPendingKeygenErrorsCovCD(c *C) {
	ctx, _, ek, vm := covCDValSetup(c)
	for _, f := range []string{
		"GetAsgardVaultsByStatus:" + ActiveVault.String(),
		"GetAsgardVaultsByStatus:" + InitVault.String(),
		"GetKeygenBlock",
	} {
		ek.fail[f] = true
		_, err := vm.hasPendingKeygen(ctx, AsgardKeygen)
		c.Check(err, NotNil, Commentf("%s", f))
		delete(ek.fail, f)
	}
	pending, err := vm.hasPendingKeygen(ctx, AsgardKeygen)
	c.Assert(err, IsNil)
	c.Check(pending, Equals, false)
}

func (s *ValidatorMgrCovCDSuite) TestPreflightCheckCovCD(c *C) {
	ctx, mgr, ek, vm := covCDValSetup(c)
	consts := mgr.GetConstants()
	good := GetRandomValidatorNode(NodeStandby)
	good.Bond = cosmos.NewUint(uint64(ek.GetConfigInt64(ctx, constants.MinimumBondInRune)) + common.One)
	c.Assert(ek.SetNodeAccount(ctx, good), IsNil)

	status, err := vm.NodeAccountPreflightCheck(ctx, good, consts)
	c.Assert(err, IsNil)
	c.Check(status, Equals, NodeReady)

	type tc struct {
		mutate func(na *NodeAccount)
		status NodeStatus
		msg    string
	}
	for _, t := range []tc{
		{func(na *NodeAccount) { na.ForcedToLeave = true }, NodeDisabled, "node account has been banned"},
		{func(na *NodeAccount) { na.RequestedToLeave = true }, NodeStandby, "node account has requested to leave"},
		{func(na *NodeAccount) { na.Maintenance = true }, NodeStandby, "node account is in maintenance mode"},
		{func(na *NodeAccount) { na.IPAddress = "nope" }, NodeStandby, "node account has invalid registered IP address"},
		{func(na *NodeAccount) { na.PubKeySet = common.PubKeySet{} }, NodeWhiteListed, "node account has not registered their pubkey set"},
		{func(na *NodeAccount) { na.Bond = cosmos.ZeroUint() }, NodeStandby, "node account does not have minimum bond requirement.*"},
	} {
		na := good
		t.mutate(&na)
		status, err = vm.NodeAccountPreflightCheck(ctx, na, consts)
		c.Check(status, Equals, t.status)
		c.Check(err, ErrorMatches, t.msg)
	}

	// active nodes on the current version set the minimum join version
	for i := 0; i < 4; i++ {
		an := GetRandomValidatorNode(NodeActive)
		an.Version = mgr.GetVersion().String()
		c.Assert(ek.SetNodeAccount(ctx, an), IsNil)
	}
	old := good
	old.Version = "0.0.1"
	status, err = vm.NodeAccountPreflightCheck(ctx, old, consts)
	c.Check(status, Equals, NodeStandby)
	c.Check(err, ErrorMatches, "node account does not meet min version requirement.*")

	ek.fail["GetNodeAccountJail"] = true
	status, err = vm.NodeAccountPreflightCheck(ctx, good, consts)
	c.Check(status, Equals, NodeStandby)
	c.Check(err, ErrorMatches, "cannot fetch jail status.*")
	delete(ek.fail, "GetNodeAccountJail")

	c.Assert(ek.SetNodeAccountJail(ctx, good.NodeAddress, ctx.BlockHeight()+100, "naughty"), IsNil)
	status, err = vm.NodeAccountPreflightCheck(ctx, good, consts)
	c.Check(status, Equals, NodeStandby)
	c.Check(err, ErrorMatches, "node account is jailed until block .*naughty")

	fresh := good
	fresh.NodeAddress = GetRandomBech32Addr()
	ek.SetRagnarokBlockHeight(ctx, 1)
	status, err = vm.NodeAccountPreflightCheck(ctx, fresh, consts)
	c.Check(status, Equals, NodeStandby)
	c.Check(err, ErrorMatches, "ragnarok is currently in progress.*")
}

func (s *ValidatorMgrCovCDSuite) TestMarkActorErrorsCovCD(c *C) {
	ctx, _, ek, vm := covCDValSetup(c)
	ek.fail["ListActiveValidators"] = true
	c.Check(vm.markOldActor(ctx), Equals, errKaboom)
	c.Check(vm.markLowBondActor(ctx, 1), Equals, errKaboom)
	c.Check(vm.markLowBondActor(ctx, 0), Equals, errKaboom)
	_, err := vm.markBadActor(ctx, 1, 3)
	c.Check(err, Equals, errKaboom)
	c.Check(vm.markMissingActors(ctx), NotNil)
	c.Check(vm.clearLeaveScores(ctx), Equals, errKaboom)
	_, err = vm.findOldActor(ctx)
	c.Check(err, Equals, errKaboom)
	_, _, err = vm.nextVaultNodeAccounts(ctx, 4)
	c.Check(err, NotNil)
	delete(ek.fail, "ListActiveValidators")

	ek.fail["ListValidatorsByStatus"] = true
	// low-version marking only runs ChurnOutForLowVersionBlocks after the last change
	c.Check(vm.markLowVersionValidators(ctx), IsNil)
	c.Check(vm.markLowVersionValidators(ctx.WithBlockHeight(10_000_000)), Equals, errKaboom)
	c.Check(vm.markReadyActors(ctx), Equals, errKaboom)
	_, _, err = vm.nextVaultNodeAccounts(ctx, 4)
	c.Check(err, Equals, errKaboom)
	delete(ek.fail, "ListValidatorsByStatus")
	ek.fail["ListValidatorsByStatus:"+NodeReady.String()] = true
	c.Check(vm.markReadyActors(ctx), Equals, errKaboom)
	delete(ek.fail, "ListValidatorsByStatus:"+NodeReady.String())

	// a zero redline disables bad-actor detection instead of dividing by zero
	bad, err := vm.findBadActors(ctx, 1, 0)
	c.Assert(err, IsNil)
	c.Check(bad, HasLen, 0)

	// with active nodes, slash point lookups failing aborts bad actor detection
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(ek.SetNodeAccount(ctx, na), IsNil)
	ek.fail["GetNodeAccountSlashPoints"] = true
	_, err = vm.findBadActors(ctx, 0, 3)
	c.Check(err, NotNil)
	delete(ek.fail, "GetNodeAccountSlashPoints")

	// SetNodeAccount failures propagate from the marking helpers
	na.LeaveScore = 5
	c.Assert(ek.SetNodeAccount(ctx, na), IsNil)
	ek.fail["SetNodeAccount"] = true
	c.Check(vm.clearLeaveScores(ctx), Equals, errKaboom)
	delete(ek.fail, "SetNodeAccount")
	c.Assert(vm.clearLeaveScores(ctx), IsNil)
	got, err := ek.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.LeaveScore, Equals, uint64(0))
}

func (s *ValidatorMgrCovCDSuite) TestEndBlockErrorsCovCD(c *C) {
	ctx, mgr, ek, vm := covCDValSetup(c)
	ek.fail["ListActiveValidators"] = true
	c.Check(vm.EndBlock(ctx, mgr), IsNil)
	delete(ek.fail, "ListActiveValidators")

	// no active vault: node change detection fails
	c.Assert(ek.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Check(vm.EndBlock(ctx, mgr), IsNil)
	_, _, err := vm.getChangedNodes(ctx, nil)
	c.Check(err, ErrorMatches, "no active vault")
	ek.fail["GetAsgardVaultsByStatus"] = true
	_, _, err = vm.getChangedNodes(ctx, nil)
	c.Check(err, ErrorMatches, "fail to get active asgards.*")
}

func (s *ValidatorMgrCovCDSuite) TestProcessRagnarokCovCD(c *C) {
	ctx, mgr, ek, vm := covCDValSetup(c)
	ctx = ctx.WithBlockHeight(100)

	ek.fail["GetRagnarokBlockHeight"] = true
	c.Check(vm.processRagnarok(ctx, mgr), ErrorMatches, "fail to get ragnarok height.*")
	delete(ek.fail, "GetRagnarokBlockHeight")

	// first ragnarok block: bond rewards first, atomically with the start height
	ek.fail["ListActiveValidators"] = true
	c.Check(vm.processRagnarok(ctx, mgr), ErrorMatches, "when ragnarok triggered.*")
	delete(ek.fail, "ListActiveValidators")
	h, err := ek.GetRagnarokBlockHeight(ctx)
	c.Assert(err, IsNil)
	c.Check(h, Equals, int64(0))
	ek.fail["GetNetwork"] = true
	c.Check(vm.distributeBondReward(ctx, mgr), ErrorMatches, "fail to get network.*")
	delete(ek.fail, "GetNetwork")

	c.Assert(vm.processRagnarok(ctx, mgr), IsNil)
	h, err = ek.GetRagnarokBlockHeight(ctx)
	c.Assert(err, IsNil)
	c.Check(h, Equals, int64(100))

	for _, f := range []string{"GetRagnarokNth", "GetRagnarokWithdrawPosition", "GetRagnarokPending"} {
		ek.fail[f] = true
		c.Check(vm.processRagnarok(ctx, mgr), NotNil, Commentf("%s", f))
		delete(ek.fail, f)
	}

	// a withdraw position in progress continues ragnaroking pools (no nodes => error)
	ek.SetRagnarokWithdrawPosition(ctx, RagnarokWithdrawPosition{Number: 1, Pool: common.BTCAsset})
	c.Check(vm.processRagnarok(ctx, mgr), ErrorMatches, "fail to ragnarok pools: can't find any active nodes")
	ek.SetRagnarokWithdrawPosition(ctx, RagnarokWithdrawPosition{})

	// pending ragnarok transactions still in the outbound queue: wait
	ek.SetRagnarokPending(ctx, 1)
	txOut := NewTxOut(ctx.BlockHeight())
	txOut.TxArray = []TxOutItem{{Chain: common.BTCChain, ToAddress: GetRandomBTCAddress(), Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(1))}}
	c.Assert(ek.SetTxOut(ctx, txOut), IsNil)
	c.Assert(vm.processRagnarok(ctx, mgr), IsNil)
	nth, err := ek.GetRagnarokNth(ctx)
	c.Assert(err, IsNil)
	c.Check(nth, Equals, int64(0))
	ek.fail["GetTxOut"] = true
	c.Check(vm.processRagnarok(ctx, mgr), ErrorMatches, "fail to get pending tx out item.*")
	delete(ek.fail, "GetTxOut")

	// queue drained (item signed): stage 2 runs and fails without active nodes
	txOut.TxArray[0].OutHash = GetRandomTxHash()
	c.Assert(ek.SetTxOut(ctx, txOut), IsNil)
	c.Check(vm.processRagnarok(ctx, mgr), ErrorMatches, "fail to ragnarok pools.*")

	// ragnarokPools lookup failures
	c.Assert(ek.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	ek.fail["GetRagnarokWithdrawPosition"] = true
	c.Check(vm.ragnarokPools(ctx, 1, mgr), ErrorMatches, "fail to get ragnarok position.*")
	delete(ek.fail, "GetRagnarokWithdrawPosition")
	ek.fail["GetPools"] = true
	c.Check(vm.ragnarokPools(ctx, 1, mgr), ErrorMatches, "fail to get pools.*")
	delete(ek.fail, "GetPools")
	ek.fail["ListActiveValidators"] = true
	c.Check(vm.ragnarokPools(ctx, 1, mgr), ErrorMatches, "fail to get active nodes.*")
	delete(ek.fail, "ListActiveValidators")

	// pools are moved to staged; SetPool failures abort
	covCDPool(c, ctx, ek, common.BTCAsset, 100*common.One, 10*common.One)
	ek.fail["SetPool"] = true
	c.Check(vm.ragnarokPools(ctx, 1, mgr), ErrorMatches, "fail to set pool .* to Stage status.*")
	delete(ek.fail, "SetPool")
	c.Assert(vm.ragnarokPools(ctx, 1, mgr), IsNil)
	p, err := ek.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(p.Status, Equals, PoolStaged)
}

func (s *ValidatorMgrCovCDSuite) TestRagnarokBondCovCD(c *C) {
	ctx, mgr, ek, vm := covCDValSetup(c)

	// the first ten iterations never touch bonds
	c.Assert(vm.ragnarokBond(ctx, 10, mgr), IsNil)

	ek.fail["ListValidatorsWithBond"] = true
	c.Check(vm.ragnarokBond(ctx, 11, mgr), Equals, errKaboom)
	delete(ek.fail, "ListValidatorsWithBond")

	na := GetRandomValidatorNode(NodeActive)
	na.Bond = cosmos.NewUint(100 * common.One)
	c.Assert(ek.SetNodeAccount(ctx, na), IsNil)
	FundModule(c, ctx, ek, BondName, 100*common.One)
	c.Assert(ek.SetVault(ctx, GetRandomVault()), IsNil)

	ek.fail["GetBondProviders"] = true
	c.Check(vm.ragnarokBond(ctx, 11, mgr), ErrorMatches, "fail to get bond providers.*")
	delete(ek.fail, "GetBondProviders")

	// nth is capped at 10 (100%): a single round refunds the whole bond
	c.Assert(vm.ragnarokBond(ctx, 50, mgr), IsNil)
	got, err := ek.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.Bond.IsZero(), Equals, true)
	pending, err := ek.GetRagnarokPending(ctx)
	c.Assert(err, IsNil)
	c.Check(pending, Equals, int64(1))

	// already refunded nodes are skipped
	c.Assert(vm.ragnarokBond(ctx, 50, mgr), IsNil)
	pending, err = ek.GetRagnarokPending(ctx)
	c.Assert(err, IsNil)
	c.Check(pending, Equals, int64(1))
}

func (s *ValidatorMgrCovCDSuite) TestVaultMembershipNeedsKeygenCovCD(c *C) {
	ctx, _, ek, vm := covCDValSetup(c)
	need, err := vm.xmrVaultMembershipNeedsKeygen(ctx, nil)
	c.Assert(err, IsNil)
	c.Check(need, Equals, false)

	active := NodeAccounts{GetRandomValidatorNode(NodeActive), GetRandomValidatorNode(NodeActive)}
	ek.fail["GetAsgardVaults"] = true
	_, err = vm.xmrVaultMembershipNeedsKeygen(ctx, active)
	c.Check(err, ErrorMatches, "fail to get Asgard vaults.*")
	delete(ek.fail, "GetAsgardVaults")

	ek.SetMimir(ctx, constants.AsgardSize.String(), 0)
	need, err = vm.regularVaultMembershipNeedsKeygen(ctx, active)
	c.Assert(err, IsNil)
	c.Check(need, Equals, false)
	ek.SetMimir(ctx, constants.AsgardSize.String(), 20)
	need, err = vm.regularVaultMembershipNeedsKeygen(ctx, nil)
	c.Assert(err, IsNil)
	c.Check(need, Equals, false)
	ek.fail["GetAsgardVaultsByStatus"] = true
	_, err = vm.regularVaultMembershipNeedsKeygen(ctx, active)
	c.Check(err, ErrorMatches, "fail to get active asgards.*")
}

func (s *ValidatorMgrCovCDSuite) TestPayNodeAccountBondAwardSkipsCovCD(c *C) {
	ctx, mgr, ek, vm := covCDValSetup(c)
	one := cosmos.NewUint(common.One)
	na := GetRandomValidatorNode(NodeActive)
	na.ActiveBlockHeight = 0
	c.Assert(vm.payNodeAccountBondAward(ctx, 0, na, one, one, one, mgr), IsNil)
	na.ActiveBlockHeight = 1
	na.Bond = cosmos.ZeroUint()
	c.Assert(vm.payNodeAccountBondAward(ctx, 0, na, one, one, one, mgr), IsNil)

	na.Bond = one
	for _, f := range []string{"GetNetwork", "GetNodeAccountSlashPoints", "GetBondProviders"} {
		ek.fail[f] = true
		c.Check(vm.payNodeAccountBondAward(ctx, 0, na, one, one, one, mgr), NotNil, Commentf("%s", f))
		delete(ek.fail, f)
	}
}
