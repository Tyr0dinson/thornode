package thorchain

import (
	"strings"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type DynamicFeeMgrSuite struct{}

var _ = Suite(&DynamicFeeMgrSuite{})

func (s *DynamicFeeMgrSuite) SetUpSuite(_ *C) {
	SetupConfigForTest()
}

// setDynamicFeeTestMimirs sets all DynamicFee-related Mimirs to sensible test values.
// Deadband is 0 by default so tests exercise the move logic without having to craft
// large fee swings; individual tests can override the deadband to test hold behavior.
func setDynamicFeeTestMimirs(ctx cosmos.Context, k keeper.Keeper) {
	k.SetMimir(ctx, "L1DynamicFeeEnabled", 1)
	k.SetMimir(ctx, "L1DynamicFeeEpochBlocks", 10)
	k.SetMimir(ctx, "L1DynamicFeeFloorBPS", 1)
	k.SetMimir(ctx, "L1DynamicFeeCeilingBPS", 100)
	k.SetMimir(ctx, "L1DynamicFeeStepBPS", 1)
	k.SetMimir(ctx, "L1DynamicFeeDeadbandBPS", 0)
	// Liquidity gate off by default; the gate tests set it explicitly.
	k.SetMimir(ctx, "L1DynamicFeeMinSwaps", 0)
	k.SetMimir(ctx, "L1DynamicFeeMinVolumeUSD", 0)
}

// endBlockT seals an epoch in tests. It first gives every live accumulator's pair
// a pool volume of exactly SignalScale, which makes NormalizedSignal() return
// FeesTor unchanged -- so tests written against the raw-fee signal keep their
// arithmetic, and no sealed entry looks like pre-upgrade history.
func endBlockT(ctx cosmos.Context, k keeper.Keeper, dfm DynamicFeeManager) error {
	var accs []types.DynamicFeeAccumulator
	iter := k.GetDynamicFeeAccumulatorIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		var acc types.DynamicFeeAccumulator
		if err := k.Cdc().Unmarshal(iter.Value(), &acc); err != nil {
			continue
		}
		accs = append(accs, acc)
	}
	iter.Close()
	for _, acc := range accs {
		poolAcc, err := k.GetDynamicFeePoolAccumulator(ctx, acc.PairAssetA, acc.PairAssetB)
		if err != nil {
			continue
		}
		poolAcc.VolumeTor = types.SignalScale
		if err := k.SetDynamicFeePoolAccumulator(ctx, poolAcc); err != nil {
			continue
		}
	}
	return dfm.EndBlock(ctx)
}

// registerThorname creates and stores a THORName, and optionally whitelists it.
func registerThorname(ctx cosmos.Context, k keeper.Keeper, name string, whitelistState int64) {
	tn := types.THORName{
		Name:              name,
		ExpireBlockHeight: 100_000_000,
		Aliases:           []types.THORNameAlias{{Chain: common.THORChain, Address: GetRandomTHORAddress()}},
	}
	k.SetTHORName(ctx, tn)
	if whitelistState != 0 {
		key := "DYNAMICFEE-WHITELIST-" + strings.ToUpper(name)
		k.SetMimir(ctx, key, whitelistState)
	}
}

// TestCreditSwapDisabled verifies that CreditSwap is a no-op when the feature is disabled.
func (s *DynamicFeeMgrSuite) TestCreditSwapDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	// Feature disabled by default (L1DynamicFeeEnabled = 0), do NOT set it to 1
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "mytn", 1)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1000), cosmos.NewUint(10),
		true,
	)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	acc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(acc.VolumeTor.IsZero(), Equals, true)
}

// TestCreditSwapNonL1Asset verifies that non-L1 assets (synths, trade, derived) are ignored.
func (s *DynamicFeeMgrSuite) TestCreditSwapNonL1Asset(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "mytn", 1)

	synthBTC, err := common.NewAsset("BTC/BTC")
	c.Assert(err, IsNil)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		synthBTC, common.RuneAsset(),
		cosmos.NewUint(1000), cosmos.NewUint(10),
		true,
	)

	_, _, ok := dynamicFeePairKey(synthBTC, common.RuneAsset())
	c.Assert(ok, Equals, false)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	acc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(acc.VolumeTor.IsZero(), Equals, true)
}

// TestCreditSwapBasicFlow verifies the happy path: the thorname accumulator is
// credited and a DynamicFeeRecord is lazily created at the configured floor bps.
// (The revenue-gradient algorithm does not maintain a pair-wide accumulator.)
func (s *DynamicFeeMgrSuite) TestCreditSwapBasicFlow(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "mytn", 1)

	volume := cosmos.NewUint(500_000)
	fees := cosmos.NewUint(5_000)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		volume, fees,
		true,
	)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)

	tnAcc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(tnAcc.VolumeTor.Equal(volume), Equals, true)
	c.Assert(tnAcc.FeesTor.Equal(fees), Equals, true)

	rec, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.DynamicBps, Equals, uint64(1)) // L1DynamicFeeFloorBPS = 1

	// Credit again to verify accumulation
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		volume, fees,
		true,
	)
	tnAcc2, err := k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(tnAcc2.VolumeTor.Equal(volume.MulUint64(2)), Equals, true)
}

// TestGetDynamicBpsBasic verifies the simple lookup path returns the stored DynamicBps value.
func (s *DynamicFeeMgrSuite) TestGetDynamicBpsBasic(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	rec := types.DynamicFeeRecord{
		Thorname:   "mytn",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 42,
		History:    []types.DynamicFeeEpochRecord{{Epoch: 1, FeesTor: cosmos.NewUint(100), BpsAtClose: 42, PoolVolumeTor: types.SignalScale}},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	bps := dfm.GetDynamicBps(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
	)
	c.Assert(bps, Equals, uint64(42))
}

func (s *DynamicFeeMgrSuite) TestGetDynamicBpsActiveWithoutRecordReturnsFloor(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeFloorBPS", 3)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "mytn", 1)

	bps := dfm.GetDynamicBps(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
	)
	c.Assert(bps, Equals, uint64(3))

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	rec, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.DynamicBps, Equals, uint64(0), Commentf("quote lookup must not create records"))
	c.Assert(len(rec.History), Equals, 0, Commentf("quote lookup must not create records"))
}

func (s *DynamicFeeMgrSuite) TestGetDynamicBpsInactiveWithoutRecordReturnsZero(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "rando", 0)
	registerThorname(ctx, k, "monitor", types.DynamicFeeStatePerPairMonitor)

	for _, tc := range []struct {
		name      string
		affiliate string
	}{
		{name: "unwhitelisted", affiliate: "rando"},
		{name: "monitor", affiliate: "monitor"},
	} {
		bps := dfm.GetDynamicBps(
			ctx,
			[]string{tc.affiliate}, []cosmos.Uint{cosmos.NewUint(100)},
			common.BTCAsset, common.RuneAsset(),
		)
		c.Assert(bps, Equals, uint64(0), Commentf(tc.name))
	}
}

