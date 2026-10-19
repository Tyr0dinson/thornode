package thorchain

import (
	"math"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// probesT builds completed one-epoch probes from bps → mean signal pairs.
func probesT(pairs ...uint64) []types.DynamicFeeProbe {
	var out []types.DynamicFeeProbe
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, types.DynamicFeeProbe{Bps: pairs[i], SignalSum: cosmos.NewUint(pairs[i+1]), Epochs: 1})
	}
	return out
}

func (s *DynamicFeeMgrSuite) TestNextSearchBps(c *C) {
	tenK := cosmos.NewUint(10_000)
	zero := cosmos.ZeroUint()
	for _, tc := range []struct {
		name       string
		probes     []types.DynamicFeeProbe
		floor, cei uint64
		deadband   cosmos.Uint
		best, next uint64
		done       bool
	}{
		{name: "floor measured → ceiling", probes: probesT(1, 100), floor: 1, cei: 20, deadband: zero, best: 1, next: 20},
		{name: "floor best → midpoint", probes: probesT(1, 100, 20, 50), floor: 1, cei: 20, deadband: zero, best: 1, next: 10},
		{name: "ceiling best → midpoint", probes: probesT(1, 100, 20, 150), floor: 1, cei: 20, deadband: zero, best: 20, next: 10},
		{name: "middle best → larger gap", probes: probesT(1, 100, 10, 200, 20, 150), floor: 1, cei: 20, deadband: zero, best: 10, next: 15},
		{name: "equal gaps → lower side", probes: probesT(10, 100, 15, 200, 20, 150), floor: 1, cei: 20, deadband: zero, best: 15, next: 12},
		{name: "neighbours tested → done", probes: probesT(9, 1, 10, 2, 11, 1), floor: 9, cei: 11, deadband: zero, best: 10, done: true},
		{name: "floor best with tested neighbour → done", probes: probesT(1, 5, 2, 3), floor: 1, cei: 20, deadband: zero, best: 1, done: true},
		{name: "start mid-range → ceiling first", probes: probesT(4, 100), floor: 1, cei: 20, deadband: zero, best: 4, next: 20},
		{name: "start mid-range, ceiling worse → wider side", probes: probesT(4, 100, 20, 10), floor: 1, cei: 20, deadband: zero, best: 4, next: 12},
		{name: "start mid-range, upper side done → floor", probes: probesT(4, 100, 5, 10), floor: 1, cei: 5, deadband: zero, best: 4, next: 1},
		{name: "near-tie keeps lower bps", probes: probesT(1, 100, 20, 105), floor: 1, cei: 20, deadband: cosmos.NewUint(1000), best: 1, next: 10},
		{name: "clear win moves up", probes: probesT(1, 100, 20, 111), floor: 1, cei: 20, deadband: cosmos.NewUint(1000), best: 20, next: 10},
		{name: "range moved away → restart at floor", probes: probesT(30, 100), floor: 1, cei: 20, deadband: zero, best: 1, next: 1},
	} {
		best, next, done := nextSearchBps(tc.probes, tc.floor, tc.cei, tc.deadband, tenK)
		c.Check(best, Equals, tc.best, Commentf(tc.name))
		c.Check(done, Equals, tc.done, Commentf(tc.name))
		if !tc.done {
			c.Check(next, Equals, tc.next, Commentf(tc.name))
		}
	}
}

