package types

import (
	"flag"
	"testing"

	"cosmossdk.io/math"
	. "gopkg.in/check.v1"
)

// TestDynamicFeeTypes runs only DynamicFeeTypesSuite. The suite is not
// registered with Suite: TestingT runs every registered suite, which would run
// the whole package a second time next to TestPackage.
func TestDynamicFeeTypes(t *testing.T) {
	flagValue := func(name string) string { return flag.Lookup(name).Value.String() }
	result := Run(&DynamicFeeTypesSuite{}, &RunConf{
		Filter:  flagValue("check.f"),
		Verbose: flagValue("check.v") == "true",
		Stream:  flagValue("check.vv") == "true",
	})
	println(result.String())
	if !result.Passed() {
		t.Fail()
	}
}

type DynamicFeeTypesSuite struct{}

func (s *DynamicFeeTypesSuite) TestNormalizePairKey(c *C) {
	a, b := NormalizePairAssets("ETH.ETH", "BTC.BTC")
	c.Check(a, Equals, "BTC.BTC")
	c.Check(b, Equals, "ETH.ETH")

	a, b = NormalizePairAssets("AVAX.AVAX", "ETH.ETH")
	c.Check(a, Equals, "AVAX.AVAX")
	c.Check(b, Equals, "ETH.ETH")

	a, b = NormalizePairAssets("THOR.RUNE", "BTC.BTC")
	c.Check(a, Equals, "BTC.BTC")
	c.Check(b, Equals, "THOR.RUNE")
}

func (s *DynamicFeeTypesSuite) TestDynamicFeeRecordKey(c *C) {
	rec := DynamicFeeRecord{
		Thorname:   "myname",
		PairAssetA: "BTC.BTC",
		PairAssetB: "ETH.ETH",
	}
	c.Check(rec.Key(), Equals, "MYNAME|BTC.BTC|ETH.ETH")
}

func (s *DynamicFeeTypesSuite) TestDynamicFeeAccumulatorKey(c *C) {
	acc := DynamicFeeAccumulator{
		Thorname:   "myname",
		PairAssetA: "BTC.BTC",
		PairAssetB: "ETH.ETH",
	}
	c.Check(acc.Key(), Equals, "MYNAME|BTC.BTC|ETH.ETH")
}

func (s *DynamicFeeTypesSuite) TestAppendAndCapHistory(c *C) {
	rec := DynamicFeeRecord{
		Thorname:   "test",
		PairAssetA: "BTC.BTC",
		PairAssetB: "ETH.ETH",
	}
	for i := uint64(0); i < 31; i++ {
		rec.AppendHistory(DynamicFeeEpochRecord{Epoch: i})
	}
	c.Check(len(rec.History), Equals, 30)
	c.Check(rec.History[0].Epoch, Equals, uint64(1))
	c.Check(rec.History[29].Epoch, Equals, uint64(30))
}

func (s *DynamicFeeTypesSuite) TestDynamicFeeRecordLatestHistory(c *C) {
	rec := DynamicFeeRecord{}
	_, ok := rec.LatestHistory()
	c.Check(ok, Equals, false)

	rec.AppendHistory(DynamicFeeEpochRecord{Epoch: 1, BpsAtClose: 10})
	rec.AppendHistory(DynamicFeeEpochRecord{Epoch: 2, BpsAtClose: 20})
	latest, ok := rec.LatestHistory()
	c.Check(ok, Equals, true)
	c.Check(latest.Epoch, Equals, uint64(2))
	c.Check(latest.BpsAtClose, Equals, uint64(20))
}

func (s *DynamicFeeTypesSuite) TestNewDynamicFeeAccumulator(c *C) {
	acc := NewDynamicFeeAccumulator("myname", "BTC.BTC", "ETH.ETH")
	c.Check(acc.Thorname, Equals, "myname")
	c.Check(acc.PairAssetA, Equals, "BTC.BTC")
	c.Check(acc.PairAssetB, Equals, "ETH.ETH")
	c.Check(acc.VolumeTor.Equal(math.ZeroUint()), Equals, true)
	c.Check(acc.FeesTor.Equal(math.ZeroUint()), Equals, true)
}

func (s *DynamicFeeTypesSuite) TestNormalizePairAssetsEqual(c *C) {
	a, b := NormalizePairAssets("BTC.BTC", "BTC.BTC")
	c.Check(a, Equals, "BTC.BTC")
	c.Check(b, Equals, "BTC.BTC")
}

