//go:build mainnet
// +build mainnet

package thorchain

import (
	"fmt"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// MigrationsMainnetCovSuite exercises the mainnet-only store migrations
// (migrations_mainnet.go) against synthetic keeper state that mirrors the
// on-chain records each migration reads.
type MigrationsMainnetCovSuite struct{}

var _ = Suite(&MigrationsMainnetCovSuite{})

func (s *MigrationsMainnetCovSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

// mainnetCovRuneBalance returns the RUNE balance of a bech32 account.
func mainnetCovRuneBalance(c *C, ctx cosmos.Context, mgr *Mgrs, bech32 string) cosmos.Uint {
	acc, err := cosmos.AccAddressFromBech32(bech32)
	c.Assert(err, IsNil)
	return cosmos.NewUint(mgr.Keeper().GetBalance(ctx, acc).AmountOf(common.RuneNative.Native()).Uint64())
}

// mainnetCovFundModuleCoin mints an arbitrary coin and sends it to a module.
func mainnetCovFundModuleCoin(c *C, ctx cosmos.Context, mgr *Mgrs, module string, coin common.Coin) {
	c.Assert(mgr.Keeper().MintToModule(ctx, ModuleName, coin), IsNil)
	if module != ModuleName {
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, ModuleName, module, common.NewCoins(coin)), IsNil)
	}
}

// mainnetCovDrainModules burns the RUNE that setupManagerForTest pre-funds into
// the given modules so tests can reason about exact balances.
func mainnetCovDrainModules(c *C, ctx cosmos.Context, mgr *Mgrs, modules ...string) {
	for _, module := range modules {
		bal := mgr.Keeper().GetRuneBalanceOfModule(ctx, module)
		if bal.IsZero() {
			continue
		}
		coin := common.NewCoin(common.RuneNative, bal)
		c.Assert(mgr.Keeper().SendFromModuleToModule(ctx, module, ModuleName, common.NewCoins(coin)), IsNil)
		c.Assert(mgr.Keeper().BurnFromModule(ctx, ModuleName, coin), IsNil)
	}
}

// mainnetCovSumRefunds sums the expected per-address refund for a slash table.
func mainnetCovSumRefunds(rows []struct {
	address string
	amount  uint64
},
) (uint64, map[string]uint64) {
	total := uint64(0)
	per := map[string]uint64{}
	for _, r := range rows {
		total += r.amount
		per[r.address] += r.amount
	}
	return total, per
}

// mainnetCovSetup returns a manager whose Asgard and Reserve hold no RUNE.
func mainnetCovSetup(c *C) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	mainnetCovDrainModules(c, ctx, mgr, AsgardName, ReserveName)
	return ctx, mgr
}

func mainnetCovActiveVault(c *C, ctx cosmos.Context, mgr *Mgrs, coins common.Coins, chains ...common.Chain) Vault {
	v := GetRandomVault()
	v.Status = ActiveVault
	v.Type = AsgardVault
	v.Coins = coins
	v.Chains = common.Chains(chains).Strings()
	c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
	return v
}

func (s *MigrationsMainnetCovSuite) TestMigrate1to2DeletesStoreVersionKey(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	store := ctx.KVStore(keyThorchain)
	key := []byte("_ver//")
	store.Set(key, []byte("1"))
	c.Assert(store.Has(key), Equals, true)

	c.Assert(NewMigrator(mgr).Migrate1to2(ctx), IsNil)
	c.Assert(store.Has(key), Equals, false)

	// idempotent: second run with no key is a no-op
	c.Assert(NewMigrator(mgr).Migrate1to2(ctx), IsNil)
}

func (s *MigrationsMainnetCovSuite) TestMigrate2to3SchedulesStagenetRefund(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	ctx = ctx.WithBlockHeight(100)

	// no active vault -> refund cannot be scheduled
	c.Assert(NewMigrator(mgr).Migrate2to3(ctx), ErrorMatches, ".*fail to get active asgard vaults.*")

	bscUsdt := mustMigrationAsset(c, "BSC.USDT-0X55D398326F99059FF775485246999027B3197955")
	saveMigrationNetworkFee(c, ctx, mgr, common.BSCChain)
	vault := mainnetCovActiveVault(c, ctx, mgr, common.Coins{
		common.NewCoin(bscUsdt, cosmos.NewUint(5_000_000_000_000)),
	}, common.BSCChain)

	c.Assert(NewMigrator(mgr).Migrate2to3(ctx), IsNil)

	txOut, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 1)
	item := txOut.TxArray[0]
	c.Assert(item.Chain.Equals(common.BSCChain), Equals, true)
	c.Assert(item.ToAddress.String(), Equals, "0x3021C479f7F8C9f1D5c7d8523BA5e22C0Bcb5430")
	c.Assert(item.Coin.Asset.Equals(bscUsdt), Equals, true)
	c.Assert(item.Coin.Amount.Uint64(), Equals, uint64(4860737515919))
	c.Assert(item.Memo, Equals, "REFUND:A9AF3ED203079BB246CEE0ACD837FBA024BC846784DE488D5BE70044D8877C52")
	c.Assert(item.VaultPubKey.Equals(vault.PubKey), Equals, true)
}

func (s *MigrationsMainnetCovSuite) TestMigrate4to5MintsTCYAndRefundsSlashes(c *C) {
	ctx, mgr := mainnetCovSetup(c)

	total, per := mainnetCovSumRefunds(mainnetSlashRefunds4to5)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, total)

	// obsolete mimirs that must be cleared, plus a BSC one that must survive
	mgr.Keeper().SetMimir(ctx, "HALTBNBCHAIN", 1)
	mgr.Keeper().SetMimir(ctx, "HALTBSCBNB", 1)
	mgr.Keeper().SetMimir(ctx, "MaxConfirmations", 5)

	treasury := "thor10qh5272ktq4wes8ex343ky9rsuehcypddjh08k"
	treasuryAcc, err := cosmos.AccAddressFromBech32(treasury)
	c.Assert(err, IsNil)
	runeBefore := mainnetCovRuneBalance(c, ctx, mgr, treasury)

	c.Assert(NewMigrator(mgr).Migrate4to5(ctx), IsNil)

	// TCY: 210M minted, 206.6M to the claiming module, the remainder to treasury
	claiming := mgr.Keeper().GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native())
	c.Assert(claiming.Uint64(), Equals, uint64(206_606_541_28874864))
	treasuryTCY := mgr.Keeper().GetBalanceOf(ctx, treasuryAcc, common.TCY)
	c.Assert(treasuryTCY.Amount.Uint64(), Equals, uint64(210_000_000_00000000-206_606_541_28874864))
	c.Assert(mgr.Keeper().GetTotalSupply(ctx, common.TCY).Uint64(), Equals, uint64(210_000_000_00000000))

	// slash refunds were paid from the Reserve, which is now drained
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).IsZero(), Equals, true)
	for addr, amt := range per {
		got := mainnetCovRuneBalance(c, ctx, mgr, addr)
		if addr == treasury {
			got = common.SafeSub(got, runeBefore)
		}
		c.Assert(got.Uint64(), Equals, amt, Commentf(addr))
	}

	// TCY claimers were loaded from the mainnet claim list
	iter := mgr.Keeper().GetTCYClaimerIterator(ctx)
	c.Assert(iter.Valid(), Equals, true)
	iter.Close()

	// obsolete mimir cleanup ran
	v, err := mgr.Keeper().GetMimir(ctx, "HALTBNBCHAIN")
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(-1))
	v, err = mgr.Keeper().GetMimir(ctx, "MaxConfirmations")
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(-1))
	v, err = mgr.Keeper().GetMimir(ctx, "HALTBSCBNB")
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(1))
}

