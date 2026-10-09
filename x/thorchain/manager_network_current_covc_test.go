package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type NetworkMgrCovCDSuite struct{}

var _ = Suite(&NetworkMgrCovCDSuite{})

// covCDKeeper wraps a keeper and fails the named methods. Send failures can be
// scoped to a module pair with "Send:<from>:<to>".
type covCDKeeper struct {
	keeper.Keeper
	fail map[string]bool
}

func newCovCDKeeper(k keeper.Keeper) covCDKeeper {
	return covCDKeeper{Keeper: k, fail: map[string]bool{}}
}

func (k covCDKeeper) GetAsgardVaultsByStatus(ctx cosmos.Context, s VaultStatus) (Vaults, error) {
	if k.fail["GetAsgardVaultsByStatus"] || k.fail["GetAsgardVaultsByStatus:"+s.String()] {
		return nil, errKaboom
	}
	return k.Keeper.GetAsgardVaultsByStatus(ctx, s)
}

func (k covCDKeeper) GetAsgardVaults(ctx cosmos.Context) (Vaults, error) {
	if k.fail["GetAsgardVaults"] {
		return nil, errKaboom
	}
	return k.Keeper.GetAsgardVaults(ctx)
}

func (k covCDKeeper) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	if k.fail["GetPool"] || k.fail["GetPool:"+asset.String()] {
		return Pool{}, errKaboom
	}
	return k.Keeper.GetPool(ctx, asset)
}

func (k covCDKeeper) SetPool(ctx cosmos.Context, pool Pool) error {
	if k.fail["SetPool"] {
		return errKaboom
	}
	return k.Keeper.SetPool(ctx, pool)
}

func (k covCDKeeper) SendFromModuleToModule(ctx cosmos.Context, from, to string, coins common.Coins) error {
	if k.fail["Send"] || k.fail["Send:"+from+":"+to] {
		return errKaboom
	}
	return k.Keeper.SendFromModuleToModule(ctx, from, to, coins)
}

func (k covCDKeeper) SendFromModuleToAccount(ctx cosmos.Context, from string, to cosmos.AccAddress, coins common.Coins) error {
	if k.fail["SendToAccount"] {
		return errKaboom
	}
	return k.Keeper.SendFromModuleToAccount(ctx, from, to, coins)
}

func (k covCDKeeper) MintToModule(ctx cosmos.Context, module string, coin common.Coin) error {
	if k.fail["MintToModule"] {
		return errKaboom
	}
	return k.Keeper.MintToModule(ctx, module, coin)
}

func (k covCDKeeper) BurnFromModule(ctx cosmos.Context, module string, coin common.Coin) error {
	if k.fail["BurnFromModule"] {
		return errKaboom
	}
	return k.Keeper.BurnFromModule(ctx, module, coin)
}

func (k covCDKeeper) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	if k.fail["ListActiveValidators"] {
		return nil, errKaboom
	}
	return k.Keeper.ListActiveValidators(ctx)
}

func (k covCDKeeper) SetVault(ctx cosmos.Context, v Vault) error {
	if k.fail["SetVault"] {
		return errKaboom
	}
	return k.Keeper.SetVault(ctx, v)
}

func (k covCDKeeper) GetNodeAccountByPubKey(ctx cosmos.Context, pk common.PubKey) (NodeAccount, error) {
	if k.fail["GetNodeAccountByPubKey"] {
		return NodeAccount{}, errKaboom
	}
	return k.Keeper.GetNodeAccountByPubKey(ctx, pk)
}

func (k covCDKeeper) SetNodeAccount(ctx cosmos.Context, na NodeAccount) error {
	if k.fail["SetNodeAccount"] {
		return errKaboom
	}
	return k.Keeper.SetNodeAccount(ctx, na)
}

func (k covCDKeeper) GetKeygenBlock(ctx cosmos.Context, height int64) (KeygenBlock, error) {
	if k.fail["GetKeygenBlock"] {
		return KeygenBlock{}, errKaboom
	}
	return k.Keeper.GetKeygenBlock(ctx, height)
}

func (k covCDKeeper) ListTCYStakers(ctx cosmos.Context) ([]TCYStaker, error) {
	if k.fail["ListTCYStakers"] {
		return nil, errKaboom
	}
	return k.Keeper.ListTCYStakers(ctx)
}

func (k covCDKeeper) GetPools(ctx cosmos.Context) (Pools, error) {
	if k.fail["GetPools"] {
		return nil, errKaboom
	}
	return k.Keeper.GetPools(ctx)
}

func (k covCDKeeper) GetModuleAddress(module string) (common.Address, error) {
	if k.fail["GetModuleAddress"] || k.fail["GetModuleAddress:"+module] {
		return common.NoAddress, errKaboom
	}
	return k.Keeper.GetModuleAddress(module)
}

func (k covCDKeeper) GetPOL(ctx cosmos.Context) (ProtocolOwnedLiquidity, error) {
	if k.fail["GetPOL"] {
		return ProtocolOwnedLiquidity{}, errKaboom
	}
	return k.Keeper.GetPOL(ctx)
}

func (k covCDKeeper) GetPOLReserveDeposit(ctx cosmos.Context, asset common.Asset) (POLReserveDeposit, error) {
	if k.fail["GetPOLReserveDeposit"] {
		return POLReserveDeposit{}, errKaboom
	}
	return k.Keeper.GetPOLReserveDeposit(ctx, asset)
}

func (k covCDKeeper) SetPOLReserveDeposit(ctx cosmos.Context, d POLReserveDeposit) error {
	if k.fail["SetPOLReserveDeposit"] {
		return errKaboom
	}
	return k.Keeper.SetPOLReserveDeposit(ctx, d)
}

func (k covCDKeeper) GetTxOut(ctx cosmos.Context, height int64) (*TxOut, error) {
	if k.fail["GetTxOut"] {
		return nil, errKaboom
	}
	return k.Keeper.GetTxOut(ctx, height)
}

func (k covCDKeeper) GetVault(ctx cosmos.Context, pk common.PubKey) (Vault, error) {
	if k.fail["GetVault"] {
		return Vault{}, errKaboom
	}
	return k.Keeper.GetVault(ctx, pk)
}

func (k covCDKeeper) RemoveFromAsgardIndex(ctx cosmos.Context, pk common.PubKey) error {
	if k.fail["RemoveFromAsgardIndex"] {
		return errKaboom
	}
	return k.Keeper.RemoveFromAsgardIndex(ctx, pk)
}

func (k covCDKeeper) GetLiquidityProvider(ctx cosmos.Context, asset common.Asset, addr common.Address) (LiquidityProvider, error) {
	if k.fail["GetLiquidityProvider"] {
		return LiquidityProvider{}, errKaboom
	}
	return k.Keeper.GetLiquidityProvider(ctx, asset, addr)
}

func (k covCDKeeper) GetMimir(ctx cosmos.Context, key string) (int64, error) {
	if k.fail["GetMimir:"+key] {
		return -1, errKaboom
	}
	return k.Keeper.GetMimir(ctx, key)
}