// TestGetDynamicBpsMonitorMode verifies that monitor mode (state=3) causes GetDynamicBps to
// return 0 (monitoring only, no fee adjustment applied), while CreditSwap still accumulates.
func (s *DynamicFeeMgrSuite) TestGetDynamicBpsMonitorMode(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "montn", types.DynamicFeeStatePerPairMonitor)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)

	dfm.CreditSwap(
		ctx,
		[]string{"montn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(500_000), cosmos.NewUint(5_000),
		true,
	)
	tnAcc, err := k.GetDynamicFeeAccumulator(ctx, "montn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(tnAcc.VolumeTor.IsZero(), Equals, false, Commentf("monitor mode should still accumulate"))

	rec := types.DynamicFeeRecord{
		Thorname:   "montn",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 55,
		History:    []types.DynamicFeeEpochRecord{{Epoch: 1, FeesTor: cosmos.NewUint(100), BpsAtClose: 55, PoolVolumeTor: types.SignalScale}},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	bps := dfm.GetDynamicBps(
		ctx,
		[]string{"montn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
	)
	c.Assert(bps, Equals, uint64(0), Commentf("monitor mode should return 0 bps"))
}

// TestGetDynamicBpsMultiAffiliate verifies that with multiple affiliates, the one with the
// largest bps split is used for the lookup.
func (s *DynamicFeeMgrSuite) TestGetDynamicBpsMultiAffiliate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "small", 1)
	registerThorname(ctx, k, "large", 1)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)

	recSmall := types.DynamicFeeRecord{
		Thorname:   "small",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 10,
		History:    []types.DynamicFeeEpochRecord{{Epoch: 1, FeesTor: cosmos.NewUint(100), BpsAtClose: 10, PoolVolumeTor: types.SignalScale}},
	}
	recLarge := types.DynamicFeeRecord{
		Thorname:   "large",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 77,
		History:    []types.DynamicFeeEpochRecord{{Epoch: 1, FeesTor: cosmos.NewUint(100), BpsAtClose: 77, PoolVolumeTor: types.SignalScale}},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, recSmall), IsNil)
	c.Assert(k.SetDynamicFeeRecord(ctx, recLarge), IsNil)

	bps := dfm.GetDynamicBps(
		ctx,
		[]string{"small", "large"},
		[]cosmos.Uint{cosmos.NewUint(100), cosmos.NewUint(300)},
		common.BTCAsset, common.RuneAsset(),
	)
	c.Assert(bps, Equals, uint64(77))
}

// TestEndBlockEpochBoundary verifies that at an epoch boundary the thorname
// accumulator is sealed into a history entry and cleared.
func (s *DynamicFeeMgrSuite) TestEndBlockEpochBoundary(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "mytn", 1)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)

	ctx = ctx.WithBlockHeight(20)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)

	tnAcc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(tnAcc.VolumeTor.IsZero(), Equals, true)

	rec, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(len(rec.History), Equals, 1)
	c.Assert(rec.History[0].FeesTor.Equal(cosmos.NewUint(10_000)), Equals, true)
}

// TestEndBlockColdStartProbesUp verifies that once the search has handed off,
// the +/-step controller's first two epochs probe upward by one step — it has
// no prior move of its own to evaluate, so it probes to generate data for the
// third-epoch gradient.
func (s *DynamicFeeMgrSuite) TestEndBlockColdStartProbesUp(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	c.Assert(k.SetDynamicFeeRecord(ctx, types.DynamicFeeRecord{
		Thorname: "mytn", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 1,
		SearchDone: true,
	}), IsNil)

	// --- Epoch 1 at block 20 ---
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)
	ctx = ctx.WithBlockHeight(20)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)
	rec1, _ := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(rec1.DynamicBps, Equals, uint64(2), Commentf("first epoch probes up from floor=1"))

	// --- Epoch 2 at block 30 --- another cold-start probe since no prior
	// change has been recorded yet (the first epoch is its own starting point).
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)
	ctx = ctx.WithBlockHeight(30)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)
	rec2, _ := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(rec2.DynamicBps, Equals, uint64(3), Commentf("second epoch probes up again"))
}

// TestEndBlockContinueUpOnFeeRise verifies that after a prior upward move,
// a fees increase causes the controller to continue raising the floor.
func (s *DynamicFeeMgrSuite) TestEndBlockContinueUpOnFeeRise(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	// Seed history: bps moved 5 → 6 at epoch 10 with fees 100; epoch 11 fees also 100.
	// Then this epoch 12 shows fees = 200 (100% increase). Direction was up, fees up → continue up to 7.
	pre := types.DynamicFeeRecord{
		Thorname: "mytn", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 6,
		SearchDone: true,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 9, FeesTor: cosmos.NewUint(100), BpsAtClose: 5, PoolVolumeTor: types.SignalScale},
			{Epoch: 10, FeesTor: cosmos.NewUint(100), BpsAtClose: 6, PoolVolumeTor: types.SignalScale},
			{Epoch: 11, FeesTor: cosmos.NewUint(100), BpsAtClose: 6, PoolVolumeTor: types.SignalScale},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, pre), IsNil)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(10_000), cosmos.NewUint(200),
		true,
	)
	ctx = ctx.WithBlockHeight(120)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec, _ := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(rec.DynamicBps, Equals, uint64(7), Commentf("continue up: fees rose, direction was up"))
}

// TestEndBlockReverseDownOnFeeFall verifies that after a prior upward move,
// a fees decrease causes the controller to reverse and lower the floor.
func (s *DynamicFeeMgrSuite) TestEndBlockReverseDownOnFeeFall(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	// Pre: bps went 5 → 8 at epoch 10; fees before=200, fees after should drop.
	pre := types.DynamicFeeRecord{
		Thorname: "mytn", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 8,
		SearchDone: true,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 9, FeesTor: cosmos.NewUint(200), BpsAtClose: 5, PoolVolumeTor: types.SignalScale},
			{Epoch: 10, FeesTor: cosmos.NewUint(200), BpsAtClose: 8, PoolVolumeTor: types.SignalScale},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, pre), IsNil)

	// This epoch shows fees = 50 (75% drop) under bps=8.
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(10_000), cosmos.NewUint(50),
		true,
	)
	ctx = ctx.WithBlockHeight(110)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec, _ := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(rec.DynamicBps, Equals, uint64(7), Commentf("reverse down: raising floor hurt fees"))
}

// TestEndBlockContinueDownOnFeeRise verifies the mirror case: last move was
// down, fees rose → keep lowering.
func (s *DynamicFeeMgrSuite) TestEndBlockContinueDownOnFeeRise(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	pre := types.DynamicFeeRecord{
		Thorname: "mytn", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 5,
		SearchDone: true,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 9, FeesTor: cosmos.NewUint(100), BpsAtClose: 8, PoolVolumeTor: types.SignalScale},
			{Epoch: 10, FeesTor: cosmos.NewUint(100), BpsAtClose: 5, PoolVolumeTor: types.SignalScale},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, pre), IsNil)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(10_000), cosmos.NewUint(150),
		true,
	)
	ctx = ctx.WithBlockHeight(110)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec, _ := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(rec.DynamicBps, Equals, uint64(4), Commentf("continue down: lowering floor helped fees"))
}

// TestEndBlockHoldInDeadband verifies that small fee deltas within the
// deadband do not move the floor.
func (s *DynamicFeeMgrSuite) TestEndBlockHoldInDeadband(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeDeadbandBPS", 1000) // 10%
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	// Fees barely moved (5%) — within the 10% deadband → hold.
	pre := types.DynamicFeeRecord{
		Thorname: "mytn", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 6,
		SearchDone: true,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 9, FeesTor: cosmos.NewUint(100), BpsAtClose: 5, PoolVolumeTor: types.SignalScale},
			{Epoch: 10, FeesTor: cosmos.NewUint(100), BpsAtClose: 6, PoolVolumeTor: types.SignalScale},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, pre), IsNil)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(10_000), cosmos.NewUint(105), // 5% increase
		true,
	)
	ctx = ctx.WithBlockHeight(110)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec, _ := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(rec.DynamicBps, Equals, uint64(6), Commentf("within deadband: hold at 6"))
}

