package keeperv1

import (
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tcysmartcontract"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type KeeperCoreCovSuite struct{}

var _ = Suite(&KeeperCoreCovSuite{})

func (s *KeeperCoreCovSuite) TestNewKeeperAndDeleteKey(c *C) {
	ctx, k := setupKeeperForTest(c)
	nk := NewKeeper(k.cdc, k.storeService, k.coinKeeper, k.accountKeeper, k.upgradeKeeper)
	c.Check(nk.GetVersion().String(), Equals, "0.0.0")
	c.Check(nk.Cdc(), NotNil)

	key := k.GetKey(prefixMimir, "deletekeycov")
	k.setInt64(ctx, key, 1)
	c.Check(k.has(ctx, key), Equals, true)
	k.DeleteKey(ctx, key)
	c.Check(k.has(ctx, key), Equals, false)
	// deleting a missing key is a no-op
	k.DeleteKey(ctx, key)
}

func (s *KeeperCoreCovSuite) TestPrimitiveGettersCorrupt(c *C) {
	ctx, k := setupKeeperForTest(c)
	key := []byte("cov/corrupt")
	corruptKeyCov(ctx, k, key)

	var i64 int64
	found, err := k.getInt64(ctx, key, &i64)
	c.Check(found, Equals, true)
	c.Check(err, NotNil)
	var u64 uint64
	found, err = k.getUint64(ctx, key, &u64)
	c.Check(found, Equals, true)
	c.Check(err, NotNil)
	var addrs []cosmos.AccAddress
	found, err = k.getAccAddresses(ctx, key, &addrs)
	c.Check(found, Equals, true)
	c.Check(err, NotNil)
	var str string
	found, err = k.getString(ctx, string(key), &str)
	c.Check(found, Equals, true)
	c.Check(err, NotNil)
	var strs []string
	found, err = k.getStrings(ctx, key, &strs)
	c.Check(found, Equals, true)
	c.Check(err, NotNil)
	u := cosmos.ZeroUint()
	found, err = k.getUint(ctx, key, &u)
	c.Check(found, Equals, false)
	c.Check(err, NotNil)

	// round trips
	k.setUint(ctx, key, cosmos.NewUint(99))
	found, err = k.getUint(ctx, key, &u)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(u.Uint64(), Equals, uint64(99))
	k.setStrings(ctx, key, []string{"a", "b"})
	_, err = k.getStrings(ctx, key, &strs)
	c.Assert(err, IsNil)
	c.Check(strs, DeepEquals, []string{"a", "b"})
	k.setStrings(ctx, key, nil)
	c.Check(k.has(ctx, key), Equals, false)
}

func (s *KeeperCoreCovSuite) TestSendBurnAndMint(c *C) {
	ctx, k := setupKeeperForTest(c)
	from, to := GetRandomBech32Addr(), GetRandomBech32Addr()
	c.Assert(k.SendFromModuleToAccount(ctx, AsgardName, from, common.NewCoins(common.NewCoin(common.RuneNative, cosmos.NewUint(1000)))), IsNil)

	c.Assert(k.SendCoins(ctx, from, to, cosmos.NewCoins(cosmos.NewCoin("rune", math.NewInt(400)))), IsNil)
	c.Check(k.GetBalanceOf(ctx, to, common.RuneNative).Amount.Int64(), Equals, int64(400))
	c.Check(k.HasCoins(ctx, to, cosmos.NewCoins(cosmos.NewCoin("rune", math.NewInt(400)))), Equals, true)
	c.Check(k.HasCoins(ctx, to, cosmos.NewCoins(cosmos.NewCoin("rune", math.NewInt(401)))), Equals, false)
	c.Check(k.SendCoins(ctx, to, from, cosmos.NewCoins(cosmos.NewCoin("rune", math.NewInt(401)))), NotNil)

	// burn: non native, missing permission, success
	err := k.BurnFromModule(ctx, ModuleName, common.NewCoin(common.BTCAsset, cosmos.NewUint(1)))
	c.Check(err, ErrorMatches, "fail to parse coins.*")
	c.Assert(k.MintToModule(ctx, ModuleName, common.NewCoin(common.RuneNative, cosmos.NewUint(10))), IsNil)
	before := k.GetRuneBalanceOfModule(ctx, ModuleName)
	c.Assert(k.BurnFromModule(ctx, ModuleName, common.NewCoin(common.RuneNative, cosmos.NewUint(10))), IsNil)
	c.Check(k.GetRuneBalanceOfModule(ctx, ModuleName).Uint64(), Equals, before.Uint64()-10)
	// burning more than the module holds fails
	err = k.BurnFromModule(ctx, ModuleName, common.NewCoin(common.RuneNative, cosmos.NewUint(before.Uint64()+1)))
	c.Check(err, ErrorMatches, "fail to burn assets.*")

	// mint: non native coins are refused
	err = k.MintToModule(ctx, ModuleName, common.NewCoin(common.BTCAsset, cosmos.NewUint(1)))
	c.Check(err, ErrorMatches, "fail to parse coins.*")

	// mint and send
	acct := GetRandomBech32Addr()
	c.Assert(k.MintAndSendToAccount(ctx, acct, common.NewCoin(common.RuneNative, cosmos.NewUint(77))), IsNil)
	c.Check(k.GetBalanceOf(ctx, acct, common.RuneNative).Amount.Int64(), Equals, int64(77))
	c.Check(k.MintAndSendToAccount(ctx, acct, common.NewCoin(common.BTCAsset, cosmos.NewUint(1))), NotNil)

	addr, err := k.GetModuleAddress(ReserveName)
	c.Assert(err, IsNil)
	c.Check(addr.String(), Equals, k.GetModuleAccAddress(ReserveName).String())
}

func (s *KeeperCoreCovSuite) TestMintToModuleSupplyCap(c *C) {
	ctx, k := setupKeeperForTest(c)
	supply := k.GetTotalSupply(ctx, common.RuneAsset())

	// cap at current supply: everything is borrowed from the reserve, but the
	// reserve is empty so the mint fails and nothing is minted
	k.SetMimir(ctx, constants.MaxRuneSupply.String(), int64(supply.Uint64()))
	err := k.MintToModule(ctx, ModuleName, common.NewCoin(common.RuneNative, cosmos.NewUint(100)))
	c.Assert(err, NotNil)
	c.Check(k.GetTotalSupply(ctx, common.RuneAsset()).Equal(supply), Equals, true)

	// with a funded reserve the full amount is borrowed (zero minted)
	c.Assert(k.SendFromModuleToModule(ctx, AsgardName, ReserveName, common.NewCoins(common.NewCoin(common.RuneNative, cosmos.NewUint(1000)))), IsNil)
	modBefore := k.GetRuneBalanceOfModule(ctx, ModuleName)
	sim := ctx.WithValue(constants.CtxSimulationMode, true)
	c.Assert(k.MintToModule(sim, ModuleName, common.NewCoin(common.RuneNative, cosmos.NewUint(100))), IsNil)
	c.Check(k.GetRuneBalanceOfModule(ctx, ModuleName).Uint64(), Equals, modBefore.Uint64()+100)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(900))
	c.Check(k.GetTotalSupply(ctx, common.RuneAsset()).Equal(supply), Equals, true)

	// raising the cap by 30 mints 30 and borrows 70
	k.SetMimir(ctx, constants.MaxRuneSupply.String(), int64(supply.Uint64())+30)
	c.Assert(k.MintToModule(ctx, ModuleName, common.NewCoin(common.RuneNative, cosmos.NewUint(100))), IsNil)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(830))
	c.Check(k.GetTotalSupply(ctx, common.RuneAsset()).Uint64(), Equals, supply.Uint64()+30)
}

