//go:build mainnet
// +build mainnet

package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// The mainnet store migrations run once, inside the upgrade pre-blocker; any
// keeper failure must surface as a returned (wrapped) error so the upgrade
// aborts instead of committing partial state. These tests inject keeper
// failures at each step and assert the migration reports the failing step.

var errMainnetCovInjected = errors.New("injected keeper failure")

// mainnetCovFaults selects which keeper calls fail. A nil predicate / false
// flag passes the call through to the real keeper.
type mainnetCovFaults struct {
	getPool        func(common.Asset) bool
	setPool        func(Pool) bool
	send           func(from, to string, coins common.Coins) bool
	sendAcc        func(from string) bool
	burn           func(common.Coin) bool
	listActive     func() bool
	getPools       func() bool
	mint           bool
	burnAll        func(common.Asset) bool
	getPOL         bool
	setPOL         bool
	getRUNEPool    bool
	getLP          bool
	vaultsByStatus bool
	tradeUnit      bool
	securedAsset   bool
	getStreaming   bool
	setVault       bool
	deleteMimir    bool
}

type mainnetCovFaultKeeper struct {
	keeper.Keeper
	f *mainnetCovFaults
}

func (k mainnetCovFaultKeeper) GetPool(ctx cosmos.Context, asset common.Asset) (Pool, error) {
	if k.f.getPool != nil && k.f.getPool(asset) {
		return Pool{}, errMainnetCovInjected
	}
	return k.Keeper.GetPool(ctx, asset)
}

func (k mainnetCovFaultKeeper) SetPool(ctx cosmos.Context, pool Pool) error {
	if k.f.setPool != nil && k.f.setPool(pool) {
		return errMainnetCovInjected
	}
	return k.Keeper.SetPool(ctx, pool)
}

func (k mainnetCovFaultKeeper) GetPools(ctx cosmos.Context) (Pools, error) {
	if k.f.getPools != nil && k.f.getPools() {
		return nil, errMainnetCovInjected
	}
	return k.Keeper.GetPools(ctx)
}

func (k mainnetCovFaultKeeper) SendFromModuleToModule(ctx cosmos.Context, from, to string, coins common.Coins) error {
	if k.f.send != nil && k.f.send(from, to, coins) {
		return errMainnetCovInjected
	}
	return k.Keeper.SendFromModuleToModule(ctx, from, to, coins)
}

func (k mainnetCovFaultKeeper) SendFromModuleToAccount(ctx cosmos.Context, from string, to cosmos.AccAddress, coins common.Coins) error {
	if k.f.sendAcc != nil && k.f.sendAcc(from) {
		return errMainnetCovInjected
	}
	return k.Keeper.SendFromModuleToAccount(ctx, from, to, coins)
}

func (k mainnetCovFaultKeeper) MintToModule(ctx cosmos.Context, module string, coin common.Coin) error {
	if k.f.mint {
		return errMainnetCovInjected
	}
	return k.Keeper.MintToModule(ctx, module, coin)
}

func (k mainnetCovFaultKeeper) BurnFromModule(ctx cosmos.Context, module string, coin common.Coin) error {
	if k.f.burn != nil && k.f.burn(coin) {
		return errMainnetCovInjected
	}
	return k.Keeper.BurnFromModule(ctx, module, coin)
}

func (k mainnetCovFaultKeeper) BurnEntireSynthSupply(ctx cosmos.Context, synthAsset common.Asset) (cosmos.Uint, error) {
	if k.f.burnAll != nil && k.f.burnAll(synthAsset) {
		return cosmos.ZeroUint(), errMainnetCovInjected
	}
	return k.Keeper.BurnEntireSynthSupply(ctx, synthAsset)
}

func (k mainnetCovFaultKeeper) GetPOL(ctx cosmos.Context) (ProtocolOwnedLiquidity, error) {
	if k.f.getPOL {
		return ProtocolOwnedLiquidity{}, errMainnetCovInjected
	}
	return k.Keeper.GetPOL(ctx)
}

