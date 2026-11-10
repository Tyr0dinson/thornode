//go:build mainnet

package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// 3.22.0 bond-slash refunds. Three erroneous slashes each debited the bonders of
// a set of nodes for an outbound that in fact paid exactly once on chain, and each
// slash separately mis-credited the asset pool: it added RUNE to the pool and
// debited the pool asset (the "pool leg"). Mirroring the merged 19to20 TRON
// refund (!5175), each event is reversed in its OWN cache context so any one can
// soft-skip (or be toggled off) without affecting the others:
//
//   - pool leg: credit the debited asset back to the pool, remove the RUNE the
//     slash added, and move that freed pool RUNE Asgard->Reserve;
//   - bond leg: pay each affected bond provider its pinned share of its node's
//     bond_cost as liquid RUNE from the Reserve, which then holds the slash's
//     penalty half plus the freed pool-reimburse half (bond_cost == 2 x pool RUNE
//     credit for every event — "half pool, half Reserve").
//
// DIVERGENCE FROM 19to20 (called out for reviewers): there is NO vault-solvency
// gate here and this migration writes NO vault coin balances. 19to20 gated the
// pool-asset credit on unaccounted asset sitting in the vaults, because that slash
// double-debited the pool while the vault kept the asset. These slashes are
// different: the asset legitimately left (the swap/trade-account withdrawal took
// it, the outbound paid once), and only the pool was mis-debited — so there is no
// vault headroom to gate on. The guards that remain are pool-drift (the pool must
// still hold the RUNE the slash added) and Reserve coverage; both SOFT-SKIP
// non-silently (an event + an error log carrying the event height and the
// shortfall) and discard the event's cache context, leaving it to be re-pinned at
// the cut. The BTC vault books also deducted the dead 045DE30B outbound, which
// overlaps a separate BTC re-pay plan and needs the devs' sign-off, so the vault is
// left strictly untouched.

// tronSlashRefundEnabled322 toggles the whole TRON event (h28476980). The inbound
// 37A43908 was the ETH-reorg arb bot's swap; Itz decided ON (2026-11-09 16:14Z) —
// the refund stands.
var tronSlashRefundEnabled322 = true

// btcSlashRefundEnabled322 toggles the whole BTC event (h28574121). A separate
// re-pay plan may restore the BTC pool separately, and reversing it here too would
// double-restore; Itz decided ON (2026-11-09 16:14Z) — the BTC event stays in.
//
// The toggle gates the ENTIRE BTC event (pool reversal AND bond refund), not the
// pool leg alone, because the two are financially inseparable: each bond_cost is
// 2 x the pool RUNE credit ("half pool, half Reserve"), and the pool half of every
// refund is funded by the RUNE freed from reversing the pool (moved Asgard->Reserve).
// Skipping only the pool reversal while still paying the full refund would pay that
// half out of the general Reserve while the pool keeps its erroneous RUNE credit —
// a double-spend. So deferring the BTC pool to that separate plan means deferring the BTC
// bond refund with it; refunding BTC bonders while the pool waits on that plan would need
// a separate funding decision.
var btcSlashRefundEnabled322 = true

// ethSlashRefundEnabled322 toggles the whole ETH event (h28518406). ETH is the
// largest event (refundTotal ~106,676 RUNE, 180 providers), so it gets the same
// decision-time kill-switch as TRON and BTC — if a last-minute pool-drift or
// double-restore concern surfaces at the cut, this flips it off in one line rather
// than forcing an edit-and-rebuild. Default ON.
var ethSlashRefundEnabled322 = true