// covCDSetup returns a manager whose keeper (and the network manager's keeper)
// is the failure-injecting wrapper.
func covCDSetup(c *C) (cosmos.Context, *Mgrs, covCDKeeper, *NetworkMgr) {
	ctx, mgr := setupManagerForTest(c)
	ek := newCovCDKeeper(mgr.Keeper())
	mgr.K = ek
	return ctx, mgr, ek, newNetworkMgr(ek, mgr.TxOutStore(), mgr.EventMgr())
}

func covCDPool(c *C, ctx cosmos.Context, k keeper.Keeper, asset common.Asset, rune, bal uint64) Pool {
	p := NewPool()
	p.Asset = asset
	p.Status = PoolAvailable
	p.BalanceRune = cosmos.NewUint(rune)
	p.BalanceAsset = cosmos.NewUint(bal)
	p.LPUnits = cosmos.NewUint(rune)
	c.Assert(k.SetPool(ctx, p), IsNil)
	return p
}

func (s *NetworkMgrCovCDSuite) TestBeginBlockNoActiveVaultsCovCD(c *C) {
	ctx, mgr, ek, vm := covCDSetup(c)
	c.Check(vm.BeginBlock(ctx, mgr), ErrorMatches, "dev error: no active asgard vaults")
	ek.fail["GetAsgardVaultsByStatus"] = true
	c.Check(vm.BeginBlock(ctx, mgr), Equals, errKaboom)
	delete(ek.fail, "GetAsgardVaultsByStatus")

	// a vault serving THOR and an ETH-gas L2 only spawns TOR and ETH derived assets
	v := GetRandomVault()
	v.Chains = []string{common.THORChain.String(), common.BASEChain.String(), common.ETHChain.String()}
	c.Assert(ek.SetVault(ctx, v), IsNil)
	c.Check(vm.BeginBlock(ctx, mgr), IsNil)
	// no anchors are available, so no derived pools exist
	c.Check(ek.PoolExist(ctx, common.ETHAsset.GetDerivedAsset()), Equals, false)
}

func (s *NetworkMgrCovCDSuite) TestSpawnDerivedAssetEdgesCovCD(c *C) {
	ctx, mgr, ek, vm := covCDSetup(c)

	// non-gas, non-TOR assets are ignored
	vm.SpawnDerivedAsset(ctx, common.TCY, mgr)
	c.Check(ek.PoolExist(ctx, common.TCY.GetDerivedAsset()), Equals, false)
	// a derived asset whose symbol is not a chain is ignored
	bogus := common.Asset{Chain: common.THORChain, Symbol: "NOTACHAIN", Ticker: "NOTACHAIN"}
	vm.SpawnDerivedAsset(ctx, bogus, mgr)

	// an existing derived pool is suspended when its layer1 pool cannot be read
	derived := common.BTCAsset.GetDerivedAsset()
	dp := NewPool()
	dp.Asset = derived
	dp.Status = PoolAvailable
	dp.BalanceRune = cosmos.NewUint(common.One)
	dp.BalanceAsset = cosmos.NewUint(common.One)
	c.Assert(ek.SetPool(ctx, dp), IsNil)
	ek.fail["GetPool:"+common.BTCAsset.String()] = true
	evts := len(ctx.EventManager().Events())
	vm.SpawnDerivedAsset(ctx, common.BTCAsset, mgr)
	delete(ek.fail, "GetPool:"+common.BTCAsset.String())
	got, err := ek.GetPool(ctx, derived)
	c.Assert(err, IsNil)
	c.Check(got.Status, Equals, PoolSuspended)
	c.Check(got.StatusSince, Equals, ctx.BlockHeight())
	c.Check(len(ctx.EventManager().Events()) > evts, Equals, true)

	// suspending an already suspended pool keeps it suspended (no new status)
	vm.suspendVirtualPool(ctx, mgr, common.BTCAsset, errKaboom)
	got, err = ek.GetPool(ctx, derived)
	c.Assert(err, IsNil)
	c.Check(got.Status, Equals, PoolSuspended)
	// derived pool read failure and missing pools are ignored
	ek.fail["GetPool:"+derived.String()] = true
	vm.suspendVirtualPool(ctx, mgr, common.BTCAsset, errKaboom)
	delete(ek.fail, "GetPool:"+derived.String())
	vm.suspendVirtualPool(ctx, mgr, common.DOGEAsset, errKaboom)
	c.Check(ek.PoolExist(ctx, common.DOGEAsset.GetDerivedAsset()), Equals, false)

	// Enable derived depth, but make the layer1 pool unavailable as an anchor.
	anchor := covCDPool(c, ctx, ek, common.BTCAsset, 1000*common.One, 10*common.One)
	anchor.Status = PoolStaged
	c.Assert(ek.SetPool(ctx, anchor), IsNil)
	ek.SetMimir(ctx, constants.DerivedDepthBasisPts.String(), 10000)
	dp.Status = PoolAvailable
	c.Assert(ek.SetPool(ctx, dp), IsNil)
	vm.SpawnDerivedAsset(ctx, common.BTCAsset, mgr)
	got, err = ek.GetPool(ctx, derived)
	c.Assert(err, IsNil)
	c.Check(got.Status, Equals, PoolSuspended)

	// Restoring the anchor makes the derived pool available with depth enabled.
	anchor.Status = PoolAvailable
	c.Assert(ek.SetPool(ctx, anchor), IsNil)
	vm.SpawnDerivedAsset(ctx, common.BTCAsset, mgr)
	got, err = ek.GetPool(ctx, derived)
	c.Assert(err, IsNil)
	c.Check(got.Status, Equals, PoolAvailable)

	// disabling derived depth suspends as well
	dp.Status = PoolAvailable
	c.Assert(ek.SetPool(ctx, dp), IsNil)
	ek.SetMimir(ctx, constants.DerivedDepthBasisPts.String(), 0)
	vm.SpawnDerivedAsset(ctx, common.BTCAsset, mgr)
	got, err = ek.GetPool(ctx, derived)
	c.Assert(err, IsNil)
	c.Check(got.Status, Equals, PoolSuspended)
}