func (k mainnetCovFaultKeeper) SetPOL(ctx cosmos.Context, pol ProtocolOwnedLiquidity) error {
	if k.f.setPOL {
		return errMainnetCovInjected
	}
	return k.Keeper.SetPOL(ctx, pol)
}

func (k mainnetCovFaultKeeper) GetRUNEPool(ctx cosmos.Context) (RUNEPool, error) {
	if k.f.getRUNEPool {
		return RUNEPool{}, errMainnetCovInjected
	}
	return k.Keeper.GetRUNEPool(ctx)
}

func (k mainnetCovFaultKeeper) GetLiquidityProvider(ctx cosmos.Context, asset common.Asset, addr common.Address) (LiquidityProvider, error) {
	if k.f.getLP {
		return LiquidityProvider{}, errMainnetCovInjected
	}
	return k.Keeper.GetLiquidityProvider(ctx, asset, addr)
}

func (k mainnetCovFaultKeeper) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	if k.f.listActive != nil && k.f.listActive() {
		return nil, errMainnetCovInjected
	}
	return k.Keeper.ListActiveValidators(ctx)
}

func (k mainnetCovFaultKeeper) GetAsgardVaultsByStatus(ctx cosmos.Context, status VaultStatus) (Vaults, error) {
	if k.f.vaultsByStatus {
		return nil, errMainnetCovInjected
	}
	return k.Keeper.GetAsgardVaultsByStatus(ctx, status)
}

func (k mainnetCovFaultKeeper) GetTradeUnit(ctx cosmos.Context, asset common.Asset) (TradeUnit, error) {
	if k.f.tradeUnit {
		return TradeUnit{}, errMainnetCovInjected
	}
	return k.Keeper.GetTradeUnit(ctx, asset)
}

func (k mainnetCovFaultKeeper) GetSecuredAsset(ctx cosmos.Context, asset common.Asset) (SecuredAsset, error) {
	if k.f.securedAsset {
		return SecuredAsset{}, errMainnetCovInjected
	}
	return k.Keeper.GetSecuredAsset(ctx, asset)
}

func (k mainnetCovFaultKeeper) GetStreamingSwap(ctx cosmos.Context, txID common.TxID) (StreamingSwap, error) {
	if k.f.getStreaming {
		return StreamingSwap{}, errMainnetCovInjected
	}
	return k.Keeper.GetStreamingSwap(ctx, txID)
}

func (k mainnetCovFaultKeeper) SetVault(ctx cosmos.Context, vault Vault) error {
	if k.f.setVault {
		return errMainnetCovInjected
	}
	return k.Keeper.SetVault(ctx, vault)
}

func (k mainnetCovFaultKeeper) DeleteMimir(ctx cosmos.Context, key string) error {
	if k.f.deleteMimir {
		return errMainnetCovInjected
	}
	return k.Keeper.DeleteMimir(ctx, key)
}

// mainnetCovInject swaps the manager's keeper for a fault-injecting wrapper.
func mainnetCovInject(mgr *Mgrs, f *mainnetCovFaults) {
	mgr.K = mainnetCovFaultKeeper{Keeper: mgr.K, f: f}
}

// mainnetCovNth returns a predicate that is true only on its n-th (1-based)
// matching invocation.
func mainnetCovNth(n int) func() bool {
	calls := 0
	return func() bool {
		calls++
		return calls == n
	}
}

func mainnetCovAssetNth(asset common.Asset, n int) func(common.Asset) bool {
	calls := 0
	return func(a common.Asset) bool {
		if !a.Equals(asset) {
			return false
		}
		calls++
		return calls == n
	}
}

func mainnetCovIsAsset(asset common.Asset) func(Pool) bool {
	return func(p Pool) bool { return p.Asset.Equals(asset) }
}

func mainnetCovCoinIs(asset common.Asset) func(common.Coin) bool {
	return func(c common.Coin) bool { return c.Asset.Equals(asset) }
}

func mainnetCovSendOf(from, to string, asset common.Asset) func(string, string, common.Coins) bool {
	return func(f, t string, coins common.Coins) bool {
		return f == from && t == to && len(coins) > 0 && coins[0].Asset.Equals(asset)
	}
}

