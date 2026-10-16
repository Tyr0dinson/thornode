package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Dynamic fees apply only to single-affiliate memos (ADR-026 "Fee floor
// selection" step 4). These cover the `ns/ss:24/1` free-rider pattern, where
// an unwhitelisted THORName keeps most of the affiliate bps while co-listing a
// whitelisted partner at a token 1 bp to inherit the partner's floor.

const singleAffStaticBps = 10

// setupSingleAffiliateTest enables dynamic fees with a static L1 floor of
// 10 bps and registers ns (registered, not whitelisted), ss and tt (active)
// and mon (monitor). ss, tt and mon get a 3 bps dynamic record for BTC|RUNE.
func setupSingleAffiliateTest(c *C) (cosmos.Context, keeper.Keeper, *DynamicFeeMgr, string, string) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	setDynamicFeeTestMimirs(ctx, k)
	k.SetMimir(ctx, "L1SlipMinBps", singleAffStaticBps)

	registerThorname(ctx, k, "ns", 0)
	registerThorname(ctx, k, "ss", 1)
	registerThorname(ctx, k, "tt", 1)
	registerThorname(ctx, k, "mon", 2)

	pairA, pairB, ok := dynamicFeePairKey(common.BTCAsset, common.RuneAsset())
	c.Assert(ok, Equals, true)
	for _, name := range []string{"ss", "tt", "mon"} {
		c.Assert(k.SetDynamicFeeRecord(ctx, types.DynamicFeeRecord{
			Thorname:   name,
			PairAssetA: pairA,
			PairAssetB: pairB,
			DynamicBps: 3,
			History:    []types.DynamicFeeEpochRecord{{Epoch: 1, FeesTor: cosmos.NewUint(100), BpsAtClose: 3}},
		}), IsNil)
	}
	return ctx, k, newDynamicFeeMgr(k), pairA, pairB
}

func singleAffBps(vals ...uint64) []cosmos.Uint {
	out := make([]cosmos.Uint, len(vals))
	for i, v := range vals {
		out[i] = cosmos.NewUint(v)
	}
	return out
}

func (s *DynamicFeeMgrSuite) TestSingleAffiliateOnly(c *C) {
	thorAddr := GetRandomTHORAddress().String()
	for _, tc := range []struct {
		name       string
		affiliates []string
		bps        []cosmos.Uint
		dynBps     uint64 // GetDynamicBps result; 0 means the static path
		credited   string // thorname whose accumulator is credited; "" for none
	}{
		{"single active", []string{"ss"}, singleAffBps(1), 3, "ss"},
		{"single monitor", []string{"mon"}, singleAffBps(24), 0, "mon"},
		{"single unwhitelisted", []string{"ns"}, singleAffBps(24), 0, ""},
		{"single unregistered", []string{"qqqqqq"}, singleAffBps(24), 0, ""},
		{"single raw address", []string{thorAddr}, singleAffBps(24), 0, ""},
		{"single active at zero bps", []string{"ss"}, singleAffBps(0), 0, ""},
		{"ns/ss:24/1", []string{"ns", "ss"}, singleAffBps(24, 1), 0, ""},
		{"ss/ns:1/24", []string{"ss", "ns"}, singleAffBps(1, 24), 0, ""},
		{"ss/ns:13/12", []string{"ss", "ns"}, singleAffBps(13, 12), 0, ""},
		{"ns/ss:25 shared bps", []string{"ns", "ss"}, singleAffBps(25, 25), 0, ""},
		{"ss/ss duplicate", []string{"ss", "ss"}, singleAffBps(12, 12), 0, ""},
		{"ss/tt both active", []string{"ss", "tt"}, singleAffBps(20, 5), 0, ""},
		{"ss/mon active and monitor", []string{"ss", "mon"}, singleAffBps(5, 20), 0, ""},
		{"ss/raw address", []string{"ss", thorAddr}, singleAffBps(24, 1), 0, ""},
	} {
		ctx, k, dfm, pairA, pairB := setupSingleAffiliateTest(c)

		dyn := dfm.GetDynamicBps(ctx, tc.affiliates, tc.bps, common.BTCAsset, common.RuneAsset())
		c.Check(dyn, Equals, tc.dynBps, Commentf("%s: GetDynamicBps", tc.name))

		wantMinSlip := uint64(singleAffStaticBps)
		if tc.dynBps > 0 {
			wantMinSlip = tc.dynBps
		}
		minSlip := getMinSlipBps(ctx, k, common.NoAddress, common.BTCAsset, false,
			tc.affiliates, tc.bps, common.BTCAsset, common.RuneAsset(), dfm)
		c.Check(minSlip.Uint64(), Equals, wantMinSlip, Commentf("%s: getMinSlipBps", tc.name))

		dfm.CreditSwap(ctx, tc.affiliates, tc.bps, common.BTCAsset, common.RuneAsset(),
			cosmos.NewUint(1_000_000), cosmos.NewUint(10_000))
		for _, name := range []string{"ns", "ss", "tt", "mon", "qqqqqq"} {
			acc, err := k.GetDynamicFeeAccumulator(ctx, name, pairA, pairB)
			c.Assert(err, IsNil)
			if name == tc.credited {
				c.Check(acc.VolumeTor.Equal(cosmos.NewUint(1_000_000)), Equals, true, Commentf("%s: %s volume", tc.name, name))
				c.Check(acc.FeesTor.Equal(cosmos.NewUint(10_000)), Equals, true, Commentf("%s: %s fees", tc.name, name))
				continue
			}
			c.Check(acc.VolumeTor.IsZero(), Equals, true, Commentf("%s: %s must not be credited", tc.name, name))
		}
	}
}

// TestSingleAffiliateNoRecordCreatedForMultiAffiliate: a multi-affiliate memo
// must not lazily create a record for a whitelisted thorname that has none.
func (s *DynamicFeeMgrSuite) TestSingleAffiliateNoRecordCreatedForMultiAffiliate(c *C) {
	ctx, k, dfm, pairA, pairB := setupSingleAffiliateTest(c)
	registerThorname(ctx, k, "fresh", 1)

	dfm.CreditSwap(ctx, []string{"ns", "fresh"}, singleAffBps(24, 1), common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000))
	rec, err := k.GetDynamicFeeRecord(ctx, "fresh", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(rec.DynamicBps, Equals, uint64(0))
	c.Check(len(rec.History), Equals, 0)

	dfm.CreditSwap(ctx, []string{"fresh"}, singleAffBps(1), common.BTCAsset, common.RuneAsset(),
		cosmos.NewUint(1_000_000), cosmos.NewUint(10_000))
	rec, err = k.GetDynamicFeeRecord(ctx, "fresh", pairA, pairB)
	c.Assert(err, IsNil)
	c.Check(rec.DynamicBps, Equals, uint64(1), Commentf("single-affiliate credit creates the record at the floor"))
}