// TestEndBlockSearchConvergesOnUnimodalCurve drives a fresh record through the
// whole search against revenue f(b) = b*(30-b), which peaks at 15 on [1, 20].
// It pins the probe order (floor, ceiling, midpoint, then halving around the
// best), the hand-off to the +/-step controller, and that the search costs a
// handful of epochs rather than one per bps value.
func (s *DynamicFeeMgrSuite) TestEndBlockSearchConvergesOnUnimodalCurve(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeCeilingBPS", 20)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	var tested []uint64
	height := int64(10)
	for epoch := 0; epoch < 30; epoch++ {
		bps := dfm.GetDynamicBps(ctx, []string{"tn"}, []cosmos.Uint{cosmos.NewUint(100)}, common.BTCAsset, common.RuneAsset())
		tested = append(tested, bps)
		creditOneSwap(ctx, dfm, "tn", cosmos.NewUint(1_000_000), cosmos.NewUint(bps*(30-bps)))
		height += 10
		ctx = ctx.WithBlockHeight(height)
		c.Assert(endBlockT(ctx, k, dfm), IsNil)

		rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
		c.Assert(err, IsNil)
		if rec.SearchDone {
			c.Check(rec.DynamicBps, Equals, uint64(15))
			c.Check(rec.History, HasLen, 0, Commentf("the +/-step controller starts fresh"))
			c.Check(rec.Probes, HasLen, len(tested), Commentf("probes stay visible after convergence"))
			break
		}
	}
	c.Check(tested, DeepEquals, []uint64{1, 20, 10, 15, 12, 17, 13, 14, 16})

	var converged bool
	for _, evt := range ctx.EventManager().Events() {
		if evt.Type != types.DynamicL1FeeUpdateEventType {
			continue
		}
		for _, attr := range evt.Attributes {
			if attr.Key == "reason" && attr.Value == "search_converged" {
				converged = true
			}
		}
	}
	c.Check(converged, Equals, true, Commentf("convergence emits an event even though bps may not change"))
}

// TestEndBlockSearchProbeEpochs pins that L1DynamicFeeProbeEpochs holds each
// tested value for that many sealed epochs before moving on.
func (s *DynamicFeeMgrSuite) TestEndBlockSearchProbeEpochs(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeProbeEpochs", 2)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	for i, want := range []uint64{1, 100} {
		creditOneSwap(ctx, dfm, "tn", cosmos.NewUint(1_000_000), cosmos.NewUint(100))
		ctx = ctx.WithBlockHeight(int64(10 * (i + 2)))
		c.Assert(endBlockT(ctx, k, dfm), IsNil)
		rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
		c.Assert(err, IsNil)
		c.Check(rec.DynamicBps, Equals, want, Commentf("epoch %d", i+1))
		c.Assert(rec.Probes, HasLen, 1)
		c.Check(rec.Probes[0].Epochs, Equals, uint64(i+1))
	}
}

// TestEndBlockSearchStartsFromCurrentBps covers records that predate the
// search (search_done unset): the search measures their current bps first
// instead of dropping them back to the floor.
func (s *DynamicFeeMgrSuite) TestEndBlockSearchStartsFromCurrentBps(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeCeilingBPS", 20)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(k.SetDynamicFeeRecord(ctx, types.DynamicFeeRecord{
		Thorname: "tn", PairAssetA: pairA, PairAssetB: pairB,
		DynamicBps: 4,
		History: []types.DynamicFeeEpochRecord{
			{Epoch: 1, FeesTor: cosmos.NewUint(100), VolumeTor: cosmos.NewUint(1000), BpsAtClose: 3},
		},
	}), IsNil)

	creditOneSwap(ctx, dfm, "tn", cosmos.NewUint(1_000_000), cosmos.NewUint(100))
	ctx = ctx.WithBlockHeight(20)
	c.Assert(endBlockT(ctx, k, dfm), IsNil)

	rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.Probes, HasLen, 1)
	c.Check(rec.Probes[0].Bps, Equals, uint64(4))
	c.Check(rec.DynamicBps, Equals, uint64(20), Commentf("the larger untested side is above 4"))
}