func (s *KeeperCoreCovSuite) TestBurnEntireSynthSupply(c *C) {
	ctx, k := setupKeeperForTest(c)
	synth := common.BTCAsset.GetSyntheticAsset()
	a1, a2 := GetRandomBech32Addr(), GetRandomBech32Addr()
	c.Assert(k.MintAndSendToAccount(ctx, a1, common.NewCoin(synth, cosmos.NewUint(30))), IsNil)
	c.Assert(k.MintAndSendToAccount(ctx, a2, common.NewCoin(synth, cosmos.NewUint(12))), IsNil)
	// unrelated balance is untouched
	c.Assert(k.MintAndSendToAccount(ctx, a1, common.NewCoin(common.RuneNative, cosmos.NewUint(5))), IsNil)

	total, err := k.BurnEntireSynthSupply(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(total.Uint64(), Equals, uint64(42))
	c.Check(k.GetBalanceOf(ctx, a1, synth).Amount.IsZero(), Equals, true)
	c.Check(k.GetBalanceOf(ctx, a2, synth).Amount.IsZero(), Equals, true)
	c.Check(k.GetBalanceOf(ctx, a1, common.RuneNative).Amount.Int64(), Equals, int64(5))
	c.Check(k.GetTotalSupply(ctx, synth).IsZero(), Equals, true)

	// nothing left to burn
	total, err = k.BurnEntireSynthSupply(ctx, synth)
	c.Assert(err, IsNil)
	c.Check(total.IsZero(), Equals, true)
}

func (s *KeeperCoreCovSuite) TestFees(c *C) {
	ctx, k := setupKeeperForTest(c)

	// RUNE denominated defaults
	c.Check(k.GetNativeTxFee(ctx).Uint64(), Equals, uint64(k.GetConfigInt64(ctx, constants.NativeTransactionFee)))
	c.Check(k.GetTHORNameRegisterFee(ctx).Uint64(), Equals, uint64(k.GetConstants().GetInt64Value(constants.TNSRegisterFee)))
	c.Check(k.GetTHORNamePerBlockFee(ctx).Uint64(), Equals, uint64(k.GetConstants().GetInt64Value(constants.TNSFeePerBlock)))
	c.Check(k.GetOutboundTxFee(ctx).Uint64(), Equals, uint64(k.GetConfigInt64(ctx, constants.OutboundTransactionFee)))

	// USD fees with no price feed fall back to the USD value
	k.SetMimir(ctx, constants.EnableUSDFees.String(), 1)
	usd := uint64(k.GetConfigInt64(ctx, constants.NativeTransactionFeeUSD))
	c.Check(k.GetNativeTxFee(ctx).Uint64(), Equals, usd)

	// with a price of $2 per RUNE the fee halves (rounded to sig figs)
	k.SetMimir(ctx, "DollarsPerRune", 2*common.One)
	digits := k.GetConfigInt64(ctx, constants.FeeUSDRoundSignificantDigits)
	expect := func(name constants.ConstantName) uint64 {
		v := uint64(k.GetConfigInt64(ctx, name)) * common.One / (2 * common.One)
		return common.RoundSignificantFigures(v, digits)
	}
	c.Check(k.GetNativeTxFee(ctx).Uint64(), Equals, expect(constants.NativeTransactionFeeUSD))
	c.Check(k.GetTHORNameRegisterFee(ctx).Uint64(), Equals, expect(constants.TNSRegisterFeeUSD))
	c.Check(k.GetTHORNamePerBlockFee(ctx).Uint64(), Equals, expect(constants.TNSFeePerBlockUSD))
	c.Check(k.GetOutboundTxFee(ctx).Uint64(), Equals, expect(constants.NativeOutboundFeeUSD))
}

func (s *KeeperCoreCovSuite) TestDeductNativeTxFeeFromAccount(c *C) {
	ctx, k := setupKeeperForTest(c)
	acct := GetRandomBech32Addr()
	c.Assert(k.SendFromModuleToAccount(ctx, AsgardName, acct, common.NewCoins(common.NewCoin(common.RuneNative, cosmos.NewUint(10*common.One)))), IsNil)

	fee := k.GetNativeTxFee(ctx)
	c.Assert(fee.IsZero(), Equals, false)
	reserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Assert(k.DeductNativeTxFeeFromAccount(ctx, acct), IsNil)
	c.Check(k.GetBalanceOf(ctx, acct, common.RuneNative).Amount.Uint64(), Equals, 10*common.One-fee.Uint64())
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, reserve.Uint64()+fee.Uint64())

	// insufficient balance
	c.Check(k.DeductNativeTxFeeFromAccount(ctx, GetRandomBech32Addr()), NotNil)

	// zero fee is a no-op
	k.SetMimir(ctx, constants.NativeTransactionFee.String(), 0)
	c.Assert(k.DeductNativeTxFeeFromAccount(ctx, GetRandomBech32Addr()), IsNil)
}

