//go:build mainnet

package thorchain

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type Migrate20to21SlashRefundSuite struct{}

var _ = Suite(&Migrate20to21SlashRefundSuite{})

// current mainnet depths (2026-11-09) for the three reversed pools.
var slash322Depths = map[string]struct {
	rune, asset, units uint64
}{
	"TRON.TRX": {41_848_453_233_547, 79_276_204_207_682, 30_127_250_235_164},
	"ETH.ETH":  {678_905_478_571_958, 188_843_545_573, 62_485_614_865_548},
	"BTC.BTC":  {2_309_290_940_166_299, 18_309_041_452, 196_412_898_527_083},
}

// slash322Setup builds the three pools at mainnet depth (funding Asgard to the
// pool RUNE, as mainnet holds pool RUNE in Asgard) and seeds the Reserve with the
// slash's penalty half of every event (sum of poolRuneDebit). The pool reversals
// then move the other half Asgard->Reserve, so the Reserve covers each full
// refund — mirroring "half pool, half Reserve".
func slash322Setup(c *C) (cosmos.Context, *Mgrs) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	penalty := cosmos.ZeroUint()
	for _, sr := range slashRefunds322 {
		d := slash322Depths[sr.pool]
		asset, err := common.NewAsset(sr.pool)
		c.Assert(err, IsNil)
		pool := NewPool()
		pool.Asset = asset
		pool.Status = PoolAvailable
		pool.BalanceRune = cosmos.NewUint(d.rune)
		pool.BalanceAsset = cosmos.NewUint(d.asset)
		pool.LPUnits = cosmos.NewUint(d.units)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		// Asgard holds the pool RUNE on mainnet; fund it so the Asgard->Reserve
		// move has the freed RUNE to send.
		FundModule(c, ctx, k, AsgardName, d.rune)
		penalty = penalty.Add(cosmos.NewUint(sr.poolRuneDebit))
	}
	setReserveRune(c, ctx, k, penalty.Uint64())
	return ctx, mgr
}

// The happy path: every event reverses its pool (asset credited back, RUNE
// removed) and pays its bond providers their pinned shares from the Reserve.
// (Vaults are left unseeded, so the soft vault-backing check reads short / errors on
// every event — this run therefore also proves that check is log-only and NEVER
// gates: all three refunds still apply with it firing.)
func (s *Migrate20to21SlashRefundSuite) TestSlashRefund322AppliesAll(c *C) {
	ctx, mgr := slash322Setup(c)
	k := mgr.Keeper()

	// a known provider + amount from the TRON slice (first row).
	tronProvider, err := cosmos.AccAddressFromBech32(tronSlashBondRefunds322[0].address)
	c.Assert(err, IsNil)
	tronBefore := acctRune(ctx, k, tronProvider)

	NewMigrator(mgr).runSlashRefunds322(ctx)

	for _, sr := range slashRefunds322 {
		asset, err := common.NewAsset(sr.pool)
		c.Assert(err, IsNil)
		d := slash322Depths[sr.pool]
		pool, err := k.GetPool(ctx, asset)
		c.Assert(err, IsNil)
		c.Check(pool.BalanceAsset.Uint64(), Equals, d.asset+sr.poolAssetCredit, Commentf("%s asset not credited back", sr.key))
		c.Check(pool.BalanceRune.Uint64(), Equals, d.rune-sr.poolRuneDebit, Commentf("%s pool RUNE not removed", sr.key))
	}
	c.Check(acctRune(ctx, k, tronProvider)-tronBefore, Equals, tronSlashBondRefunds322[0].amount)
	// Reserve fully spent: it held T/2 (penalties), the moves added T/2, both legs
	// paid T.
	c.Check(k.GetRuneBalanceOfModule(ctx, ReserveName).IsZero(), Equals, true, Commentf("reserve not fully drained"))
}

// The three reversals move LP value only within each pool's declared bound: the
// LUVI migration gate finds no undeclared drop. Logs each pool's normalized dG.
func (s *Migrate20to21SlashRefundSuite) TestSlashRefund322LuviGate(c *C) {
	ctx, mgr := slash322Setup(c)
	moved := make([]common.Asset, 0, len(slashRefunds322))
	for _, sr := range slashRefunds322 {
		a, err := common.NewAsset(sr.pool)
		c.Assert(err, IsNil)
		moved = append(moved, a)
	}
	luviGateStep(c, ctx, mgr, luviMigrationScopeName(20),
		func(stepCtx sdk.Context) error { NewMigrator(mgr).runSlashRefunds322(stepCtx); return nil }, moved...)
}

