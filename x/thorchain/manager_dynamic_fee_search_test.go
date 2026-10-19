package thorchain

import (
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

// TestGlobalModeSealUsesTotalPoolVolume pins the global denominator: the
// epoch's volume across every L1 pair, not any single pair's.
func (s *DynamicFeeMgrSuite) TestGlobalModeSealUsesTotalPoolVolume(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	dfm := newDynamicFeeMgr(k)
	registerThorname(ctx, k, "glob", types.DynamicFeeStateGlobal)
	g := types.DynamicFeeGlobalPair

	dfm.CreditPoolVolume(ctx, common.BTCAsset, common.RuneAsset(), cosmos.NewUint(3_000))
	dfm.CreditPoolVolume(ctx, common.ETHAsset, common.RuneAsset(), cosmos.NewUint(2_000))
	dfm.CreditSwap(ctx, []string{"glob"}, []cosmos.Uint{cosmos.NewUint(100)},
		common.BTCAsset, common.RuneAsset(), cosmos.NewUint(1_000), cosmos.NewUint(10), true)

	resp, err := queryServer{mgr: mgr}.queryDynamicL1FeesCurrent(ctx, &types.QueryDynamicL1FeesCurrentRequest{})
	c.Assert(err, IsNil)
	c.Assert(resp.Entries, HasLen, 1)
	c.Check(resp.Entries[0].Pair, Equals, "*|*")
	c.Check(resp.Entries[0].PoolVolumeTor, Equals, "5000")

	ctx = ctx.WithBlockHeight(20)
	c.Assert(dfm.EndBlock(ctx), IsNil)

	rec, err := k.GetDynamicFeeRecord(ctx, "glob", g, g)
	c.Assert(err, IsNil)
	c.Assert(rec.History, HasLen, 1)
	c.Check(rec.History[0].PoolVolumeTor.Equal(cosmos.NewUint(5_000)), Equals, true)
	c.Assert(rec.Probes, HasLen, 1)
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
		reason, bps, _, _, _ := decideNextBps(at(tc.old), tc.old, tc.floor, tc.ceiling, 1, 3, zero, tenK)
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
}
