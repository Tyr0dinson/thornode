//go:build !stagenet && !chainnet && !mainnet
// +build !stagenet,!chainnet,!mainnet

package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type MigrationsCovCSuite struct{}

var _ = Suite(&MigrationsCovCSuite{})

func (s *MigrationsCovCSuite) TestNoopMigrationsCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	m := NewMigrator(mgr)
	c.Check(m.mgr, Equals, mgr)

	for name, fn := range map[string]func(cosmos.Context) error{
		"2to3":   m.Migrate2to3,
		"3to4":   m.Migrate3to4,
		"5to6":   m.Migrate5to6,
		"6to7":   m.Migrate6to7,
		"8to9":   m.Migrate8to9,
		"9to10":  m.Migrate9to10,
		"10to11": m.Migrate10to11,
		"11to12": m.Migrate11to12,
		"12to13": m.Migrate12to13,
		"14to15": m.Migrate14to15,
		"15to16": m.Migrate15to16,
		"16to17": m.Migrate16to17,
		"17to18": m.Migrate17to18,
	} {
		c.Check(fn(ctx), IsNil, Commentf("migration %s", name))
	}
}

func (s *MigrationsCovCSuite) TestMigrate1to2DeletesStoreVersionCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	m := NewMigrator(mgr)
	store := ctx.KVStore(keyThorchain)
	key := []byte("_ver//")

	// no key present: nothing to do
	c.Assert(m.Migrate1to2(ctx), IsNil)
	c.Check(store.Has(key), Equals, false)

	store.Set(key, []byte("1.0.0"))
	c.Assert(store.Has(key), Equals, true)
	c.Assert(m.Migrate1to2(ctx), IsNil)
	c.Check(store.Has(key), Equals, false)
}

func (s *MigrationsCovCSuite) TestMigrate4to5ClearsObsoleteMimirsCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	m := NewMigrator(mgr)
	acc := GetRandomBech32Addr()

	cleared := []string{
		"HALTBNBCHAIN", "TERRAFOO", "YGGFUNDLIMIT", "MAXCONFIRMATIONS",
		"CONFMULTIPLIERBASISPOINTS", "SYSTEMINCOMEBURNRATEBP",
	}
	kept := []string{
		"HALTBSCBNB", "MAXCONFIRMATIONS-ETH", "SYSTEMINCOMEBURNRATEBPS", "HALTETHCHAIN",
	}
	for _, key := range append(append([]string{}, cleared...), kept...) {
		k.SetMimir(ctx, key, 1)
		c.Assert(k.SetNodeMimir(ctx, key, 1, acc), IsNil)
	}

	evtsBefore := len(ctx.EventManager().Events())
	c.Assert(m.Migrate4to5(ctx), IsNil)

	for _, key := range cleared {
		val, err := k.GetMimir(ctx, key)
		c.Assert(err, IsNil)
		c.Check(val, Equals, int64(-1), Commentf("mimir %s should be cleared", key))
		nm, err := k.GetNodeMimirs(ctx, key)
		c.Assert(err, IsNil)
		c.Check(nm.Mimirs, HasLen, 0, Commentf("node mimir %s should be cleared", key))
	}
	for _, key := range kept {
		val, err := k.GetMimir(ctx, key)
		c.Assert(err, IsNil)
		c.Check(val, Equals, int64(1), Commentf("mimir %s should be kept", key))
		nm, err := k.GetNodeMimirs(ctx, key)
		c.Assert(err, IsNil)
		c.Check(nm.Mimirs, HasLen, 1, Commentf("node mimir %s should be kept", key))
	}

	// one set_mimir deletion event per cleared admin mimir
	setMimirEvents := 0
	for _, evt := range ctx.EventManager().Events()[evtsBefore:] {
		if evt.Type == "set_mimir" {
			setMimirEvents++
			for _, attr := range evt.Attributes {
				if attr.Key == "value" {
					c.Check(attr.Value, Equals, "-1")
				}
			}
		}
	}
	c.Check(setMimirEvents, Equals, len(cleared))
}

func (s *MigrationsCovCSuite) TestMigrate13to14CovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	m := NewMigrator(mgr)

	c.Assert(m.Migrate13to14(ctx), IsNil)
	reserve := k.GetRuneBalanceOfModule(ctx, ReserveName)
	c.Check(reserve.Uint64(), Equals, uint64(9_300_000_00000000))
	supply := k.GetTotalSupply(ctx, common.RuneAsset())
	maxSupply, err := k.GetMimir(ctx, constants.MaxRuneSupply.String())
	c.Assert(err, IsNil)
	c.Check(maxSupply, Equals, int64(supply.Uint64()))

	// a second run is a no-op on the reserve but still pins MaxRuneSupply to supply
	k.SetMimir(ctx, constants.MaxRuneSupply.String(), 1)
	c.Assert(m.BurnReserveAndReduceMaxSupply(ctx), IsNil)
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).Uint64(), Equals, uint64(9_300_000_00000000))
	c.Check(k.GetTotalSupply(ctx, common.RuneAsset()).Equal(supply), Equals, true)
	maxSupply, err = k.GetMimir(ctx, constants.MaxRuneSupply.String())
	c.Assert(err, IsNil)
	c.Check(maxSupply, Equals, int64(supply.Uint64()))
}