// TestEndBlockZeroVolumeAccumulator verifies that EndBlock seals safely when
// an accumulator exists with zero volume and zero fees for this epoch. This
// can arise from a prior partial state (e.g. a stored accumulator with no
// CreditSwap calls in the current epoch) and exercises windowMeanFees across
// all-zero entries — a scenario where naive divide-by-size logic in the
// gradient path (common.GetSafeShare) would be prone to footguns.
func (s *DynamicFeeMgrSuite) TestEndBlockZeroVolumeAccumulator(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	// Pre-seed a record whose history contains a prior bps change (8 → 5) but
	// whose window-before fees are all zero. decideNextBps must hit the
	// feesBefore.IsZero() guard and return "hold" without a divide-by-zero.
	pre := types.DynamicFeeRecord{
		Thorname: "mytn", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 5,
		SearchDone: true,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 9, FeesTor: cosmos.ZeroUint(), BpsAtClose: 8, PoolVolumeTor: types.SignalScale},
			{Epoch: 10, FeesTor: cosmos.ZeroUint(), BpsAtClose: 5, PoolVolumeTor: types.SignalScale},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, pre), IsNil)

	// Write an accumulator directly with zero volume/fees (no CreditSwap this
	// epoch) to simulate a partial/leftover state the sealer must tolerate.
	c.Assert(k.SetDynamicFeeAccumulator(ctx, types.DynamicFeeAccumulator{
		Thorname:   "mytn",
		PairAssetA: pairA,
		PairAssetB: pairB,
		VolumeTor:  cosmos.ZeroUint(),
		FeesTor:    cosmos.ZeroUint(),
	}), IsNil)

	ctx = ctx.WithBlockHeight(110)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.DynamicBps, Equals, uint64(5), Commentf("all-zero window: hold at 5 without div-by-zero"))
	c.Assert(len(rec.History), Equals, 3, Commentf("seal must append a zero-fee history entry"))
	c.Assert(rec.History[2].FeesTor.IsZero(), Equals, true)
	c.Assert(rec.History[2].BpsAtClose, Equals, uint64(5), Commentf("hold: bps_at_close equals prior bps"))

	// Accumulator must be deleted after sealing.
	acc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(acc.VolumeTor.IsZero(), Equals, true)
	c.Assert(acc.FeesTor.IsZero(), Equals, true)
}

// TestEndBlockFloorClamp verifies that reversing down at the floor does not
// cross below the floor.
func (s *DynamicFeeMgrSuite) TestEndBlockFloorClamp(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	// At floor. Last move was up (5 → 1? No — reverse would be down from 1
	// at floor). To test floor clamp on reverse_down: seed with bps=1 (floor),
	// last move up from 0 conceptually isn't possible since floor=1. Instead
	// simulate continue_down path from bps=1: last move was down (2→1), fees
	// rose so we want to continue down — but floor should clamp to 1.
	pre := types.DynamicFeeRecord{
		Thorname: "mytn", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 1,
		SearchDone: true,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 9, FeesTor: cosmos.NewUint(100), BpsAtClose: 2, PoolVolumeTor: types.SignalScale},
			{Epoch: 10, FeesTor: cosmos.NewUint(100), BpsAtClose: 1, PoolVolumeTor: types.SignalScale},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, pre), IsNil)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(10_000), cosmos.NewUint(500),
		true,
	)
	ctx = ctx.WithBlockHeight(110)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec, _ := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(rec.DynamicBps, Equals, uint64(1), Commentf("floor clamp: cannot go below floor=1"))
}

// TestEndBlockCeilingClamp verifies the symmetric case at the ceiling.
func (s *DynamicFeeMgrSuite) TestEndBlockCeilingClamp(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeCeilingBPS", 10)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	// At ceiling. Last move was up (9→10); fees rose. Continue-up would push
	// past the ceiling, so should clamp to 10.
	pre := types.DynamicFeeRecord{
		Thorname: "mytn", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 10,
		SearchDone: true,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 9, FeesTor: cosmos.NewUint(100), BpsAtClose: 9, PoolVolumeTor: types.SignalScale},
			{Epoch: 10, FeesTor: cosmos.NewUint(100), BpsAtClose: 10, PoolVolumeTor: types.SignalScale},
		},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, pre), IsNil)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(10_000), cosmos.NewUint(500),
		true,
	)
	ctx = ctx.WithBlockHeight(110)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec, _ := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(rec.DynamicBps, Equals, uint64(10), Commentf("ceiling clamp: cannot exceed 10"))
}

// TestEndBlockSoloOnPair verifies the algorithm runs and moves for a thorname
// that is the only affiliate active on its pair — the solo-on-pair dead zone
// that plagued the share-based design is gone.
func (s *DynamicFeeMgrSuite) TestEndBlockSoloOnPair(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	for epoch := int64(1); epoch <= 3; epoch++ {
		dfm.CreditSwap(
			ctx,
			[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
			common.BTCAsset, common.RuneAsset(),
			cosmos.NewUint(1_000_000), cosmos.NewUint(uint64(100*epoch)),
			true,
		)
		ctx = ctx.WithBlockHeight(epoch * 10)
		c.Assert(endBlockT(ctx, k, dfm), IsNil)
	}

	rec, _ := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(rec.DynamicBps > 1, Equals, true,
		Commentf("solo-on-pair: bps should still have moved up from floor"))
}

// TestEndBlockWhitelistRemovalCleanup verifies that when a thorname is removed from the
// whitelist, all associated DynamicFee state is deleted on the next EndBlock call.
func (s *DynamicFeeMgrSuite) TestEndBlockWhitelistRemovalCleanup(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "mytn", 1)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(500_000), cosmos.NewUint(5_000),
		true,
	)
	ctx = ctx.WithBlockHeight(20)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	rec, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.DynamicBps > 0 || len(rec.History) > 0, Equals, true,
		Commentf("record should exist after epoch"))

	k.SetMimir(ctx, "DYNAMICFEE-WHITELIST-MYTN", 0)

	// Cleanup runs at epoch boundaries only.
	ctx = ctx.WithBlockHeight(21)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)
	recMid, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(recMid.DynamicBps > 0 || len(recMid.History) > 0, Equals, true,
		Commentf("record should survive non-epoch block after whitelist removal"))

	ctx = ctx.WithBlockHeight(30)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec2, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec2.DynamicBps == 0 && len(rec2.History) == 0, Equals, true,
		Commentf("record should be deleted after whitelist removal"))
}

// TestCreditSwapDoubleSwapPerLegKeys verifies that an L1→L1 double swap
// (e.g. BTC→ETH) credits two separate (L1, RUNE) pair keys — one per leg —
// and does NOT create a (BTC, ETH) end-to-end pair entry. This mirrors how
// handler_swap.go iterates swap events and calls CreditSwap once per leg,
// and is the regression guard for the pair-key mismatch class of bug that
// would otherwise record credits at keys no lookup path consults.
func (s *DynamicFeeMgrSuite) TestCreditSwapDoubleSwapPerLegKeys(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "mytn", 1)

	legVolume := cosmos.NewUint(1_000_000)
	legFees := cosmos.NewUint(5_000)

	// Leg 1: BTC → RUNE
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		legVolume, legFees,
		true,
	)
	// Leg 2: RUNE → ETH
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.RuneAsset(), common.ETHAsset,
		legVolume, legFees,
		true,
	)

	btcRuneA, btcRuneB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	ethRuneA, ethRuneB, ok := dynamicFeePairKey(common.ETHAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)

	btcRuneAcc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", btcRuneA, btcRuneB)
	c.Assert(err, IsNil)
	c.Assert(btcRuneAcc.VolumeTor.Equal(legVolume), Equals, true,
		Commentf("BTC/RUNE leg should be credited"))

	ethRuneAcc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", ethRuneA, ethRuneB)
	c.Assert(err, IsNil)
	c.Assert(ethRuneAcc.VolumeTor.Equal(legVolume), Equals, true,
		Commentf("ETH/RUNE leg should be credited"))

	// End-to-end (BTC, ETH) key must not be used — GetDynamicBps also
	// normalises by leg, so any credit under (BTC, ETH) would be invisible.
	btcEthA, btcEthB := types.NormalizePairAssets(common.BTCAsset.String(), common.ETHAsset.String())
	btcEthAcc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", btcEthA, btcEthB)
	c.Assert(err, IsNil)
	c.Assert(btcEthAcc.VolumeTor.IsZero(), Equals, true,
		Commentf("(BTC, ETH) should not be credited; credit is per-leg"))

	// GetDynamicBps lookups match the credited per-leg keys.
	btcRuneRec, err := k.GetDynamicFeeRecord(ctx, "mytn", btcRuneA, btcRuneB)
	c.Assert(err, IsNil)
	c.Assert(btcRuneRec.DynamicBps, Equals, uint64(1),
		Commentf("BTC/RUNE record should be lazily created at floor"))
	ethRuneRec, err := k.GetDynamicFeeRecord(ctx, "mytn", ethRuneA, ethRuneB)
	c.Assert(err, IsNil)
	c.Assert(ethRuneRec.DynamicBps, Equals, uint64(1),
		Commentf("ETH/RUNE record should be lazily created at floor"))

	btcRuneBps := dfm.GetDynamicBps(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
	)
	c.Assert(btcRuneBps, Equals, uint64(1))
	ethRuneBps := dfm.GetDynamicBps(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.ETHAsset, common.RuneAsset(),
	)
	c.Assert(ethRuneBps, Equals, uint64(1))
}

