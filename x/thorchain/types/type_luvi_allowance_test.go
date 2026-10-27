package types

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type LuviAllowanceSuite struct{}

var _ = Suite(&LuviAllowanceSuite{})

func luviTestState(r, a, l, s uint64) LuviState {
	return NewLuviState(Pool{
		BalanceRune:  cosmos.NewUint(r),
		BalanceAsset: cosmos.NewUint(a),
		LPUnits:      cosmos.NewUint(l),
	}, cosmos.NewUint(s))
}

func (LuviAllowanceSuite) TestAllowanceTerms(c *C) {
	prev := luviTestState(1e12, 1e10, 1e12, 0)

	// nothing happened: nothing allowed
	c.Check(LuviAllowance(prev, prev, cosmos.ZeroUint(), 0).IsZero(), Equals, true)

	// gas: (g/A)^2, capped at g = A
	gas := LuviAllowance(prev, prev, cosmos.NewUint(1e8), 0)
	c.Check(gas.String(), Equals, "0.000100000000000000")
	c.Check(LuviAllowance(prev, prev, cosmos.NewUint(5e10), 0).String(), Equals, "1.000000000000000000")

	// LP-flow rounding: 1/A + 1/R per flow
	c.Check(LuviAllowance(prev, prev, cosmos.ZeroUint(), 2).String(), Equals, "0.000000000202000000")

	// synth mint of y = 1% of A: y^3 * (1/16 + 2y)
	next := luviTestState(1e12, 1e10, 1e12, 1e8)
	c.Check(LuviAllowance(prev, next, cosmos.ZeroUint(), 0).String(), Equals, "0.000000082500000000")
	// symmetric for a burn, and capped at y = 1
	c.Check(LuviAllowance(next, prev, cosmos.ZeroUint(), 0).String(), Equals, "0.000000082500000000")
	big := luviTestState(1e12, 1e10, 1e12, 5e10)
	c.Check(LuviAllowance(prev, big, cosmos.ZeroUint(), 0).String(), Equals, "2.062500000000000000")

	// an empty previous state has no value to protect
	empty := luviTestState(0, 0, 0, 0)
	c.Check(LuviAllowance(empty, prev, cosmos.NewUint(1), 3).IsZero(), Equals, true)
}

func (LuviAllowanceSuite) TestStepEvents(c *C) {
	events := cosmos.Events{
		cosmos.NewEvent(GasEventType, cosmos.NewAttribute("asset", "ETH.ETH"), cosmos.NewAttribute("asset_amt", "10")),
		cosmos.NewEvent(GasEventType, cosmos.NewAttribute("asset", "ETH.ETH"), cosmos.NewAttribute("asset_amt", "5")),
		cosmos.NewEvent(GasEventType, cosmos.NewAttribute("asset", "ETH.ETH"), cosmos.NewAttribute("asset_amt", "bad")),
		cosmos.NewEvent(SlashEventType, cosmos.NewAttribute("pool", "BTC.BTC"), cosmos.NewAttribute("BTC.BTC", "-7"), cosmos.NewAttribute("THOR.RUNE", "70")),
		cosmos.NewEvent(SlashEventType, cosmos.NewAttribute("pool", "BTC.BTC"), cosmos.NewAttribute("BTC.BTC", "3")),
		cosmos.NewEvent(AddLiquidityEventType, cosmos.NewAttribute("pool", "ETH.ETH"), cosmos.NewAttribute("liquidity_provider_units", "100")),
		cosmos.NewEvent(WithdrawEventType, cosmos.NewAttribute("pool", "ETH.ETH"), cosmos.NewAttribute("liquidity_provider_units", "40")),
		cosmos.NewEvent(WithdrawEventType, cosmos.NewAttribute("pool", "ETH.ETH"), cosmos.NewAttribute("liquidity_provider_units", "x")),
		cosmos.NewEvent(ErrataEventType, cosmos.NewAttribute("asset", "LTC.LTC")),
		cosmos.NewEvent("swap", cosmos.NewAttribute("pool", "ETH.ETH")),
		// synth legs: a mint, a burn, a plain leg and a bad slip
		cosmos.NewEvent("swap", cosmos.NewAttribute("pool", "BTC.BTC"), cosmos.NewAttribute("pool_slip", "99"),
			cosmos.NewAttribute("coin", "100 THOR.RUNE"), cosmos.NewAttribute("emit_asset", "5 BTC/BTC")),
		cosmos.NewEvent("swap", cosmos.NewAttribute("pool", "BTC.BTC"), cosmos.NewAttribute("pool_slip", "0"),
			cosmos.NewAttribute("coin", "5 BTC/BTC"), cosmos.NewAttribute("emit_asset", "90 THOR.RUNE")),
		cosmos.NewEvent("swap", cosmos.NewAttribute("pool", "BTC.BTC"), cosmos.NewAttribute("pool_slip", "40"),
			cosmos.NewAttribute("coin", "5 BTC.BTC"), cosmos.NewAttribute("emit_asset", "90 THOR.RUNE")),
		cosmos.NewEvent("swap", cosmos.NewAttribute("pool", "BTC.BTC"), cosmos.NewAttribute("pool_slip", "x"),
			cosmos.NewAttribute("coin", "100 THOR.RUNE"), cosmos.NewAttribute("emit_asset", "5 BTC/BTC")),
	}
	ev := NewLuviStepEvents(FlattenLuviEvents(events))
	c.Check(ev.AssetPaidOf("ETH.ETH").String(), Equals, "15")
	c.Check(ev.AssetPaidOf("BTC.BTC").String(), Equals, "7")
	c.Check(ev.AssetPaidOf("DOGE.DOGE").IsZero(), Equals, true)
	c.Check(ev.Flows("ETH.ETH"), Equals, 2)
	c.Check(ev.RoundingOps("ETH.ETH"), Equals, 4)
	c.Check(ev.RoundingOps("DOGE.DOGE"), Equals, 1)
	c.Check(ev.Errata["LTC.LTC"], Equals, true)
	c.Check(ev.Errata["ETH.ETH"], Equals, false)
	c.Check(ev.SynthSlips["BTC.BTC"], DeepEquals, []int64{99, 0})
	c.Check(ev.SynthSlips["ETH.ETH"], HasLen, 0)
}