func (s *MigrationsMainnetCovSuite) TestMigrate4to5UnderfundedReserveLogsAndContinues(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	// Reserve is empty: every refund transfer fails but is only logged.
	c.Assert(NewMigrator(mgr).Migrate4to5(ctx), IsNil)
	c.Assert(mgr.Keeper().GetBalanceOfModule(ctx, TCYClaimingName, common.TCY.Native()).Uint64(),
		Equals, uint64(206_606_541_28874864))
	c.Assert(mainnetCovRuneBalance(c, ctx, mgr, mainnetSlashRefunds4to5[0].address).IsZero(), Equals, true)
}

func (s *MigrationsMainnetCovSuite) TestMigrate5to6SlashRefunds(c *C) {
	ctx, mgr := mainnetCovSetup(c)

	// Reserve below the hard-coded 14,856,919,212,689 requirement aborts
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 14856919212688)
	c.Assert(NewMigrator(mgr).Migrate5to6(ctx), ErrorMatches, "insufficient reserve balance for migration.*")
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(14856919212688))

	_, per := mainnetCovSumRefunds(mainnetSlashRefunds5to6)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 1)
	c.Assert(NewMigrator(mgr).Migrate5to6(ctx), IsNil)
	for addr, amt := range per {
		c.Assert(mainnetCovRuneBalance(c, ctx, mgr, addr).Uint64(), Equals, amt, Commentf(addr))
	}
}

func (s *MigrationsMainnetCovSuite) TestMigrate6to7ManualOutboundsAndInbounds(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	ctx = ctx.WithBlockHeight(50)

	// Without network fees the outbounds cannot be built and without active
	// validators the inbounds cannot be observed - both are only logged.
	c.Assert(NewMigrator(mgr).Migrate6to7(ctx), IsNil)
	txOut, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight()+300)
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 0)

	saveMigrationNetworkFee(c, ctx, mgr, common.ETHChain)
	saveMigrationNetworkFee(c, ctx, mgr, common.BSCChain)

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)

	// create the vaults the dropped inbounds were sent to so the fake
	// observations are recorded
	inbounds, err := mainnetManualInbounds6to7()
	c.Assert(err, IsNil)
	c.Assert(inbounds, HasLen, 3)
	for _, in := range inbounds {
		v := NewVault(1, ActiveVault, AsgardVault, in.ObservedPubKey, []string{in.Tx.Chain.String()}, nil)
		c.Assert(mgr.Keeper().SetVault(ctx, v), IsNil)
	}

	c.Assert(NewMigrator(mgr).Migrate6to7(ctx), IsNil)

	txOut, err = mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight()+300)
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 3)
	ethUSDT := mustMigrationAsset(c, "ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	for _, item := range txOut.TxArray {
		c.Assert(item.ToAddress.String(), Equals, "0x3c4a7c01811e14bb3d723d4961b4f2c28afc5e6e")
		c.Assert(item.Memo, Equals, fmt.Sprintf("OUT:%s", item.InHash))
	}
	c.Assert(txOut.TxArray[0].Coin.Asset.Equals(ethUSDT), Equals, true)
	c.Assert(txOut.TxArray[0].Coin.Amount.Uint64(), Equals, uint64(2999900000000))

	for _, in := range inbounds {
		voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, in.Tx.ID)
		c.Assert(err, IsNil)
		c.Assert(voter.Txs, Not(HasLen), 0, Commentf(in.Tx.ID.String()))
		c.Assert(voter.Txs[0].HasSigned(na.NodeAddress), Equals, true)
	}
}

func (s *MigrationsMainnetCovSuite) TestMakeFakeTxInObservation(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	tx := GetRandomObservedTx()

	err := makeFakeTxInObservation(ctx, mgr, ObservedTxs{tx})
	c.Assert(err, ErrorMatches, "no active validators to record fake observation")

	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	c.Assert(mgr.Keeper().SetVault(ctx, NewVault(1, ActiveVault, AsgardVault, tx.ObservedPubKey,
		[]string{tx.Tx.Chain.String()}, nil)), IsNil)

	c.Assert(makeFakeTxInObservation(ctx, mgr, ObservedTxs{tx}), IsNil)
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, tx.Tx.ID)
	c.Assert(err, IsNil)
	c.Assert(voter.Txs, HasLen, 1)
	c.Assert(voter.Txs[0].HasSigned(na.NodeAddress), Equals, true)
}

func (s *MigrationsMainnetCovSuite) TestMigrate8to9(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	ctx = ctx.WithBlockHeight(77)
	saveMigrationNetworkFee(c, ctx, mgr, common.ETHChain)
	saveMigrationNetworkFee(c, ctx, mgr, common.BSCChain)
	saveMigrationNetworkFee(c, ctx, mgr, common.AVAXChain)

	c.Assert(NewMigrator(mgr).Migrate8to9(ctx), IsNil)

	// CommonMigrate8to9 mimirs
	v, err := mgr.Keeper().GetMimir(ctx, constants.MinimumL1OutboundFeeUSD.String())
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(25000000))
	v, err = mgr.Keeper().GetMimir(ctx, constants.TargetOutboundFeeSurplusRune.String())
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(10000_00000000))

	expected, err := mainnetManualOutbounds8to9(ctx, mgr)
	c.Assert(err, IsNil)
	txOut, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, len(expected))
	for i := range expected {
		c.Assert(txOut.TxArray[i].InHash.Equals(expected[i].InHash), Equals, true)
		c.Assert(txOut.TxArray[i].Coin.Equals(expected[i].Coin), Equals, true)
	}
}

func (s *MigrationsMainnetCovSuite) TestMigrate8to9WithoutGasLogsOnly(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	c.Assert(NewMigrator(mgr).Migrate8to9(ctx), IsNil)
	txOut, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 0)
}

func (s *MigrationsMainnetCovSuite) TestMigrate9to10MovesNamiAndRetriesRecoveries(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	ctx = ctx.WithBlockHeight(88)

	// without NAMI in Asgard nor gas data, both steps are only logged
	c.Assert(NewMigrator(mgr).Migrate9to10(ctx), IsNil)
	c.Assert(mgr.Keeper().GetBalanceOfModule(ctx, TreasuryName, common.NAMI.Native()).IsZero(), Equals, true)

	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(common.NAMI, cosmos.NewUint(1_000_000)))
	for _, ch := range []common.Chain{common.ETHChain, common.BSCChain, common.AVAXChain, common.BTCChain, common.BASEChain} {
		saveMigrationNetworkFee(c, ctx, mgr, ch)
	}

	c.Assert(NewMigrator(mgr).Migrate9to10(ctx), IsNil)
	c.Assert(mgr.Keeper().GetBalanceOfModule(ctx, TreasuryName, common.NAMI.Native()).Uint64(), Equals, uint64(524245))
	c.Assert(mgr.Keeper().GetBalanceOfModule(ctx, AsgardName, common.NAMI.Native()).Uint64(), Equals, uint64(1_000_000-524245))

	expected, err := mainnetManualOutbounds9to10(ctx, mgr)
	c.Assert(err, IsNil)
	txOut, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, len(expected))
}

func (s *MigrationsMainnetCovSuite) TestMigrate10to11IsNoop(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	c.Assert(NewMigrator(mgr).Migrate10to11(ctx), IsNil)
}