func (s *NetworkMgrCovCDSuite) TestRedeemSynthAssetToReserveCovCD(c *C) {
	ctx, _, ek, vm := covCDSetup(c)
	pool := covCDPool(c, ctx, ek, common.BTCAsset, 100*common.One, 10*common.One)
	reserveBefore := ek.GetRuneBalanceOfModule(ctx, ReserveName)
	asgardBefore := ek.GetRuneBalanceOfModule(ctx, AsgardName)
	eventsBefore := ctx.EventManager().Events()
	assertUnchanged := func() {
		c.Check(ek.GetRuneBalanceOfModule(ctx, ReserveName).Equal(reserveBefore), Equals, true)
		c.Check(ek.GetRuneBalanceOfModule(ctx, AsgardName).Equal(asgardBefore), Equals, true)
		got, err := ek.GetPool(ctx, pool.Asset)
		c.Assert(err, IsNil)
		c.Check(got, DeepEquals, pool)
		start, err := ek.GetPoolRagnarokStart(ctx, pool.Asset)
		c.Assert(err, IsNil)
		c.Check(start, Equals, int64(0))
		c.Check(ctx.EventManager().Events(), DeepEquals, eventsBefore)
	}

	// no synths: nothing happens
	c.Assert(vm.redeemSynthAssetToReserve(ctx, pool), IsNil)
	assertUnchanged()

	// 1 BTC of synths is worth 10 RUNE at this pool price
	synth := common.NewCoin(common.BTCAsset.GetSyntheticAsset(), cosmos.NewUint(common.One))
	c.Assert(ek.MintToModule(ctx, ModuleName, synth), IsNil)
	eventsBefore = ctx.EventManager().Events()

	ek.fail["Send:"+AsgardName+":"+ReserveName] = true
	c.Check(vm.redeemSynthAssetToReserve(ctx, pool), ErrorMatches, "fail to send redeemed synth RUNE to reserve.*")
	delete(ek.fail, "Send:"+AsgardName+":"+ReserveName)
	assertUnchanged()
	ek.fail["SetPool"] = true
	c.Check(vm.redeemSynthAssetToReserve(ctx, pool), ErrorMatches, "fail to save pool.*")
	delete(ek.fail, "SetPool")
	assertUnchanged()
	c.Check(ek.GetTotalSupply(ctx, synth.Asset).Equal(synth.Amount), Equals, true)

	c.Assert(vm.redeemSynthAssetToReserve(ctx, pool), IsNil)
	c.Check(ek.GetRuneBalanceOfModule(ctx, ReserveName).Sub(reserveBefore).Uint64(), Equals, uint64(10*common.One))
	c.Check(asgardBefore.Sub(ek.GetRuneBalanceOfModule(ctx, AsgardName)).Uint64(), Equals, uint64(10*common.One))
	got, err := ek.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(got.BalanceRune.Uint64(), Equals, uint64(90*common.One))
	start, err := ek.GetPoolRagnarokStart(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(start, Equals, ctx.BlockHeight())
	c.Check(ek.GetTotalSupply(ctx, synth.Asset).IsZero(), Equals, true)
	// A retry after success must not redeem the same synth supply again.
	c.Assert(vm.redeemSynthAssetToReserve(ctx, got), IsNil)
	c.Check(ek.GetRuneBalanceOfModule(ctx, ReserveName).Sub(reserveBefore).Uint64(), Equals, uint64(10*common.One))
	c.Check(asgardBefore.Sub(ek.GetRuneBalanceOfModule(ctx, AsgardName)).Uint64(), Equals, uint64(10*common.One))

	// synth value larger than the pool's RUNE is capped at the pool's RUNE
	small := covCDPool(c, ctx, ek, common.ETHAsset, common.One, common.One)
	c.Assert(ek.MintToModule(ctx, ModuleName, common.NewCoin(common.ETHAsset.GetSyntheticAsset(), cosmos.NewUint(100*common.One))), IsNil)
	reserveBefore = ek.GetRuneBalanceOfModule(ctx, ReserveName)
	asgardBefore = ek.GetRuneBalanceOfModule(ctx, AsgardName)
	c.Assert(vm.redeemSynthAssetToReserve(ctx, small), IsNil)
	got, err = ek.GetPool(ctx, common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(got.BalanceRune.IsZero(), Equals, true)
	c.Check(ek.GetRuneBalanceOfModule(ctx, ReserveName).Sub(reserveBefore).Equal(small.BalanceRune), Equals, true)
	c.Check(asgardBefore.Sub(ek.GetRuneBalanceOfModule(ctx, AsgardName)).Equal(small.BalanceRune), Equals, true)
}

func (s *NetworkMgrCovCDSuite) TestClaimingSwapRuneToTCYCovCD(c *C) {
	ctx, mgr, ek, vm := covCDSetup(c)

	// halted by default on mocknet? force both states explicitly
	ek.SetMimir(ctx, constants.TCYClaimingSwapHalt.String(), 1)
	FundModule(c, ctx, ek, TCYClaimingName, 10*common.One)
	c.Assert(vm.claimingSwapRuneToTCY(ctx, mgr), IsNil)
	c.Check(ek.GetRuneBalanceOfModule(ctx, TCYClaimingName).Uint64(), Equals, uint64(10*common.One))

	ek.SetMimir(ctx, constants.TCYClaimingSwapHalt.String(), 0)

	// pool read failure
	ek.fail["GetPool:"+common.TCY.String()] = true
	c.Check(vm.claimingSwapRuneToTCY(ctx, mgr), Equals, errKaboom)
	delete(ek.fail, "GetPool:"+common.TCY.String())

	// empty TCY pool: nothing disbursed, nothing committed
	c.Assert(vm.claimingSwapRuneToTCY(ctx, mgr), IsNil)
	c.Check(ek.GetRuneBalanceOfModule(ctx, TCYClaimingName).Uint64(), Equals, uint64(10*common.One))

	// a funded TCY pool swaps the claiming module RUNE into TCY
	covCDPool(c, ctx, ek, common.TCY, 1000*common.One, 1000*common.One)
	c.Assert(ek.MintToModule(ctx, ModuleName, common.NewCoin(common.TCY, cosmos.NewUint(1000*common.One))), IsNil)
	c.Assert(ek.SendFromModuleToModule(ctx, ModuleName, AsgardName, common.NewCoins(common.NewCoin(common.TCY, cosmos.NewUint(1000*common.One)))), IsNil)

	for _, f := range []string{"Send:" + TCYClaimingName + ":" + AsgardName, "SetPool", "Send:" + AsgardName + ":" + TCYClaimingName} {
		ek.fail[f] = true
		c.Check(vm.claimingSwapRuneToTCY(ctx, mgr), NotNil, Commentf("%s", f))
		delete(ek.fail, f)
		// atomic: nothing moved
		c.Check(ek.GetRuneBalanceOfModule(ctx, TCYClaimingName).Uint64(), Equals, uint64(10*common.One))
	}

	c.Assert(vm.claimingSwapRuneToTCY(ctx, mgr), IsNil)
	c.Check(ek.GetRuneBalanceOfModule(ctx, TCYClaimingName).IsZero(), Equals, true)
	tcyGot := ek.GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())
	c.Check(tcyGot.IsZero(), Equals, false)
	pool, err := ek.GetPool(ctx, common.TCY)
	c.Assert(err, IsNil)
	c.Check(pool.BalanceRune.Uint64(), Equals, uint64(1010*common.One))
	c.Check(pool.BalanceAsset.Equal(cosmos.NewUint(1000*common.One).Sub(tcyGot)), Equals, true)

	// nothing left to swap
	c.Assert(vm.claimingSwapRuneToTCY(ctx, mgr), IsNil)
}

func (s *NetworkMgrCovCDSuite) TestDistributeTCYStakeCovCD(c *C) {
	ctx, mgr, ek, vm := covCDSetup(c)
	ek.SetMimir(ctx, constants.TCYClaimingSwapHalt.String(), 1)

	FundModule(c, ctx, ek, TCYStakeName, 100*common.One)
	// halted distribution leaves the RUNE in place
	ek.SetMimir(ctx, constants.TCYStakeDistributionHalt.String(), 1)
	vm.distributeTCYStake(ctx, mgr)
	c.Check(ek.GetRuneBalanceOfModule(ctx, TCYStakeName).Uint64(), Equals, uint64(100*common.One))
	ek.SetMimir(ctx, constants.TCYStakeDistributionHalt.String(), 0)
	ek.SetMimir(ctx, constants.MinRuneForTCYStakeDistribution.String(), common.One)
	ek.SetMimir(ctx, constants.MinTCYForTCYStakeDistribution.String(), 1)

	// TCY supply: 30 held by a staker, 10 by claiming, 10 by asgard, 50 unclaimed elsewhere
	stakerAcc := GetRandomBech32Addr()
	c.Assert(ek.SetTCYStaker(ctx, TCYStaker{Address: common.Address(stakerAcc.String()), Amount: cosmos.NewUint(30 * common.One)}), IsNil)
	zeroStaker := common.Address(GetRandomBech32Addr().String())
	c.Assert(ek.SetTCYStaker(ctx, TCYStaker{Address: zeroStaker, Amount: cosmos.ZeroUint()}), IsNil)
	c.Assert(ek.SetTCYStaker(ctx, TCYStaker{Address: common.Address("not-an-address"), Amount: cosmos.NewUint(common.One)}), IsNil)
	mint := func(module string, amt uint64) {
		coin := common.NewCoin(common.TCY, cosmos.NewUint(amt))
		c.Assert(ek.MintToModule(ctx, ModuleName, coin), IsNil)
		c.Assert(ek.SendFromModuleToModule(ctx, ModuleName, module, common.NewCoins(coin)), IsNil)
	}
	mint(TCYClaimingName, 10*common.One)
	mint(AsgardName, 10*common.One)
	mint(ReserveName, 50*common.One)
	mint(TCYStakeName, 30*common.One)

	claimAcc := ek.GetModuleAccAddress(TCYClaimingName)
	dists, total := vm.getTCYDistributions(ctx, mgr, 1, claimAcc)
	c.Check(total.Uint64(), Equals, uint64(100*common.One))
	c.Assert(dists, HasLen, 4)
	c.Check(dists[0].Account.Equals(stakerAcc), Equals, true)
	for _, d := range dists[1:] {
		c.Check(d.Account.Equals(claimAcc), Equals, true)
	}
	// the zero-amount staker is pruned
	stakers, err := ek.ListTCYStakers(ctx)
	c.Assert(err, IsNil)
	for _, st := range stakers {
		c.Check(st.Address.Equals(zeroStaker), Equals, false)
	}

	// listing failure yields nothing
	ek.fail["ListTCYStakers"] = true
	dists, total = vm.getTCYDistributions(ctx, mgr, 1, claimAcc)
	c.Check(dists, HasLen, 0)
	c.Check(total.IsZero(), Equals, true)
	delete(ek.fail, "ListTCYStakers")

	// account send failures keep the RUNE in the stake module
	ek.fail["SendToAccount"] = true
	vm.distributeTCYStake(ctx, mgr)
	delete(ek.fail, "SendToAccount")
	c.Check(ek.GetBalance(ctx, stakerAcc).AmountOf(common.RuneNative.Native()).IsZero(), Equals, true)
	c.Check(ek.GetRuneBalanceOfModule(ctx, TCYStakeName).Uint64(), Equals, uint64(30*common.One))

	// the remaining 30 RUNE are split pro rata; the staker holds 30% of TCY
	vm.distributeTCYStake(ctx, mgr)
	c.Check(ek.GetBalance(ctx, stakerAcc).AmountOf(common.RuneNative.Native()).Uint64(), Equals, uint64(9*common.One))
}

func (s *NetworkMgrCovCDSuite) TestRagnarokPoolCovCD(c *C) {
	ctx, mgr, ek, vm := covCDSetup(c)
	pool := covCDPool(c, ctx, ek, common.BTCAsset, 100*common.One, 10*common.One)

	pool.Status = PoolSuspended
	c.Check(vm.ragnarokPool(ctx, mgr, pool), IsNil)
	c.Check(vm.withdrawLiquidity(ctx, pool, NodeAccount{}, mgr), IsNil)
	pool.Status = PoolAvailable
	c.Check(vm.ragnarokPool(ctx, mgr, pool), ErrorMatches, "can't find any active nodes")
	ek.fail["ListActiveValidators"] = true
	c.Check(vm.ragnarokPool(ctx, mgr, pool), Equals, errKaboom)
}

func (s *NetworkMgrCovCDSuite) TestCalcBlockRewardsInvalidConfigCovCD(c *C) {
	ctx, _, _, vm := covCDSetup(c)
	one := cosmos.NewUint(common.One)
	results := []cosmos.Uint{}
	a, b, d, e, f, g, h, i := vm.calcBlockRewards(ctx, one, one, one, one, one, one, cosmos.ZeroUint(), 0, 100, 0, 0, 0, 0, 0)
	results = append(results, a, b, d, e, f, g, h, i)
	a, b, d, e, f, g, h, i = vm.calcBlockRewards(ctx, one, one, one, one, one, one, cosmos.ZeroUint(), 6, 0, 0, 0, 0, 0, 0)
	results = append(results, a, b, d, e, f, g, h, i)
	for _, r := range results {
		c.Check(r.IsZero(), Equals, true)
	}

	// income splits above 100% are clamped so the total never exceeds system income
	reserve := cosmos.NewUint(1_000_000 * common.One)
	fees := cosmos.NewUint(100 * common.One)
	_, _, _, dev, burn, tcy, mkt, pol := vm.calcBlockRewards(ctx, one, one, one, one, reserve, fees, cosmos.ZeroUint(),
		6, 100, 6000, 6000, 1000, 6000, 6000)
	blockReward := cosmos.NewUint(uint64(cosmos.NewDec(int64(reserve.Uint64())).QuoInt64(6).QuoInt64(100).RoundInt64()))
	income := blockReward.Add(fees)
	sum := dev.Add(burn).Add(tcy).Add(mkt).Add(pol)
	c.Check(sum.LTE(income), Equals, true, Commentf("sum %s income %s", sum, income))
	c.Check(tcy.Equal(common.GetSafeShare(cosmos.NewUint(1000), cosmos.NewUint(10_000), income)), Equals, true)
	c.Check(mkt.IsZero(), Equals, true)
	c.Check(pol.IsZero(), Equals, true)
}

func (s *NetworkMgrCovCDSuite) TestSchedulingKeygenCovCD(c *C) {
	ctx, _, ek, vm := covCDSetup(c)
	ctx = ctx.WithBlockHeight(100)
	nas := NodeAccounts{}
	for i := 0; i < 4; i++ {
		na := GetRandomValidatorNode(NodeActive)
		c.Assert(ek.SetNodeAccount(ctx, na), IsNil)
		nas = append(nas, na)
	}

	// churning halted by mimir: nothing scheduled
	ek.SetMimir(ctx, "HaltChurning", 50)
	c.Assert(vm.scheduleKeygen(ctx, nas, AsgardKeygen, 200), IsNil)
	kb, err := ek.GetKeygenBlock(ctx, 200)
	c.Assert(err, IsNil)
	c.Check(kb.Keygens, HasLen, 0)
	ek.SetMimir(ctx, "HaltChurning", 0)

	// too many forced-to-leave members drops below the keygen threshold
	forced := make(NodeAccounts, len(nas))
	copy(forced, nas)
	for i := 0; i < 3; i++ {
		forced[i].ForcedToLeave = true
	}
	c.Check(vm.scheduleKeygen(ctx, forced, AsgardKeygen, 200), ErrorMatches, "not enough eligible keygen members.*")
	for i := range forced {
		forced[i].ForcedToLeave = true
	}
	c.Check(vm.scheduleKeygen(ctx, forced, AsgardKeygen, 200), ErrorMatches, "no eligible keygen members")
	// eligibility mimir read failure
	ek.fail["GetMimir:"+constants.MimirKeyDisableTSSSignerFiltering] = true
	c.Check(vm.scheduleKeygen(ctx, nas, AsgardKeygen, 200), NotNil)
	delete(ek.fail, "GetMimir:"+constants.MimirKeyDisableTSSSignerFiltering)

	ek.fail["GetKeygenBlock"] = true
	c.Check(vm.scheduleKeygen(ctx, nas, AsgardKeygen, 200), ErrorMatches, "fail to get keygen block.*")
	delete(ek.fail, "GetKeygenBlock")
	ek.fail["GetAsgardVaultsByStatus:"+ActiveVault.String()] = true
	c.Check(vm.scheduleKeygen(ctx, nas, AsgardKeygen, 200), ErrorMatches, "fail to get active vaults.*")
	delete(ek.fail, "GetAsgardVaultsByStatus:"+ActiveVault.String())

	// an active vault with the same membership short-circuits the keygen
	members := common.PubKeys{}
	for _, na := range nas {
		members = append(members, na.PubKeySet.Secp256k1)
	}
	existing := GetRandomVault()
	existing.Membership = members.Strings()
	c.Assert(ek.SetVault(ctx, existing), IsNil)
	c.Assert(vm.scheduleKeygen(ctx, nas, AsgardKeygen, 200), IsNil)
	kb, err = ek.GetKeygenBlock(ctx, 200)
	c.Assert(err, IsNil)
	c.Check(kb.Keygens, HasLen, 0)
	existing.Status = InactiveVault
	c.Assert(ek.SetVault(ctx, existing), IsNil)

	// empty init vaults are retired when a keygen is scheduled
	emptyInit := GetRandomVault()
	emptyInit.Status = InitVault
	emptyInit.Coins = nil
	c.Assert(ek.SetVault(ctx, emptyInit), IsNil)
	fundedInit := GetRandomVault()
	fundedInit.Status = InitVault
	fundedInit.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)))
	c.Assert(ek.SetVault(ctx, fundedInit), IsNil)

	c.Assert(vm.scheduleKeygen(ctx, nas, AsgardKeygen, 200), IsNil)
	kb, err = ek.GetKeygenBlock(ctx, 200)
	c.Assert(err, IsNil)
	c.Assert(kb.Keygens, HasLen, 1)
	c.Check(kb.Keygens[0].GetMembers(), HasLen, 4)
	got, err := ek.GetVault(ctx, emptyInit.PubKey)
	c.Assert(err, IsNil)
	c.Check(got.Status, Equals, InactiveVault)
	got, err = ek.GetVault(ctx, fundedInit.PubKey)
	c.Assert(err, IsNil)
	c.Check(got.Status, Equals, InitVault)

	// init vault lookup failure is tolerated
	ek.fail["GetAsgardVaultsByStatus:"+InitVault.String()] = true
	c.Assert(vm.scheduleKeygen(ctx, nas, AsgardKeygen, 201), IsNil)
	delete(ek.fail, "GetAsgardVaultsByStatus:"+InitVault.String())
}