func luviCheckNear(c *C, got cosmos.Dec, want string) {
	w, err := cosmos.NewDecFromStr(want)
	c.Assert(err, IsNil)
	c.Check(got.Sub(w).Abs().LT(cosmos.NewDecWithPrec(1, 15)), Equals, true, Commentf("got %s, want %s", got, want))
}

func (LuviAllowanceSuite) TestSynthSubsidyAllowance(c *C) {
	// no virtual depth (mainnet default) or below 1x: nothing allowed
	c.Check(LuviSynthSubsidyAllowance([]int64{99, 5000}, 10_000).IsZero(), Equals, true)
	c.Check(LuviSynthSubsidyAllowance([]int64{99}, 5_000).IsZero(), Equals, true)
	c.Check(LuviSynthSubsidyAllowance(nil, 20_000).IsZero(), Equals, true)

	// 2x, 99 bps rounded up to 1%: r = 2*0.01/0.99,
	// r^2 * (1 - 1/2) / ((1 + r/2) * (1 + r))
	luviCheckNear(c, LuviSynthSubsidyAllowance([]int64{99}, 20_000), "0.000198019801980198")
	// legs add up, and a 0 bps leg still counts as 1 bps
	luviCheckNear(c, LuviSynthSubsidyAllowance([]int64{99, 0}, 20_000), "0.000198039799980398")
	// a shallower subsidy allows less
	luviCheckNear(c, LuviSynthSubsidyAllowance([]int64{99}, 15_000), "0.000074626865671642")
	// a full-slip leg allows everything; a negative slip is ignored
	c.Check(LuviSynthSubsidyAllowance([]int64{9_999}, 20_000).String(), Equals, "1.000000000000000000")
	c.Check(LuviSynthSubsidyAllowance([]int64{-1}, 20_000).IsZero(), Equals, true)
}

// luviSwapLeg applies swapOne's math to x on input depth inDepth and output
// depth outDepth, both scaled by mult: it returns emit and the liquidity fee
// (output units) and the pool slip in basis points.
func luviSwapLeg(x, inDepth, outDepth cosmos.Uint, mult uint64) (emit, fee cosmos.Uint, bps int64) {
	X := inDepth.MulUint64(mult).QuoUint64(10_000)
	Y := outDepth.MulUint64(mult).QuoUint64(10_000)
	den := x.Add(X).Mul(x.Add(X))
	emit = x.Mul(X).Mul(Y).Quo(den)
	fee = x.Mul(x).Mul(Y).Quo(den)
	bps = int64(x.MulUint64(10_000).Add(x.Add(X).QuoUint64(2)).Quo(x.Add(X)).Uint64())
	return emit, fee, bps
}