// TestGlobalModeSharesOneRecordAcrossPairs pins mode 2: every L1 pair credits
// and reads the thorname's single "*|*" record, and nothing is kept per pair.
func (s *DynamicFeeMgrSuite) TestGlobalModeSharesOneRecordAcrossPairs(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "glob", types.DynamicFeeStateGlobal)
	aff, affBps := []string{"glob"}, []cosmos.Uint{cosmos.NewUint(100)}
	g := types.DynamicFeeGlobalPair

	dfm.CreditSwap(ctx, aff, affBps, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(300), cosmos.NewUint(3), true)
	dfm.CreditSwap(ctx, aff, affBps, common.RuneAsset(), common.ETHAsset, cosmos.NewUint(200), cosmos.NewUint(2), true)

	acc, err := k.GetDynamicFeeAccumulator(ctx, "glob", g, g)
	c.Assert(err, IsNil)
	c.Check(acc.VolumeTor.Equal(cosmos.NewUint(500)), Equals, true)
	c.Check(acc.FeesTor.Equal(cosmos.NewUint(5)), Equals, true)
	c.Check(acc.SwapCount, Equals, uint64(2))
	btcA, btcB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	perPair, err := k.GetDynamicFeeAccumulator(ctx, "glob", btcA, btcB)
	c.Assert(err, IsNil)
	c.Check(perPair.VolumeTor.IsZero(), Equals, true, Commentf("global mode keeps no per-pair state"))

	rec, err := k.GetDynamicFeeRecord(ctx, "glob", g, g)
	c.Assert(err, IsNil)
	rec.DynamicBps = 7
	c.Assert(k.SetDynamicFeeRecord(ctx, rec), IsNil)
	for _, target := range []common.Asset{common.BTCAsset, common.ETHAsset} {
		c.Check(dfm.GetDynamicBps(ctx, aff, affBps, common.RuneAsset(), target), Equals, uint64(7), Commentf("%s", target))
	}

	// Global monitor (4) accumulates into the same record but never charges.
	registerThorname(ctx, k, "globmon", types.DynamicFeeStateGlobalMonitor)
	monAff := []string{"globmon"}
	dfm.CreditSwap(ctx, monAff, affBps, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(100), cosmos.NewUint(1), true)
	monAcc, err := k.GetDynamicFeeAccumulator(ctx, "globmon", g, g)
	c.Assert(err, IsNil)
	c.Check(monAcc.VolumeTor.Equal(cosmos.NewUint(100)), Equals, true)
	c.Check(dfm.GetDynamicBps(ctx, monAff, affBps, common.BTCAsset, common.RuneAsset()), Equals, uint64(0))
}

// TestGlobalModeSealUsesTradedPairsPoolVolume pins the global denominator:
// the epoch's volume on the pairs the thorname traded, less its own volume.
// Volume on a pair it never touched (LTC here) must not move its signal.
func (s *DynamicFeeMgrSuite) TestGlobalModeSealUsesTradedPairsPoolVolume(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "glob", types.DynamicFeeStateGlobal)
	g := types.DynamicFeeGlobalPair
	aff, affBps := []string{"glob"}, []cosmos.Uint{cosmos.NewUint(100)}

	dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(3_000))
	dfm.CreditPoolVolume(ctx, common.ETHAsset, common.RuneAsset(), cosmos.NewUint(2_000))
	dfm.CreditPoolVolume(ctx, common.LTCAsset, common.RuneAsset(), cosmos.NewUint(10_000))
	dfm.CreditSwap(ctx, aff, affBps, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(1_000), cosmos.NewUint(10), true)
	dfm.CreditSwap(ctx, aff, affBps, common.RuneAsset(), common.ETHAsset, cosmos.NewUint(500), cosmos.NewUint(5), true)

	acc, err := k.GetDynamicFeeAccumulator(ctx, "glob", g, g)
	c.Assert(err, IsNil)
	btcA, btcB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	ethA, ethB, _ := dynamicFeePairKey(common.ETHAsset, common.RuneAsset())
	c.Check(acc.Pairs, DeepEquals, []string{btcA + "|" + btcB, ethA + "|" + ethB})
	c.Check(acc.OwnPoolVolumeTor.Equal(cosmos.NewUint(1_500)), Equals, true)

	resp, err := queryServer{mgr: mgr}.queryDynamicL1FeesCurrent(ctx, &types.QueryDynamicL1FeesCurrentRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.Entries, HasLen, 1)
	c.Check(resp.Entries[0].Pair, Equals, "*|*")
	c.Check(resp.Entries[0].PoolVolumeTor, Equals, "5000", Commentf("BTC and ETH only, not LTC"))
	c.Check(resp.Entries[0].OwnPoolVolumeTor, Equals, "1500")

	ctx = ctx.WithBlockHeight(20)
	c.Assert(dfm.EndBlock(ctx), IsNil)

	rec, err := k.GetDynamicFeeRecord(ctx, "glob", g, g)
	c.Assert(err, IsNil)
	c.Assert(rec.History, HasLen, 1)
	c.Check(rec.History[0].PoolVolumeTor.Equal(cosmos.NewUint(5_000)), Equals, true)
	c.Check(rec.History[0].OwnPoolVolumeTor.Equal(cosmos.NewUint(1_500)), Equals, true)
	c.Assert(rec.Probes, HasLen, 1)
	// 15 fees over 3,500 of other swappers' volume, scaled by 1e12 and
	// rounded half up.
	c.Check(rec.Probes[0].SignalSum.Equal(cosmos.NewUint(4_285_714_286)), Equals, true)
}