const mainnetCovInjectedRe = ".*injected keeper failure"

func (s *MigrationsMainnetCovSuite) TestMigrate4to5KeeperFaults(c *C) {
	for _, tc := range []struct {
		f   *mainnetCovFaults
		err string
	}{
		{&mainnetCovFaults{mint: true}, "^injected keeper failure$"},
		{&mainnetCovFaults{send: func(string, string, common.Coins) bool { return true }}, "^injected keeper failure$"},
		{&mainnetCovFaults{sendAcc: func(from string) bool { return from == ModuleName }}, "^injected keeper failure$"},
	} {
		ctx, mgr := mainnetCovSetup(c)
		mainnetCovInject(mgr, tc.f)
		c.Assert(NewMigrator(mgr).Migrate4to5(ctx), ErrorMatches, tc.err)
	}
}

// Individual slash-refund transfer failures are logged and skipped.
func (s *MigrationsMainnetCovSuite) TestSlashRefundTransferFailuresAreSkipped(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, mainnetSlashRefunds12to13Total+14856919212689)
	mainnetCovInject(mgr, &mainnetCovFaults{sendAcc: func(from string) bool { return from == ReserveName }})
	m := NewMigrator(mgr)
	c.Assert(m.Migrate5to6(ctx), IsNil)
	c.Assert(m.Migrate12to13(ctx), IsNil)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, mainnetSlashRefunds12to13Total+14856919212689)
	c.Assert(mainnetCovRuneBalance(c, ctx, mgr, mainnetSlashRefunds5to6[0].address).IsZero(), Equals, true)

	const runeCredit = uint64(26_008_730_208_467)
	ctx, mgr = mainnetCov14to15Setup(c, runeCredit*2, runeCredit)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, mainnetSlashRefunds14to15Total)
	mainnetCovInject(mgr, &mainnetCovFaults{sendAcc: func(from string) bool { return from == ReserveName }})
	c.Assert(NewMigrator(mgr).Migrate14to15(ctx), IsNil)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, mainnetSlashRefunds14to15Total+runeCredit)
}

func (s *MigrationsMainnetCovSuite) TestMakeFakeTxInObservationFaults(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	tx := GetRandomObservedTx()
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, GetRandomValidatorNode(NodeActive)), IsNil)
	realKeeper := mgr.K

	mainnetCovInject(mgr, &mainnetCovFaults{listActive: mainnetCovNth(1)})
	c.Assert(makeFakeTxInObservation(ctx, mgr, ObservedTxs{tx}), ErrorMatches, "^injected keeper failure$")

	// the handler itself cannot list validators -> every invocation failed
	mgr.K = realKeeper
	mainnetCovInject(mgr, &mainnetCovFaults{listActive: mainnetCovNth(2)})
	c.Assert(makeFakeTxInObservation(ctx, mgr, ObservedTxs{tx}), ErrorMatches, "all 1 ObservedTxIn handler invocations failed")
}

func (s *MigrationsMainnetCovSuite) TestMigrate11to12KeeperFaults(c *C) {
	avaxUSDC := mustMigrationAsset(c, adr028AssetAVAXUSDC)
	for _, tc := range []struct {
		f   *mainnetCovFaults
		err string
	}{
		{&mainnetCovFaults{getPool: func(common.Asset) bool { return true }}, "failed to get AVAX.USDC pool.*"},
		{&mainnetCovFaults{setPool: func(Pool) bool { return true }}, "failed to save updated pool.*"},
		// LP lookups and mimir deletions are best-effort
		{&mainnetCovFaults{getLP: true, deleteMimir: true}, ""},
	} {
		ctx, mgr := mainnetCovSetup(c)
		mainnetCovSetPool(c, ctx, mgr, avaxUSDC, common.One, common.One, 1000)
		mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{
			Asset: avaxUSDC, AssetAddress: "0x467a0ec2d2f23f0bb165eb7e44a9b16b12f5a7b4", Units: cosmos.NewUint(10),
		})
		mgr.Keeper().SetMimir(ctx, "ADR012", 1)
		FundModule(c, ctx, mgr.Keeper(), ReserveName, 400*common.One)
		mainnetCovInject(mgr, tc.f)
		err := NewMigrator(mgr).Migrate11to12(ctx)
		if tc.err == "" {
			c.Assert(err, IsNil)
			p, poolErr := mgr.Keeper().GetPool(ctx, avaxUSDC)
			c.Assert(poolErr, IsNil)
			c.Assert(p.LPUnits.Uint64(), Equals, uint64(1000))
			v, mimirErr := mgr.Keeper().GetMimir(ctx, "ADR012")
			c.Assert(mimirErr, IsNil)
			c.Assert(v, Equals, int64(1))
			continue
		}
		c.Assert(err, ErrorMatches, tc.err)
	}
}