func (s *NetworkMgrCovCDSuite) TestRotateVaultErrorsCovCD(c *C) {
	ctx, _, ek, vm := covCDSetup(c)
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(ek.SetNodeAccount(ctx, na), IsNil)
	old := GetRandomVault()
	old.Membership = []string{na.PubKeySet.Secp256k1.String()}
	c.Assert(ek.SetVault(ctx, old), IsNil)
	// an inactive vault still in the index is cleaned up after rotation
	stale := GetRandomVault()
	stale.Status = InactiveVault
	c.Assert(ek.SetVault(ctx, stale), IsNil)

	next := GetRandomVault()
	next.Status = InitVault
	next.Membership = []string{na.PubKeySet.Secp256k1.String()}
	next.PubKeyFrost = GetRandomEd25519PubKey()

	ek.fail["GetAsgardVaultsByStatus"] = true
	c.Check(vm.RotateVault(ctx, next), Equals, errKaboom)
	delete(ek.fail, "GetAsgardVaultsByStatus")
	ek.fail["SetVault"] = true
	c.Check(vm.RotateVault(ctx, next), Equals, errKaboom)
	delete(ek.fail, "SetVault")
	ek.fail["GetNodeAccountByPubKey"] = true
	c.Check(vm.RotateVault(ctx, next), Equals, errKaboom)
	delete(ek.fail, "GetNodeAccountByPubKey")
	ek.fail["SetNodeAccount"] = true
	c.Check(vm.RotateVault(ctx, next), Equals, errKaboom)
	delete(ek.fail, "SetNodeAccount")

	ek.fail["RemoveFromAsgardIndex"] = true
	c.Assert(vm.RotateVault(ctx, next), IsNil)
	delete(ek.fail, "RemoveFromAsgardIndex")
	got, err := ek.GetVault(ctx, next.PubKey)
	c.Assert(err, IsNil)
	c.Check(got.Status, Equals, ActiveVault)
	updated, err := ek.GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(updated.GetSignerMembership().Contains(next.PubKey), Equals, true)
	c.Check(updated.GetSignerMembership().Contains(next.PubKeyFrost), Equals, true)

	// cleanup removes inactive asgards from the index
	c.Assert(vm.cleanupAsgardIndex(ctx), IsNil)
	asgards, err := ek.GetAsgardVaults(ctx)
	c.Assert(err, IsNil)
	for _, v := range asgards {
		c.Check(v.PubKey.Equals(stale.PubKey), Equals, false)
	}
	ek.fail["GetAsgardVaults"] = true
	c.Check(vm.cleanupAsgardIndex(ctx), ErrorMatches, "fail to get all asgards.*")
}