// TestColdStartAtCeilingProbesDown pins that a record which converged at (or
// was clamped to) the ceiling still explores: a cold start that cannot probe
// up probes down, so the +/-step controller gets a bps change to evaluate
// instead of cold-starting in place forever.
func (s *DynamicFeeMgrSuite) TestColdStartAtCeilingProbesDown(c *C) {
	tenK, zero := cosmos.NewUint(10_000), cosmos.ZeroUint()
	at := func(bps uint64) types.DynamicFeeRecord {
		return types.DynamicFeeRecord{History: []types.DynamicFeeEpochRecord{{BpsAtClose: bps}}}
	}
	for _, tc := range []struct {
		name                        string
		old, floor, ceiling, expect uint64
	}{
		{name: "below ceiling probes up", old: 5, floor: 1, ceiling: 8, expect: 6},
		{name: "at ceiling probes down", old: 8, floor: 1, ceiling: 8, expect: 7},
		{name: "above a lowered ceiling clamps to it", old: 20, floor: 1, ceiling: 8, expect: 8},
		{name: "floor == ceiling cannot move", old: 4, floor: 4, ceiling: 4, expect: 4},
	} {
		reason, bps, _, _, _ := decideNextBps(at(tc.old), tc.old, tc.floor, tc.ceiling, 1, 3, zero, tenK, 0)
		c.Check(reason, Equals, "cold_start_probe", Commentf(tc.name))
		c.Check(bps, Equals, tc.expect, Commentf(tc.name))
	}

	// End to end: converged at the ceiling, revenue falling every epoch.
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeCeilingBPS", 8)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(k.SetDynamicFeeRecord(ctx, types.DynamicFeeRecord{
		Thorname: "tn", PairAssetA: pairA, PairAssetB: pairB, DynamicBps: 8, SearchDone: true,
	}), IsNil)
	var seen []uint64
	for i := 0; i < 4; i++ {
		creditOneSwap(ctx, dfm, "tn", cosmos.NewUint(1_000_000), cosmos.NewUint(uint64(100-10*i)))
		ctx = ctx.WithBlockHeight(int64(20 + 10*i))
		c.Assert(endBlockT(ctx, k, dfm), IsNil)
		rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
		c.Assert(err, IsNil)
		seen = append(seen, rec.DynamicBps)
	}
	// 8 -> 7: cold start, probing down because 8 is the ceiling. 7 -> 8: the
	// second cold start (the 8->7 change is the newest entry, so it is not
	// evaluated yet) probes up. Then the gradient rule runs: revenue fell after
	// the down move (reverse up, clamped at 8), then fell after the up move
	// (reverse down). Before the fix every value was 8.
	c.Check(seen, DeepEquals, []uint64{7, 8, 8, 7})
}