// A Reserve shortfall soft-skips non-silently: no pool or balance moves, and a
// slash_refund_skip_322 event carries the event height and the RUNE shortfall.
func (s *Migrate20to21SlashRefundSuite) TestSlashRefund322SoftSkipReserveShort(c *C) {
	ctx, mgr := slash322Setup(c)
	k := mgr.Keeper()
	// Drain the Reserve so even the first reversal (which only tops up by its own
	// freed pool half) cannot cover its refund total.
	setReserveRune(c, ctx, k, 0)

	tron := slashRefunds322[0]
	asset, err := common.NewAsset(tron.pool)
	c.Assert(err, IsNil)
	poolBefore, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)

	ctx = ctx.WithEventManager(cosmos.NewEventManager())
	NewMigrator(mgr).runSlashRefunds322(ctx)

	// Pool untouched (cache discarded).
	poolAfter, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(poolAfter.BalanceAsset.Equal(poolBefore.BalanceAsset), Equals, true)
	c.Check(poolAfter.BalanceRune.Equal(poolBefore.BalanceRune), Equals, true)
	// A skip event was emitted for TRON with its height and a non-zero shortfall.
	var found bool
	for _, e := range ctx.EventManager().Events() {
		if e.Type != "slash_refund_skip_322" {
			continue
		}
		attrs := map[string]string{}
		for _, a := range e.Attributes {
			attrs[a.Key] = a.Value
		}
		if attrs["event"] == "tron" {
			found = true
			c.Check(attrs["height"], Equals, "28476980")
			c.Check(attrs["shortfall"], Not(Equals), "0", Commentf("shortfall not reported"))
		}
	}
	c.Check(found, Equals, true, Commentf("no slash_refund_skip_322 event for tron"))
}

// Even when the tcso reimbursement batch aborts (Reserve too short to pay it), the
// 3.22.0 slash refunds still run — they are no longer gated behind the tcso batch.
// With the Reserve drained the refunds soft-skip, but each still emits its
// slash_refund_skip_322 event, proving runSlashRefunds322 was reached.
func (s *Migrate20to21SlashRefundSuite) TestMigrate20to21RefundsRunDespiteTcsoFailure(c *C) {
	ctx, mgr := slash322Setup(c)
	k := mgr.Keeper()
	setReserveRune(c, ctx, k, 0) // tcso batch cannot pay -> it aborts, writing nothing
	ctx = ctx.WithEventManager(cosmos.NewEventManager())

	c.Assert(NewMigrator(mgr).migrate20to21Mainnet(ctx), IsNil)

	var ran bool
	for _, e := range ctx.EventManager().Events() {
		if e.Type == "slash_refund_skip_322" {
			ran = true
			break
		}
	}
	c.Check(ran, Equals, true, Commentf("slash refunds did not run after the tcso batch aborted"))
}

// soloProvider322 returns the first provider of `self` whose address appears in no
// other slice, so its RUNE balance isolates that one event's payout (providers can
// recur across events, which would otherwise blur a per-event paid/unpaid check).
func soloProvider322(c *C, self []struct {
	address string
	amount  uint64
}, others ...[]struct {
	address string
	amount  uint64
},
) (string, uint64) {
	elsewhere := map[string]bool{}
	for _, o := range others {
		for _, r := range o {
			elsewhere[r.address] = true
		}
	}
	for _, r := range self {
		if !elsewhere[r.address] {
			return r.address, r.amount
		}
	}
	c.Fatalf("no solo provider found")
	return "", 0
}

// skipEventFor322 reports whether a slash_refund_skip_322 event was emitted for key.
func skipEventFor322(ctx cosmos.Context, key string) bool {
	for _, e := range ctx.EventManager().Events() {
		if e.Type != "slash_refund_skip_322" {
			continue
		}
		for _, a := range e.Attributes {
			if a.Key == "event" && a.Value == key {
				return true
			}
		}
	}
	return false
}