// A savers deposit swaps L1 to RUNE and RUNE to the synth in the same pool,
// in one step. At a 2x virtual synth depth the drop in G is beyond the base
// allowance and within the synth subsidy term; at 1x it needs no subsidy.
func (LuviAllowanceSuite) TestSynthSubsidyCoversSaversDeposit(c *C) {
	for _, tc := range []struct {
		mult uint64
		inBp uint64 // deposit, in basis points of the asset depth
	}{{20_000, 10}, {20_000, 100}, {20_000, 1_000}, {30_000, 300}, {10_000, 1_000}} {
		runeD, assetD, synth := cosmos.NewUint(1e12), cosmos.NewUint(1e10), cosmos.NewUint(3e9)
		prev := NewLuviState(Pool{BalanceRune: runeD, BalanceAsset: assetD, LPUnits: cosmos.NewUint(1e12)}, synth)

		// L1 -> RUNE: no virtual depth, the fee leaves the RUNE side
		x := assetD.MulUint64(tc.inBp).QuoUint64(10_000)
		emit, fee, _ := luviSwapLeg(x, assetD, runeD, 10_000)
		assetD = assetD.Add(x)
		runeD = runeD.Sub(emit).Sub(fee)

		// RUNE -> synth at the virtual depth, the fee converted to RUNE
		synthOut, synthFee, slip := luviSwapLeg(emit, runeD, assetD, tc.mult)
		runeD = runeD.Add(emit)
		synth = synth.Add(synthOut)
		runeD = runeD.Sub(runeD.Mul(synthFee).Quo(assetD))

		next := NewLuviState(Pool{BalanceRune: runeD, BalanceAsset: assetD, LPUnits: cosmos.NewUint(1e12)}, synth)
		tracker, err := NewLuviTracker(prev)
		c.Assert(err, IsNil)
		step, err := tracker.Observe(next)
		c.Assert(err, IsNil)
		drop := step.NormalizedDeltaG.Neg()

		base := LuviDefaultTolerance.Add(LuviAllowance(prev, next, cosmos.ZeroUint(), 3))
		subsidy := LuviSynthSubsidyAllowance([]int64{slip}, int64(tc.mult))
		comment := Commentf("mult %d, deposit %d bps: drop %s, base %s, subsidy %s", tc.mult, tc.inBp, drop, base, subsidy)
		c.Check(drop.LTE(base.Add(subsidy)), Equals, true, comment)
		if tc.mult > 10_000 {
			c.Check(drop.GT(base), Equals, true, comment)
		} else {
			c.Check(subsidy.IsZero(), Equals, true, comment)
		}
	}
}

func (LuviAllowanceSuite) TestRestoreTracker(c *C) {
	a := luviTestState(1e12, 1e10, 1e12, 3e9)
	b := luviTestState(1.1e12, 1e10, 1e12, 3e9)
	d := luviTestState(1.2e12, 0.95e10, 1e12, 4e9)

	whole, err := NewLuviTracker(a)
	c.Assert(err, IsNil)
	_, err = whole.Observe(b)
	c.Assert(err, IsNil)
	c.Check(whole.Prev().BalanceRune.String(), Equals, b.BalanceRune.String())

	// resuming from a stored snapshot continues exactly
	resumed, err := RestoreLuviTracker(whole.Prev(), whole.RawC(), whole.RawG())
	c.Assert(err, IsNil)
	_, err = whole.Observe(d)
	c.Assert(err, IsNil)
	_, err = resumed.Observe(d)
	c.Assert(err, IsNil)
	c.Check(resumed.RawG().String(), Equals, whole.RawG().String())
	c.Check(resumed.RawC().String(), Equals, whole.RawC().String())

	// the raw accessors are copies
	g := resumed.RawG()
	g.SetInt64(0)
	c.Check(resumed.RawG().String(), Equals, whole.RawG().String())

	_, err = RestoreLuviTracker(luviTestState(0, 0, 0, 0), g, g)
	c.Check(err, NotNil)
}