// TestGlobalModeMultiLegSwapCountsOnce pins that a double swap (two RUNE legs)
// counts as one swap and one swap's volume on a global record, while both legs'
// fees accrue. Per-pair records still count each leg on its own pair.
func (s *DynamicFeeMgrSuite) TestGlobalModeMultiLegSwapCountsOnce(c *C) {
	legs := func() []*EventSwap {
		mk := func(in, out common.Asset, inAmt uint64) *EventSwap {
			return &EventSwap{
				LiquidityFeeInRune: cosmos.NewUint(common.One),
				EmitAsset:          common.NewCoin(out, cosmos.NewUint(common.One)),
				InTx: common.NewTx(GetRandomTxHash(), GetRandomRUNEAddress(), GetRandomBTCAddress(),
					common.Coins{common.NewCoin(in, cosmos.NewUint(inAmt))}, common.Gas{}, ""),
			}
		}
		return []*EventSwap{
			mk(common.BTCAsset, common.RuneAsset(), common.One),    // BTC -> RUNE
			mk(common.RuneAsset(), common.ETHAsset, 10*common.One), // RUNE -> ETH
		}
	}
	affBps := []cosmos.Uint{cosmos.NewUint(100)}

	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	setupTorAnchorPool(c, ctx, k, 41_000_000)
	registerThorname(ctx, k, "glob", types.DynamicFeeStateGlobal)
	registerThorname(ctx, k, "pp", types.DynamicFeeStatePerPair)

	creditDynamicFeeForLegs(ctx, mgr, legs(), []string{"glob"}, affBps, true)
	g := types.DynamicFeeGlobalPair
	acc, err := k.GetDynamicFeeAccumulator(ctx, "glob", g, g)
	c.Assert(err, IsNil)
	c.Check(acc.SwapCount, Equals, uint64(1), Commentf("one user swap"))

	// Compare against the same legs credited per pair: the global record holds
	// the first leg's volume only, and both legs' fees.
	creditDynamicFeeForLegs(ctx, mgr, legs(), []string{"pp"}, affBps, true)
	btcA, btcB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	ethA, ethB, _ := dynamicFeePairKey(common.RuneAsset(), common.ETHAsset)
	btc, err := k.GetDynamicFeeAccumulator(ctx, "pp", btcA, btcB)
	c.Assert(err, IsNil)
	eth, err := k.GetDynamicFeeAccumulator(ctx, "pp", ethA, ethB)
	c.Assert(err, IsNil)
	c.Check(btc.SwapCount, Equals, uint64(1), Commentf("per-pair counts each leg's pair"))
	c.Check(eth.SwapCount, Equals, uint64(1))
	c.Check(acc.VolumeTor.Equal(btc.VolumeTor), Equals, true, Commentf("global volume is the first leg's"))
	c.Check(acc.FeesTor.Equal(btc.FeesTor.Add(eth.FeesTor)), Equals, true, Commentf("global fees are both legs'"))
	c.Check(acc.OwnPoolVolumeTor.Equal(btc.OwnPoolVolumeTor.Add(eth.OwnPoolVolumeTor)), Equals, true,
		Commentf("own pool volume counts every leg, matching the per-leg pool volume it is subtracted from"))
}

// sealWith seals one epoch for "tn" on BTC|RUNE with the given own volume,
// other swappers' volume and fees.
func sealWith(c *C, ctx cosmos.Context, dfm *DynamicFeeMgr, height int64, own, other, fees uint64) cosmos.Context {
	dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(own+other))
	creditOneSwap(ctx, dfm, "tn", cosmos.NewUint(own), cosmos.NewUint(fees))
	ctx = ctx.WithBlockHeight(height)
	c.Assert(dfm.EndBlock(ctx), IsNil)
	return ctx
}

// TestSearchHoldsWithoutOtherFlow: when the thorname is (nearly) the whole
// pool there is no outside reference to normalize against, so an epoch is
// recorded but not counted as a measurement of the fee.
func (s *DynamicFeeMgrSuite) TestSearchHoldsWithoutOtherFlow(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	// Other flow is 5% of the pool, under the 10% default.
	ctx = sealWith(c, ctx, dfm, 20, 950_000, 50_000, 100)
	rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(rec.History, HasLen, 1, Commentf("the epoch is recorded"))
	c.Check(rec.Probes, HasLen, 0, Commentf("but it is not a measurement"))
	c.Check(rec.DynamicBps, Equals, uint64(1))

	// At 20% other flow it counts.
	ctx = sealWith(c, ctx, dfm, 30, 800_000, 200_000, 100)
	rec, err = k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.Probes, HasLen, 1)
	c.Check(rec.DynamicBps, Equals, uint64(100), Commentf("measured at the floor; on to the ceiling"))
}