// TestCreditSwapDominantOnly locks in that with multiple affiliates, only the
// dominant-bps thorname gets credited — mirroring GetDynamicBps so accounting
// stays consistent with the thorname whose fee was actually applied. A
// regression that credited every affiliate would inflate aggregate volume
// across thornames on the same pair.
func (s *DynamicFeeMgrSuite) TestCreditSwapDominantOnly(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "small", 1)
	registerThorname(ctx, k, "large", 1)

	dfm.CreditSwap(
		ctx,
		[]string{"small", "large"},
		[]cosmos.Uint{cosmos.NewUint(100), cosmos.NewUint(300)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)

	largeAcc, err := k.GetDynamicFeeAccumulator(ctx, "large", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(largeAcc.VolumeTor.Equal(cosmos.NewUint(1_000_000)), Equals, true,
		Commentf("dominant affiliate should be credited"))

	smallAcc, err := k.GetDynamicFeeAccumulator(ctx, "small", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(smallAcc.VolumeTor.IsZero(), Equals, true,
		Commentf("non-dominant affiliate must not be credited"))
}

// TestDominantEligibleAffiliateBypass exercises the floor-collapse bypass:
// a memo such as `xdefi/qqqqqq:1/500` lists an unparsable secondary
// affiliate at higher bps than the real whitelisted affiliate. Strict
// largest-bps selection would pick `qqqqqq`, fail the THORName/whitelist
// gate, and short-circuit both CreditSwap and GetDynamicBps — letting the
// floor collapse to static and starving xdefi's revenue accumulator.
// dominantEligibleAffiliate must filter the ineligible entry out of the
// selection so xdefi continues to govern the floor and accrue credit.
func (s *DynamicFeeMgrSuite) TestDominantEligibleAffiliateBypass(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "xdefi", 1)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	rec := types.DynamicFeeRecord{
		Thorname:   "xdefi",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 50,
		History:    []types.DynamicFeeEpochRecord{{Epoch: 1, FeesTor: cosmos.NewUint(100), BpsAtClose: 50, PoolVolumeTor: types.SignalScale}},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	// `qqqqqq` is neither a registered THORName nor a parseable address, but
	// its 500 bps would dominate strict largest-bps selection.
	bps := dfm.GetDynamicBps(
		ctx,
		[]string{"xdefi", "qqqqqq"},
		[]cosmos.Uint{cosmos.NewUint(1), cosmos.NewUint(500)},
		common.BTCAsset, common.RuneAsset(),
	)
	c.Assert(bps, Equals, uint64(50),
		Commentf("ineligible high-bps secondary must not collapse the floor to static"))

	dfm.CreditSwap(
		ctx,
		[]string{"xdefi", "qqqqqq"},
		[]cosmos.Uint{cosmos.NewUint(1), cosmos.NewUint(500)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)
	xdefiAcc, err := k.GetDynamicFeeAccumulator(ctx, "xdefi", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(xdefiAcc.VolumeTor.Equal(cosmos.NewUint(1_000_000)), Equals, true,
		Commentf("eligible affiliate must accrue credit despite higher-bps ineligible secondary"))
}

// TestDominantEligibleAffiliateAddressForm covers the address-form variant of
// the bypass: a valid THORChain address listed at higher bps is also not a
// THORName, so it must not steer dynamic-fee selection.
func (s *DynamicFeeMgrSuite) TestDominantEligibleAffiliateAddressForm(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "xdefi", 1)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	rec := types.DynamicFeeRecord{
		Thorname:   "xdefi",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 33,
		History:    []types.DynamicFeeEpochRecord{{Epoch: 1, FeesTor: cosmos.NewUint(100), BpsAtClose: 33, PoolVolumeTor: types.SignalScale}},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	addr := GetRandomTHORAddress().String()
	bps := dfm.GetDynamicBps(
		ctx,
		[]string{"xdefi", addr},
		[]cosmos.Uint{cosmos.NewUint(50), cosmos.NewUint(450)},
		common.BTCAsset, common.RuneAsset(),
	)
	c.Assert(bps, Equals, uint64(33),
		Commentf("address-form secondary must not steer fee selection"))
}

// TestDominantEligibleAffiliateUnwhitelisted ensures a registered THORName
// that is not whitelisted (state 0) is also filtered out of selection so it
// cannot steer dynamic-fee selection away from a whitelisted peer.
func (s *DynamicFeeMgrSuite) TestDominantEligibleAffiliateUnwhitelisted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "xdefi", 1)
	registerThorname(ctx, k, "rando", 0) // exists but not whitelisted

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	rec := types.DynamicFeeRecord{
		Thorname:   "xdefi",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 25,
		History:    []types.DynamicFeeEpochRecord{{Epoch: 1, FeesTor: cosmos.NewUint(100), BpsAtClose: 25, PoolVolumeTor: types.SignalScale}},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	bps := dfm.GetDynamicBps(
		ctx,
		[]string{"xdefi", "rando"},
		[]cosmos.Uint{cosmos.NewUint(10), cosmos.NewUint(900)},
		common.BTCAsset, common.RuneAsset(),
	)
	c.Assert(bps, Equals, uint64(25),
		Commentf("unwhitelisted THORName must not dominate dynamic-fee selection"))
}

// TestDominantEligibleAffiliateMonitorVsActive verifies the asymmetric
// allowed-state sets between CreditSwap and GetDynamicBps. CreditSwap admits
// state 1 or 2, GetDynamicBps admits only state 1. With a higher-bps
// monitor-mode peer present alongside an active whitelisted thorname, the
// active thorname must govern live fees, while the monitor-mode thorname
// receives the credit signal.
func (s *DynamicFeeMgrSuite) TestDominantEligibleAffiliateMonitorVsActive(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "active", 1)
	registerThorname(ctx, k, "monitor", types.DynamicFeeStatePerPairMonitor)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	rec := types.DynamicFeeRecord{
		Thorname:   "active",
		PairAssetA: pairA,
		PairAssetB: pairB,
		DynamicBps: 17,
		History:    []types.DynamicFeeEpochRecord{{Epoch: 1, FeesTor: cosmos.NewUint(100), BpsAtClose: 17, PoolVolumeTor: types.SignalScale}},
	}
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)

	// GetDynamicBps allows only state 1 → picks "active" even though
	// "monitor" has higher bps.
	bps := dfm.GetDynamicBps(
		ctx,
		[]string{"active", "monitor"},
		[]cosmos.Uint{cosmos.NewUint(100), cosmos.NewUint(900)},
		common.BTCAsset, common.RuneAsset(),
	)
	c.Assert(bps, Equals, uint64(17))

	// CreditSwap allows the monitor states too → picks "monitor" by largest bps.
	dfm.CreditSwap(
		ctx,
		[]string{"active", "monitor"},
		[]cosmos.Uint{cosmos.NewUint(100), cosmos.NewUint(900)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)
	monitorAcc, err := k.GetDynamicFeeAccumulator(ctx, "monitor", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(monitorAcc.VolumeTor.Equal(cosmos.NewUint(1_000_000)), Equals, true,
		Commentf("monitor-mode dominant peer must accumulate"))
	activeAcc, err := k.GetDynamicFeeAccumulator(ctx, "active", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(activeAcc.VolumeTor.IsZero(), Equals, true,
		Commentf("non-dominant active peer must not be credited"))
}

// TestCreditSwapMismatchedAffiliateLengths verifies that CreditSwap is a no-op
// when affiliates and affiliateBps have different lengths.
// dominantEligibleAffiliate rejects this case, preventing an out-of-bounds
// index when selecting the largest-bps thorname.
func (s *DynamicFeeMgrSuite) TestCreditSwapMismatchedAffiliateLengths(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	// affiliates has 2 entries, affiliateBps has 1 → reject.
	dfm.CreditSwap(
		ctx,
		[]string{"mytn", "other"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	acc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(acc.VolumeTor.IsZero(), Equals, true,
		Commentf("mismatched slice lengths must not mutate state"))

	bps := dfm.GetDynamicBps(
		ctx,
		[]string{"mytn", "other"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
	)
	c.Assert(bps, Equals, uint64(0),
		Commentf("mismatched slice lengths must return zero bps"))
}

// TestCreditSwapCacheContextRollback verifies that CreditSwap state changes
// made against a cache context are discarded if the parent commit never runs,
// e.g. when a swap is rejected by the synth-cap check after CreditSwap was
// already called. handler.Run wraps each handler in CacheContext and only
// commits on err==nil.
func (s *DynamicFeeMgrSuite) TestCreditSwapCacheContextRollback(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	cacheCtx, _ := ctx.CacheContext()
	dfm.CreditSwap(
		cacheCtx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)
	// Deliberately do NOT call commit — simulates the handler returning an error.

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	parentAcc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(parentAcc.VolumeTor.IsZero(), Equals, true,
		Commentf("uncommitted cache writes must not leak to parent ctx"))
	parentRec, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(parentRec.DynamicBps == 0 && len(parentRec.History) == 0, Equals, true,
		Commentf("lazily-created record must not leak to parent ctx"))
}

// TestEndBlockSealBeforePruneSurvivesLateStart verifies that a record that
// first appears via CreditSwap lazy creation at a block height where
// currentEpoch >= MaxDynamicFeeHistory is NOT pruned in the same EndBlock.
// The seal phase sets LastActiveEpoch = currentEpoch before the prune phase
// runs, so the diff is zero. A regression that reordered seal-after-prune
// would nuke fresh records on first appearance.
func (s *DynamicFeeMgrSuite) TestEndBlockSealBeforePruneSurvivesLateStart(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	// Epoch 50 > MaxDynamicFeeHistory (30). If prune ran before seal, a
	// fresh record with LastActiveEpoch == 0 would satisfy
	// currentEpoch - 0 >= 30 and be deleted on its first appearance.
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)
	ctx = ctx.WithBlockHeight(500) // epoch = 500 / 10 = 50
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	rec, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(len(rec.History) > 0, Equals, true,
		Commentf("fresh record created at epoch %d must survive EndBlock", 50))
	c.Assert(rec.LastActiveEpoch, Equals, uint64(50))
}

// TestEndBlockRebasesOnEpochBlocksShrink verifies that when
// L1DynamicFeeEpochBlocks is decreased mid-operation (e.g. 100 → 10), a
// record's LastActiveEpoch is rebased so it does not look artificially
// stale under the new, larger currentEpoch values. Without the rebase,
// currentEpoch - LastActiveEpoch would blow past MaxDynamicFeeHistory and
// the record would be pruned at the next EndBlock.
func (s *DynamicFeeMgrSuite) TestEndBlockRebasesOnEpochBlocksShrink(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeEpochBlocks", 100)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	// Seal epoch under EB=100 at block 1000 → LastActiveEpoch = 10.
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)
	ctx = ctx.WithBlockHeight(1000)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	rec, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.LastActiveEpoch, Equals, uint64(10))
	c.Assert(len(rec.History), Equals, 1)

	// Governance shrinks epoch to 10. At block 1100, currentEpoch = 110.
	// Without rebase: diff = 110 - 10 = 100 >= 30 → premature prune.
	// With rebase: LastActiveEpoch becomes 10*100/10 = 100; diff = 10.
	k.SetMimir(ctx, "L1DynamicFeeEpochBlocks", 10)
	ctx = ctx.WithBlockHeight(1100)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec2, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec2.DynamicBps > 0 || len(rec2.History) > 0, Equals, true,
		Commentf("record must survive epochBlocks shrink from 100 to 10"))
	c.Check(rec2.LastActiveEpoch, Equals, uint64(100),
		Commentf("LastActiveEpoch should be rebased via old*oldEB/newEB = 10*100/10"))

	observed, err := k.GetLastObservedDynamicFeeEpochBlocks(ctx)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, int64(10),
		Commentf("observed epoch blocks should track the new mimir value"))
}

// TestEndBlockRebasesOnEpochBlocksGrow verifies the mirror direction: when
// L1DynamicFeeEpochBlocks is increased, the existing underflow guard keeps
// the record alive and the rebase preserves its history in the new scale.
func (s *DynamicFeeMgrSuite) TestEndBlockRebasesOnEpochBlocksGrow(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeEpochBlocks", 10)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	// Seal at block 100 under EB=10 → LastActiveEpoch = 10.
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)
	ctx = ctx.WithBlockHeight(100)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	// Grow epoch to 100. At block 200, currentEpoch = 2. LastActiveEpoch
	// under old scale was 10 — without rebase, the underflow guard keeps
	// the record alive but LastActiveEpoch > currentEpoch for many epochs.
	// After rebase: LastActiveEpoch = 10*10/100 = 1, diff = 1.
	k.SetMimir(ctx, "L1DynamicFeeEpochBlocks", 100)
	ctx = ctx.WithBlockHeight(200)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.LastActiveEpoch <= 2, Equals, true,
		Commentf("LastActiveEpoch %d should rebase to the new, smaller scale", rec.LastActiveEpoch))

	observed, err := k.GetLastObservedDynamicFeeEpochBlocks(ctx)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, int64(100))
}