func (s *KeeperCoreCovSuite) TestRagnarokAccount(c *C) {
	ctx, k := setupKeeperForTest(c)
	acct := GetRandomBech32Addr()
	synth := common.BTCAsset.GetSyntheticAsset()
	c.Assert(k.MintAndSendToAccount(ctx, acct, common.NewCoin(common.RuneNative, cosmos.NewUint(50))), IsNil)
	c.Assert(k.MintAndSendToAccount(ctx, acct, common.NewCoin(synth, cosmos.NewUint(20))), IsNil)
	// a denom that is not a valid asset is skipped
	bad := sdk.NewCoins(sdk.NewCoin("x/foo", math.NewInt(3)))
	c.Assert(k.coinKeeper.MintCoins(ctx, ModuleName, bad), IsNil)
	c.Assert(k.coinKeeper.SendCoinsFromModuleToAccount(ctx, ModuleName, acct, bad), IsNil)

	reserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
	k.RagnarokAccount(ctx, acct)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, reserve.Uint64()+50)
	c.Check(k.GetTotalSupply(ctx, synth).IsZero(), Equals, true)
	c.Check(k.GetAccount(ctx, acct), IsNil)

	// unknown account is a no-op
	k.RagnarokAccount(ctx, GetRandomBech32Addr())
}