func (s *DynamicFeeTypesSuite) TestDynamicFeeStates(c *C) {
	for state, want := range map[int64][2]bool{
		-1: {false, false}, 0: {false, false},
		DynamicFeeStatePerPair:        {true, false},
		DynamicFeeStateGlobal:         {true, true},
		DynamicFeeStatePerPairMonitor: {true, false},
		DynamicFeeStateGlobalMonitor:  {true, true},
		5:                             {false, false},
	} {
		c.Check(IsValidDynamicFeeState(state), Equals, want[0], Commentf("state %d", state))
		c.Check(IsGlobalDynamicFeeState(state), Equals, want[1], Commentf("state %d", state))
	}
}

func (s *DynamicFeeTypesSuite) TestNormalizedSignal(c *C) {
	e := DynamicFeeEpochRecord{FeesTor: math.NewUint(5)}
	_, ok := e.NormalizedSignal(0)
	c.Check(ok, Equals, false, Commentf("nil pool volume: entry predates pool tracking"))
	e.PoolVolumeTor = math.ZeroUint()
	_, ok = e.NormalizedSignal(0)
	c.Check(ok, Equals, false)
	e.PoolVolumeTor = math.NewUint(1_000)
	signal, ok := e.NormalizedSignal(0)
	c.Check(ok, Equals, true, Commentf("nil own volume reads as zero"))
	c.Check(signal.Equal(math.NewUint(5_000_000_000)), Equals, true, Commentf("5/1000 scaled by 1e12"))

	// The thorname's own volume is excluded: 5 / (1000 - 750).
	e.OwnPoolVolumeTor = math.NewUint(750)
	signal, ok = e.NormalizedSignal(1000)
	c.Check(ok, Equals, true)
	c.Check(signal.Equal(math.NewUint(20_000_000_000)), Equals, true)

	// Other flow is 25% of the pool: below a 30% minimum, at a 25% minimum.
	_, ok = e.NormalizedSignal(3000)
	c.Check(ok, Equals, false, Commentf("too little outside flow to normalize against"))
	_, ok = e.NormalizedSignal(2500)
	c.Check(ok, Equals, true, Commentf("exactly at the minimum counts"))

	e.OwnPoolVolumeTor = math.NewUint(1_000)
	_, ok = e.NormalizedSignal(0)
	c.Check(ok, Equals, false, Commentf("the thorname is the whole pool"))
}

func (s *DynamicFeeTypesSuite) TestProbeAddAndPairs(c *C) {
	p := DynamicFeeProbe{Bps: 4}
	p.Add(math.NewUint(3))
	p.Add(math.NewUint(5))
	c.Check(p.Epochs, Equals, uint64(2))
	c.Check(p.SignalSum.Equal(math.NewUint(8)), Equals, true)
	c.Check(p.Mean().Equal(math.NewUint(4)), Equals, true)

	acc := NewDynamicFeeAccumulator("t", "*", "*")
	c.Check(acc.OwnPoolVolumeTor.IsZero(), Equals, true)
	acc.AddPair("ETH.ETH", "THOR.RUNE")
	acc.AddPair("BTC.BTC", "THOR.RUNE")
	acc.AddPair("ETH.ETH", "THOR.RUNE")
	c.Check(acc.Pairs, DeepEquals, []string{"BTC.BTC|THOR.RUNE", "ETH.ETH|THOR.RUNE"}, Commentf("sorted, unique"))
}

func (s *DynamicFeeTypesSuite) TestProbeMean(c *C) {
	c.Check((&DynamicFeeProbe{Bps: 3}).Mean().IsZero(), Equals, true, Commentf("no epochs, nil sum"))
	c.Check((&DynamicFeeProbe{SignalSum: math.NewUint(9)}).Mean().IsZero(), Equals, true, Commentf("no epochs"))
	c.Check((&DynamicFeeProbe{SignalSum: math.NewUint(9), Epochs: 2}).Mean().Equal(math.NewUint(4)), Equals, true)
}

// TestNormalizedSignalDoesNotSaturate: when a dominant thorname pays more in
// fees than other swappers trade, the signal must still tell revenues apart.
// A share capped at SignalScale would read both of these as equal.
func (s *DynamicFeeTypesSuite) TestNormalizedSignalDoesNotSaturate(c *C) {
	low := DynamicFeeEpochRecord{FeesTor: math.NewUint(120), PoolVolumeTor: math.NewUint(1000), OwnPoolVolumeTor: math.NewUint(900)}
	high := low
	high.FeesTor = math.NewUint(180)
	lowSignal, ok := low.NormalizedSignal(1000)
	c.Assert(ok, Equals, true)
	highSignal, ok := high.NormalizedSignal(1000)
	c.Assert(ok, Equals, true)
	c.Check(lowSignal.Equal(SignalScale.MulUint64(12).QuoUint64(10)), Equals, true, Commentf("120/100 of scale, got %s", lowSignal))
	c.Check(highSignal.GT(lowSignal), Equals, true)
}