// TestEndBlockRebaseFirstObservationNoop verifies that on the first
// EndBlock after the feature is enabled (no prior observation stored),
// the rebase records the current value and leaves records untouched.
func (s *DynamicFeeMgrSuite) TestEndBlockRebaseFirstObservationNoop(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "mytn", 1)

	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)
	ctx = ctx.WithBlockHeight(20)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	observed, err := k.GetLastObservedDynamicFeeEpochBlocks(ctx)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, int64(10),
		Commentf("first EndBlock must record current epochBlocks"))

	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	rec, err := k.GetDynamicFeeRecord(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.LastActiveEpoch, Equals, uint64(2),
		Commentf("first observation should not rebase existing LastActiveEpoch"))
}

// TestDecideNextBpsPureUnit exercises the gradient-decision helper directly
// without a manager or keeper, covering all reason branches in one place.
func (s *DynamicFeeMgrSuite) TestDecideNextBpsPureUnit(c *C) {
	tenK := cosmos.NewUint(10000)
	deadband := cosmos.NewUint(500) // 5%

	mkRec := func(entries ...types.DynamicFeeEpochRecord) types.DynamicFeeRecord {
		return types.DynamicFeeRecord{History: entries}
	}

	// Cold start: single entry, no prior bps change.
	rec := mkRec(types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(100), BpsAtClose: 1, PoolVolumeTor: types.SignalScale})
	reason, newBps, _, _, _ := decideNextBps(rec, 1, 1, 20, 1, 3, deadband, tenK)
	c.Check(reason, Equals, "cold_start_probe")
	c.Check(newBps, Equals, uint64(2))

	// Continue up: 3→4 at epoch 10, fees rose.
	rec = mkRec(
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(100), BpsAtClose: 3, PoolVolumeTor: types.SignalScale},
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(100), BpsAtClose: 4, PoolVolumeTor: types.SignalScale}, // change here
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(200), BpsAtClose: 4, PoolVolumeTor: types.SignalScale}, // now
	)
	reason, newBps, _, _, _ = decideNextBps(rec, 4, 1, 20, 1, 3, deadband, tenK)
	c.Check(reason, Equals, "continue_up")
	c.Check(newBps, Equals, uint64(5))

	// Reverse down: 3→4, fees fell.
	rec = mkRec(
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(200), BpsAtClose: 3, PoolVolumeTor: types.SignalScale},
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(200), BpsAtClose: 4, PoolVolumeTor: types.SignalScale},
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(50), BpsAtClose: 4, PoolVolumeTor: types.SignalScale},
	)
	reason, newBps, _, _, _ = decideNextBps(rec, 4, 1, 20, 1, 3, deadband, tenK)
	c.Check(reason, Equals, "reverse_down")
	c.Check(newBps, Equals, uint64(3))

	// Continue down: 8→7, fees rose.
	rec = mkRec(
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(100), BpsAtClose: 8, PoolVolumeTor: types.SignalScale},
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(100), BpsAtClose: 7, PoolVolumeTor: types.SignalScale},
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(200), BpsAtClose: 7, PoolVolumeTor: types.SignalScale},
	)
	reason, newBps, _, _, _ = decideNextBps(rec, 7, 1, 20, 1, 3, deadband, tenK)
	c.Check(reason, Equals, "continue_down")
	c.Check(newBps, Equals, uint64(6))

	// Reverse up: 8→7, fees fell.
	rec = mkRec(
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(200), BpsAtClose: 8, PoolVolumeTor: types.SignalScale},
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(200), BpsAtClose: 7, PoolVolumeTor: types.SignalScale},
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(50), BpsAtClose: 7, PoolVolumeTor: types.SignalScale},
	)
	reason, newBps, _, _, _ = decideNextBps(rec, 7, 1, 20, 1, 3, deadband, tenK)
	c.Check(reason, Equals, "reverse_up")
	c.Check(newBps, Equals, uint64(8))

	// Hold in deadband.
	rec = mkRec(
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(100), BpsAtClose: 3, PoolVolumeTor: types.SignalScale},
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(100), BpsAtClose: 4, PoolVolumeTor: types.SignalScale},
		types.DynamicFeeEpochRecord{FeesTor: cosmos.NewUint(101), BpsAtClose: 4, PoolVolumeTor: types.SignalScale},
	)
	reason, newBps, _, _, _ = decideNextBps(rec, 4, 1, 20, 1, 3, deadband, tenK)
	c.Check(reason, Equals, "hold")
	c.Check(newBps, Equals, uint64(4))
}