func (s *KeeperCoreCovSuite) TestLiquidityFees(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Assert(k.AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(10)), IsNil)
	v, err := k.GetEffectivePoolLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(v, Equals, uint64(10))

	k.SavePrevCyclePoolLiquidityFee(ctx, common.BTCAsset, 7)
	v, err = k.GetPrevCyclePoolLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(v, Equals, uint64(7))
	v, err = k.GetEffectivePoolLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(v, Equals, uint64(7))

	k.ResetRollingPoolLiquidityFee(ctx, common.BTCAsset)
	v, err = k.GetRollingPoolLiquidityFee(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(v, Equals, uint64(0))

	// corrupt prev cycle surfaces the error
	corruptKeyCov(ctx, k, k.GetKey(prefixPrevCyclePoolLiquidityFee, common.BTCAsset.String()))
	_, err = k.GetEffectivePoolLiquidityFee(ctx, common.BTCAsset)
	c.Check(err, NotNil)

	// corrupt rolling fee: per-block fees are still recorded, rolling skipped
	corruptKeyCov(ctx, k, k.GetKey(prefixRollingPoolLiquidityFee, common.ETHAsset.String()))
	c.Assert(k.AddToLiquidityFees(ctx, common.ETHAsset, cosmos.NewUint(5)), IsNil)
	pf, err := k.GetPoolLiquidityFees(ctx, uint64(ctx.BlockHeight()), common.ETHAsset)
	c.Assert(err, IsNil)
	c.Check(pf.Uint64(), Equals, uint64(5))

	// corrupt per-block totals abort the add
	corruptKeyCov(ctx, k, k.GetKey(prefixPoolLiquidityFee, fmt.Sprintf("%d-%s", ctx.BlockHeight(), common.DOGEAsset.String())))
	c.Check(k.AddToLiquidityFees(ctx, common.DOGEAsset, cosmos.NewUint(5)), NotNil)
	corruptKeyCov(ctx, k, k.GetKey(prefixTotalLiquidityFee, fmt.Sprintf("%d", ctx.BlockHeight())))
	c.Check(k.AddToLiquidityFees(ctx, common.BTCAsset, cosmos.NewUint(5)), NotNil)
}

