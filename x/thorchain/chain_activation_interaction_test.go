package thorchain

import (
	"slices"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// strandedLTC is the balance a dropped chain leaves behind on the retiring vault.
var strandedLTC = common.NewCoin(common.LTCAsset, cosmos.NewUint(100*common.One))

// setupStrandedDrop is setupDroppedChain with funds left on the dropped chain, plus a
// BTC balance the fresh vault does carry, and migration armed on every block. The
// enable mimir is left unset, so the chain stays off the active vault until a test
// sets it.
func setupStrandedDrop(c *C) (cosmos.Context, Manager, *NetworkMgr, Vault, Vault) {
	ctx, mgr, retiring, fresh := setupDroppedChain(c, 1, staleProofHeight)
	k := mgr.Keeper()
	k.SetMimir(ctx, activationMimirKey(activationChain), 0)
	k.SetMimir(ctx, constants.FundMigrationInterval.String(), 1)
	c.Assert(k.SaveNetworkFee(ctx, common.BTCChain, NetworkFee{
		Chain:              common.BTCChain,
		TransactionSize:    1000,
		TransactionFeeRate: 10,
	}), IsNil)

	old := retiring[0]
	old.StatusSince = ctx.BlockHeight() - 1
	old.AddFunds(common.NewCoins(strandedLTC, common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One))))
	c.Assert(k.SetVault(ctx, old), IsNil)

	return ctx, mgr, newNetworkMgr(k, mgr.TxOutStore(), mgr.EventMgr()), old, fresh
}

// migrationsByChain returns the migrate items scheduled at the current height, keyed by
// chain.
func migrationsByChain(ctx cosmos.Context, c *C, mgr Manager) map[common.Chain]TxOutItem {
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	byChain := make(map[common.Chain]TxOutItem, len(items))
	for _, item := range items {
		memo, err := ParseMemo(mgr.GetVersion(), item.Memo)
		c.Assert(err, IsNil)
		c.Assert(memo.IsType(TxMigrate), Equals, true, Commentf("unexpected outbound %s", item.Memo))
		byChain[item.Chain] = item
	}
	return byChain
}

// With the removed migrateFunds fallback, a chain no active vault carries has nowhere to
// go: its funds stay on the retiring vault rather than being signed out to a vault that
// would no-op them on arrival. The chain the fresh vault does carry still migrates, so
// the stall is specific to the dropped chain and not an inert migrateFunds.
func (s *ChainActivationTestSuite) TestMigrateFundsStallsChainNoActiveVaultCarries(c *C) {
	ctx, mgr, networkMgr, old, fresh := setupStrandedDrop(c)
	k := mgr.Keeper()

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	migrations := migrationsByChain(ctx, c, mgr)
	_, ltcScheduled := migrations[activationChain]
	c.Check(ltcScheduled, Equals, false)
	btc, btcScheduled := migrations[common.BTCChain]
	c.Assert(btcScheduled, Equals, true)
	c.Check(btc.VaultPubKey.Equals(old.PubKey), Equals, true)
	freshBTC, err := fresh.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)
	c.Check(btc.ToAddress.Equals(freshBTC), Equals, true)

	retiring, err := k.GetVault(ctx, old.PubKey)
	c.Assert(err, IsNil)
	c.Check(retiring.Status, Equals, RetiringVault)
	c.Check(retiring.GetCoin(common.LTCAsset).Amount.Equal(strandedLTC.Amount), Equals, true)
	c.Check(retiring.HasFundsAboveDust(), Equals, true, Commentf("the stranded funds must keep holding the churn"))
}

// A non-XMR chain must never migrate into the XMR monovault, even one that somehow
// carries the chain: the explicit exclusion that replaced the fallback, not merely the
// monovault's {THOR, XMR} pinning, is what keeps it out.
func (s *ChainActivationTestSuite) TestMigrateFundsNeverTargetsMonovaultForOtherChain(c *C) {
	ctx, mgr, networkMgr, _, _ := setupStrandedDrop(c)
	k := mgr.Keeper()

	nodes, err := k.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	monovault := newActivationVault(c, ActiveVault, nodes)
	monovault.BlockHeight = droppedKeygenHeight
	monovault.Chains = common.Chains{common.THORChain, common.XMRChain, activationChain}.Strings()
	c.Assert(k.SetVault(ctx, monovault), IsNil)

	c.Assert(networkMgr.migrateFunds(ctx, mgr), IsNil)

	_, ltcScheduled := migrationsByChain(ctx, c, mgr)[activationChain]
	c.Check(ltcScheduled, Equals, false)
}