// TestEndBlockClearsAccumulatorsWhenDisabled verifies that turning the feature
// off drops any accumulator rows still carrying volume/fees. Without this,
// a later re-enable would add new volume on top of old and seal the mix into
// a wrong epoch.
func (s *DynamicFeeMgrSuite) TestEndBlockClearsAccumulatorsWhenDisabled(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)

	registerThorname(ctx, k, "mytn", 1)
	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)

	// Accumulate within an epoch under the enabled feature.
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000),
		true,
	)
	acc, err := k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(acc.VolumeTor.Equal(cosmos.NewUint(1_000_000)), Equals, true)

	// Disable the feature and run EndBlock at a non-epoch-boundary height.
	// The accumulator row should be cleared even though the epoch wouldn't
	// normally seal here.
	k.SetMimir(ctx, "L1DynamicFeeEnabled", 0)
	ctx = ctx.WithBlockHeight(5)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	acc, err = k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(acc.VolumeTor.IsZero(), Equals, true, Commentf("accumulator should be cleared on disable"))
	c.Assert(acc.FeesTor.IsZero(), Equals, true)

	// Re-enable and accumulate again — no pre-disable residue should be
	// visible to the next seal.
	k.SetMimir(ctx, "L1DynamicFeeEnabled", 1)
	dfm.CreditSwap(
		ctx,
		[]string{"mytn"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(7), cosmos.NewUint(3),
		true,
	)
	acc, err = k.GetDynamicFeeAccumulator(ctx, "mytn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(acc.VolumeTor.Equal(cosmos.NewUint(7)), Equals, true, Commentf("post-reenable volume should not include pre-disable residue"))
	c.Assert(acc.FeesTor.Equal(cosmos.NewUint(3)), Equals, true)
}

// TestNonNegUint64 pins the defense-in-depth guard that prevents int64→uint64
// wrap from turning a negative governance value into MaxUint64. `GetConfigInt64`
// already falls back to the constant default on negative mimirs, so this helper
// is belt-and-suspenders; keep it correct in case a future caller reaches
// `GetMimir` directly or the defaults ever go negative.
func (s *DynamicFeeMgrSuite) TestNonNegUint64(c *C) {
	c.Check(nonNegUint64(0), Equals, uint64(0))
	c.Check(nonNegUint64(1), Equals, uint64(1))
	c.Check(nonNegUint64(100), Equals, uint64(100))
	c.Check(nonNegUint64(-1), Equals, uint64(0), Commentf("must NOT wrap to MaxUint64"))
	c.Check(nonNegUint64(-100), Equals, uint64(0))
	// Sentinel value that would be catastrophic if it wrapped: ensure it
	// clamps to 0 rather than becoming 18446744073709551615.
	c.Check(nonNegUint64(int64(-1)), Equals, uint64(0))
}

// TestEndBlockDisabledEmptyStoreIdempotent pins the genesis / clean-boot
// behavior for a chain that boots with L1DynamicFeeEnabled=0 and no prior
// dynamic-fee state: EndBlock must return nil, write nothing, and remain
// cheap across repeated calls. The accumulator iterator should find nothing,
// no record should appear, and the last-observed epoch-blocks value should
// stay at its zero default (first observation is only recorded when the
// feature is enabled and an epoch boundary fires).
func (s *DynamicFeeMgrSuite) TestEndBlockDisabledEmptyStoreIdempotent(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	dfm := newDynamicFeeMgr(k)

	// Feature stays disabled (default). Call EndBlock repeatedly at varied
	// heights, including values that would be epoch boundaries if the feature
	// were on, to confirm the short-circuit holds regardless of height.
	for _, h := range []int64{1, 5, 10, 20, 100} {
		ctx = ctx.WithBlockHeight(h)
		c.Assert(endBlockT(ctx, k, dfm), IsNil)
	}

	recIter := k.GetDynamicFeeRecordIterator(ctx)
	recCount := 0
	for ; recIter.Valid(); recIter.Next() {
		recCount++
	}
	recIter.Close()
	c.Check(recCount, Equals, 0, Commentf("no records should exist on a clean-boot disabled store"))

	accIter := k.GetDynamicFeeAccumulatorIterator(ctx)
	accumulatorCount := 0
	for ; accIter.Valid(); accIter.Next() {
		accumulatorCount++
	}
	accIter.Close()
	c.Check(accumulatorCount, Equals, 0, Commentf("no accumulators should exist on a clean-boot disabled store"))

	observed, err := k.GetLastObservedDynamicFeeEpochBlocks(ctx)
	c.Assert(err, IsNil)
	c.Check(observed, Equals, int64(0), Commentf("first-observation write is gated on the feature being enabled"))
}

// setupTorAnchorPool wires a single TOR anchor pool so DollarsPerRune is
// non-zero in tests. dollarsPerRune is the desired RUNE/USD price in 1e8 units
// (e.g. 41_000_000 for $0.41/RUNE).
func setupTorAnchorPool(c *C, ctx cosmos.Context, k keeper.Keeper, dollarsPerRune uint64) {
	// Anchor RUNE valuation: pool with BalanceRune = R, BalanceAsset = R*price
	// (asset is interpreted as USD equivalent). The simpler escape hatch is the
	// `DollarsPerRune` mimir override which the keeper consults first.
	c.Assert(dollarsPerRune > 0, Equals, true)
	k.SetMimir(ctx, "DollarsPerRune", int64(dollarsPerRune))
}

// TestCreditDynamicFeeForLegsTORConversion pins the RUNE→TOR conversion in
// the swap-completion hook: at $0.41/RUNE, a 100 RUNE leg fee should be
// recorded as 41 TOR (1e8-precision USD). Verifies both volume and fees use
// the same scale.
func (s *DynamicFeeMgrSuite) TestCreditDynamicFeeForLegsTORConversion(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	registerThorname(ctx, k, "alice", 1)

	// $0.41 per RUNE in 1e8 units → 41,000,000.
	setupTorAnchorPool(c, ctx, k, 41_000_000)

	affiliates := []string{"alice"}
	affiliateBps := []cosmos.Uint{cosmos.NewUint(100)}

	swapEvents := []*EventSwap{{
		Pool:               common.BTCAsset,
		LiquidityFeeInRune: cosmos.NewUint(100 * common.One), // 100 RUNE
		EmitAsset:          common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		InTx: common.NewTx(
			GetRandomTxHash(),
			GetRandomRUNEAddress(),
			GetRandomBTCAddress(),
			common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))}, // 10 RUNE
			common.Gas{}, "",
		),
	}}

	creditDynamicFeeForLegs(ctx, mgr, swapEvents, affiliates, affiliateBps, true)

	pairA, pairB, ok := dynamicFeePairKey(common.RuneAsset(), common.BTCAsset)
	c.Assert(ok, Equals, true)
	acc, err := k.GetDynamicFeeAccumulator(ctx, "alice", pairA, pairB)
	c.Assert(err, IsNil)

	// volume = 10 RUNE * 0.41 = 4.1 USD = 4.1 * 1e8 TOR units.
	// 10*common.One * 41_000_000 / common.One = 41_000_000 * 10 = 410_000_000.
	c.Check(acc.VolumeTor.Equal(cosmos.NewUint(uint64(10*common.One)*41_000_000/common.One)), Equals, true,
		Commentf("volume should be RUNE*DollarsPerRune/1e8; got %s", acc.VolumeTor.String()))
	// fees = 100 RUNE * 0.41 = 41 USD = 41 * 1e8 TOR units.
	c.Check(acc.FeesTor.Equal(cosmos.NewUint(uint64(100*common.One)*41_000_000/common.One)), Equals, true,
		Commentf("fees should be RUNE*DollarsPerRune/1e8; got %s", acc.FeesTor.String()))
}