// TestSearchFindsPeakForDominantAffiliate drives the whole search for an
// affiliate that is 90% of its pair's flow, with demand that falls with the
// fee so revenue peaks at 7 bps. Dividing by the whole pool's volume would
// reward the fee rate itself and settle near 15; dividing by other swappers'
// volume tracks revenue.
func (s *DynamicFeeMgrSuite) TestSearchFindsPeakForDominantAffiliate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeCeilingBPS", 20)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	const other = 1_000_000 // 10% of the pair at 1 bps
	volume := func(b uint64) uint64 { return uint64(9_000_000 * math.Exp(-float64(b-1)/7)) }
	height := int64(10)
	for epoch := 0; epoch < 30; epoch++ {
		b := dfm.GetDynamicBps(ctx, []string{"tn"}, []cosmos.Uint{cosmos.NewUint(100)}, common.BTCAsset, common.RuneAsset())
		v := volume(b)
		height += 10
		ctx = sealWith(c, ctx, dfm, height, v, other, v*b/10_000)
		rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
		c.Assert(err, IsNil)
		if rec.SearchDone {
			c.Check(rec.DynamicBps >= 6 && rec.DynamicBps <= 8, Equals, true, Commentf("settled on %d", rec.DynamicBps))
			return
		}
	}
	c.Fatal("search did not converge")
}

// TestMonitorRecordsHistoryOnly: a monitor thorname's swaps pay the static
// minimum, so its record collects history but never searches or steps.
func (s *DynamicFeeMgrSuite) TestMonitorRecordsHistoryOnly(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPairMonitor)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	for i := int64(0); i < 3; i++ {
		ctx = sealWith(c, ctx, dfm, 20+10*i, 1_000, 1_000_000, 100)
	}
	rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(rec.History, HasLen, 3)
	c.Check(rec.Probes, HasLen, 0)
	c.Check(rec.DynamicBps, Equals, uint64(1))
	c.Check(rec.SearchDone, Equals, false)
	for _, evt := range ctx.EventManager().Events() {
		c.Check(evt.Type, Not(Equals), types.DynamicL1FeeUpdateEventType)
	}

	qs := queryServer{mgr: mgr}
	byName, err := qs.queryDynamicL1FeesByThorname(ctx, &types.QueryDynamicL1FeesByThornameRequest{Thorname: "tn"})
	c.Assert(err, IsNil)
	c.Check(byName.Pairs[0].Phase, Equals, "monitor")
	all, err := qs.queryDynamicL1Fees(ctx, &types.QueryDynamicL1FeesRequest{})
	c.Assert(err, IsNil)
	c.Check(all.Entries[0].Phase, Equals, "monitor")
}

// TestSpanCapSealsBelowGate: a span that never clears the liquidity gate
// rolls until it has run L1DynamicFeeMaxEpochBlocks, then the next boundary
// seals it with what it has, so a fee too high to draw the gate's volume
// still gets measured.
func (s *DynamicFeeMgrSuite) TestSpanCapSealsBelowGate(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeMinSwaps", 5)
	k.SetMimir(ctx, "L1DynamicFeeMaxEpochBlocks", 30)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())

	ctx = ctx.WithBlockHeight(10)
	dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(10_000))
	creditOneSwap(ctx, dfm, "tn", cosmos.NewUint(1_000), cosmos.NewUint(10))
	for _, h := range []int64{20, 30} {
		ctx = ctx.WithBlockHeight(h)
		c.Assert(dfm.EndBlock(ctx), IsNil)
		rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
		c.Assert(err, IsNil)
		c.Check(rec.History, HasLen, 0, Commentf("height %d: below the gate and under the cap, it rolls", h))
	}

	ctx = ctx.WithBlockHeight(40)
	c.Assert(dfm.EndBlock(ctx), IsNil)
	rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.History, HasLen, 1, Commentf("30 blocks reached: sealed below the gate"))
	c.Check(rec.History[0].Blocks, Equals, int64(30))
	c.Check(rec.History[0].SwapCount, Equals, uint64(1))
	c.Check(rec.History[0].Capped, Equals, true, Commentf("sealed by the cap below the gate"))
	c.Check(rec.Probes[0].Capped, Equals, true)
	c.Check(rec.SpanStartHeight, Equals, int64(40), Commentf("the next span starts at the seal"))
	c.Check(rec.Probes, HasLen, 1)
	acc, err := k.GetDynamicFeeAccumulator(ctx, "tn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(acc.SwapCount, Equals, uint64(0), Commentf("the sealed accumulator is consumed"))

	var capped bool
	for _, evt := range ctx.EventManager().Events() {
		for _, attr := range evt.Attributes {
			if evt.Type == types.DynamicL1FeeUpdateEventType && attr.Key == "capped" && attr.Value == "true" {
				capped = true
			}
		}
	}
	c.Check(capped, Equals, true)

	qs := queryServer{mgr: mgr}
	byName, err := qs.queryDynamicL1FeesByThorname(ctx, &types.QueryDynamicL1FeesByThornameRequest{Thorname: "tn"})
	c.Assert(err, IsNil)
	c.Check(byName.Pairs[0].History[0].Blocks, Equals, int64(30))

	creditOneSwap(ctx, dfm, "tn", cosmos.NewUint(1_000), cosmos.NewUint(10))
	ctx = ctx.WithBlockHeight(47)
	current, err := qs.queryDynamicL1FeesCurrent(ctx, &types.QueryDynamicL1FeesCurrentRequest{})
	c.Assert(err, IsNil)
	c.Assert(current.Entries, HasLen, 1)
	c.Check(current.Entries[0].SpanBlocks, Equals, int64(7))
}