// Toggling the TRON event off gates its WHOLE event — the pool is NOT reversed AND
// its bond providers are NOT paid (the toggle's double-spend guard: never pool-only)
// — while the still-enabled siblings reverse their pool and pay their providers.
func (s *Migrate20to21SlashRefundSuite) TestSlashRefund322TronToggleOff(c *C) {
	defer func() { tronSlashRefundEnabled322 = true }()
	tronSlashRefundEnabled322 = false

	ctx, mgr := slash322Setup(c)
	k := mgr.Keeper()
	ctx = ctx.WithEventManager(cosmos.NewEventManager())
	tronAsset, err := common.NewAsset("TRON.TRX")
	c.Assert(err, IsNil)
	tronPoolBefore, err := k.GetPool(ctx, tronAsset)
	c.Assert(err, IsNil)

	// A TRON provider unique to the TRON slice, so its balance isolates the TRON
	// payout (providers can recur across events). ETH/BTC providers all overlap other
	// slices, so the enabled-sibling-ran proof is the ETH pool reversal below (plus the
	// happy-path test, which asserts an actual provider payout).
	tronSolo, _ := soloProvider322(c, tronSlashBondRefunds322, ethSlashBondRefunds322, btcSlashBondRefunds322)
	tronSoloAddr, err := cosmos.AccAddressFromBech32(tronSolo)
	c.Assert(err, IsNil)
	tronSoloBefore := acctRune(ctx, k, tronSoloAddr)

	NewMigrator(mgr).runSlashRefunds322(ctx)

	// TRON: pool untouched AND its provider UNPAID (both legs gated, not pool-only).
	tronPoolAfter, err := k.GetPool(ctx, tronAsset)
	c.Assert(err, IsNil)
	c.Check(tronPoolAfter.BalanceRune.Equal(tronPoolBefore.BalanceRune), Equals, true, Commentf("tron pool moved while disabled"))
	c.Check(acctRune(ctx, k, tronSoloAddr), Equals, tronSoloBefore, Commentf("disabled tron provider was paid — toggle gated pool-only, a double-spend"))
	// ETH still reversed AND its provider paid (so a 'nothing ran' regression can't
	// masquerade as a correct skip).
	ethAsset, err := common.NewAsset("ETH.ETH")
	c.Assert(err, IsNil)
	ethPool, err := k.GetPool(ctx, ethAsset)
	c.Assert(err, IsNil)
	c.Check(ethPool.BalanceAsset.Uint64(), Equals, slash322Depths["ETH.ETH"].asset+slashRefunds322[1].poolAssetCredit, Commentf("eth not reversed — nothing ran"))
	c.Check(skipEventFor322(ctx, "tron"), Equals, true)
}

// Symmetric to the TRON case: toggling BTC off gates its whole event (pool + bonders)
// while TRON and ETH still apply.
func (s *Migrate20to21SlashRefundSuite) TestSlashRefund322BtcToggleOff(c *C) {
	defer func() { btcSlashRefundEnabled322 = true }()
	btcSlashRefundEnabled322 = false

	ctx, mgr := slash322Setup(c)
	k := mgr.Keeper()
	ctx = ctx.WithEventManager(cosmos.NewEventManager())
	btcAsset, err := common.NewAsset("BTC.BTC")
	c.Assert(err, IsNil)
	btcPoolBefore, err := k.GetPool(ctx, btcAsset)
	c.Assert(err, IsNil)
	btcSolo, _ := soloProvider322(c, btcSlashBondRefunds322, tronSlashBondRefunds322, ethSlashBondRefunds322)
	btcSoloAddr, err := cosmos.AccAddressFromBech32(btcSolo)
	c.Assert(err, IsNil)
	btcSoloBefore := acctRune(ctx, k, btcSoloAddr)

	NewMigrator(mgr).runSlashRefunds322(ctx)

	btcPoolAfter, err := k.GetPool(ctx, btcAsset)
	c.Assert(err, IsNil)
	c.Check(btcPoolAfter.BalanceRune.Equal(btcPoolBefore.BalanceRune), Equals, true, Commentf("btc pool moved while disabled"))
	c.Check(acctRune(ctx, k, btcSoloAddr), Equals, btcSoloBefore, Commentf("disabled btc provider was paid"))
	tronAsset, err := common.NewAsset("TRON.TRX")
	c.Assert(err, IsNil)
	tronPool, err := k.GetPool(ctx, tronAsset)
	c.Assert(err, IsNil)
	c.Check(tronPool.BalanceAsset.Uint64(), Equals, slash322Depths["TRON.TRX"].asset+slashRefunds322[0].poolAssetCredit, Commentf("tron not reversed while btc disabled"))
	c.Check(skipEventFor322(ctx, "btc"), Equals, true)
}