func (s *MigrationsMainnetCovSuite) TestMigrate14to15KeeperFaults(c *C) {
	const runeCredit = uint64(26_008_730_208_467)
	for _, tc := range []struct {
		f   *mainnetCovFaults
		err string
	}{
		{&mainnetCovFaults{getPool: func(common.Asset) bool { return true }}, "fail to get DOGE pool.*"},
		{&mainnetCovFaults{setPool: func(Pool) bool { return true }}, "fail to save DOGE pool.*"},
	} {
		ctx, mgr := mainnetCov14to15Setup(c, runeCredit*2, runeCredit)
		mainnetCovInject(mgr, tc.f)
		c.Assert(NewMigrator(mgr).Migrate14to15(ctx), ErrorMatches, tc.err)
	}
}

func (s *MigrationsMainnetCovSuite) TestSplitFreedRuneBurnFault(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 100)
	mainnetCovInject(mgr, &mainnetCovFaults{burn: func(common.Coin) bool { return true }})
	m := NewMigrator(mgr)
	c.Assert(m.splitFreedRune(ctx, cosmos.NewUint(10), "test"), ErrorMatches, "burn freed RUNE.*")
}

func (s *MigrationsMainnetCovSuite) TestMigrate15to16KeeperFaults(c *C) {
	atomSynth := common.ATOMAsset.GetSyntheticAsset()
	dogeSynth := common.DOGEAsset.GetSyntheticAsset()
	baseUSDC := mustMigrationAsset(c, adr028AssetBASEUSDC)
	bscBTCB := mustMigrationAsset(c, adr028AssetBSCBTCB)
	dogeDeltaR := cosmos.NewUint(mainnetCov15to16Step1(c)[common.DOGEAsset.String()])
	for _, tc := range []struct {
		f   *mainnetCovFaults
		err string
	}{
		{&mainnetCovFaults{setVault: true}, "conciliation: vault 2v28: fail to zero vault 2v28.*"},
		{&mainnetCovFaults{getRUNEPool: true}, "conciliation: pol haircut: fail to get RUNEPool.*"},
		{&mainnetCovFaults{getPools: mainnetCovNth(1)}, "conciliation: pol haircut: fail to compute runePoolValue pre.*"},
		{&mainnetCovFaults{getPool: mainnetCovAssetNth(common.BCHAsset, 1)}, "conciliation: pol haircut: fail to get pool BCH.BCH.*"},
		{&mainnetCovFaults{setPool: mainnetCovIsAsset(common.BCHAsset)}, "conciliation: pol haircut: fail to save pool BCH.BCH.*"},
		{&mainnetCovFaults{getPOL: true}, "conciliation: pol haircut: fail to get POL.*"},
		{&mainnetCovFaults{setPOL: true}, "conciliation: pol haircut: fail to save POL.*"},
		{&mainnetCovFaults{send: mainnetCovSendOf(ReserveName, ModuleName, dogeSynth)}, "conciliation: reserve synth burn: fail to send DOGE/DOGE.*"},
		{&mainnetCovFaults{burn: mainnetCovCoinIs(dogeSynth)}, "conciliation: reserve synth burn: fail to burn DOGE/DOGE.*"},
		{&mainnetCovFaults{getPool: mainnetCovAssetNth(common.DOGEAsset, 2)}, "conciliation: reserve synth burn: fail to get pool DOGE.DOGE.*"},
		{&mainnetCovFaults{setPool: func(p Pool) bool {
			return p.Asset.Equals(common.DOGEAsset) && p.BalanceRune.LT(dogeDeltaR)
		}}, "conciliation: reserve synth burn: fail to save paired cut DOGE.DOGE.*"},
		{&mainnetCovFaults{getPool: mainnetCovAssetNth(common.BTCAsset, 2)}, "conciliation: savers haircut: fail to re-read pool BTC.BTC.*"},
		{&mainnetCovFaults{getPool: mainnetCovAssetNth(atomSynth, 1)}, "conciliation: savers haircut: fail to get saver pool GAIA/ATOM.*"},
		{&mainnetCovFaults{setPool: mainnetCovIsAsset(atomSynth)}, "conciliation: savers haircut: fail to save saver pool GAIA/ATOM.*"},
		{&mainnetCovFaults{send: mainnetCovSendOf(AsgardName, ModuleName, atomSynth)}, "conciliation: savers haircut: fail to move synths from Asgard for GAIA/ATOM.*"},
		{&mainnetCovFaults{burn: mainnetCovCoinIs(atomSynth)}, "conciliation: savers haircut: fail to burn synths for GAIA/ATOM.*"},
		{&mainnetCovFaults{getPool: mainnetCovAssetNth(baseUSDC, 1)}, "conciliation: treasury draw: fail to get pool BASE.USDC.*"},
		{&mainnetCovFaults{setPool: mainnetCovIsAsset(baseUSDC)}, "conciliation: treasury draw: fail to save pool BASE.USDC.*"},
		{&mainnetCovFaults{getPool: mainnetCovAssetNth(bscBTCB, 2)}, "conciliation: synthunits recompute: fail to get pool BSC.BTCB.*"},
		{&mainnetCovFaults{setPool: mainnetCovIsAsset(bscBTCB)}, "conciliation: synthunits recompute: fail to save pool BSC.BTCB.*"},
		{&mainnetCovFaults{getPools: mainnetCovNth(2)}, "conciliation: pol solve: fail to compute runePoolValue post.*"},
		{&mainnetCovFaults{getStreaming: true}, "conciliation: streaming swap: failed to get streaming swap.*"},
	} {
		f := mainnetCov15to16Setup(c)
		mainnetCovInject(f.mgr, tc.f)
		c.Assert(NewMigrator(f.mgr).Migrate15to16(f.ctx), ErrorMatches, tc.err+mainnetCovInjectedRe)
	}
}