// TestCreditDynamicFeeForLegsZeroPriceSkipped pins that swaps are skipped (not
// credited as zero) when DollarsPerRune is unavailable. Recording zeros would
// drag the gradient signal toward zero on the next epoch boundary.
func (s *DynamicFeeMgrSuite) TestCreditDynamicFeeForLegsZeroPriceSkipped(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	registerThorname(ctx, k, "alice", 1)

	// Note: do NOT set DollarsPerRune. With no anchor pools and no override,
	// the keeper returns zero and the credit hook must skip.

	affiliates := []string{"alice"}
	affiliateBps := []cosmos.Uint{cosmos.NewUint(100)}

	swapEvents := []*EventSwap{{
		Pool:               common.BTCAsset,
		LiquidityFeeInRune: cosmos.NewUint(100 * common.One),
		EmitAsset:          common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
		InTx: common.NewTx(
			GetRandomTxHash(),
			GetRandomRUNEAddress(),
			GetRandomBTCAddress(),
			common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))},
			common.Gas{}, "",
		),
	}}

	creditDynamicFeeForLegs(ctx, mgr, swapEvents, affiliates, affiliateBps, true)

	pairA, pairB, ok := dynamicFeePairKey(common.RuneAsset(), common.BTCAsset)
	c.Assert(ok, Equals, true)
	acc, err := k.GetDynamicFeeAccumulator(ctx, "alice", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(acc.VolumeTor.IsZero(), Equals, true,
		Commentf("zero DollarsPerRune must skip the credit, not record zeros"))
	c.Check(acc.FeesTor.IsZero(), Equals, true)
}

// --- Liquidity gate (Change 1) ---

// creditOneSwap is the shorthand the gate tests use: one BTC/RUNE swap for
// `name`, counted as a user swap.
func creditOneSwap(ctx cosmos.Context, dfm *DynamicFeeMgr, name string, volume, fees cosmos.Uint) {
	dfm.CreditSwap(
		ctx,
		[]string{name}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(),
		volume, fees,
		true,
	)
}