func (s *MigrationsMainnetCovSuite) TestMigrate11to12RemovesBadLPsAndMimirs(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	avaxUSDC := mustMigrationAsset(c, "AVAX.USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E")

	pool := NewPool()
	pool.Asset = avaxUSDC
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.LPUnits = cosmos.NewUint(1000)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	bad := []LiquidityProvider{
		{Asset: avaxUSDC, RuneAddress: "thor1pe0pspu4ep85gxr5h9l6k49g024vemtr80hg4c", AssetAddress: "0x03c42ab083bd46202ee430AfC4D3dd8eD8c76c07", Units: cosmos.NewUint(100)},
		{Asset: avaxUSDC, RuneAddress: "", AssetAddress: "0x467a0ec2d2f23f0bb165eb7e44a9b16b12f5a7b4", Units: cosmos.NewUint(50)},
		// the affiliate record is intentionally absent: GetLiquidityProvider
		// returns an empty record (0 units) and removal is a no-op
	}
	for _, lp := range bad {
		mgr.Keeper().SetLiquidityProvider(ctx, lp)
	}
	good := LiquidityProvider{Asset: avaxUSDC, RuneAddress: GetRandomRUNEAddress(), Units: cosmos.NewUint(850)}
	mgr.Keeper().SetLiquidityProvider(ctx, good)

	for _, k := range []string{"HALTRADING", "ADR012", "ENABLEBSC", "ADD-CHAIN-XRP"} {
		mgr.Keeper().SetMimir(ctx, k, 1)
	}
	mgr.Keeper().SetMimir(ctx, "HALTTRADING", 1)

	// Reserve cannot pay the 400 RUNE refund -> error (after pool/mimir edits)
	c.Assert(NewMigrator(mgr).Migrate11to12(ctx), NotNil)

	ctx, mgr = mainnetCovSetup(c)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	for _, lp := range bad {
		mgr.Keeper().SetLiquidityProvider(ctx, lp)
	}
	mgr.Keeper().SetLiquidityProvider(ctx, good)
	for _, k := range []string{"HALTRADING", "ADR012", "ENABLEBSC", "ADD-CHAIN-XRP"} {
		mgr.Keeper().SetMimir(ctx, k, 1)
	}
	mgr.Keeper().SetMimir(ctx, "HALTTRADING", 1)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 500*common.One)

	c.Assert(NewMigrator(mgr).Migrate11to12(ctx), IsNil)

	got, err := mgr.Keeper().GetPool(ctx, avaxUSDC)
	c.Assert(err, IsNil)
	c.Assert(got.LPUnits.Uint64(), Equals, uint64(850))
	for _, lp := range bad {
		gotLP, lpErr := mgr.Keeper().GetLiquidityProvider(ctx, avaxUSDC, lp.GetAddress())
		c.Assert(lpErr, IsNil)
		c.Assert(gotLP.Units.IsZero(), Equals, true)
	}
	gotGood, err := mgr.Keeper().GetLiquidityProvider(ctx, avaxUSDC, good.GetAddress())
	c.Assert(err, IsNil)
	c.Assert(gotGood.Units.Uint64(), Equals, uint64(850))

	for _, k := range []string{"HALTRADING", "ADR012", "ENABLEBSC", "ADD-CHAIN-XRP"} {
		v, mimirErr := mgr.Keeper().GetMimir(ctx, k)
		c.Assert(mimirErr, IsNil)
		c.Assert(v, Equals, int64(-1), Commentf(k))
	}
	v, err := mgr.Keeper().GetMimir(ctx, "HALTTRADING")
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(1))

	c.Assert(mainnetCovRuneBalance(c, ctx, mgr, "thor1dvvr4kdeurs8fdwgrql6je7l2v9ma73dp50n7m").Uint64(), Equals, uint64(40000000000))
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(100*common.One))
}

func (s *MigrationsMainnetCovSuite) TestMigrate12to13SlashRefunds(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, mainnetSlashRefunds12to13Total-1)
	c.Assert(NewMigrator(mgr).Migrate12to13(ctx), ErrorMatches, "insufficient reserve balance for migration.*")

	total, per := mainnetCovSumRefunds(mainnetSlashRefunds12to13)
	c.Assert(total, Equals, mainnetSlashRefunds12to13Total)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 1)
	c.Assert(NewMigrator(mgr).Migrate12to13(ctx), IsNil)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).IsZero(), Equals, true)
	for addr, amt := range per {
		c.Assert(mainnetCovRuneBalance(c, ctx, mgr, addr).Uint64(), Equals, amt, Commentf(addr))
	}
}

func (s *MigrationsMainnetCovSuite) TestMigrate13to14BurnsReserve(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	FundModule(c, ctx, mgr.Keeper(), ReserveName, 10_000_000_00000000)
	c.Assert(NewMigrator(mgr).Migrate13to14(ctx), IsNil)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(9_300_000_00000000))
	v, err := mgr.Keeper().GetMimir(ctx, constants.MaxRuneSupply.String())
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(mgr.Keeper().GetTotalSupply(ctx, common.RuneAsset()).Uint64()))
}

// mainnetCov14to15Setup seeds the DOGE pool, Asgard and Reserve so that
// Migrate14to15 can run all four steps.
func mainnetCov14to15Setup(c *C, poolRune, asgard uint64) (cosmos.Context, *Mgrs) {
	ctx, mgr := mainnetCovSetup(c)
	pool := NewPool()
	pool.Asset = common.DOGEAsset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(poolRune)
	pool.BalanceAsset = cosmos.NewUint(1_000_000 * common.One)
	pool.LPUnits = cosmos.NewUint(1000)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	if asgard > 0 {
		FundModule(c, ctx, mgr.Keeper(), AsgardName, asgard)
	}
	return ctx, mgr
}

func (s *MigrationsMainnetCovSuite) TestMigrate14to15Aborts(c *C) {
	const runeCredit = uint64(26_008_730_208_467)

	// empty DOGE pool
	ctx, mgr := mainnetCovSetup(c)
	c.Assert(NewMigrator(mgr).Migrate14to15(ctx), ErrorMatches, "DOGE pool is empty.*")

	// drifted pool RUNE depth
	ctx, mgr = mainnetCov14to15Setup(c, runeCredit-1, 0)
	c.Assert(NewMigrator(mgr).Migrate14to15(ctx), ErrorMatches, ".*below expected slash credit.*")
	pool, err := mgr.Keeper().GetPool(ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceRune.Uint64(), Equals, runeCredit-1)

	// Asgard cannot fund the Reserve
	ctx, mgr = mainnetCov14to15Setup(c, runeCredit*2, 0)
	c.Assert(NewMigrator(mgr).Migrate14to15(ctx), ErrorMatches, "fail to move asgard->reserve.*")

	// Reserve short of the refund total after the Asgard credit
	ctx, mgr = mainnetCov14to15Setup(c, runeCredit*2, runeCredit)
	c.Assert(NewMigrator(mgr).Migrate14to15(ctx), ErrorMatches, "insufficient reserve balance for migration.*")
}

func (s *MigrationsMainnetCovSuite) TestMigrate14to15ReversesDogeSlash(c *C) {
	const (
		stolenDoge = uint64(122_221_658_117_429)
		runeCredit = uint64(26_008_730_208_467)
	)
	ctx, mgr := mainnetCov14to15Setup(c, runeCredit*3, runeCredit*2)
	total, per := mainnetCovSumRefunds(mainnetSlashRefunds14to15)
	c.Assert(total, Equals, mainnetSlashRefunds14to15Total)
	// Reserve holds the slash credited at slash time; with the Asgard move it
	// covers the full refund table.
	FundModule(c, ctx, mgr.Keeper(), ReserveName, total-runeCredit)

	c.Assert(NewMigrator(mgr).Migrate14to15(ctx), IsNil)

	pool, err := mgr.Keeper().GetPool(ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Assert(pool.BalanceRune.Uint64(), Equals, runeCredit*2)
	c.Assert(pool.BalanceAsset.Uint64(), Equals, 1_000_000*common.One+stolenDoge)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName).Uint64(), Equals, runeCredit)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).IsZero(), Equals, true)
	for addr, amt := range per {
		c.Assert(mainnetCovRuneBalance(c, ctx, mgr, addr).Uint64(), Equals, amt, Commentf(addr))
	}
}