// TestSpanCapSealsEmptySpan: a fee so high that nobody swaps still gets
// measured. Once the span reaches the cap with no swaps at all, it is sealed
// empty, which reads as zero revenue and moves the search on. The empty seal
// is not activity, so an idle record still ages towards pruning. Monitor
// records are never sealed empty.
func (s *DynamicFeeMgrSuite) TestSpanCapSealsEmptySpan(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeCeilingBPS", 20)
	k.SetMimir(ctx, "L1DynamicFeeMaxEpochBlocks", 20)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	registerThorname(ctx, k, "mon", types.DynamicFeeStatePerPairMonitor)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	for _, tn := range []string{"tn", "mon"} {
		c.Assert(k.SetDynamicFeeRecord(ctx, types.DynamicFeeRecord{
			Thorname: tn, PairAssetA: pairA, PairAssetB: pairB,
			DynamicBps: 20, LastActiveEpoch: 1, SpanStartHeight: 10,
			Probes: []types.DynamicFeeProbe{{Bps: 1, SignalSum: cosmos.NewUint(500), Epochs: 1}},
		}), IsNil)
	}

	// Other swappers keep trading; "tn" draws no swaps at 20 bps.
	for _, h := range []int64{20, 30} {
		dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(10_000))
		ctx = ctx.WithBlockHeight(h)
		c.Assert(dfm.EndBlock(ctx), IsNil)
	}

	rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Assert(rec.History, HasLen, 1, Commentf("sealed empty at the cap"))
	c.Check(rec.History[0].FeesTor.IsZero(), Equals, true)
	c.Check(rec.History[0].Blocks, Equals, int64(20))
	c.Assert(rec.Probes, HasLen, 2)
	c.Check(rec.Probes[1].Bps, Equals, uint64(20))
	c.Check(rec.Probes[1].Mean().IsZero(), Equals, true, Commentf("zero revenue at 20 bps"))
	c.Check(rec.DynamicBps, Equals, uint64(10), Commentf("1 beat 20, so the search tests the midpoint"))
	c.Check(rec.LastActiveEpoch, Equals, uint64(1), Commentf("an empty seal is not activity"))

	mon, err := k.GetDynamicFeeRecord(ctx, "mon", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(mon.History, HasLen, 0, Commentf("monitor records are never sealed empty"))
}