func (s *NetworkMgrCovCDSuite) TestPaySaverYieldCovCD(c *C) {
	ctx, _, ek, vm := covCDSetup(c)
	ek.SetMimir(ctx, constants.SynthYieldBasisPoints.String(), 5000)
	ek.SetMimir(ctx, constants.MaxSynthsForSaversYield.String(), 0)
	runeAmt := cosmos.NewUint(10 * common.One)

	ek.fail["GetPool"] = true
	c.Check(vm.paySaverYield(ctx, common.BTCAsset, runeAmt), Equals, errKaboom)
	delete(ek.fail, "GetPool")

	// no layer1 pool: nothing to pay
	c.Check(vm.paySaverYield(ctx, common.BTCAsset, runeAmt), IsNil)
	covCDPool(c, ctx, ek, common.BTCAsset, 1000*common.One, 10*common.One)

	synth := common.BTCAsset.GetSyntheticAsset()
	ek.fail["GetPool:"+synth.String()] = true
	c.Check(vm.paySaverYield(ctx, common.BTCAsset, runeAmt), Equals, errKaboom)
	delete(ek.fail, "GetPool:"+synth.String())

	saver := NewPool()
	saver.Asset = synth
	saver.Status = PoolAvailable
	saver.BalanceAsset = cosmos.NewUint(5 * common.One)
	saver.LPUnits = cosmos.NewUint(5 * common.One)
	c.Assert(ek.SetPool(ctx, saver), IsNil)

	for _, f := range []string{"MintToModule", "Send", "SetPool", "GetModuleAddress:" + ModuleName, "GetModuleAddress:" + AsgardName} {
		ek.fail[f] = true
		c.Check(vm.paySaverYield(ctx, common.BTCAsset, runeAmt), Equals, errKaboom, Commentf("%s", f))
		delete(ek.fail, f)
	}
	// a broken mimir read is surfaced
	ek.fail["GetMimir:"+constants.SynthYieldBasisPoints.String()] = true
	c.Check(vm.paySaverYield(ctx, common.BTCAsset, runeAmt), Equals, errKaboom)
	delete(ek.fail, "GetMimir:"+constants.SynthYieldBasisPoints.String())

	// atomic: failures above left the saver untouched
	got, err := ek.GetPool(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(got.BalanceAsset.Uint64(), Equals, uint64(5*common.One))

	// 10 RUNE = 0.1 BTC; saver share 5/(10*2) = 25%; 50% yield => 0.0125 BTC
	c.Assert(vm.paySaverYield(ctx, common.BTCAsset, runeAmt), IsNil)
	got, err = ek.GetPool(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(got.BalanceAsset.Uint64(), Equals, uint64(5*common.One+1_250_000))
	c.Check(ek.GetBalanceOfModule(ctx, AsgardName, synth.Native()).Uint64(), Equals, uint64(1_250_000))

	// a pool that is not available pays nothing
	p, err := ek.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	p.Status = PoolStaged
	c.Assert(ek.SetPool(ctx, p), IsNil)
	c.Assert(vm.paySaverYield(ctx, common.BTCAsset, runeAmt), IsNil)
	got, err = ek.GetPool(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(got.BalanceAsset.Uint64(), Equals, uint64(5*common.One+1_250_000))
}

func (s *NetworkMgrCovCDSuite) TestOverSolvencyCovCD(c *C) {
	ctx, mgr, ek, vm := covCDSetup(c)
	ctx = ctx.WithBlockHeight(10_000)

	// interval disabled
	ek.SetMimir(ctx, constants.OverSolvencyCheckInterval.String(), 0)
	c.Check(vm.processOverSolvency(ctx, mgr), IsNil)
	ek.SetMimir(ctx, constants.OverSolvencyCheckInterval.String(), 100)

	btcPool := covCDPool(c, ctx, ek, common.BTCAsset, 1000*common.One, 10*common.One)
	_ = btcPool
	active := GetRandomVault()
	active.Coins = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(10*common.One)))
	c.Assert(ek.SetVault(ctx, active), IsNil)
	inactive := GetRandomVault()
	inactive.Status = InactiveVault
	c.Assert(ek.SetVault(ctx, inactive), IsNil)

	signingPeriod := ek.GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	gas := common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)), common.NewCoin(common.RuneNative, cosmos.NewUint(1))}
	mkItem := func(vault common.PubKey, coin common.Coin) TxOutItem {
		return TxOutItem{
			Chain: coin.Asset.GetChain(), ToAddress: GetRandomBTCAddress(), VaultPubKey: vault,
			Coin: coin, MaxGas: gas, InHash: GetRandomTxHash(), Memo: "OUT:" + GetRandomTxHash().String(),
		}
	}
	btc := func(amt uint64) common.Coin { return common.NewCoin(common.BTCAsset, cosmos.NewUint(amt)) }
	// pending (past) outbounds
	past := NewTxOut(ctx.BlockHeight() - 1)
	past.TxArray = []TxOutItem{
		mkItem(active.PubKey, btc(common.One)),                                      // counted
		mkItem(inactive.PubKey, btc(common.One)),                                    // inactive vault
		mkItem(active.PubKey, common.NewCoin(common.RuneNative, cosmos.NewUint(5))), // native
	}
	c.Assert(ek.SetTxOut(ctx, past), IsNil)
	// too old for the BTC signing window
	old := NewTxOut(ctx.BlockHeight() - signingPeriod - 1)
	old.TxArray = []TxOutItem{mkItem(active.PubKey, btc(common.One))}
	c.Assert(ek.SetTxOut(ctx, old), IsNil)
	// scheduled (current) outbounds
	now := NewTxOut(ctx.BlockHeight())
	now.TxArray = []TxOutItem{
		mkItem(active.PubKey, btc(2*common.One)),
		mkItem(inactive.PubKey, btc(common.One)),
		mkItem(active.PubKey, common.NewCoin(common.RuneNative, cosmos.NewUint(5))),
	}
	c.Assert(ek.SetTxOut(ctx, now), IsNil)

	amounts, err := vm.calculateNetworkSolvency(ctx, mgr)
	c.Assert(err, IsNil)
	c.Assert(amounts, HasLen, 1)
	c.Check(amounts[0].Asset.Equals(common.BTCAsset), Equals, true)
	// 10 BTC in the vault - 10 BTC owed to the pool - 1 pending - 2 scheduled - 2x gas
	c.Check(amounts[0].Amount.Int64(), Equals, int64(-3*common.One-2000))

	// txout read failures are surfaced
	ek.fail["GetTxOut"] = true
	_, err = vm.calculateNetworkSolvency(ctx, mgr)
	c.Check(err, ErrorMatches, "fail to get txout.*")
	delete(ek.fail, "GetTxOut")

	// an outbound in an asset without a pool is an accounting error
	stray := NewTxOut(ctx.BlockHeight())
	stray.TxArray = append(append([]TxOutItem{}, now.TxArray...), mkItem(active.PubKey, common.NewCoin(common.ETHAsset, cosmos.NewUint(1))))
	c.Assert(ek.SetTxOut(ctx, stray), IsNil)
	_, err = vm.calculateNetworkSolvency(ctx, mgr)
	c.Check(err, ErrorMatches, ".*unexpected asset ETH.ETH not found in tracking map.*")
	c.Check(vm.processOverSolvency(ctx, mgr), ErrorMatches, "failed to calculate network solvency.*")
	strayPast := NewTxOut(ctx.BlockHeight() - 1)
	strayPast.TxArray = append(append([]TxOutItem{}, past.TxArray...), mkItem(active.PubKey, common.NewCoin(common.ETHAsset, cosmos.NewUint(1))))
	c.Assert(ek.SetTxOut(ctx, now), IsNil)
	c.Assert(ek.SetTxOut(ctx, strayPast), IsNil)
	_, err = vm.calculateNetworkSolvency(ctx, mgr)
	c.Check(err, ErrorMatches, ".*unexpected asset ETH.ETH not found in tracking map \\(pending txout\\).*")
	c.Assert(ek.SetTxOut(ctx, past), IsNil)

	// retiring vaults mean a churn is in progress
	ek.fail["GetAsgardVaultsByStatus:"+RetiringVault.String()] = true
	c.Check(vm.processOverSolvency(ctx, mgr), ErrorMatches, "failed to get retiring vaults.*")
	delete(ek.fail, "GetAsgardVaultsByStatus:"+RetiringVault.String())
	retiring := GetRandomVault()
	retiring.Status = RetiringVault
	c.Assert(ek.SetVault(ctx, retiring), IsNil)
	c.Check(vm.processOverSolvency(ctx, mgr), IsNil)
	_, err = vm.calculateNetworkSolvency(ctx, mgr)
	c.Check(err, ErrorMatches, "churn in progress.*")
}

