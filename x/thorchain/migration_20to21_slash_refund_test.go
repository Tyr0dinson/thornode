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

// Toggling the TRON event off skips its whole refund (an event is emitted) while
// the other events still run.
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

	NewMigrator(mgr).runSlashRefunds322(ctx)

	tronPoolAfter, err := k.GetPool(ctx, tronAsset)
	c.Assert(err, IsNil)
	c.Check(tronPoolAfter.BalanceRune.Equal(tronPoolBefore.BalanceRune), Equals, true, Commentf("tron pool moved while disabled"))
	// ETH still reversed.
	ethAsset, err := common.NewAsset("ETH.ETH")
	c.Assert(err, IsNil)
	ethPool, err := k.GetPool(ctx, ethAsset)
	c.Assert(err, IsNil)
	c.Check(ethPool.BalanceAsset.Uint64(), Equals, slash322Depths["ETH.ETH"].asset+slashRefunds322[1].poolAssetCredit)
	// A disabled skip event for TRON.
	var disabled bool
	for _, e := range ctx.EventManager().Events() {
		if e.Type == "slash_refund_skip_322" {
			for _, a := range e.Attributes {
				if a.Key == "event" && a.Value == "tron" {
					disabled = true
				}
			}
		}
	}
	c.Check(disabled, Equals, true)
}