// A pool whose RUNE drifted below the slash's RUNE credit soft-skips with the drift
// shortfall and is left untouched, while the other two events still apply. Exercises
// the pool-drift guard + the BalanceRune.Sub boundary (an under-flow there would
// panic the whole upgrade).
func (s *Migrate20to21SlashRefundSuite) TestSlashRefund322PoolDriftSoftSkip(c *C) {
	ctx, mgr := slash322Setup(c)
	k := mgr.Keeper()
	ctx = ctx.WithEventManager(cosmos.NewEventManager())

	// Drift the ETH pool RUNE to one below its slash credit so the guard trips.
	ethAsset, err := common.NewAsset("ETH.ETH")
	c.Assert(err, IsNil)
	ethPool, err := k.GetPool(ctx, ethAsset)
	c.Assert(err, IsNil)
	ethPool.BalanceRune = cosmos.NewUint(slashRefunds322[1].poolRuneDebit - 1)
	c.Assert(k.SetPool(ctx, ethPool), IsNil)

	NewMigrator(mgr).runSlashRefunds322(ctx)

	// ETH soft-skipped: pool RUNE still the drifted value (cache discarded), skip event
	// emitted with a non-zero shortfall.
	ethAfter, err := k.GetPool(ctx, ethAsset)
	c.Assert(err, IsNil)
	c.Check(ethAfter.BalanceRune.Uint64(), Equals, slashRefunds322[1].poolRuneDebit-1, Commentf("drifted eth pool moved"))
	c.Check(ethAfter.BalanceAsset.Uint64(), Equals, slash322Depths["ETH.ETH"].asset, Commentf("drifted eth pool asset credited despite skip"))
	var shortfall string
	for _, e := range ctx.EventManager().Events() {
		if e.Type != "slash_refund_skip_322" {
			continue
		}
		attrs := map[string]string{}
		for _, a := range e.Attributes {
			attrs[a.Key] = a.Value
		}
		if attrs["event"] == "eth" {
			shortfall = attrs["shortfall"]
		}
	}
	c.Check(shortfall, Equals, "1", Commentf("eth drift shortfall not reported as 1"))
	// TRON still reversed.
	tronAsset, err := common.NewAsset("TRON.TRX")
	c.Assert(err, IsNil)
	tronPool, err := k.GetPool(ctx, tronAsset)
	c.Assert(err, IsNil)
	c.Check(tronPool.BalanceAsset.Uint64(), Equals, slash322Depths["TRON.TRX"].asset+slashRefunds322[0].poolAssetCredit)
}

// Pins the gas split to its decomposition: each poolAssetCredit is the slash's full
// pool debit MINUS the outbound gas. A regression that credited the full (gas-
// inclusive) debit — over-restoring asset the vaults don't back — would fail here
// (the happy-path equality check reuses poolAssetCredit on both sides, so it cannot).
func (s *Migrate20to21SlashRefundSuite) TestSlashRefund322GasSplitDecomposition(c *C) {
	cases := map[string]struct{ fullDebit, gas uint64 }{
		"tron": {239051149400, 133900000},
		"eth":  {1600007205, 8205},
		"btc":  {10296474, 1000},
	}
	for _, sr := range slashRefunds322 {
		d, ok := cases[sr.key]
		c.Assert(ok, Equals, true, Commentf("no gas-split case for %s", sr.key))
		c.Check(sr.poolAssetCredit, Equals, d.fullDebit-d.gas, Commentf("%s poolAssetCredit != fullDebit-gas", sr.key))
		c.Check(sr.poolAssetCredit < d.fullDebit, Equals, true, Commentf("%s gas not excluded", sr.key))
	}
}

// Pins the "half pool, half Reserve" identity per event (the aggregate Reserve-drains-
// to-zero happy-path check only constrains the sum, so a compensating per-event
// imbalance would slip through it).
func (s *Migrate20to21SlashRefundSuite) TestSlashRefund322HalfPoolHalfReserve(c *C) {
	for _, sr := range slashRefunds322 {
		c.Check(sr.refundTotal, Equals, sr.poolRuneDebit*2, Commentf("%s refundTotal != 2 x poolRuneDebit", sr.key))
	}
}