// The freed-RUNE disposal of the paired cut and the treasury draw abort the
// migration if Asgard cannot move the RUNE. Asgard→thorchain RUNE moves happen
// once per step-1 pool (9), then for the DOGE paired cut, then for the first
// treasury draw (BASE.USDC).
func (s *MigrationsMainnetCovSuite) TestMigrate15to16FreedRuneFaults(c *C) {
	for _, tc := range []struct {
		nth int
		err string
	}{
		{10, "conciliation: reserve synth burn: fail to dispose paired RUNE DOGE.DOGE.*"},
		{11, "conciliation: treasury draw: fail to dispose freed RUNE for BASE.USDC.*"},
	} {
		f := mainnetCov15to16Setup(c)
		nth := mainnetCovNth(tc.nth)
		mainnetCovInject(f.mgr, &mainnetCovFaults{send: func(from, to string, coins common.Coins) bool {
			return from == AsgardName && to == ModuleName && coins[0].Asset.IsRune() && nth()
		}})
		c.Assert(NewMigrator(f.mgr).Migrate15to16(f.ctx), ErrorMatches, tc.err+mainnetCovInjectedRe)
	}
}

type mainnetCov17to18Fixture struct {
	ctx           cosmos.Context
	mgr           *Mgrs
	fox, usdc     common.Asset
	usdt, baseETH common.Asset
	foxDelta      uint64
	usdcSaver     common.Address
	usdtSaver     common.Address
	securedOpsPre cosmos.Uint
}