// TestSpanStartsForRecordsWithoutOne: a record from before span tracking (no
// start height) starts its span at the first boundary instead of being sealed
// by the cap on the spot.
func (s *DynamicFeeMgrSuite) TestSpanStartsForRecordsWithoutOne(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1DynamicFeeMaxEpochBlocks", 20)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "tn", types.DynamicFeeStatePerPair)
	pairA, pairB, _ := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(k.SetDynamicFeeRecord(ctx, types.DynamicFeeRecord{Thorname: "tn", PairAssetA: pairA, PairAssetB: pairB, DynamicBps: 9}), IsNil)

	dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(10_000))
	ctx = ctx.WithBlockHeight(100)
	c.Assert(dfm.EndBlock(ctx), IsNil)
	rec, err := k.GetDynamicFeeRecord(ctx, "tn", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(rec.SpanStartHeight, Equals, int64(100))
	c.Check(rec.History, HasLen, 0)
	c.Check(rec.DynamicBps, Equals, uint64(9))
}

// TestCappedReadingsOnlyLower pins that a span sealed by the time cap below the
// liquidity gate is too thin to raise the fee: in the search it never
// displaces a lower-fee best (though it counts as tested), and in the +/-step
// controller a raise becomes capped_hold and a cold start probes down. Lowering
// the fee on a capped reading is allowed.
func (s *DynamicFeeMgrSuite) TestCappedReadingsOnlyLower(c *C) {
	tenK, zero := cosmos.NewUint(10_000), cosmos.ZeroUint()

	// Search: the capped 20 reads far better than 1 but cannot win.
	probes := []types.DynamicFeeProbe{
		{Bps: 1, SignalSum: cosmos.NewUint(100), Epochs: 1},
		{Bps: 20, SignalSum: cosmos.NewUint(900), Epochs: 1, Capped: true},
	}
	best, next, done := nextSearchBps(probes, 1, 20, zero, tenK)
	c.Check(best, Equals, uint64(1))
	c.Check(done, Equals, false)
	c.Check(next, Equals, uint64(10), Commentf("20 counts as tested: the gap (1, 20) is next"))
	// A capped lower reading that looks better may keep the fee low.
	probes = []types.DynamicFeeProbe{
		{Bps: 4, SignalSum: cosmos.NewUint(900), Epochs: 1, Capped: true},
		{Bps: 8, SignalSum: cosmos.NewUint(100), Epochs: 1},
	}
	best, _, _ = nextSearchBps(probes, 1, 20, zero, tenK)
	c.Check(best, Equals, uint64(4))

	// Tracker.
	hist := func(capped bool, bps []uint64, fees []uint64) types.DynamicFeeRecord {
		rec := types.DynamicFeeRecord{SearchDone: true}
		for i := range bps {
			rec.History = append(rec.History, types.DynamicFeeEpochRecord{BpsAtClose: bps[i], FeesTor: cosmos.NewUint(fees[i]), PoolVolumeTor: types.SignalScale})
		}
		rec.History[len(rec.History)-1].Capped = capped
		return rec
	}
	// Up 5 -> 6 and revenue rose: continue_up normally, held when capped.
	reason, bps, _, _, _ := decideNextBps(hist(false, []uint64{5, 6, 6}, []uint64{100, 200, 200}), 6, 1, 20, 1, 3, zero, tenK, 0)
	c.Check(reason, Equals, "continue_up")
	c.Check(bps, Equals, uint64(7))
	reason, bps, _, _, _ = decideNextBps(hist(true, []uint64{5, 6, 6}, []uint64{100, 200, 200}), 6, 1, 20, 1, 3, zero, tenK, 0)
	c.Check(reason, Equals, "capped_hold")
	c.Check(bps, Equals, uint64(6))
	// Up 5 -> 6 and revenue fell: reverse_down is allowed when capped.
	reason, bps, _, _, _ = decideNextBps(hist(true, []uint64{5, 6, 6}, []uint64{200, 100, 100}), 6, 1, 20, 1, 3, zero, tenK, 0)
	c.Check(reason, Equals, "reverse_down")
	c.Check(bps, Equals, uint64(5))
	// Cold start on a capped reading probes down.
	reason, bps, _, _, _ = decideNextBps(hist(true, []uint64{6}, []uint64{100}), 6, 1, 20, 1, 3, zero, tenK, 0)
	c.Check(reason, Equals, "cold_start_probe")
	c.Check(bps, Equals, uint64(5))
}