func (s *KeeperCoreCovSuite) TestReferenceMemo(c *C) {
	ctx, k := setupKeeperForTest(c)
	hash := GetRandomTxHash()
	rec := ReferenceMemo{Asset: common.BTCAsset, Memo: "=:ETH.ETH:0xabc", Reference: "7", Height: 18, RegistrationHash: hash}
	k.SetReferenceMemo(ctx, rec)

	// lookups are padded to the configured width
	width := len(fmt.Sprintf("%d", k.GetConfigInt64(ctx, constants.MemolessTxnRefCount)))
	padded := k.normalizeReference(width, "7")
	c.Check(len(padded), Equals, width)
	c.Check(k.ReferenceMemoExists(ctx, common.BTCAsset, "7"), Equals, true)
	c.Check(k.ReferenceMemoExists(ctx, common.BTCAsset, padded), Equals, true)
	got, err := k.GetReferenceMemo(ctx, common.BTCAsset, "7")
	c.Assert(err, IsNil)
	c.Check(got.Memo, Equals, rec.Memo)
	c.Check(got.Reference, Equals, padded)
	got, err = k.GetReferenceMemoByTxnHash(ctx, hash)
	c.Assert(err, IsNil)
	c.Check(got.Memo, Equals, rec.Memo)
	_, err = k.GetReferenceMemoByTxnHash(ctx, GetRandomTxHash())
	c.Check(err, ErrorMatches, "reference memo not found.*")

	// the hash alias can't be overwritten by a later registration
	rec2 := rec
	rec2.Reference = "8"
	rec2.Memo = "other"
	k.SetReferenceMemo(ctx, rec2)
	got, err = k.GetReferenceMemoByTxnHash(ctx, hash)
	c.Assert(err, IsNil)
	c.Check(got.Memo, Equals, rec.Memo)

	iter := k.GetReferenceMemoIterator(ctx)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(count, Equals, 2)

	c.Assert(k.DeleteReferenceMemo(ctx, common.BTCAsset, "8"), IsNil)
	c.Check(k.ReferenceMemoExists(ctx, common.BTCAsset, "8"), Equals, false)

	// normalization keeps longer strings unchanged
	c.Check(k.normalizeReference(3, "12345"), Equals, "12345")
	c.Check(k.normalizeReference(5, "12"), Equals, "00012")

	// last reference number and window
	c.Check(k.GetLastReferenceNumber(ctx, common.BTCAsset), Equals, "")
	k.SetLastReferenceNumber(ctx, common.BTCAsset, "00042")
	c.Check(k.GetLastReferenceNumber(ctx, common.BTCAsset), Equals, "00042")
	k.SetReferenceMemoWindow(ctx, common.BTCAsset, 100, 3, 4, 600)
	start, cur, prev, w := k.GetReferenceMemoWindow(ctx, common.BTCAsset)
	c.Check([]int64{start, cur, prev, w}, DeepEquals, []int64{100, 3, 4, 600})

	// without a configured ref count no padding is applied
	k.SetMimir(ctx, constants.MemolessTxnRefCount.String(), 0)
	c.Check(k.ReferenceMemoExists(ctx, common.BTCAsset, "7"), Equals, false)
	k.SetReferenceMemo(ctx, ReferenceMemo{Asset: common.ETHAsset, Reference: "9"})
	c.Check(k.ReferenceMemoExists(ctx, common.ETHAsset, "9"), Equals, true)
	got, err = k.GetReferenceMemo(ctx, common.ETHAsset, "9")
	c.Assert(err, IsNil)
	c.Check(got.Reference, Equals, "9")
	c.Assert(k.DeleteReferenceMemo(ctx, common.ETHAsset, "9"), IsNil)
	c.Check(k.ReferenceMemoExists(ctx, common.ETHAsset, "9"), Equals, false)

	// corrupt record
	bad := ReferenceMemo{Asset: common.DOGEAsset, Reference: "1"}
	corruptKeyCov(ctx, k, k.GetKey(prefixReferenceMemo, bad.Key()))
	_, err = k.GetReferenceMemo(ctx, common.DOGEAsset, "1")
	c.Check(err, NotNil)
}