func (s *MigrationsMainnetCovSuite) TestSplitFreedRune(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	m := NewMigrator(mgr)

	c.Assert(m.splitFreedRune(ctx, cosmos.ZeroUint(), "test"), IsNil)

	// Asgard empty -> the burn half cannot move
	c.Assert(m.splitFreedRune(ctx, cosmos.NewUint(11), "test"), ErrorMatches, "move burn-half.*")

	FundModule(c, ctx, mgr.Keeper(), AsgardName, 100)
	supplyBefore := mgr.Keeper().GetTotalSupply(ctx, common.RuneAsset())
	c.Assert(m.splitFreedRune(ctx, cosmos.NewUint(11), "test"), IsNil)
	// odd unit goes to the burn: 6 burned, 5 to reserve
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, AsgardName).Uint64(), Equals, uint64(89))
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(5))
	c.Assert(supplyBefore.Sub(mgr.Keeper().GetTotalSupply(ctx, common.RuneAsset())).Uint64(), Equals, uint64(6))

	// a single unit is all burn, nothing to reserve
	c.Assert(m.splitFreedRune(ctx, cosmos.NewUint(1), "test"), IsNil)
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(5))

	// burn half fits but the reserve half does not
	ctx, mgr = mainnetCovSetup(c)
	m = NewMigrator(mgr)
	FundModule(c, ctx, mgr.Keeper(), AsgardName, 6)
	c.Assert(m.splitFreedRune(ctx, cosmos.NewUint(11), "test"), ErrorMatches, "move reserve-half.*")
}

func (s *MigrationsMainnetCovSuite) TestADR028SolveOverflowBps(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	m := NewMigrator(mgr)

	postL := cosmos.NewUint(1000)
	postA := cosmos.NewUint(10_000)
	supply := cosmos.NewUint(4_000)
	target := cosmos.NewUint(1200)
	// no savers pool -> no overflow lever
	c.Assert(m.adr028SolveOverflowBps(ctx, common.BTCAsset, postL, postA, supply, target), Equals, uint64(0))

	saver := NewPool()
	saver.Asset = common.BTCAsset.GetSyntheticAsset()
	saver.BalanceAsset = cosmos.NewUint(5_000)
	saver.LPUnits = cosmos.NewUint(5_000)
	c.Assert(mgr.Keeper().SetPool(ctx, saver), IsNil)

	got := m.adr028SolveOverflowBps(ctx, common.BTCAsset, postL, postA, supply, target)
	c.Assert(got, Equals, adr028OverflowBps(postL, postA, supply, target, saver.BalanceAsset))
	c.Assert(got > 0, Equals, true)
}

// ---------------------------------------------------------------------------
// Migrate15to16 (ADR-028 conciliation)
// ---------------------------------------------------------------------------

// mainnetCov15to16Step1 lists the step-1 (POL haircut) ΔR per pool, mirroring
// the haircut table in Migrate15to16.
func mainnetCov15to16Step1(c *C) map[string]uint64 {
	return map[string]uint64{
		common.DOGEAsset.String(): 1_419_918_684_958,
		common.BCHAsset.String():  13_013_460_708_269,
		adr028AssetETHUSDT:        33_880_349_267_266,
		common.ETHAsset.String():  420_098_359_472_574,
		adr028AssetETHUSDC:        4_640_667_267_256,
		common.XRPAsset.String():  4_831_551_272,
		common.BTCAsset.String():  568_945_622_679_283,
		common.LTCAsset.String():  69_426_603_965_037,
		common.AVAXAsset.String(): 1_444_896_137_532,
	}
}

func mainnetCovSetPool(c *C, ctx cosmos.Context, mgr *Mgrs, asset common.Asset, runeDepth, assetDepth, units uint64) Pool {
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.BalanceRune = cosmos.NewUint(runeDepth)
	pool.BalanceAsset = cosmos.NewUint(assetDepth)
	pool.LPUnits = cosmos.NewUint(units)
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)
	return pool
}

type mainnetCov15to16Fixture struct {
	ctx           cosmos.Context
	mgr           *Mgrs
	asgardFunded  uint64
	saverAddr     common.Address
	destination   common.Address
	usdtVault     Vault
	stuckTxID     common.TxID
	treasuryAddr  common.Address
	polAddr       common.Address
	runePoolStart RUNEPool
}