// slashRefund322 is one erroneous bond-slash reversal.
type slashRefund322 struct {
	key         string // short event id, used in logs/events
	eventHeight int64  // the slash height H (bonds dropped by exactly bond_cost at H)
	quorumTx    string // the MsgObservedTxQuorum tx that carried the slash, for audit
	pool        string // the pool the slash mis-debited
	// poolAssetCredit is the PRINCIPAL the slash wrongly debited from the pool, which
	// the reversal credits back. The slash debited Coins+Gas (CommonOutboundTxHandler
	// .slash), but the Gas was really spent on the outbound, so — like the 19to20 ZEC
	// and TRON reversals — only the principal (the outbound's own coin amount) is
	// restored; the gas stays debited. Pinned = the slash's pool debit minus the
	// outbound gas: TRON 239051149400-133900000, ETH 1600007205-8205, BTC 10296474-1000.
	// poolRuneDebit is the RUNE the slash wrongly added to the pool; the reversal
	// removes it and moves it Asgard->Reserve. refundTotal is the bond refund paid from
	// the Reserve and equals 2 x poolRuneDebit (unaffected by the gas split).
	poolAssetCredit uint64
	poolRuneDebit   uint64
	refundTotal     uint64
	refunds         []struct {
		address string
		amount  uint64
	}
}

// slashRefunds322 lists the three reversals in event order. The per-event toggles
// are read at run time (not baked in here) so tests and the cut can flip them.
var slashRefunds322 = []slashRefund322{
	{
		key: "tron", eventHeight: 28476980, quorumTx: "DC951C7A...E519",
		pool: "TRON.TRX", poolAssetCredit: 238917249400, poolRuneDebit: 111827573715,
		refundTotal: 223655147430, refunds: tronSlashBondRefunds322,
	},
	{
		key: "eth", eventHeight: 28518406, quorumTx: "140C2F77...EEE6",
		pool: "ETH.ETH", poolAssetCredit: 1599999000, poolRuneDebit: 5333813893723,
		refundTotal: 10667627787446, refunds: ethSlashBondRefunds322,
	},
	{
		key: "btc", eventHeight: 28574121, quorumTx: "16EFB26A...F8D8",
		pool: "BTC.BTC", poolAssetCredit: 10295474, poolRuneDebit: 1312800440071,
		refundTotal: 2625600880142, refunds: btcSlashBondRefunds322,
	},
}

// slashRefundEnabled322 reports whether the event is enabled. Reading the package
// var here keeps a one-line flip (or a test override) authoritative without
// rebuilding slashRefunds322. Each toggle gates its WHOLE event (pool + bond).
func slashRefundEnabled322(key string) bool {
	switch key {
	case "tron":
		return tronSlashRefundEnabled322
	case "eth":
		return ethSlashRefundEnabled322
	case "btc":
		return btcSlashRefundEnabled322
	}
	return true
}

// runSlashRefunds322 applies the three bond-slash reversals, each in its own cache
// context so one soft-skip or toggle never blocks the others. It never returns an
// error, so it cannot fail the upgrade.
func (m Migrator) runSlashRefunds322(ctx cosmos.Context) {
	for _, sr := range slashRefunds322 {
		if !slashRefundEnabled322(sr.key) {
			emitSlashRefundSkip322(ctx, sr, "event disabled", cosmos.ZeroUint())
			ctx.Logger().Info("3.22.0 slash refund skipped: event disabled",
				"event", sr.key, "height", sr.eventHeight)
			continue
		}
		cacheCtx, commit := ctx.CacheContext()
		shortfall, err := applySlashRefund322(cacheCtx, m.mgr, sr)
		if err != nil {
			// Non-silent soft-skip: emit the skip event on the PARENT ctx (the cache
			// context is discarded) carrying the event height and the shortfall, and
			// log loudly. Re-pin at the cut.
			emitSlashRefundSkip322(ctx, sr, err.Error(), shortfall)
			ctx.Logger().Error("3.22.0 slash refund skipped",
				"event", sr.key, "height", sr.eventHeight, "shortfall", shortfall.String(), "error", err)
			continue
		}
		commit()
		ctx.Logger().Info("3.22.0 slash refund applied",
			"event", sr.key, "height", sr.eventHeight,
			"refund_total", sr.refundTotal, "providers", len(sr.refunds),
			"pool", sr.pool, "pool_asset_restored", sr.poolAssetCredit, "pool_rune_removed", sr.poolRuneDebit)
	}
}