// The documented way out of a stranded drop, end to end through NetworkMgr.EndBlock:
// while the mimir is unset the dropped chain stalls; once it is set, ONE EndBlock both
// re-stamps the chain (activation runs before migrateFunds) and schedules the stranded
// funds onto the fresh vault, and the delivered migration is credited there.
func (s *ChainActivationTestSuite) TestStrandedDropRecoversInOneEndBlock(c *C) {
	ctx, mgr, networkMgr, old, fresh := setupStrandedDrop(c)
	k := mgr.Keeper()

	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)
	c.Check(slices.Contains(vaultChains(ctx, c, k, fresh.PubKey), activationChain.String()), Equals, false)
	_, ltcScheduled := migrationsByChain(ctx, c, mgr)[activationChain]
	c.Check(ltcScheduled, Equals, false, Commentf("must stall while the chain is off the active vault"))

	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	k.SetMimir(ctx, activationMimirKey(activationChain), 1)
	c.Assert(networkMgr.EndBlock(ctx, mgr), IsNil)

	c.Assert(slices.Contains(vaultChains(ctx, c, k, fresh.PubKey), activationChain.String()), Equals, true)
	migration, ltcScheduled := migrationsByChain(ctx, c, mgr)[activationChain]
	c.Assert(ltcScheduled, Equals, true, Commentf("activation must settle chain membership before migrateFunds reads it"))
	c.Check(migration.VaultPubKey.Equals(old.PubKey), Equals, true)
	freshLTC, err := fresh.GetAddress(activationChain)
	c.Assert(err, IsNil)
	c.Check(migration.ToAddress.Equals(freshLTC), Equals, true)

	// Deliver the migration exactly as scheduled. The fresh vault now carries the chain,
	// so this is the ordinary credit path, not the legacy chainless carve-out.
	oldLTC, err := old.GetAddress(activationChain)
	c.Assert(err, IsNil)
	delivered := common.NewTx(
		GetRandomTxHash(),
		oldLTC,
		freshLTC,
		common.NewCoins(migration.Coin),
		common.Gas{common.NewCoin(common.LTCAsset, cosmos.NewUint(1000))},
		migration.Memo,
	)
	nodes, err := k.ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	for _, na := range nodes {
		msg := NewMsgObservedTxIn(ObservedTxs{NewObservedTx(delivered, activationChainHeight+1, fresh.PubKey, activationChainHeight+1)}, na.NodeAddress)
		_, err = NewObservedTxInHandler(mgr).handle(ctx, *msg)
		c.Assert(err, IsNil)
	}

	credited, err := k.GetVault(ctx, fresh.PubKey)
	c.Assert(err, IsNil)
	c.Check(credited.GetCoin(common.LTCAsset).Amount.Equal(migration.Coin.Amount), Equals, true,
		Commentf("credited %s, scheduled %s", credited.GetCoin(common.LTCAsset).Amount, migration.Coin.Amount))
	c.Check(noOpSecurityMsgFromEvents(ctx, unsupportedChainNoOpSecurityMsg), Equals, "")
}

// chainSetActivation is an activation fixture shaped for the chain-set checks from the
// incomplete-keygen-chain-set churn-out: one active vault keygen'd at height 500, every
// active node attesting the chain, and the reports judged at height 600.
type chainSetActivation struct {
	ctx   cosmos.Context
	mgr   Manager
	vm    *ValidatorMgr
	nodes NodeAccounts
	vault Vault
}

const (
	chainSetVaultHeight  = int64(500)
	chainSetReportHeight = int64(600)
)

func setupChainSetActivation(c *C) chainSetActivation {
	ctx, mgr, nodes := setupActivation(c, 4)
	ctx = ctx.WithBlockHeight(1000)
	k := mgr.Keeper()

	vault := newActivationVault(c, ActiveVault, nodes)
	vault.BlockHeight = chainSetVaultHeight
	c.Assert(k.SetVault(ctx, vault), IsNil)
	satisfyActivationGates(ctx, c, k, activationChain, nodes)
	k.SetMimir(ctx, constants.MinimumBondInRune.String(), 1000000)

	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	return chainSetActivation{ctx: ctx, mgr: mgr, vm: vm, nodes: nodes, vault: vault}
}