// mainnetCov15to16Setup seeds every pool the conciliation waterfall reads.
func mainnetCov15to16Setup(c *C) *mainnetCov15to16Fixture {
	ctx, mgr := mainnetCovSetup(c)
	ctx = ctx.WithBlockHeight(500)
	f := &mainnetCov15to16Fixture{ctx: ctx, mgr: mgr}

	// step 0: the compromised vault
	v2v28 := NewVault(1, RetiringVault, AsgardVault, vault2v28PubKey, []string{common.ETHChain.String()}, nil)
	v2v28.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One))}
	c.Assert(mgr.Keeper().SetVault(ctx, v2v28), IsNil)

	// step 1 pools: RUNE depth = 2·ΔR so the haircut halves each pool
	total := uint64(0)
	for raw, deltaR := range mainnetCov15to16Step1(c) {
		asset := mustMigrationAsset(c, raw)
		mainnetCovSetPool(c, ctx, mgr, asset, 2*deltaR, 1_000_000*common.One, 1_000_000*common.One)
		total += 2 * deltaR
	}
	f.asgardFunded = total
	FundModule(c, ctx, mgr.Keeper(), AsgardName, total)

	// POL owns 10% of BTC; the RUNEPool is half reserve, half providers.
	polAddr, err := mgr.Keeper().GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	f.polAddr = polAddr
	mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{
		Asset: common.BTCAsset, RuneAddress: polAddr, Units: cosmos.NewUint(100_000 * common.One),
	})
	f.runePoolStart = RUNEPool{
		PoolUnits:    cosmos.NewUint(500 * common.One),
		ReserveUnits: cosmos.NewUint(500 * common.One),
	}
	mgr.Keeper().SetRUNEPool(ctx, f.runePoolStart)

	// BTC savers: synth supply held by Asgard and a savers vault pool
	btcSynth := common.BTCAsset.GetSyntheticAsset()
	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(btcSynth, cosmos.NewUint(100_000*common.One)))
	mainnetCovSetPool(c, ctx, mgr, btcSynth, 0, 100_000*common.One, 100_000*common.One)
	f.saverAddr = GetRandomBTCAddress()
	mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{
		Asset: btcSynth, AssetAddress: f.saverAddr, Units: cosmos.NewUint(100_000 * common.One),
	})

	// step 2: Reserve holds fewer DOGE synths than planned (capped) and the
	// exact BSC.BNB amount (whose L1 pool has no depth so pairing is skipped).
	mainnetCovSetPool(c, ctx, mgr, common.BNBBEP20Asset, 0, 0, 0)
	mainnetCovFundModuleCoin(c, ctx, mgr, ReserveName, common.NewCoin(common.DOGEAsset.GetSyntheticAsset(), cosmos.NewUint(1_000*common.One)))
	mainnetCovFundModuleCoin(c, ctx, mgr, ReserveName, common.NewCoin(common.BNBBEP20Asset.GetSyntheticAsset(), cosmos.NewUint(4_574_000_000)))

	// step 3: GAIA.ATOM tabled 3121 bps savers haircut, fully backed by Asgard
	// (the L1 pools must exist: step 4.5 re-saves every table pool)
	mainnetCovSetPool(c, ctx, mgr, common.ATOMAsset, 100_000*common.One, 100_000*common.One, 100_000*common.One)
	mainnetCovSetPool(c, ctx, mgr, mustMigrationAsset(c, adr028AssetAVAXUSDC), 100_000*common.One, 100_000*common.One, 100_000*common.One)
	atomSynth := common.ATOMAsset.GetSyntheticAsset()
	mainnetCovSetPool(c, ctx, mgr, atomSynth, 0, 10_000*common.One, 10_000*common.One)
	mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{
		Asset: atomSynth, AssetAddress: GetRandomGAIAAddress(), Units: cosmos.NewUint(6_000 * common.One),
	})
	mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{
		Asset: atomSynth, AssetAddress: GetRandomGAIAAddress(), Units: cosmos.NewUint(4_000 * common.One),
	})
	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(atomSynth, cosmos.NewUint(10_000*common.One)))
	// AVAX.USDC savers: Asgard holds less synth than the tabled haircut (capped)
	avaxUSDCSynth := mustMigrationAsset(c, adr028AssetAVAXUSDC).GetSyntheticAsset()
	mainnetCovSetPool(c, ctx, mgr, avaxUSDCSynth, 0, 10_000*common.One, 10_000*common.One)
	mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{
		Asset: avaxUSDCSynth, AssetAddress: GetRandomETHAddress(), Units: cosmos.NewUint(10_000 * common.One),
	})
	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(avaxUSDCSynth, cosmos.NewUint(100*common.One)))

	// step 4: treasury draws
	treasuryAddr, err := common.NewAddress(adr028TreasuryAddr)
	c.Assert(err, IsNil)
	f.treasuryAddr = treasuryAddr
	treasuryFunded := uint64(0)
	// BASE.USDC: full draw; BSC.BUSD: treasury short of units (capped);
	// BSC.ETH: no treasury LP record; BSC.BTCB: pool has zero depth;
	// TRON.TRX: full draw.
	mainnetCovSetPool(c, ctx, mgr, mustMigrationAsset(c, adr028AssetBSCBTCB), 0, 0, 0)
	for raw, cfg := range map[string]struct{ asset, units, treasury uint64 }{
		adr028AssetBASEUSDC:      {100_000 * common.One, 100_000 * common.One, 100_000 * common.One},
		adr028AssetBSCBUSD:       {100_000 * common.One, 100_000 * common.One, 1_000 * common.One},
		adr028AssetBSCETH:        {100_000 * common.One, 100_000 * common.One, 0},
		common.TRXAsset.String(): {100_000 * common.One, 100_000 * common.One, 100_000 * common.One},
	} {
		asset := mustMigrationAsset(c, raw)
		mainnetCovSetPool(c, ctx, mgr, asset, 100_000*common.One, cfg.asset, cfg.units)
		treasuryFunded += 100_000 * common.One
		if cfg.treasury > 0 {
			mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{
				Asset: asset, RuneAddress: treasuryAddr, Units: cosmos.NewUint(cfg.treasury),
			})
		}
	}
	FundModule(c, ctx, mgr.Keeper(), AsgardName, treasuryFunded)
	f.asgardFunded += treasuryFunded

	// step 5: the stuck streaming swap with already-produced ETH.USDT output
	ethUSDT := mustMigrationAsset(c, adr028AssetETHUSDT)
	f.stuckTxID, err = common.NewTxID(stuckStreamingTxStr)
	c.Assert(err, IsNil)
	ss := NewStreamingSwap(f.stuckTxID, 1182, 1, cosmos.NewUint(10_000*common.One), cosmos.NewUint(20_000*common.One))
	ss.Count = 484
	ss.Out = cosmos.NewUint(4_000 * common.One)
	mgr.Keeper().SetStreamingSwap(ctx, ss)
	f.destination = GetRandomETHAddress()
	tx := common.NewTx(f.stuckTxID, GetRandomETHAddress(), GetRandomETHAddress(),
		common.Coins{common.NewCoin(mustMigrationAsset(c, "ETH.DAI-0X6B175474E89094C44DA98B954EEDEAC495271D0F"), cosmos.NewUint(20_000*common.One))},
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.OneUint())}, "")
	msg := NewMsgSwap(tx, ethUSDT, f.destination, cosmos.NewUint(10_000*common.One), common.NoAddress,
		cosmos.ZeroUint(), "", "", nil, MarketSwap, 1182, 1, types.SwapVersion_v1, GetRandomBech32Addr())
	c.Assert(mgr.Keeper().SetSwapQueueItem(ctx, *msg, 0), IsNil)
	saveMigrationNetworkFee(c, ctx, mgr, common.ETHChain)
	f.usdtVault = mainnetCovActiveVault(c, ctx, mgr, common.Coins{
		common.NewCoin(ethUSDT, cosmos.NewUint(1_000_000*common.One)),
		common.NewCoin(common.ETHAsset, cosmos.NewUint(100*common.One)),
	}, common.ETHChain)

	return f
}