// mainnetCov17to18Setup seeds a plain writedown pool (ETH.FOX), the
// savers-haircut pool with live free backing (ETH.USDC), an UNBACKED
// zero-savers pool (ETH.USDT), a re-seeded pool with residual synths
// (BASE.ETH) and an untouched pool (BTC.BTC).
func mainnetCov17to18Setup(c *C) *mainnetCov17to18Fixture {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	k := mgr.Keeper()
	f := &mainnetCov17to18Fixture{ctx: ctx, mgr: mgr}
	f.fox = mustMigrationAsset(c, "ETH.FOX-0XC770EEFAD204B5180DF6A14EE197D99D808EE52D")
	f.usdc = mustMigrationAsset(c, adr028AssetETHUSDC)
	f.usdt = mustMigrationAsset(c, adr028AssetETHUSDT)
	f.baseETH = mustMigrationAsset(c, "BASE.ETH")
	f.foxDelta = 47623739145492

	mainnetCovSetPool(c, ctx, mgr, f.fox, 1_000*common.One, 2*f.foxDelta, 1_000)

	// ETH.USDC: 100 synths (90 Asgard/savers, 10 Reserve), vault holds 50
	usdcDelta := uint64(106612841513705)
	mainnetCovSetPool(c, ctx, mgr, f.usdc, 1_000*common.One, usdcDelta+10*common.One, 1_000)
	usdcSynth := f.usdc.GetSyntheticAsset()
	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(usdcSynth, cosmos.NewUint(90*common.One)))
	mainnetCovFundModuleCoin(c, ctx, mgr, ReserveName, common.NewCoin(usdcSynth, cosmos.NewUint(10*common.One)))
	mainnetCovSetPool(c, ctx, mgr, usdcSynth, 0, 90*common.One, 90*common.One)
	f.usdcSaver = GetRandomETHAddress()
	k.SetLiquidityProvider(ctx, LiquidityProvider{Asset: usdcSynth, AssetAddress: f.usdcSaver, Units: cosmos.NewUint(90 * common.One)})
	mainnetCovActiveVault(c, ctx, mgr, common.Coins{common.NewCoin(f.usdc, cosmos.NewUint(50*common.One))}, common.ETHChain)

	// ETH.USDT: unbacked, savers get zeroed after the full burn
	usdtDelta := uint64(98310077343961)
	mainnetCovSetPool(c, ctx, mgr, f.usdt, 1_000*common.One, usdtDelta+10*common.One, 1_000)
	usdtSynth := f.usdt.GetSyntheticAsset()
	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(usdtSynth, cosmos.NewUint(100*common.One)))
	mainnetCovSetPool(c, ctx, mgr, usdtSynth, 0, 100*common.One, 100*common.One)
	f.usdtSaver = GetRandomETHAddress()
	k.SetLiquidityProvider(ctx, LiquidityProvider{Asset: usdtSynth, AssetAddress: f.usdtSaver, Units: cosmos.NewUint(100 * common.One)})

	// BASE.ETH seed pool with residual synths above 2x its seed depth
	mainnetCovSetPool(c, ctx, mgr, f.baseETH, 2_000_000*common.One, 50*common.One, 1_000)
	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(f.baseETH.GetSyntheticAsset(), cosmos.NewUint(10*common.One)))

	mainnetCovSetPool(c, ctx, mgr, common.BTCAsset, 1_000*common.One, 10*common.One, 1_000)
	f.securedOpsPre = mainnetCovRuneBalance(c, ctx, mgr, adr028SecuredOpsAddr)
	return f
}

