package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type ValidatorMgrSigningHaltSuite struct{}

var _ = Suite(&ValidatorMgrSigningHaltSuite{})

func (s *ValidatorMgrSigningHaltSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

// churnStartsWith runs one regular churn tick over a single active vault
// holding coins and reports whether the churn started (the standby node turned
// Ready). mimirs are set before the tick.
func churnStartsWith(c *C, coins common.Coins, chains common.Chains, mimirs map[string]int64) bool {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgr()
	mgr.K = k
	vm := newValidatorMgr(k, mgr.NetworkMgr(), mgr.TxOutStore(), mgr.EventMgr())
	minBond := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.MinimumBondInRune)

	standbyNode := GetRandomValidatorNode(NodeStandby)
	standbyNode.Bond = cosmos.NewUint(uint64(minBond))
	c.Assert(k.SetNodeAccount(ctx, standbyNode), IsNil)

	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), chains.Strings(), []ChainContract{})
	vault.Membership = append(vault.Membership, standbyNode.PubKeySet.Secp256k1.String())
	vault.Coins = coins
	c.Assert(k.SetVault(ctx, vault), IsNil)

	k.SetMimir(ctx, constants.ChurnInterval.String(), 60)
	ctx = ctx.WithBlockHeight(61)
	for key, value := range mimirs {
		k.SetMimir(ctx, key, value)
	}

	c.Assert(vm.BeginBlock(ctx, mgr, nil), IsNil)
	ready, err := k.ListValidatorsByStatus(ctx, NodeReady)
	c.Assert(err, IsNil)
	return len(ready) == 1
}

func (s *ValidatorMgrSigningHaltSuite) TestChurnPausesUnderSigningHalt(c *C) {
	btc := common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}
	chains := common.Chains{common.BTCChain, common.ETHChain}

	c.Check(churnStartsWith(c, btc, chains, nil), Equals, true, Commentf("baseline churn"))

	c.Check(churnStartsWith(c, btc, chains, map[string]int64{"HaltSigningBTC": 1}), Equals, false,
		Commentf("HaltSigningBTC must pause a churn that would migrate BTC"))
	c.Check(churnStartsWith(c, btc, chains, map[string]int64{"HaltSigningBTC": 61}), Equals, false,
		Commentf("a halt that activates at this height applies"))
	c.Check(churnStartsWith(c, btc, chains, map[string]int64{"HALTSIGNING": 1}), Equals, false,
		Commentf("HALTSIGNING must pause the churn"))

	c.Check(churnStartsWith(c, btc, chains, map[string]int64{"HaltSigningBTC": 62}), Equals, true,
		Commentf("a halt scheduled for a later height does not apply yet"))
	c.Check(churnStartsWith(c, btc, chains, map[string]int64{"HaltSigningBTC": 0}), Equals, true,
		Commentf("zero is not a halt"))
	c.Check(churnStartsWith(c, btc, chains, map[string]int64{"HaltSigningBTC": -1}), Equals, true,
		Commentf("unset is not a halt"))
}

// HaltOutbound keeps migrations signing, so it must never pause the churn.
func (s *ValidatorMgrSigningHaltSuite) TestChurnProceedsUnderOutboundHalt(c *C) {
	btc := common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}
	chains := common.Chains{common.BTCChain}
	c.Check(churnStartsWith(c, btc, chains, map[string]int64{"HaltOutboundBTC": 1}), Equals, true)
	c.Check(churnStartsWith(c, btc, chains, map[string]int64{constants.MimirKeyHaltOutbound: 1}), Equals, true)
}