func (s *MigrationsMainnetCovSuite) TestMigrate15to16Conciliation(c *C) {
	f := mainnetCov15to16Setup(c)
	ctx, mgr := f.ctx, f.mgr
	k := mgr.Keeper()

	supplyBefore := k.GetTotalSupply(ctx, common.RuneAsset())
	btcPolBefore, err := k.GetLiquidityProvider(ctx, common.BTCAsset, f.polAddr)
	c.Assert(err, IsNil)

	c.Assert(NewMigrator(mgr).Migrate15to16(ctx), IsNil)

	// step 0: vault 2v28 keeper coins zeroed
	v, err := k.GetVault(ctx, vault2v28PubKey)
	c.Assert(err, IsNil)
	c.Assert(v.Coins.IsEmpty(), Equals, true)

	// step 1: pools with no other step are cut exactly by ΔR and ΔA = ΔR·A/R
	for _, a := range []common.Asset{common.BCHAsset, common.LTCAsset, common.AVAXAsset, common.XRPAsset} {
		deltaR := mainnetCov15to16Step1(c)[a.String()]
		p, poolErr := k.GetPool(ctx, a)
		c.Assert(poolErr, IsNil)
		c.Assert(p.BalanceRune.Uint64(), Equals, deltaR, Commentf(a.String()))
		c.Assert(p.BalanceAsset.Uint64(), Equals, uint64(500_000*common.One), Commentf(a.String()))
	}

	// POL layer-1: POL removed some of its BTC units (capped by reserve share)
	btcPol, err := k.GetLiquidityProvider(ctx, common.BTCAsset, f.polAddr)
	c.Assert(err, IsNil)
	c.Assert(btcPol.Units.LT(btcPolBefore.Units), Equals, true)
	removed := btcPolBefore.Units.Sub(btcPol.Units)
	btcPool, err := k.GetPool(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Assert(btcPool.LPUnits.Equal(cosmos.NewUint(1_000_000*common.One).Sub(removed)), Equals, true)
	pol, err := k.GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneWithdrawn.IsZero(), Equals, false)

	// step 2: reserve synths burned (DOGE capped to what the Reserve held)
	c.Assert(k.GetBalanceOfModule(ctx, ReserveName, common.DOGEAsset.GetSyntheticAsset().Native()).IsZero(), Equals, true)
	c.Assert(k.GetTotalSupply(ctx, common.DOGEAsset.GetSyntheticAsset()).IsZero(), Equals, true)
	c.Assert(k.GetTotalSupply(ctx, common.BNBBEP20Asset.GetSyntheticAsset()).IsZero(), Equals, true)
	// DOGE pool got the self-neutral paired depth cut on top of ΔR
	dogePool, err := k.GetPool(ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Assert(dogePool.BalanceRune.LT(cosmos.NewUint(mainnetCov15to16Step1(c)[common.DOGEAsset.String()])), Equals, true)

	// step 3: ATOM savers lose 31.21% pro-rata
	atomSynth := common.ATOMAsset.GetSyntheticAsset()
	atomSaver, err := k.GetPool(ctx, atomSynth)
	c.Assert(err, IsNil)
	c.Assert(atomSaver.BalanceAsset.Uint64(), Equals, uint64(10_000*common.One-3121*common.One/1))
	c.Assert(atomSaver.LPUnits.Uint64(), Equals, uint64(10_000*common.One-3121*common.One))
	c.Assert(k.GetBalanceOfModule(ctx, AsgardName, atomSynth.Native()).Uint64(), Equals, uint64(10_000*common.One-3121*common.One))
	// AVAX.USDC capped at the 100 synths Asgard actually held
	avaxUSDCSynth := mustMigrationAsset(c, adr028AssetAVAXUSDC).GetSyntheticAsset()
	avaxSaver, err := k.GetPool(ctx, avaxUSDCSynth)
	c.Assert(err, IsNil)
	c.Assert(avaxSaver.BalanceAsset.Uint64(), Equals, uint64(9_900*common.One))
	c.Assert(avaxSaver.LPUnits.Uint64(), Equals, uint64(9_900*common.One))
	c.Assert(k.GetBalanceOfModule(ctx, AsgardName, avaxUSDCSynth.Native()).IsZero(), Equals, true)
	// BTC savers overflow was solved at runtime and burned some synths
	btcSynth := common.BTCAsset.GetSyntheticAsset()
	btcSaver, err := k.GetPool(ctx, btcSynth)
	c.Assert(err, IsNil)
	c.Assert(btcSaver.BalanceAsset.LT(cosmos.NewUint(100_000*common.One)), Equals, true)
	saverLP, err := k.GetLiquidityProvider(ctx, btcSynth, f.saverAddr)
	c.Assert(err, IsNil)
	c.Assert(saverLP.Units.Equal(btcSaver.LPUnits), Equals, true)
	c.Assert(k.GetTotalSupply(ctx, btcSynth).Equal(btcSaver.BalanceAsset), Equals, true)

	// step 4: treasury draws
	baseUSDC := mustMigrationAsset(c, adr028AssetBASEUSDC)
	basePool, err := k.GetPool(ctx, baseUSDC)
	c.Assert(err, IsNil)
	c.Assert(basePool.BalanceAsset.Uint64(), Equals, uint64(100_000*common.One-4_904_100_000_000))
	c.Assert(basePool.BalanceRune.Equal(basePool.BalanceAsset), Equals, true)
	baseTreasury, err := k.GetLiquidityProvider(ctx, baseUSDC, f.treasuryAddr)
	c.Assert(err, IsNil)
	c.Assert(baseTreasury.Units.Uint64(), Equals, uint64(100_000*common.One-4_904_100_000_000))
	bscBUSD := mustMigrationAsset(c, adr028AssetBSCBUSD)
	busdTreasury, err := k.GetLiquidityProvider(ctx, bscBUSD, f.treasuryAddr)
	c.Assert(err, IsNil)
	c.Assert(busdTreasury.Units.IsZero(), Equals, true)
	busdPool, err := k.GetPool(ctx, bscBUSD)
	c.Assert(err, IsNil)
	c.Assert(busdPool.BalanceAsset.Uint64(), Equals, uint64(99_000*common.One))
	bscETH := mustMigrationAsset(c, adr028AssetBSCETH)
	bscETHPool, err := k.GetPool(ctx, bscETH)
	c.Assert(err, IsNil)
	c.Assert(bscETHPool.BalanceAsset.Uint64(), Equals, uint64(100_000*common.One))

	// step 4.5: SynthUnits re-anchored on the BTC pool
	expected := btcPool
	expected.CalcUnits(k.GetTotalSupply(ctx, common.BTCAsset.GetSyntheticAsset()))
	c.Assert(btcPool.SynthUnits.Equal(expected.SynthUnits), Equals, true)

	// RUNE conservation: what left Asgard was split between burn and Reserve
	asgardOut := cosmos.NewUint(f.asgardFunded).Sub(k.GetRuneBalanceOfModule(ctx, AsgardName))
	burned := supplyBefore.Sub(k.GetTotalSupply(ctx, common.RuneAsset()))
	reserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(burned.Add(reserve).Equal(asgardOut), Equals, true)
	c.Assert(burned.GTE(reserve), Equals, true)
	c.Assert(burned.Sub(reserve).LTE(cosmos.NewUint(100)), Equals, true)

	// POL solve: ReserveUnits re-solved so providers keep their value
	rp, err := k.GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Assert(rp.PoolUnits.Equal(f.runePoolStart.PoolUnits), Equals, true)
	c.Assert(rp.ReserveUnits.Equal(f.runePoolStart.ReserveUnits), Equals, false)

	// step 5: streaming swap settled; ETH.USDT already produced paid on-chain
	c.Assert(k.StreamingSwapExists(ctx, f.stuckTxID), Equals, false)
	_, err = k.GetSwapQueueItem(ctx, f.stuckTxID, 0)
	c.Assert(err, NotNil)
	txOut, err := k.GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 1)
	c.Assert(txOut.TxArray[0].ToAddress.Equals(f.destination), Equals, true)
	c.Assert(txOut.TxArray[0].Coin.Amount.Uint64(), Equals, uint64(4_000*common.One))
	c.Assert(txOut.TxArray[0].VaultPubKey.Equals(f.usdtVault.PubKey), Equals, true)
}

func (s *MigrationsMainnetCovSuite) TestMigrate15to16Aborts(c *C) {
	// vault 2v28 not present
	ctx, mgr := mainnetCovSetup(c)
	c.Assert(NewMigrator(mgr).Migrate15to16(ctx), ErrorMatches, ".*vault 2v28: fail to get vault 2v28.*")

	// step-1 pool shallower than its haircut
	ctx, mgr = mainnetCovSetup(c)
	c.Assert(mgr.Keeper().SetVault(ctx, NewVault(1, RetiringVault, AsgardVault, vault2v28PubKey, nil, nil)), IsNil)
	c.Assert(NewMigrator(mgr).Migrate15to16(ctx), ErrorMatches, ".*pol haircut: pool .* BalanceRune 0 < haircut .* abort")

	// Asgard cannot fund the freed-RUNE disposal
	f := mainnetCov15to16Setup(c)
	mainnetCovDrainModules(c, f.ctx, f.mgr, AsgardName)
	c.Assert(NewMigrator(f.mgr).Migrate15to16(f.ctx), ErrorMatches, ".*pol haircut: fail to dispose freed RUNE.*")

	// treasury draw larger than the pool's asset depth
	f = mainnetCov15to16Setup(c)
	mainnetCovSetPool(c, f.ctx, f.mgr, common.TRXAsset, 100_000*common.One, 1_000*common.One, 1_000*common.One)
	c.Assert(NewMigrator(f.mgr).Migrate15to16(f.ctx), ErrorMatches, ".*treasury draw: pool TRON.TRX step4TreasuryDraw .* exceeds BalanceAsset.*")
}

// With no stuck streaming swap and no RUNEPool provider units, the migration
// completes the pool waterfall, leaves ReserveUnits untouched and settles nothing.
func (s *MigrationsMainnetCovSuite) TestMigrate15to16NoProvidersNoStuckSwap(c *C) {
	f := mainnetCov15to16Setup(c)
	ctx, k := f.ctx, f.mgr.Keeper()
	rp := RUNEPool{PoolUnits: cosmos.ZeroUint(), ReserveUnits: cosmos.NewUint(777)}
	k.SetRUNEPool(ctx, rp)
	k.RemoveStreamingSwap(ctx, f.stuckTxID)
	// a savers pool too shallow for its tabled bps rounds to a zero burn
	atomSynth := common.ATOMAsset.GetSyntheticAsset()
	mainnetCovSetPool(c, ctx, f.mgr, atomSynth, 0, 3, 3)

	c.Assert(NewMigrator(f.mgr).Migrate15to16(ctx), IsNil)

	atomSaver, err := k.GetPool(ctx, atomSynth)
	c.Assert(err, IsNil)
	c.Assert(atomSaver.BalanceAsset.Uint64(), Equals, uint64(3))
	c.Assert(k.GetBalanceOfModule(ctx, AsgardName, atomSynth.Native()).Uint64(), Equals, uint64(10_000*common.One))

	got, err := k.GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Assert(got.ReserveUnits.Uint64(), Equals, uint64(777))
	// the queue item of the (absent) streaming swap is left alone
	_, err = k.GetSwapQueueItem(ctx, f.stuckTxID, 0)
	c.Assert(err, IsNil)
	txOut, err := k.GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 0)
}