func (s *KeeperCoreCovSuite) TestTCYClaimer(c *C) {
	ctx, k := setupKeeperForTest(c)
	l1 := GetRandomBTCAddress()

	_, err := k.GetTCYClaimer(ctx, l1, common.BTCAsset)
	c.Check(err, ErrorMatches, "TCYClaimer doesn't exist.*")
	c.Check(k.TCYClaimerExists(ctx, l1, common.BTCAsset), Equals, false)

	c.Assert(k.UpdateTCYClaimer(ctx, l1, common.BTCAsset, cosmos.NewUint(10)), IsNil)
	c.Assert(k.UpdateTCYClaimer(ctx, l1, common.BTCAsset, cosmos.NewUint(5)), IsNil)
	cl, err := k.GetTCYClaimer(ctx, l1, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(cl.Amount.Uint64(), Equals, uint64(15))
	c.Check(k.TCYClaimerExists(ctx, l1, common.BTCAsset), Equals, true)

	iter := k.GetTCYClaimerIterator(ctx)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(count, Equals, 1)

	k.DeleteTCYClaimer(ctx, l1, common.BTCAsset)
	c.Check(k.TCYClaimerExists(ctx, l1, common.BTCAsset), Equals, false)
	_, err = k.ListTCYClaimersFromL1Address(ctx, l1)
	c.Check(err, ErrorMatches, ".*doesn't have any tcy to claim")

	// corrupt claimer: get errors, list skips it, update fails
	corruptKeyCov(ctx, k, k.GetKey(prefixTCYClaimer, fmt.Sprintf("%s/%s", l1, common.ETHAsset)))
	_, err = k.GetTCYClaimer(ctx, l1, common.ETHAsset)
	c.Check(err, NotNil)
	c.Check(k.UpdateTCYClaimer(ctx, l1, common.ETHAsset, cosmos.NewUint(1)), NotNil)
	_, err = k.ListTCYClaimersFromL1Address(ctx, l1)
	c.Check(err, NotNil)
}

func (s *KeeperCoreCovSuite) TestTCYStaker(c *C) {
	if notMocknetCov() {
		c.Skip("mocknet only: needs the mocknet TCY smart contract addresses")
	}
	ctx, k := setupKeeperForTest(c)
	addr := GetRandomTHORAddress()

	c.Assert(k.UpdateTCYStaker(ctx, addr, cosmos.NewUint(3)), IsNil)
	c.Assert(k.UpdateTCYStaker(ctx, addr, cosmos.NewUint(4)), IsNil)
	st, err := k.GetTCYStaker(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(st.Amount.Uint64(), Equals, uint64(7))

	// smart contract stakers are read only views of the contract balance
	scAddrs, err := tcysmartcontract.GetTCYSmartContractAddresses()
	c.Assert(err, IsNil)
	c.Assert(len(scAddrs) > 0, Equals, true)
	sc := scAddrs[0]
	c.Check(k.TCYStakerExists(ctx, sc), Equals, true)
	c.Check(k.UpdateTCYStaker(ctx, sc, cosmos.NewUint(1)), ErrorMatches, "cannot stake for TCY smart contract address.*")
	c.Assert(k.SetTCYStaker(ctx, NewTCYStaker(sc, cosmos.NewUint(100))), IsNil)
	k.DeleteTCYStaker(ctx, sc)
	scAcc, err := sc.AccAddress()
	c.Assert(err, IsNil)
	c.Assert(k.MintAndSendToAccount(ctx, scAcc, common.NewCoin(common.TCY, cosmos.NewUint(9))), IsNil)
	st, err = k.GetTCYStaker(ctx, sc)
	c.Assert(err, IsNil)
	c.Check(st.Amount.Uint64(), Equals, uint64(9))

	stakers, err := k.ListTCYStakers(ctx)
	c.Assert(err, IsNil)
	c.Check(len(stakers), Equals, 1+len(scAddrs))

	// corrupt record: get errors, update fails, list skips
	bad := GetRandomTHORAddress()
	corruptKeyCov(ctx, k, k.GetKey(prefixTCYStaker, bad.String()))
	_, err = k.GetTCYStaker(ctx, bad)
	c.Check(err, NotNil)
	c.Check(k.UpdateTCYStaker(ctx, bad, cosmos.NewUint(1)), NotNil)
	stakers, err = k.ListTCYStakers(ctx)
	c.Assert(err, IsNil)
	c.Check(len(stakers), Equals, 1+len(scAddrs))
}

func (s *KeeperCoreCovSuite) TestSwapSlipRollups(c *C) {
	ctx, k := setupKeeperForTest(c)
	asset := common.BTCAsset

	k.SetCurrentRollup(ctx, asset, 12)
	v, err := k.GetCurrentRollup(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(v, Equals, int64(12))

	k.SetSwapSlipSnapShot(ctx, asset, 100, 55)
	v, err = k.GetSwapSlipSnapShot(ctx, asset, 100)
	c.Assert(err, IsNil)
	c.Check(v, Equals, int64(55))
	iter := k.GetSwapSlipSnapShotIterator(ctx, asset)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(count, Equals, 1)

	k.SetLongRollup(ctx, asset, 99)
	v, err = k.GetLongRollup(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(v, Equals, int64(99))

	// corrupt keys
	corruptKeyCov(ctx, k, k.GetKey(prefixPoolSwapSnapShot, fmt.Sprintf("%s/%d", asset, 101)))
	_, err = k.GetSwapSlipSnapShot(ctx, asset, 101)
	c.Check(err, NotNil)

	corruptKeyCov(ctx, k, k.GetKey(prefixPoolSwapSlip, fmt.Sprintf("rollup/%s", asset)))
	_, err = k.GetCurrentRollup(ctx, asset)
	c.Check(err, NotNil)
	_, err = k.RollupSwapSlip(ctx, 10, asset)
	c.Check(err, NotNil)

	corruptKeyCov(ctx, k, k.GetKey(prefixPoolSwapSlip, fmt.Sprintf("rollup-count/%s", asset)))
	_, err = k.GetRollupCount(ctx, asset)
	c.Check(err, NotNil)
	_, err = k.RollupSwapSlip(ctx, 10, asset)
	c.Check(err, NotNil)

	// a corrupt previous block slip resets the rollup
	eth := common.ETHAsset
	k.SetCurrentRollup(ctx, eth, 40)
	corruptKeyCov(ctx, k, k.GetKey(prefixPoolSwapSlip, fmt.Sprintf("%d-%s", ctx.BlockHeight()-1, eth)))
	_, err = k.RollupSwapSlip(ctx, 10, eth)
	c.Check(err, NotNil)
	v, err = k.GetCurrentRollup(ctx, eth)
	c.Assert(err, IsNil)
	c.Check(v, Equals, int64(0))
}

func (s *KeeperCoreCovSuite) TestSwapperClout(c *C) {
	ctx, k := setupKeeperForTest(c)
	addr := GetRandomTHORAddress()

	cl, err := k.GetSwapperClout(ctx, common.NoAddress)
	c.Assert(err, IsNil)
	c.Check(cl.Address.IsEmpty(), Equals, true)

	rec := NewSwapperClout(addr)
	rec.Score = cosmos.NewUint(5)
	c.Assert(k.SetSwapperClout(ctx, rec), IsNil)
	cl, err = k.GetSwapperClout(ctx, addr)
	c.Assert(err, IsNil)
	c.Check(cl.Score.Uint64(), Equals, uint64(5))

	iter := k.GetSwapperCloutIterator(ctx)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(count, Equals, 1)

	bad := GetRandomTHORAddress()
	corruptKeyCov(ctx, k, k.GetKey(prefixSwapperClout, bad.String()))
	_, err = k.GetSwapperClout(ctx, bad)
	c.Check(err, NotNil)
}

func (s *KeeperCoreCovSuite) TestOutboundFees(c *C) {
	ctx, k := setupKeeperForTest(c)

	c.Assert(k.AddToOutboundFeeSpentRune(ctx, common.BTCAsset, cosmos.NewUint(10)), IsNil)
	c.Assert(k.AddToOutboundFeeWithheldRune(ctx, common.BTCAsset, cosmos.NewUint(20)), IsNil)

	for _, iter := range []cosmos.Iterator{k.GetOutboundFeeWithheldRuneIterator(ctx), k.GetOutboundFeeSpentRuneIterator(ctx)} {
		count := 0
		for ; iter.Valid(); iter.Next() {
			count++
		}
		c.Assert(iter.Close(), IsNil)
		c.Check(count, Equals, 1)
	}

	// corrupt records propagate errors
	corruptKeyCov(ctx, k, k.GetKey(prefixOutboundFeeWithheldRune, common.ETHAsset.String()))
	_, err := k.GetOutboundFeeWithheldRune(ctx, common.ETHAsset)
	c.Check(err, NotNil)
	c.Check(k.AddToOutboundFeeWithheldRune(ctx, common.ETHAsset, cosmos.NewUint(1)), NotNil)
	corruptKeyCov(ctx, k, k.GetKey(prefixOutboundFeeSpentRune, common.ETHAsset.String()))
	c.Check(k.AddToOutboundFeeSpentRune(ctx, common.ETHAsset, cosmos.NewUint(1)), NotNil)

	// overflow is reported rather than wrapped
	k.setUint64(ctx, k.GetKey(prefixOutboundFeeSpentRune, common.DOGEAsset.String()), ^uint64(0))
	c.Check(k.AddToOutboundFeeSpentRune(ctx, common.DOGEAsset, cosmos.NewUint(1)), ErrorMatches, ".*overflow.*")
	k.setUint64(ctx, k.GetKey(prefixOutboundFeeWithheldRune, common.DOGEAsset.String()), ^uint64(0))
	c.Check(k.AddToOutboundFeeWithheldRune(ctx, common.DOGEAsset, cosmos.NewUint(1)), ErrorMatches, ".*overflow.*")

	// GetSurplusForTargetMultiplier: equal min/max returns the target surplus
	k.SetMimir(ctx, constants.MaxOutboundFeeMultiplierBasisPoints.String(), 15000)
	k.SetMimir(ctx, constants.MinOutboundFeeMultiplierBasisPoints.String(), 15000)
	target := uint64(k.GetConfigInt64(ctx, constants.TargetOutboundFeeSurplusRune))
	c.Check(k.GetSurplusForTargetMultiplier(ctx, cosmos.NewUint(10_000)).Uint64(), Equals, target)

	// targets are clamped to [min, max]
	k.SetMimir(ctx, constants.MinOutboundFeeMultiplierBasisPoints.String(), 5000)
	c.Check(k.GetSurplusForTargetMultiplier(ctx, cosmos.NewUint(100)).Uint64(), Equals, target)
	c.Check(k.GetSurplusForTargetMultiplier(ctx, cosmos.NewUint(50_000)).IsZero(), Equals, true)
	c.Check(k.GetSurplusForTargetMultiplier(ctx, cosmos.NewUint(10_000)).Uint64(), Equals, target/2)
}

func (s *KeeperCoreCovSuite) TestTHORNames(c *C) {
	ctx, k := setupKeeperForTest(c)
	name := NewTHORName("covname", 100, nil)
	name.Owner = GetRandomBech32Addr()
	k.SetTHORName(ctx, name)

	rec, err := k.GetTHORNameRecord(ctx, "covname")
	c.Assert(err, IsNil)
	c.Check(rec.Owner.Equals(name.Owner), Equals, true)

	// expired names hide the owner but the record keeps it
	expired := ctx.WithBlockHeight(101)
	got, err := k.GetTHORName(expired, "covname")
	c.Assert(err, IsNil)
	c.Check(got.Owner.Empty(), Equals, true)
	c.Check(k.THORNameExists(expired, "covname"), Equals, false)
	rec, err = k.GetTHORNameRecord(expired, "covname")
	c.Assert(err, IsNil)
	c.Check(rec.Owner.Equals(name.Owner), Equals, true)

	iter := k.GetTHORNameIterator(ctx)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(count, Equals, 1)

	c.Assert(k.DeleteTHORName(ctx, "covname"), IsNil)
	c.Check(k.THORNameExists(ctx, "covname"), Equals, false)
	rec, err = k.GetTHORNameRecord(ctx, "covname")
	c.Assert(err, IsNil)
	c.Check(rec.Name, Equals, "covname")
	c.Check(rec.Owner.Empty(), Equals, true)

	bad := THORName{Name: "badname"}
	corruptKeyCov(ctx, k, k.GetKey(prefixTHORName, bad.Key()))
	_, err = k.GetTHORName(ctx.WithBlockHeight(0), "badname")
	c.Check(err, NotNil)
	_, err = k.GetTHORNameRecord(ctx, "badname")
	c.Check(err, NotNil)
	// NOTE: at a positive height the decode error is masked: the partially
	// decoded record has ExpireBlockHeight 0, so it is reported as an expired
	// (empty) name with a nil error. This documents current behaviour.
	got, err = k.GetTHORName(ctx, "badname")
	c.Check(err, IsNil)
	c.Check(got.Name, Equals, "badname")
}

func (s *KeeperCoreCovSuite) TestObservedNetworkFeeVoterAndStreamingSwap(c *C) {
	ctx, k := setupKeeperForTest(c)

	voter := NewObservedNetworkFeeVoter(10, common.BTCChain)
	voter.FeeRate = 3
	voter.TransactionSize = 250
	voter.Signers = []string{GetRandomBech32Addr().String()}
	k.SetObservedNetworkFeeVoter(ctx, voter)
	got, err := k.GetObservedNetworkFeeVoter(ctx, 10, common.BTCChain, 3, 250)
	c.Assert(err, IsNil)
	c.Check(got.Signers, HasLen, 1)
	// a different rate is a different voter
	got, err = k.GetObservedNetworkFeeVoter(ctx, 10, common.BTCChain, 4, 250)
	c.Assert(err, IsNil)
	c.Check(got.Signers, HasLen, 0)
	c.Check(got.FeeRate, Equals, int64(4))

	bad := NewObservedNetworkFeeVoter(11, common.BTCChain)
	corruptKeyCov(ctx, k, k.GetKey(prefixNetworkFeeVoter, bad.ID()))
	_, err = k.GetObservedNetworkFeeVoter(ctx, 11, common.BTCChain, 0, 0)
	c.Check(err, NotNil)

	// streaming swaps: invalid swaps are refused
	hash := GetRandomTxHash()
	k.SetStreamingSwap(ctx, NewStreamingSwap(hash, 0, 1, cosmos.ZeroUint(), cosmos.NewUint(10)))
	c.Check(k.StreamingSwapExists(ctx, hash), Equals, false)
	k.SetStreamingSwap(ctx, NewStreamingSwap(hash, 2, 1, cosmos.ZeroUint(), cosmos.NewUint(10)))
	c.Check(k.StreamingSwapExists(ctx, hash), Equals, true)
	k.RemoveStreamingSwap(ctx, hash)
	c.Check(k.StreamingSwapExists(ctx, hash), Equals, false)
}