func (s *MigrationsMainnetCovSuite) TestMigrate17to18SaversHaircutAndZeroSavers(c *C) {
	f := mainnetCov17to18Setup(c)
	ctx, k := f.ctx, f.mgr.Keeper()
	c.Assert(NewMigrator(f.mgr).Migrate17to18(ctx), IsNil)

	fox, err := k.GetPool(ctx, f.fox)
	c.Assert(err, IsNil)
	c.Assert(fox.BalanceAsset.Uint64(), Equals, f.foxDelta)
	c.Assert(fox.BalanceRune.Uint64(), Equals, uint64(500*common.One))

	// USDC savers settled at free backing (50 vault - 10 pool = 40)
	usdcSynth := f.usdc.GetSyntheticAsset()
	saverPool, err := k.GetPool(ctx, usdcSynth)
	c.Assert(err, IsNil)
	c.Assert(saverPool.BalanceAsset.Uint64(), Equals, uint64(40*common.One))
	c.Assert(saverPool.LPUnits.Uint64(), Equals, uint64(40*common.One))
	lp, err := k.GetLiquidityProvider(ctx, usdcSynth, f.usdcSaver)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.Uint64(), Equals, uint64(40*common.One))
	c.Assert(k.GetBalanceOfModule(ctx, AsgardName, usdcSynth.Native()).Uint64(), Equals, uint64(40*common.One))
	c.Assert(k.GetBalanceOfModule(ctx, ReserveName, usdcSynth.Native()).IsZero(), Equals, true)
	// still clamped (2x10 <= 40): SynthUnits are not recomputed
	usdcPool, err := k.GetPool(ctx, f.usdc)
	c.Assert(err, IsNil)
	c.Assert(usdcPool.SynthUnits.IsZero(), Equals, true)

	// USDT: synths burned, savers zeroed
	usdtSynth := f.usdt.GetSyntheticAsset()
	c.Assert(k.GetTotalSupply(ctx, usdtSynth).IsZero(), Equals, true)
	usdtSaverPool, err := k.GetPool(ctx, usdtSynth)
	c.Assert(err, IsNil)
	c.Assert(usdtSaverPool.BalanceAsset.IsZero(), Equals, true)
	c.Assert(usdtSaverPool.LPUnits.IsZero(), Equals, true)
	lp, err = k.GetLiquidityProvider(ctx, usdtSynth, f.usdtSaver)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.IsZero(), Equals, true)

	// BASE.ETH re-seeded and its residual synths burned
	base, err := k.GetPool(ctx, f.baseETH)
	c.Assert(err, IsNil)
	c.Assert(base.BalanceAsset.Uint64(), Equals, uint64(277408024))
	c.Assert(base.BalanceRune.Uint64(), Equals, uint64(1232884481190))
	c.Assert(k.GetTotalSupply(ctx, f.baseETH.GetSyntheticAsset()).IsZero(), Equals, true)

	c.Assert(mainnetCovRuneBalance(c, ctx, f.mgr, adr028SecuredOpsAddr).Sub(f.securedOpsPre).Uint64(),
		Equals, uint64(adr028SecuredOpsCoverageRune))
}