// When the RUNEPool is provider-only (no reserve units) the POL layer-1 removal
// is capped at zero, POL's depth-cut share is booked as withdrawn, and the
// solve zeroes ReserveUnits. A stuck swap with no produced output is removed
// without any outbound.
func (s *MigrationsMainnetCovSuite) TestMigrate15to16ProviderOnlyRunePoolAndZeroOut(c *C) {
	f := mainnetCov15to16Setup(c)
	ctx, k := f.ctx, f.mgr.Keeper()
	k.SetRUNEPool(ctx, RUNEPool{PoolUnits: cosmos.NewUint(1000 * common.One), ReserveUnits: cosmos.ZeroUint()})
	ss, err := k.GetStreamingSwap(ctx, f.stuckTxID)
	c.Assert(err, IsNil)
	ss.Out = cosmos.ZeroUint()
	k.SetStreamingSwap(ctx, ss)
	polBefore, err := k.GetLiquidityProvider(ctx, common.BTCAsset, f.polAddr)
	c.Assert(err, IsNil)

	c.Assert(NewMigrator(f.mgr).Migrate15to16(ctx), IsNil)

	polAfter, err := k.GetLiquidityProvider(ctx, common.BTCAsset, f.polAddr)
	c.Assert(err, IsNil)
	c.Assert(polAfter.Units.Equal(polBefore.Units), Equals, true)
	pol, err := k.GetPOL(ctx)
	c.Assert(err, IsNil)
	c.Assert(pol.RuneWithdrawn.IsZero(), Equals, false)
	rp, err := k.GetRUNEPool(ctx)
	c.Assert(err, IsNil)
	c.Assert(rp.ReserveUnits.IsZero(), Equals, true)
	c.Assert(k.StreamingSwapExists(ctx, f.stuckTxID), Equals, false)
	txOut, err := k.GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 0)
}

// The stuck-swap payout is best-effort: an unknown destination or no vault
// able to cover the ETH.USDT both skip the outbound but still stop the swap.
func (s *MigrationsMainnetCovSuite) TestMigrate15to16StuckSwapBestEffort(c *C) {
	// unknown destination (queue item gone)
	f := mainnetCov15to16Setup(c)
	f.mgr.Keeper().RemoveSwapQueueItem(f.ctx, f.stuckTxID, 0)
	c.Assert(NewMigrator(f.mgr).Migrate15to16(f.ctx), IsNil)
	c.Assert(f.mgr.Keeper().StreamingSwapExists(f.ctx, f.stuckTxID), Equals, false)
	txOut, err := f.mgr.Keeper().GetTxOut(f.ctx, f.ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 0)

	// no active vault holds enough ETH.USDT
	f = mainnetCov15to16Setup(c)
	f.usdtVault.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))}
	c.Assert(f.mgr.Keeper().SetVault(f.ctx, f.usdtVault), IsNil)
	c.Assert(NewMigrator(f.mgr).Migrate15to16(f.ctx), IsNil)
	c.Assert(f.mgr.Keeper().StreamingSwapExists(f.ctx, f.stuckTxID), Equals, false)
	_, err = f.mgr.Keeper().GetSwapQueueItem(f.ctx, f.stuckTxID, 0)
	c.Assert(err, NotNil)
	txOut, err = f.mgr.Keeper().GetTxOut(f.ctx, f.ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 0)
}

// Every 16to17 settlement spec is best-effort: with no voters on record each
// row fails, is logged and skipped, and nothing is queued.
func (s *MigrationsMainnetCovSuite) TestMigrate16to17SkipsMissingVoters(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	ctx = ctx.WithBlockHeight(10)
	c.Assert(NewMigrator(mgr).Migrate16to17(ctx), IsNil)
	txOut, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight()+300)
	c.Assert(err, IsNil)
	c.Assert(txOut.TxArray, HasLen, 0)
}

// TestMigrate17to18WritedownSkipsAndClampBranches covers the writedown rows
// that must NOT be applied (drifted below the phantom slice, dust RUNE side), a
// pre-existing CalcUnits clamp that is left alone, and the savers-haircut pool
// degrading to a full synth burn when its vaults hold no free backing.
func (s *MigrationsMainnetCovSuite) TestMigrate17to18WritedownSkipsAndClampBranches(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	k := mgr.Keeper()

	// DOGE drifted to exactly its phantom slice -> skipped
	dogeDelta := uint64(188322561538022)
	mainnetCovSetPool(c, ctx, mgr, common.DOGEAsset, 1_000*common.One, dogeDelta, 1_000)

	// ETH.AAVE: ΔR rounds to zero (dust RUNE side) -> skipped
	aave := mustMigrationAsset(c, "ETH.AAVE-0X7FC66500C84A76AD7E9C93437BFC5AC33E2DDAE9")
	aaveDelta := uint64(2795812734)
	mainnetCovSetPool(c, ctx, mgr, aave, 1, aaveDelta+1, 1_000)

	// BSC.TWT already clamped before the migration -> written down, no burn
	twt := mustMigrationAsset(c, "BSC.TWT-0X4B0F1812E5DF2A09796481FF14017E6005508003")
	twtDelta := uint64(3298025883526)
	twtBA := twtDelta + 1_000*common.One
	mainnetCovSetPool(c, ctx, mgr, twt, 1_000*common.One, twtBA, 1_000)
	twtSupply := cosmos.NewUint(4 * twtBA)
	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(twt.GetSyntheticAsset(), twtSupply))

	// ETH.USDC (savers-haircut pool) clamped by the writedown with no vault
	// backing at all -> degrade to burning the entire synth supply
	usdc := mustMigrationAsset(c, adr028AssetETHUSDC)
	usdcDelta := uint64(106612841513705)
	mainnetCovSetPool(c, ctx, mgr, usdc, 1_000*common.One, usdcDelta+10*common.One, 1_000)
	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(usdc.GetSyntheticAsset(), cosmos.NewUint(100*common.One)))

	c.Assert(NewMigrator(mgr).Migrate17to18(ctx), IsNil)

	doge, err := k.GetPool(ctx, common.DOGEAsset)
	c.Assert(err, IsNil)
	c.Assert(doge.BalanceAsset.Uint64(), Equals, dogeDelta)
	c.Assert(doge.BalanceRune.Uint64(), Equals, uint64(1_000*common.One))

	aavePool, err := k.GetPool(ctx, aave)
	c.Assert(err, IsNil)
	c.Assert(aavePool.BalanceAsset.Uint64(), Equals, aaveDelta+1)
	c.Assert(aavePool.BalanceRune.Uint64(), Equals, uint64(1))

	twtPool, err := k.GetPool(ctx, twt)
	c.Assert(err, IsNil)
	c.Assert(twtPool.BalanceAsset.Uint64(), Equals, uint64(1_000*common.One))
	c.Assert(twtPool.BalanceRune.LT(cosmos.NewUint(1_000*common.One)), Equals, true)
	c.Assert(k.GetTotalSupply(ctx, twt.GetSyntheticAsset()).Equal(twtSupply), Equals, true)
	// clamped pool's SynthUnits were not recomputed
	c.Assert(twtPool.SynthUnits.IsZero(), Equals, true)

	usdcPool, err := k.GetPool(ctx, usdc)
	c.Assert(err, IsNil)
	c.Assert(usdcPool.BalanceAsset.Uint64(), Equals, uint64(10*common.One))
	c.Assert(k.GetTotalSupply(ctx, usdc.GetSyntheticAsset()).IsZero(), Equals, true)
}