func (s *MigrationsCovCSuite) TestCommonMigrate8to9CovC(c *C) {
	if notMocknetCov() {
		c.Skip("mocknet only: fee targets use mocknet constants")
	}
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	m := NewMigrator(mgr)

	mkPool := func(asset common.Asset, status PoolStatus, rune, bal uint64) {
		p := NewPool()
		p.Asset = asset
		p.Status = status
		p.BalanceRune = cosmos.NewUint(rune)
		p.BalanceAsset = cosmos.NewUint(bal)
		p.LPUnits = cosmos.NewUint(100)
		c.Assert(k.SetPool(ctx, p), IsNil)
	}
	withheld := func(asset common.Asset) cosmos.Uint {
		w, err := k.GetOutboundFeeWithheldRune(ctx, asset)
		c.Assert(err, IsNil)
		return w
	}

	// withheld fees are lazily initialised to the 100% multiplier surplus on
	// first read, so spending is what pushes a multiplier above target.
	// BTC: half the withheld amount spent -> surplus below target -> adjusted
	mkPool(common.BTCAsset, PoolAvailable, 1000*common.One, 10*common.One)
	btcSpent := withheld(common.BTCAsset).QuoUint64(2)
	c.Assert(btcSpent.IsZero(), Equals, false)
	c.Assert(k.AddToOutboundFeeSpentRune(ctx, common.BTCAsset, btcSpent), IsNil)
	// AVAX: spent exceeds withheld -> surplus floors at zero
	mkPool(common.AVAXAsset, PoolAvailable, 1000*common.One, 10*common.One)
	avaxInit := withheld(common.AVAXAsset)
	bigSpend := cosmos.NewUint(50_000 * common.One)
	c.Assert(k.AddToOutboundFeeSpentRune(ctx, common.AVAXAsset, bigSpend), IsNil)
	// ETH: nothing spent -> exactly at target -> untouched
	mkPool(common.ETHAsset, PoolAvailable, 1000*common.One, 10*common.One)
	// DOGE: empty asset depth -> skipped
	mkPool(common.DOGEAsset, PoolAvailable, 1000*common.One, 0)
	// LTC: staged pool -> skipped
	mkPool(common.LTCAsset, PoolStaged, 1000*common.One, 10*common.One)
	// native pools are skipped
	mkPool(common.TCY, PoolAvailable, 1000*common.One, 10*common.One)
	for _, asset := range []common.Asset{common.DOGEAsset, common.LTCAsset, common.TCY} {
		c.Assert(k.AddToOutboundFeeSpentRune(ctx, asset, bigSpend), IsNil)
	}

	c.Assert(m.CommonMigrate8to9(ctx), IsNil)

	for key, want := range map[string]int64{
		constants.MinimumL1OutboundFeeUSD.String():             25000000,
		constants.TargetOutboundFeeSurplusRune.String():        10000_00000000,
		constants.MinOutboundFeeMultiplierBasisPoints.String(): 100,
	} {
		got, err := k.GetMimir(ctx, key)
		c.Assert(err, IsNil)
		c.Check(got, Equals, want, Commentf("mimir %s", key))
	}

	target100 := k.GetSurplusForTargetMultiplier(ctx, cosmos.NewUint(10_000))
	target10 := k.GetSurplusForTargetMultiplier(ctx, cosmos.NewUint(1_000))
	c.Assert(target100.IsZero(), Equals, false)
	maxM := cosmos.NewUint(uint64(k.GetConfigInt64(ctx, constants.MaxOutboundFeeMultiplierBasisPoints)))
	minM := cosmos.NewUint(uint64(k.GetConfigInt64(ctx, constants.MinOutboundFeeMultiplierBasisPoints)))
	multiplier := func(spent, withheld cosmos.Uint) uint64 {
		return mgr.GasMgr().CalcOutboundFeeMultiplier(ctx, cosmos.NewUint(10000_00000000),
			spent, withheld, maxM, minM).Uint64()
	}

	// BTC withheld was raised so that surplus (withheld - spent) hits the target
	btcWithheld := withheld(common.BTCAsset)
	c.Check(btcWithheld.Sub(btcSpent).Equal(target100), Equals, true, Commentf("btc %s", btcWithheld))
	c.Check(multiplier(btcSpent, btcWithheld) <= 10_000, Equals, true)

	// NOTE: when spent exceeds withheld, the adjustment is computed from a
	// zero-floored surplus, so the deficit is not covered and the multiplier
	// stays at the maximum after the migration (current behaviour).
	avaxWithheld := withheld(common.AVAXAsset)
	c.Check(avaxWithheld.Equal(avaxInit.Add(target100)), Equals, true, Commentf("avax %s", avaxWithheld))
	c.Check(multiplier(bigSpend, avaxWithheld), Equals, maxM.Uint64())

	// everything else keeps the lazily initialised amount
	for _, asset := range []common.Asset{common.ETHAsset, common.DOGEAsset, common.LTCAsset, common.TCY} {
		c.Check(withheld(asset).Equal(target100), Equals, true, Commentf("%s %s", asset, withheld(asset)))
	}
	// TRX is always processed, against a 10% multiplier target
	c.Check(target10.GT(target100), Equals, true)
	c.Check(withheld(common.TRXAsset).Equal(target10), Equals, true, Commentf("trx %s", withheld(common.TRXAsset)))
}