// TestEndBlockGateRollsOnLowSwapCount pins the core of the change: an epoch
// holding fewer than L1DynamicFeeMinSwaps swaps must not seal a decision. The
// accumulator survives so its volume, fees and swap count roll into the next
// epoch, and LastActiveEpoch advances so a permanently-gated integration is
// not pruned out from under its own accumulator.
func (s *DynamicFeeMgrSuite) TestEndBlockGateRollsOnLowSwapCount(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeMinSwaps", 30)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "ss", 1)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	volume, fees := cosmos.NewUint(1_000_000), cosmos.NewUint(10_000)
	creditOneSwap(ctx, dfm, "ss", volume, fees)
	dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(5_000_000))

	ctx = ctx.WithBlockHeight(20)
	c.Assert(dfm.EndBlock(ctx), IsNil)

	rec, err := k.GetDynamicFeeRecord(ctx, "ss", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(len(rec.History), Equals, 0, Commentf("a gated epoch must not append history"))
	c.Check(rec.DynamicBps, Equals, uint64(1), Commentf("bps must not step on one swap"))
	c.Check(rec.LastActiveEpoch, Equals, uint64(2), Commentf("rolling must keep the record alive"))

	acc, err := k.GetDynamicFeeAccumulator(ctx, "ss", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(acc.SwapCount, Equals, uint64(1))
	c.Check(acc.VolumeTor.Equal(volume), Equals, true)
	c.Check(acc.FeesTor.Equal(fees), Equals, true)
	c.Check(acc.PoolVolumeTor.Equal(cosmos.NewUint(5_000_000)), Equals, true,
		Commentf("the epoch's pool volume must fold into the rolling accumulator"))

	poolAcc, err := k.GetDynamicFeePoolAccumulator(ctx, pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(poolAcc.VolumeTor.IsZero(), Equals, true,
		Commentf("pool accumulators are per-epoch and cleared at every seal"))
}

// TestEndBlockGateRollsOnLowVolume covers the other half of the gate: enough
// swaps, not enough dollars.
func (s *DynamicFeeMgrSuite) TestEndBlockGateRollsOnLowVolume(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeMinVolumeUSD", 50_000)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "ss", 1)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	// $49,999 of volume — one dollar short.
	creditOneSwap(ctx, dfm, "ss", cosmos.NewUint(49_999*common.One), cosmos.NewUint(10_000))
	dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(50_000*common.One))

	ctx = ctx.WithBlockHeight(20)
	c.Assert(dfm.EndBlock(ctx), IsNil)

	rec, err := k.GetDynamicFeeRecord(ctx, "ss", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(len(rec.History), Equals, 0)
	c.Check(rec.DynamicBps, Equals, uint64(1))
	_, err = k.GetDynamicFeeAccumulator(ctx, "ss", pairA, pairB)
	c.Assert(err, IsNil)
}

// TestEndBlockGateSealsSummedRolledEpochs verifies the rolled epochs are
// merged, not dropped: three thin epochs that individually fail the gate seal
// as one history entry carrying the summed fees, volume, pool volume and swap
// count.
func (s *DynamicFeeMgrSuite) TestEndBlockGateSealsSummedRolledEpochs(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeMinSwaps", 3)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "ss", 1)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	volume, fees := cosmos.NewUint(1_000_000), cosmos.NewUint(10_000)
	poolVolume := cosmos.NewUint(5_000_000)
	for i, height := range []int64{20, 30, 40} {
		creditOneSwap(ctx, dfm, "ss", volume, fees)
		dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), poolVolume)
		ctx = ctx.WithBlockHeight(height)
		c.Assert(dfm.EndBlock(ctx), IsNil)

		rec, err := k.GetDynamicFeeRecord(ctx, "ss", pairA, pairB)
		c.Assert(err, IsNil)
		if i < 2 {
			c.Check(len(rec.History), Equals, 0, Commentf("epoch %d is still below the gate", i+1))
		}
	}

	rec, err := k.GetDynamicFeeRecord(ctx, "ss", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(len(rec.History), Equals, 1, Commentf("three rolled epochs seal as one entry"))
	h := rec.History[0]
	c.Check(h.SwapCount, Equals, uint64(3))
	c.Check(h.VolumeTor.Equal(volume.MulUint64(3)), Equals, true)
	c.Check(h.FeesTor.Equal(fees.MulUint64(3)), Equals, true)
	c.Check(h.PoolVolumeTor.Equal(poolVolume.MulUint64(3)), Equals, true)
	c.Check(rec.DynamicBps, Equals, uint64(100), Commentf("first sealed epoch sends the search to the ceiling"))

	// Sealing consumes the accumulator; the next epoch starts from zero.
	acc, err := k.GetDynamicFeeAccumulator(ctx, "ss", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(acc.SwapCount, Equals, uint64(0))
	c.Check(acc.VolumeTor.IsZero(), Equals, true)
}

// TestCreditDynamicFeeForLegsStreamDedupe pins that a streaming swap counts
// once against the gate, not once per sub-swap — otherwise a single 30-interval
// stream would satisfy MinSwaps=30 on its own.
func (s *DynamicFeeMgrSuite) TestCreditDynamicFeeForLegsStreamDedupe(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	registerThorname(ctx, k, "alice", 1)
	setupTorAnchorPool(c, ctx, k, 41_000_000)

	affiliates := []string{"alice"}
	affiliateBps := []cosmos.Uint{cosmos.NewUint(100)}
	mkEvents := func() []*EventSwap {
		return []*EventSwap{{
			Pool:               common.BTCAsset,
			LiquidityFeeInRune: cosmos.NewUint(1 * common.One),
			EmitAsset:          common.NewCoin(common.BTCAsset, cosmos.NewUint(1*common.One)),
			InTx: common.NewTx(
				GetRandomTxHash(), GetRandomRUNEAddress(), GetRandomBTCAddress(),
				common.Coins{common.NewCoin(common.RuneAsset(), cosmos.NewUint(10*common.One))},
				common.Gas{}, "",
			),
		}}
	}

	// One stream: first sub-swap counts, the following three do not.
	creditDynamicFeeForLegs(ctx, mgr, mkEvents(), affiliates, affiliateBps, true)
	for i := 0; i < 3; i++ {
		creditDynamicFeeForLegs(ctx, mgr, mkEvents(), affiliates, affiliateBps, false)
	}

	pairA, pairB, _ := dynamicFeePairKey(common.RuneAsset(), common.BTCAsset)
	acc, err := k.GetDynamicFeeAccumulator(ctx, "alice", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(acc.SwapCount, Equals, uint64(1), Commentf("a stream is one user swap"))
	// Volume and fees still accumulate on every sub-swap.
	c.Check(acc.VolumeTor.Equal(cosmos.NewUint(uint64(10*common.One)*41_000_000/common.One).MulUint64(4)), Equals, true)
	c.Check(acc.PoolVolumeTor.IsZero(), Equals, true,
		Commentf("pool volume lives in its own pair-scoped row until the seal folds it in"))

	poolAcc, err := k.GetDynamicFeePoolAccumulator(ctx, pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(poolAcc.VolumeTor.Equal(acc.VolumeTor), Equals, true,
		Commentf("every sub-swap credits the pool denominator"))
}

// --- Normalized signal (Change 2) ---

// TestDecideNextBpsNormalizesPoolVolume is the point of the change: with the
// affiliate's own fees flat but the pool's volume doubling, the affiliate is
// losing share, and the controller must read that as a fall. The old raw-fee
// rule saw no change at all and would have held.
func (s *DynamicFeeMgrSuite) TestDecideNextBpsNormalizesPoolVolume(c *C) {
	tenK := cosmos.NewUint(10000)
	deadband := cosmos.NewUint(500) // 5%
	pool := types.SignalScale

	fees := cosmos.NewUint(100)
	rec := types.DynamicFeeRecord{History: []types.DynamicFeeEpochRecord{
		{FeesTor: fees, BpsAtClose: 3, PoolVolumeTor: pool},
		{FeesTor: fees, BpsAtClose: 4, PoolVolumeTor: pool}, // moved up here
		{FeesTor: fees, BpsAtClose: 4, PoolVolumeTor: pool.MulUint64(2)},
	}}
	reason, newBps, _, _, _ := decideNextBps(rec, 4, 1, 20, 1, 3, deadband, tenK)
	c.Check(reason, Equals, "reverse_down",
		Commentf("flat fees against double the pool volume is a 50%% fall in signal"))
	c.Check(newBps, Equals, uint64(3))

	// Control: hold the pool volume flat and the same history is a hold.
	rec.History[2].PoolVolumeTor = pool
	reason, _, _, _, _ = decideNextBps(rec, 4, 1, 20, 1, 3, deadband, tenK)
	c.Check(reason, Equals, "hold")
}

// TestEndBlockDropsPreUpgradeHistory pins the migration: history written
// before pool volume was tracked carries raw fee levels in incompatible
// units, so it is discarded and the record re-enters cold start from its
// current bps — two forced probes, then a real gradient.
func (s *DynamicFeeMgrSuite) TestEndBlockDropsPreUpgradeHistory(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "ss", 1)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	// Pre-upgrade history: no PoolVolumeTor. bps sits at 8, well off the floor.
	c.Assert(k.SetDynamicFeeRecord(ctx, types.DynamicFeeRecord{
		Thorname: "ss", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 8,
		SearchDone: true,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 9, FeesTor: cosmos.NewUint(100), BpsAtClose: 7},
			{Epoch: 10, FeesTor: cosmos.NewUint(100), BpsAtClose: 8},
			{Epoch: 11, FeesTor: cosmos.NewUint(500), BpsAtClose: 8},
		},
	}), IsNil)

	bps := []uint64{9, 10}
	for i, height := range []int64{20, 30} {
		creditOneSwap(ctx, dfm, "ss", cosmos.NewUint(1_000_000), cosmos.NewUint(10_000))
		dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), types.SignalScale)
		ctx = ctx.WithBlockHeight(height)
		c.Assert(dfm.EndBlock(ctx), IsNil)

		rec, err := k.GetDynamicFeeRecord(ctx, "ss", pairA, pairB)
		c.Assert(err, IsNil)
		c.Check(len(rec.History), Equals, i+1,
			Commentf("pre-upgrade entries are dropped, not kept alongside"))
		c.Check(rec.DynamicBps, Equals, bps[i],
			Commentf("cold start probes up from the record's current bps, not from the floor"))
	}
}

// TestEndBlockDisabledClearsPoolAccumulators pins that disabling the feature
// wipes the pair-scoped rows too — otherwise a re-enable folds pre-disable
// pool volume into the first epoch and distorts the denominator.
func (s *DynamicFeeMgrSuite) TestEndBlockDisabledClearsPoolAccumulators(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "ss", 1)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	creditOneSwap(ctx, dfm, "ss", cosmos.NewUint(1_000_000), cosmos.NewUint(10_000))
	dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(5_000_000))
	poolAcc, err := k.GetDynamicFeePoolAccumulator(ctx, pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(poolAcc.VolumeTor.IsZero(), Equals, false)

	k.SetMimir(ctx, "L1DynamicFeeEnabled", 0)
	ctx = ctx.WithBlockHeight(11) // not an epoch boundary; the disabled branch still clears
	c.Assert(dfm.EndBlock(ctx), IsNil)

	poolAcc, err = k.GetDynamicFeePoolAccumulator(ctx, pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(poolAcc.VolumeTor.IsZero(), Equals, true)
}