func (s *NetworkMgrCovCDSuite) TestSwapToOverSolvencyIncomeCovCD(c *C) {
	ctx, mgr, ek, vm := covCDSetup(c)

	c.Check(vm.SwapToOverSolvencyIncome(ctx, mgr, common.NoCoin), IsNil)

	addr, err := cosmos.AccAddressFromBech32(ek.GetConstants().GetStringValue(constants.OverSolvencyAddress))
	c.Assert(err, IsNil)
	before := ek.GetBalance(ctx, addr).AmountOf(common.RuneNative.Native())
	rune := common.NewCoin(common.RuneNative, cosmos.NewUint(3*common.One))
	ek.fail["SendToAccount"] = true
	c.Check(vm.SwapToOverSolvencyIncome(ctx, mgr, rune), ErrorMatches, "fail to send RUNE to over-solvency address.*")
	delete(ek.fail, "SendToAccount")
	c.Assert(vm.SwapToOverSolvencyIncome(ctx, mgr, rune), IsNil)
	c.Check(ek.GetBalance(ctx, addr).AmountOf(common.RuneNative.Native()).Sub(before).Int64(), Equals, int64(3*common.One))

	// non-RUNE income needs the advanced swap queue
	ek.SetMimir(ctx, constants.EnableAdvSwapQueue.String(), 0)
	c.Assert(ek.AdvSwapQueueEnabled(ctx), Equals, false)
	err = vm.SwapToOverSolvencyIncome(ctx, mgr, common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)))
	c.Check(err, ErrorMatches, ".*advanced swap queue is disabled.*")
}