func (t chainSetActivation) activate(c *C) {
	t.mgr.Keeper().SetMimir(t.ctx, activationMimirKey(activationChain), 1)
	runActivation(t.ctx, c, t.mgr)
	vault, err := t.mgr.Keeper().GetVault(t.ctx, t.vault.PubKey)
	c.Assert(err, IsNil)
	c.Assert(vault.HasChain(activationChain), Equals, true)
	// Activation adds the chain without a keygen, so the vault keeps its keygen height,
	// which is the age bound both chain-set checks date reports against.
	c.Assert(vault.BlockHeight, Equals, chainSetVaultHeight)
}

func (t chainSetActivation) leaveScore(c *C, na NodeAccount) uint64 {
	after, err := t.mgr.Keeper().GetNodeAccount(t.ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	return after.LeaveScore
}

var (
	btcOnlyReport    = common.Chains{common.THORChain, common.BTCChain}
	btcAndLTCReports = common.Chains{common.THORChain, common.BTCChain, activationChain}
)

// Activation extends the preflight requirement without a churn: a standby node whose
// current-cycle keygen report omitted the newly activated chain passes before activation
// and is held back after it, while one that reported the chain is unaffected.
func (s *ChainActivationTestSuite) TestActivationExtendsPreflightChainSetRequirement(c *C) {
	t := setupChainSetActivation(c)
	k := t.mgr.Keeper()

	standby := func(declared common.Chains) NodeAccount {
		na := GetRandomValidatorNode(NodeStandby)
		na.Bond = cosmos.NewUint(10 * common.One)
		na.Version = constants.SWVersion.String()
		na.IPAddress = "192.168.0.1"
		c.Assert(k.SetNodeAccount(t.ctx, na), IsNil)
		k.SetLastKeygenChains(t.ctx, na.NodeAddress, declared, chainSetReportHeight)
		return na
	}
	without := standby(btcOnlyReport)
	with := standby(btcAndLTCReports)

	for _, na := range (NodeAccounts{without, with}) {
		status, err := t.vm.NodeAccountPreflightCheck(t.ctx, na, k.GetConstants())
		c.Assert(err, IsNil)
		c.Check(status, Equals, NodeReady)
	}

	t.activate(c)

	status, err := t.vm.NodeAccountPreflightCheck(t.ctx, without, k.GetConstants())
	c.Assert(err, NotNil)
	c.Check(status, Equals, NodeStandby)
	c.Check(err.Error(), Equals, "node account keygen report at height 600 omitted chain LTC served by the active vaults")

	status, err = t.vm.NodeAccountPreflightCheck(t.ctx, with, k.GetConstants())
	c.Assert(err, IsNil)
	c.Check(status, Equals, NodeReady)
}

// Activation also arms the Active-side churn-out for the new chain. Before activation the
// active vaults do not serve it, so a pending vault created without it stalls nothing and
// marks nobody. After activation the same pending vault is a stall of a served chain, and
// only the active node whose report omitted it is marked.
func (s *ChainActivationTestSuite) TestActivationArmsChainSetChurnOutForNewChain(c *C) {
	t := setupChainSetActivation(c)
	k := t.mgr.Keeper()
	k.SetMimir(t.ctx, constants.MimirKeyChainSetChurnOutMax, 10)

	lagging := t.nodes[0]
	k.SetLastKeygenChains(t.ctx, lagging.NodeAddress, btcOnlyReport, chainSetReportHeight)
	for _, na := range t.nodes[1:] {
		k.SetLastKeygenChains(t.ctx, na.NodeAddress, btcAndLTCReports, chainSetReportHeight)
	}

	pendingWithout := func() {
		pending := newActivationVault(c, InitVault, t.nodes)
		pending.BlockHeight = 700
		c.Assert(k.SetVault(t.ctx, pending), IsNil)
	}

	// Before activation: nothing served is missing, so nothing stalled.
	pendingWithout()
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	for _, na := range t.nodes {
		c.Check(t.leaveScore(c, na), Equals, uint64(0))
	}

	// Activation is deferred while that init vault exists (G0), so clear the failed
	// round first, the way the next keygen retires it.
	inits, err := k.GetAsgardVaultsByStatus(t.ctx, InitVault)
	c.Assert(err, IsNil)
	for _, v := range inits {
		c.Assert(k.DeleteVault(t.ctx, v.PubKey), IsNil)
	}
	t.activate(c)

	// The next round's vault again leaves the chain off: now that is a stall.
	pendingWithout()
	c.Assert(t.vm.markChainSetActors(t.ctx), IsNil)
	c.Check(t.leaveScore(c, lagging) > 0, Equals, true)
	for _, na := range t.nodes[1:] {
		c.Check(t.leaveScore(c, na), Equals, uint64(0))
	}
}