// Only chains the churn would migrate are checked: an Active vault must hold a
// non-RUNE coin above dust on the halted chain.
func (s *ValidatorMgrSigningHaltSuite) TestChurnSigningHaltOnlyCountsMigratedChains(c *C) {
	chains := common.Chains{common.BTCChain, common.ETHChain}
	btc := common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))
	ethDust := common.NewCoin(common.ETHAsset, common.ETHChain.DustThreshold())
	ethAboveDust := common.NewCoin(common.ETHAsset, common.ETHChain.DustThreshold().AddUint64(1))
	token := common.Asset{Chain: common.ETHChain, Symbol: "TOKEN", Ticker: "TOKEN"}
	haltETH := map[string]int64{"HaltSigningETH": 1}

	c.Check(churnStartsWith(c, common.Coins{btc}, chains, haltETH), Equals, true,
		Commentf("ETH supported but unfunded: no ETH migration, churn proceeds"))
	c.Check(churnStartsWith(c, common.Coins{btc, common.NewCoin(common.ETHAsset, cosmos.ZeroUint())}, chains, haltETH), Equals, true,
		Commentf("zero ETH balance: churn proceeds"))
	c.Check(churnStartsWith(c, common.Coins{btc, ethDust}, chains, haltETH), Equals, true,
		Commentf("ETH gas dust is burned, not migrated: churn proceeds"))
	c.Check(churnStartsWith(c, common.Coins{btc, ethAboveDust}, chains, haltETH), Equals, false,
		Commentf("ETH above dust: churn pauses"))
	c.Check(churnStartsWith(c, common.Coins{btc, common.NewCoin(token, cosmos.NewUint(1))}, chains, haltETH), Equals, false,
		Commentf("any ETH token balance is migration work: churn pauses"))

	// HALTSIGNING pauses only when something would migrate at all.
	c.Check(churnStartsWith(c, nil, chains, map[string]int64{"HALTSIGNING": 1}), Equals, true,
		Commentf("empty vaults: nothing to migrate, churn proceeds"))
}

// PauseChurnOnSigningHalt=0 turns the pause off.
func (s *ValidatorMgrSigningHaltSuite) TestChurnSigningHaltPauseKillSwitch(c *C) {
	btc := common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}
	chains := common.Chains{common.BTCChain}
	key := constants.PauseChurnOnSigningHalt.String()
	c.Check(churnStartsWith(c, btc, chains, map[string]int64{"HaltSigningBTC": 1, key: 0}), Equals, true)
	c.Check(churnStartsWith(c, btc, chains, map[string]int64{"HALTSIGNING": 1, key: 0}), Equals, true)
	c.Check(churnStartsWith(c, btc, chains, map[string]int64{"HaltSigningBTC": 1, key: 1}), Equals, false)
}

func (s *ValidatorMgrSigningHaltSuite) TestPauseChurnOnSigningHaltDefault(c *C) {
	consts := constants.GetConstantValues(GetCurrentVersion())
	c.Check(consts.GetInt64Value(constants.PauseChurnOnSigningHalt), Equals, int64(1))
}

func (s *ValidatorMgrSigningHaltSuite) TestChurnMigrationChains(c *C) {
	token := common.Asset{Chain: common.ETHChain, Symbol: "TOKEN", Ticker: "TOKEN"}
	v1 := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), nil, nil)
	v1.Coins = common.Coins{
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(common.One)),                         // RUNE: never migrated
		common.NewCoin(common.DOGEAsset, common.DOGEChain.DustThreshold()),                     // gas dust: burned
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),                            // BTC
		common.NewCoin(token, cosmos.NewUint(1)),                                               // ETH token
		common.NewCoin(common.ETHAsset, common.ETHChain.DustThreshold().AddUint64(common.One)), // ETH again
	}
	v2 := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), nil, nil)
	v2.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		common.NewCoin(common.LTCAsset, cosmos.NewUint(common.One)),
	}
	got := churnMigrationChains(Vaults{v1, v2})
	c.Check(got, DeepEquals, common.Chains{common.BTCChain, common.ETHChain, common.LTCChain})
	c.Check(churnMigrationChains(nil), HasLen, 0)
}

// The first halted chain in churnMigrationChains order is reported, and a
// global halt reports the first migrated chain.
func (s *ValidatorMgrSigningHaltSuite) TestChurnSigningHaltedChain(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(100)
	v := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), nil, nil)
	v.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		common.NewCoin(common.LTCAsset, cosmos.NewUint(common.One)),
	}
	vaults := Vaults{v}

	chain, global, halted := churnSigningHaltedChain(ctx, k, vaults)
	c.Check(halted, Equals, false)
	c.Check(global, Equals, false)
	c.Check(chain.IsEmpty(), Equals, true)

	k.SetMimir(ctx, "HaltSigningLTC", 100)
	chain, global, halted = churnSigningHaltedChain(ctx, k, vaults)
	c.Check(halted, Equals, true)
	c.Check(global, Equals, false)
	c.Check(chain.Equals(common.LTCChain), Equals, true)

	k.SetMimir(ctx, "HALTSIGNING", 50)
	chain, global, halted = churnSigningHaltedChain(ctx, k, vaults)
	c.Check(halted, Equals, true)
	c.Check(global, Equals, true)
	c.Check(chain.Equals(common.BTCChain), Equals, true)

	_, _, halted = churnSigningHaltedChain(ctx, k, nil)
	c.Check(halted, Equals, false, Commentf("no migrated chain: no pause even under HALTSIGNING"))
}