func (s *NetworkMgrCovCDSuite) TestMigrateFundsDustAndEdgesCovCD(c *C) {
	ctx, mgr, ek, vm := covCDSetup(c)
	const height = int64(1000)
	ctx = ctx.WithBlockHeight(height)
	ek.SetMimir(ctx, constants.FundMigrationInterval.String(), 10)
	covCDPool(c, ctx, ek, common.BTCAsset, 1000*common.One, 10*common.One)

	active := GetRandomVault()
	active.Routers = nil
	c.Assert(ek.SetVault(ctx, active), IsNil)

	// retiring vault on its 7th round (> ChurnMigrateRounds) holding only BTC dust,
	// RUNE (never migrated), a zero coin and XMR (no target vault serves XMR)
	retiring := GetRandomVault()
	retiring.Status = RetiringVault
	retiring.StatusSince = height - 60
	retiring.Chains = append(retiring.Chains, common.XMRChain.String())
	retiring.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(1000)),
		common.NewCoin(common.RuneNative, cosmos.NewUint(common.One)),
		common.NewCoin(common.DOGEAsset, cosmos.ZeroUint()),
		common.NewCoin(common.XMRAsset, cosmos.NewUint(common.One)),
	}
	c.Assert(ek.SetVault(ctx, retiring), IsNil)

	// retiring vault with nothing left but a deficit is retired to inactive
	drained := GetRandomVault()
	drained.Status = RetiringVault
	drained.StatusSince = height - 60
	drained.Coins = nil
	drained.Deficit = common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(5)))
	c.Assert(ek.SetVault(ctx, drained), IsNil)

	// error paths leave the dust in place
	for _, f := range []string{"GetPool:" + common.BTCAsset.String(), "Send:" + ReserveName + ":" + AsgardName} {
		ek.fail[f] = true
		c.Check(vm.migrateFunds(ctx, mgr), NotNil, Commentf("%s", f))
		delete(ek.fail, f)
		got, err := ek.GetVault(ctx, retiring.PubKey)
		c.Assert(err, IsNil)
		c.Check(got.GetCoin(common.BTCAsset).Amount.Uint64(), Equals, uint64(1000))
	}
	ek.fail["GetTxOut"] = true
	ek.fail["SetVault"] = true
	c.Check(vm.migrateFunds(ctx, mgr), ErrorMatches, "fail to save vault.*")
	delete(ek.fail, "GetTxOut")
	delete(ek.fail, "SetVault")
	ek.fail["SetPool"] = true
	c.Check(vm.migrateFunds(ctx, mgr), ErrorMatches, "fail to save pool.*")
	delete(ek.fail, "SetPool")

	poolBefore, err := ek.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(vm.migrateFunds(ctx, mgr), IsNil)

	got, err := ek.GetVault(ctx, retiring.PubKey)
	c.Assert(err, IsNil)
	c.Check(got.GetCoin(common.BTCAsset).Amount.IsZero(), Equals, true)
	c.Check(got.GetCoin(common.XMRAsset).Amount.Uint64(), Equals, uint64(common.One))
	c.Check(got.Status, Equals, RetiringVault)
	poolAfter, err := ek.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	// the burnt dust is compensated to the pool from the reserve
	c.Check(poolAfter.BalanceAsset.Uint64(), Equals, poolBefore.BalanceAsset.Uint64()-1000)
	c.Check(poolAfter.BalanceRune.Uint64(), Equals, poolBefore.BalanceRune.Uint64()+100_000)

	gotDrained, err := ek.GetVault(ctx, drained.PubKey)
	c.Assert(err, IsNil)
	c.Check(gotDrained.Status, Equals, InactiveVault)
	c.Check(gotDrained.HasDeficit(), Equals, true)

	// the active target vault received nothing (dust is burnt, not migrated)
	gotActive, err := ek.GetVault(ctx, active.PubKey)
	c.Assert(err, IsNil)
	c.Check(gotActive.GetCoin(common.BTCAsset).Amount.IsZero(), Equals, true)
	items, err := mgr.TxOutStore().GetOutboundItems(ctx)
	c.Assert(err, IsNil)
	c.Check(items, HasLen, 0)

	// lookup failures are surfaced; disabled interval or no active vaults is a no-op
	ek.fail["GetAsgardVaultsByStatus:"+ActiveVault.String()] = true
	c.Check(vm.migrateFunds(ctx, mgr), Equals, errKaboom)
	delete(ek.fail, "GetAsgardVaultsByStatus:"+ActiveVault.String())
	ek.fail["GetAsgardVaultsByStatus:"+RetiringVault.String()] = true
	c.Check(vm.migrateFunds(ctx, mgr), Equals, errKaboom)
	delete(ek.fail, "GetAsgardVaultsByStatus:"+RetiringVault.String())
	ek.SetMimir(ctx, constants.FundMigrationInterval.String(), 0)
	c.Check(vm.migrateFunds(ctx, mgr), IsNil)
}