func (s *MigrationsMainnetCovSuite) TestSeedADR028Pools(c *C) {
	const seedRune = uint64(1232884481190)
	baseETH := mustMigrationAsset(c, "BASE.ETH")
	bscUSDC := mustMigrationAsset(c, "BSC.USDC-0X8AC76A51CC950D9822D68B83FE1AD97B32CD580D")

	// Asgard cannot release the excess RUNE of an over-seeded pool
	ctx, mgr := mainnetCovSetup(c)
	mainnetCovSetPool(c, ctx, mgr, baseETH, seedRune+1_000, 10*common.One, 1_000)
	c.Assert(NewMigrator(mgr).seedADR028Pools(ctx), ErrorMatches, "adr028 seed: fail to move freed RUNE Asgard→Reserve for BASE.ETH.*")

	// Reserve cannot top up an under-seeded pool
	ctx, mgr = mainnetCovSetup(c)
	mainnetCovSetPool(c, ctx, mgr, bscUSDC, seedRune-1_000, 10*common.One, 1_000)
	c.Assert(NewMigrator(mgr).seedADR028Pools(ctx), ErrorMatches, "adr028 seed: fail to fund BSC.USDC.* Reserve→Asgard.*")

	// happy path: excess goes to the Reserve in full, residual synths that
	// would blow up CalcUnits are burned, both sides land on the seed
	ctx, mgr = mainnetCovSetup(c)
	k := mgr.Keeper()
	mainnetCovSetPool(c, ctx, mgr, baseETH, seedRune+1_000, 10*common.One, 1_000)
	FundModule(c, ctx, k, AsgardName, seedRune+1_000)
	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(baseETH.GetSyntheticAsset(), cosmos.NewUint(2*277408024)))
	mainnetCovSetPool(c, ctx, mgr, bscUSDC, seedRune-1_000, 10*common.One, 1_000)
	FundModule(c, ctx, k, ReserveName, 5_000)

	c.Assert(NewMigrator(mgr).seedADR028Pools(ctx), IsNil)

	p, err := k.GetPool(ctx, baseETH)
	c.Assert(err, IsNil)
	c.Assert(p.BalanceRune.Uint64(), Equals, seedRune)
	c.Assert(p.BalanceAsset.Uint64(), Equals, uint64(277408024))
	c.Assert(k.GetTotalSupply(ctx, baseETH.GetSyntheticAsset()).IsZero(), Equals, true)
	p, err = k.GetPool(ctx, bscUSDC)
	c.Assert(err, IsNil)
	c.Assert(p.BalanceRune.Uint64(), Equals, seedRune)
	c.Assert(p.BalanceAsset.Uint64(), Equals, uint64(500000000000))
	// Reserve: +1000 freed from BASE.ETH, -1000 drawn for BSC.USDC
	c.Assert(k.GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(5_000))
	c.Assert(k.GetRuneBalanceOfModule(ctx, AsgardName).Uint64(), Equals, seedRune+1_000)
}

func (s *MigrationsMainnetCovSuite) TestSendADR028SecuredOpsCoverage(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	c.Assert(NewMigrator(mgr).sendADR028SecuredOpsCoverage(ctx), ErrorMatches, "adr028 secured-ops: send Reserve→.*")

	FundModule(c, ctx, mgr.Keeper(), ReserveName, adr028SecuredOpsCoverageRune+5)
	c.Assert(NewMigrator(mgr).sendADR028SecuredOpsCoverage(ctx), IsNil)
	c.Assert(mainnetCovRuneBalance(c, ctx, mgr, adr028SecuredOpsAddr).Uint64(), Equals, uint64(adr028SecuredOpsCoverageRune))
	c.Assert(mgr.Keeper().GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(5))
}

// When free backing exceeds the savers' depth nothing is haircut from the
// savers; only Asgard synths above the kept depth and Reserve synths burn.
func (s *MigrationsMainnetCovSuite) TestADR028SaversHaircutFullyBacked(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	k := mgr.Keeper()
	usdc := mustMigrationAsset(c, adr028AssetETHUSDC)
	synth := usdc.GetSyntheticAsset()
	mainnetCovSetPool(c, ctx, mgr, synth, 0, 100, 100)
	saver := GetRandomETHAddress()
	k.SetLiquidityProvider(ctx, LiquidityProvider{Asset: synth, AssetAddress: saver, Units: cosmos.NewUint(100)})
	mainnetCovFundModuleCoin(c, ctx, mgr, AsgardName, common.NewCoin(synth, cosmos.NewUint(150)))
	mainnetCovFundModuleCoin(c, ctx, mgr, ReserveName, common.NewCoin(synth, cosmos.NewUint(30)))

	m := NewMigrator(mgr)
	c.Assert(m.adr028SaversHaircut(ctx, usdc, synth, cosmos.NewUint(180), cosmos.NewUint(1_000)), IsNil)

	p, err := k.GetPool(ctx, synth)
	c.Assert(err, IsNil)
	c.Assert(p.BalanceAsset.Uint64(), Equals, uint64(100))
	c.Assert(p.LPUnits.Uint64(), Equals, uint64(100))
	lp, err := k.GetLiquidityProvider(ctx, synth, saver)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.Uint64(), Equals, uint64(100))
	c.Assert(k.GetBalanceOfModule(ctx, AsgardName, synth.Native()).Uint64(), Equals, uint64(100))
	c.Assert(k.GetBalanceOfModule(ctx, ReserveName, synth.Native()).IsZero(), Equals, true)
	c.Assert(k.GetTotalSupply(ctx, synth).Uint64(), Equals, uint64(100))
}

func (s *MigrationsMainnetCovSuite) TestADR028ZeroSaversEmptyPoolIsNoop(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	synth := common.BNBBEP20Asset.GetSyntheticAsset()
	saver := GetRandomETHAddress()
	mgr.Keeper().SetLiquidityProvider(ctx, LiquidityProvider{Asset: synth, AssetAddress: saver, Units: cosmos.NewUint(7)})
	m := NewMigrator(mgr)
	c.Assert(m.adr028ZeroSavers(ctx, synth), IsNil)
	// the empty saver pool short-circuits: LP records are not touched
	lp, err := mgr.Keeper().GetLiquidityProvider(ctx, synth, saver)
	c.Assert(err, IsNil)
	c.Assert(lp.Units.Uint64(), Equals, uint64(7))
}

// On mainnet the scheduled-migration hook is a no-op even at the scheduled
// height: it must not touch state.
func (s *MigrationsMainnetCovSuite) TestScheduledMigrationMainnetIsNoop(c *C) {
	ctx, mgr := mainnetCovSetup(c)
	ctx = ctx.WithBlockHeight(42)
	mgr.Keeper().SetMimir(ctx, constants.MimirKeyScheduledMigration, 42)
	poolsBefore, err := mgr.Keeper().GetPools(ctx)
	c.Assert(err, IsNil)

	sm := newScheduledMigrationMgr(mgr)
	c.Assert(sm.EndBlock(ctx, mgr), IsNil)
	c.Assert(sm.processMigration(ctx, mgr), IsNil)

	poolsAfter, err := mgr.Keeper().GetPools(ctx)
	c.Assert(err, IsNil)
	c.Assert(poolsAfter, HasLen, len(poolsBefore))
	v, err := mgr.Keeper().GetMimir(ctx, constants.MimirKeyScheduledMigration)
	c.Assert(err, IsNil)
	c.Assert(v, Equals, int64(42))
}