func (s *MigrationsMainnetCovSuite) TestMigrate17to18KeeperFaults(c *C) {
	fox := mustMigrationAsset(c, "ETH.FOX-0XC770EEFAD204B5180DF6A14EE197D99D808EE52D")
	usdc := mustMigrationAsset(c, adr028AssetETHUSDC)
	usdt := mustMigrationAsset(c, adr028AssetETHUSDT)
	baseETH := mustMigrationAsset(c, "BASE.ETH")
	usdcSynth, usdtSynth := usdc.GetSyntheticAsset(), usdt.GetSyntheticAsset()
	isUSDT := func(a common.Asset) bool { return a.Equals(usdtSynth) }
	burnNth := func(asset common.Asset, n int) func(common.Coin) bool {
		calls := 0
		return func(coin common.Coin) bool {
			if !coin.Asset.Equals(asset) {
				return false
			}
			calls++
			return calls == n
		}
	}
	for _, tc := range []struct {
		f   *mainnetCovFaults
		err string
	}{
		{&mainnetCovFaults{getRUNEPool: true}, "adr028 writedown: fail to get RUNEPool"},
		{&mainnetCovFaults{getPools: mainnetCovNth(1)}, "adr028 writedown: fail to compute runePoolValue pre"},
		{&mainnetCovFaults{getPool: mainnetCovAssetNth(fox, 3)}, "adr028 writedown: fail to get pool ETH.FOX.*"},
		{&mainnetCovFaults{setPool: mainnetCovIsAsset(fox)}, "adr028 writedown: fail to set pool ETH.FOX.*"},
		{&mainnetCovFaults{vaultsByStatus: true}, "adr028 writedown: fail to get .* vaults for ETH.USDC.*"},
		{&mainnetCovFaults{tradeUnit: true}, "adr028 writedown: fail to get trade unit for ETH.USDC.*"},
		{&mainnetCovFaults{securedAsset: true}, "adr028 writedown: fail to get secured asset for ETH.USDC.*"},
		{&mainnetCovFaults{getPool: mainnetCovAssetNth(usdcSynth, 1)}, "adr028 writedown: fail savers haircut for ETH.USDC.*: fail to get saver pool.*"},
		{&mainnetCovFaults{setPool: mainnetCovIsAsset(usdcSynth)}, "adr028 writedown: fail savers haircut .*: fail to save saver pool.*"},
		{&mainnetCovFaults{send: mainnetCovSendOf(AsgardName, ModuleName, usdcSynth)}, "adr028 writedown: fail savers haircut .*: fail to move synths from Asgard.*"},
		{&mainnetCovFaults{burn: burnNth(usdcSynth, 1)}, "adr028 writedown: fail savers haircut .*: fail to burn synths from Asgard.*"},
		{&mainnetCovFaults{send: mainnetCovSendOf(ReserveName, ModuleName, usdcSynth)}, "adr028 writedown: fail savers haircut .*: fail to move synths from Reserve.*"},
		{&mainnetCovFaults{burn: burnNth(usdcSynth, 2)}, "adr028 writedown: fail savers haircut .*: fail to burn synths from Reserve.*"},
		{&mainnetCovFaults{burnAll: isUSDT}, "adr028 writedown: fail to burn synth supply for ETH.USDT.*"},
		{&mainnetCovFaults{getPool: mainnetCovAssetNth(usdtSynth, 1)}, "adr028 writedown: fail to zero savers for ETH.USDT.*: fail to get saver pool.*"},
		{&mainnetCovFaults{setPool: mainnetCovIsAsset(usdtSynth)}, "adr028 writedown: fail to zero savers for ETH.USDT.*: fail to save zeroed saver pool.*"},
		{&mainnetCovFaults{send: mainnetCovSendOf(AsgardName, ReserveName, common.RuneNative)}, "adr028 writedown: fail to move freed RUNE Asgard→Reserve for ETH.FOX.*"},
		{&mainnetCovFaults{getPool: mainnetCovAssetNth(baseETH, 3)}, "adr028 seed: fail to get pool BASE.ETH.*"},
		{&mainnetCovFaults{burnAll: func(a common.Asset) bool { return a.Equals(baseETH.GetSyntheticAsset()) }}, "adr028 seed: fail to burn synth supply for BASE.ETH.*"},
		{&mainnetCovFaults{setPool: mainnetCovIsAsset(baseETH)}, "adr028 seed: fail to set pool BASE.ETH.*"},
		{&mainnetCovFaults{getPools: mainnetCovNth(2)}, "adr028 writedown: fail to get pools for SynthUnits recompute"},
		{&mainnetCovFaults{setPool: mainnetCovIsAsset(common.BTCAsset)}, "adr028 writedown: fail to save recomputed SynthUnits for BTC.BTC.*"},
		{&mainnetCovFaults{getPools: mainnetCovNth(3)}, "adr028 writedown: fail to compute runePoolValue post"},
		{&mainnetCovFaults{sendAcc: func(from string) bool { return from == ReserveName }}, "adr028 secured-ops: send Reserve→.*"},
	} {
		f := mainnetCov17to18Setup(c)
		mainnetCovInject(f.mgr, tc.f)
		c.Assert(NewMigrator(f.mgr).Migrate17to18(f.ctx), ErrorMatches, tc.err+mainnetCovInjectedRe, Commentf(tc.err))
	}
}