func (s *NetworkMgrCovCDSuite) TestUpdateNetworkIncomeSplitsCovCD(c *C) {
	if notMocknetCov() {
		c.Skip("mocknet only: income split and burn expectations use mocknet constants")
	}
	ctx, mgr, ek, vm := covCDSetup(c)
	p := covCDPool(c, ctx, ek, common.ETHAsset, 100*common.One, 100*common.One)
	vault := GetRandomVault()
	vault.Coins = common.NewCoins(common.NewCoin(p.Asset, p.BalanceAsset))
	c.Assert(ek.SetVault(ctx, vault), IsNil)
	c.Assert(ek.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Assert(ek.SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	c.Assert(ek.SetNetwork(ctx, NewNetwork()), IsNil)

	for key, val := range map[string]int64{
		constants.DevFundSystemIncomeBps.String():       500,
		constants.MarketingFundSystemIncomeBps.String(): 500,
		constants.SystemIncomeBurnRateBps.String():      100,
		constants.TCYStakeSystemIncomeBps.String():      500,
		constants.POLReserveSystemIncomeBps.String():    500,
		constants.StableReserveEnabled.String():         1,
		constants.StableReservePOLShareBps.String():     5000,
		constants.ReserveMaxCap.String():                1,
	} {
		ek.SetMimir(ctx, key, val)
	}
	c.Assert(ek.AddToLiquidityFees(ctx, common.ETHAsset, cosmos.NewUint(50*common.One)), IsNil)

	devAddr, err := cosmos.AccAddressFromBech32(ek.GetConstants().GetStringValue(constants.DevFundAddress))
	c.Assert(err, IsNil)
	modBal := func(name string) uint64 { return ek.GetRuneBalanceOfModule(ctx, name).Uint64() }
	reserveBefore := modBal(ReserveName)

	// every transfer failure aborts the whole update atomically
	for _, f := range []string{
		"SendToAccount",
		"Send:" + ReserveName + ":" + ModuleName,
		"BurnFromModule",
		"Send:" + ReserveName + ":" + TCYStakeName,
		"Send:" + ReserveName + ":" + StableReserveName,
		"Send:" + ReserveName + ":" + POLReserveName,
		"Send:" + ReserveName + ":" + BondName,
		"Send:" + ReserveName + ":" + AsgardName,
	} {
		ek.fail[f] = true
		c.Check(vm.UpdateNetwork(ctx, mgr), NotNil, Commentf("%s", f))
		delete(ek.fail, f)
		c.Check(modBal(ReserveName), Equals, reserveBefore, Commentf("%s", f))
	}

	devBefore := ek.GetBalance(ctx, devAddr).AmountOf(common.RuneNative.Native()).Uint64()
	maxSupplyBefore := ek.GetConfigInt64(ctx, constants.MaxRuneSupply)
	c.Assert(vm.UpdateNetwork(ctx, mgr), IsNil)

	c.Check(modBal(ReserveName) < reserveBefore, Equals, true)
	// dev and marketing fund share an address on mocknet
	c.Check(ek.GetBalance(ctx, devAddr).AmountOf(common.RuneNative.Native()).Uint64() > devBefore, Equals, true)
	tcy := modBal(TCYStakeName)
	stable := modBal(StableReserveName)
	pol := modBal(POLReserveName)
	c.Check(tcy > 0, Equals, true)
	c.Check(stable > 0, Equals, true)
	// half the POL share goes to the stable reserve
	c.Check(stable == pol || stable+1 == pol || stable == pol+1, Equals, true, Commentf("stable %d pol %d", stable, pol))
	// burnt income lowers the max supply
	c.Check(ek.GetConfigInt64(ctx, constants.MaxRuneSupply) < maxSupplyBefore, Equals, true)

	// an out-of-range share is clamped: everything goes to the stable reserve
	ek.SetMimir(ctx, constants.StableReservePOLShareBps.String(), 20_000)
	c.Assert(ek.AddToLiquidityFees(ctx, common.ETHAsset, cosmos.NewUint(50*common.One)), IsNil)
	polBefore := modBal(POLReserveName)
	c.Assert(vm.UpdateNetwork(ctx, mgr), IsNil)
	c.Check(modBal(POLReserveName), Equals, polBefore)
	c.Check(modBal(StableReserveName) > stable, Equals, true)
	ek.SetMimir(ctx, constants.StableReservePOLShareBps.String(), -5)
	stableBefore := modBal(StableReserveName)
	c.Assert(vm.UpdateNetwork(ctx, mgr), IsNil)
	c.Check(modBal(StableReserveName), Equals, stableBefore)
	c.Check(modBal(POLReserveName) > polBefore, Equals, true)
}

func (k covCDKeeper) ListValidatorsByStatus(ctx cosmos.Context, status NodeStatus) (NodeAccounts, error) {
	if k.fail["ListValidatorsByStatus"] || k.fail["ListValidatorsByStatus:"+status.String()] {
		return nil, errKaboom
	}
	return k.Keeper.ListValidatorsByStatus(ctx, status)
}

func (k covCDKeeper) ListValidatorsWithBond(ctx cosmos.Context) (NodeAccounts, error) {
	if k.fail["ListValidatorsWithBond"] {
		return nil, errKaboom
	}
	return k.Keeper.ListValidatorsWithBond(ctx)
}

func (k covCDKeeper) GetNodeAccountJail(ctx cosmos.Context, addr cosmos.AccAddress) (Jail, error) {
	if k.fail["GetNodeAccountJail"] {
		return Jail{}, errKaboom
	}
	return k.Keeper.GetNodeAccountJail(ctx, addr)
}

func (k covCDKeeper) GetNodeAccountSlashPoints(ctx cosmos.Context, addr cosmos.AccAddress) (int64, error) {
	if k.fail["GetNodeAccountSlashPoints"] {
		return 0, errKaboom
	}
	return k.Keeper.GetNodeAccountSlashPoints(ctx, addr)
}

func (k covCDKeeper) GetRagnarokBlockHeight(ctx cosmos.Context) (int64, error) {
	if k.fail["GetRagnarokBlockHeight"] {
		return 0, errKaboom
	}
	return k.Keeper.GetRagnarokBlockHeight(ctx)
}

func (k covCDKeeper) GetRagnarokNth(ctx cosmos.Context) (int64, error) {
	if k.fail["GetRagnarokNth"] {
		return 0, errKaboom
	}
	return k.Keeper.GetRagnarokNth(ctx)
}

func (k covCDKeeper) GetRagnarokPending(ctx cosmos.Context) (int64, error) {
	if k.fail["GetRagnarokPending"] {
		return 0, errKaboom
	}
	return k.Keeper.GetRagnarokPending(ctx)
}

func (k covCDKeeper) GetRagnarokWithdrawPosition(ctx cosmos.Context) (RagnarokWithdrawPosition, error) {
	if k.fail["GetRagnarokWithdrawPosition"] {
		return RagnarokWithdrawPosition{}, errKaboom
	}
	return k.Keeper.GetRagnarokWithdrawPosition(ctx)
}

func (k covCDKeeper) GetNetwork(ctx cosmos.Context) (Network, error) {
	if k.fail["GetNetwork"] {
		return Network{}, errKaboom
	}
	return k.Keeper.GetNetwork(ctx)
}

func (k covCDKeeper) GetBondProviders(ctx cosmos.Context, addr cosmos.AccAddress) (BondProviders, error) {
	if k.fail["GetBondProviders"] {
		return BondProviders{}, errKaboom
	}
	return k.Keeper.GetBondProviders(ctx, addr)
}