// emitSlashRefundSkip322 emits a block event recording a skipped refund so a
// soft-skip is never silent (CoS requirement): it carries the event id, the slash
// height and the RUNE shortfall (zero when the skip is not a funding shortfall).
func emitSlashRefundSkip322(ctx cosmos.Context, sr slashRefund322, reason string, shortfall cosmos.Uint) {
	ctx.EventManager().EmitEvent(cosmos.NewEvent("slash_refund_skip_322",
		cosmos.NewAttribute("event", sr.key),
		cosmos.NewAttribute("height", fmt.Sprintf("%d", sr.eventHeight)),
		cosmos.NewAttribute("reason", reason),
		cosmos.NewAttribute("shortfall", shortfall.String()),
	))
}

// applySlashRefund322 reverses one slash: the pool leg, then the bond leg, writing
// into ctx (a cache context the caller discards on error). The two legs are always
// applied together (the pool reversal frees the RUNE that funds half the bond
// refund). It returns the RUNE shortfall (zero unless a funding/drift guard failed)
// and an error; on any error the caller discards the cache so nothing is written.
func applySlashRefund322(ctx cosmos.Context, mgr *Mgrs, sr slashRefund322) (cosmos.Uint, error) {
	k := mgr.Keeper()

	// Validate every refund address and that the pinned shares sum to the pinned
	// total before any write, so a bad row aborts the whole event.
	total := cosmos.ZeroUint()
	for _, r := range sr.refunds {
		if _, err := cosmos.AccAddressFromBech32(r.address); err != nil {
			return cosmos.ZeroUint(), fmt.Errorf("bad %s-slash refund address %s: %w", sr.key, r.address, err)
		}
		total = total.Add(cosmos.NewUint(r.amount))
	}
	if !total.Equal(cosmos.NewUint(sr.refundTotal)) {
		return cosmos.ZeroUint(), fmt.Errorf("%s-slash refund sum %s != pinned %d", sr.key, total, sr.refundTotal)
	}
	// "half pool, half Reserve": bond_cost == 2 x the RUNE the slash added to the
	// pool, so refundTotal must equal 2 x poolRuneDebit. The pool reversal frees
	// poolRuneDebit (Asgard->Reserve) to fund exactly half the refund; the other half
	// is the slash's penalty already in the Reserve. A pinned-data error that broke
	// this identity would silently draw the mismatch from the general Reserve, so
	// enforce it before any write (soft-skip, like the other guards).
	if !cosmos.NewUint(sr.refundTotal).Equal(cosmos.NewUint(sr.poolRuneDebit).MulUint64(2)) {
		return cosmos.ZeroUint(), fmt.Errorf("%s-slash refundTotal %d != 2 x poolRuneDebit %d", sr.key, sr.refundTotal, sr.poolRuneDebit)
	}

	// Reverse the pool leg first so the freed RUNE funds half the bond refund.
	asset, err := common.NewAsset(sr.pool)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("parse %s: %w", sr.pool, err)
	}
	pool, err := k.GetPool(ctx, asset)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("get %s pool: %w", sr.pool, err)
	}
	if pool.IsEmpty() {
		return cosmos.ZeroUint(), fmt.Errorf("%s pool is empty", sr.pool)
	}
	runeDebit := cosmos.NewUint(sr.poolRuneDebit)
	if pool.BalanceRune.LT(runeDebit) {
		shortfall := runeDebit.Sub(pool.BalanceRune)
		return shortfall, fmt.Errorf("%s pool BalanceRune %s below slash credit %d; pool drifted", sr.pool, pool.BalanceRune, sr.poolRuneDebit)
	}
	// Soft vault-backing check (log-only). Crediting the principal back asserts the
	// vaults hold ~poolAssetCredit more of the asset than the pool depth (the slash
	// double-debited the pool while the vault kept the coin once). If the unaccounted
	// vault asset is short, the premise may be off (pool drift, or an OverSolvency
	// sweep consumed the surplus between the slash and the cut) and the reversal would
	// over-claim the pool. It also backstops a SECOND (double) payment of the slashed
	// outbound — which would have consumed the vault's unaccounted coin — so a short
	// reading flags that too. We log loudly rather than gate: the pinned figures are
	// the source of truth and a hard gate here would skip a correct refund on a
	// transient read; re-verify at the cut if this fires. (The slashed outbounds are
	// ~100k blocks old and confirmed once on chain, far beyond any signing/reschedule
	// window, so no live re-signable copy remains to cancel.)
	if freeAsset, favErr := unaccountedVaultAsset19to20(ctx, mgr, asset); favErr != nil {
		ctx.Logger().Error("3.22.0 slash refund: could not read unaccounted vault asset for the backing check",
			"event", sr.key, "pool", sr.pool, "error", favErr)
	} else if freeAsset.LT(cosmos.NewUint(sr.poolAssetCredit)) {
		ctx.Logger().Error("3.22.0 slash refund: unaccounted vault asset below the pool credit — vaults may not back the reversal; verify at the cut",
			"event", sr.key, "pool", sr.pool, "unaccounted_vault_asset", freeAsset.String(), "pool_credit", sr.poolAssetCredit)
	}
	// Credit the debited asset back, remove the RUNE the slash added.
	pool.BalanceAsset = pool.BalanceAsset.Add(cosmos.NewUint(sr.poolAssetCredit))
	pool.BalanceRune = pool.BalanceRune.Sub(runeDebit)
	if err := k.SetPool(ctx, pool); err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("set %s pool: %w", sr.pool, err)
	}
	// Emit the reversal as a slash event for Midgard depth tracking (as 19to20).
	if err := mgr.EventMgr().EmitEvent(ctx, NewEventSlash(asset, []PoolAmt{
		{Asset: asset, Amount: int64(sr.poolAssetCredit)},
		{Asset: common.RuneAsset(), Amount: -int64(sr.poolRuneDebit)},
	})); err != nil {
		ctx.Logger().Error("emit slash reversal event", "event", sr.key, "error", err)
	}
	// Move the freed pool-reimburse RUNE Asgard->Reserve to fund the refund's pool
	// half (the penalty half is already in the Reserve from the slash).
	if err := k.SendFromModuleToModule(ctx, AsgardName, ReserveName,
		common.NewCoins(common.NewCoin(common.RuneAsset(), runeDebit))); err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("%s asgard->reserve: %w", sr.key, err)
	}

	// Pay each affected bond provider its pinned share from the Reserve. This is a
	// defensive floor on the network Reserve (which on mainnet far exceeds any one
	// refund); it soft-skips non-silently with the exact shortfall if the Reserve is
	// ever short — e.g. on a test fixture or a drained Reserve.
	if bal := k.GetRuneBalanceOfModule(ctx, ReserveName); bal.LT(total) {
		return total.Sub(bal), fmt.Errorf("reserve %s below %s-slash refund total %s", bal, sr.key, total)
	}
	for _, r := range sr.refunds {
		to, err := cosmos.AccAddressFromBech32(r.address)
		if err != nil {
			return cosmos.ZeroUint(), fmt.Errorf("bad %s-slash refund address %s: %w", sr.key, r.address, err)
		}
		coins := common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(r.amount)))
		if err := k.SendFromModuleToAccount(ctx, ReserveName, to, coins); err != nil {
			return cosmos.ZeroUint(), fmt.Errorf("%s-slash refund to %s (%d): %w", sr.key, r.address, r.amount, err)
		}
	}
	return cosmos.ZeroUint(), nil
}
