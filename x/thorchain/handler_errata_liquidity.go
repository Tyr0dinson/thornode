package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// processErrataLiquidity removes proven pending liability, then charges only the
// remaining VERIFIED inbound amount to the source pool. Custody was corrected
// independently in the caller, which only calls this for inbounds that credited
// the vault (UpdatedVault). A cap limits the pool debit, never the vault debit.
// Old ADDs have no receipt: keep their claims usable without migration and fund
// them from the pool even if historical ownership might have been reconstructed.
// This also covers refunded ADDs: a refund may already be paid or still owed.
// We do not cancel refunds or reconstruct net loss; a later cancelled/reverted
// refund can therefore leave a conservatively over-allocated pool loss.
// Paired, affiliate and unproven positions keep their committed units: their
// beneficiaries can still withdraw much of the orphan's value at other LPs'
// expense. The exception is the guarded asset-only whole-position penalty below.
// voter is the caller's snapshot from BEFORE SetReverted, preserving execution
// status without a second inbound read.
func processErrataLiquidity(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter) error {
	tx := voter.Tx.Tx
	exact := cosmos.ZeroUint()
	cacheCtx, commit := ctx.CacheContext()
	exactErr := removePendingLiquidity(cacheCtx, mgr, tx, voter.PendingReceipt)
	if exactErr == nil {
		commit()
		exact = voter.PendingReceipt.AssetAmount
	}
	// A failed attempt discards every pending write/event before the full fallback.
	// No receipt is mutated: the caller's inbound Reverted flag guards all parts.
	mods := PoolMods{}
	for _, coin := range tx.Coins {
		requested := coin.Amount
		if !exact.IsZero() && coin.Asset.Equals(voter.PendingReceipt.Asset) {
			requested = requested.Sub(exact) // receipt was bounded by this original coin
		}
		if requested.IsZero() {
			mods = append(mods, NewPoolMod(coin.Asset, cosmos.ZeroUint(), false, cosmos.ZeroUint(), false))
			continue
		}
		reason := "committed or untracked ADD amount"
		if exactErr != nil {
			reason = exactErr.Error()
		}
		pool, poolErr := mgr.Keeper().GetPool(ctx, coin.Asset)
		if poolErr != nil {
			return poolErr
		}
		burned, penaltyErr := penalizeAssetOnlyLiquidity(ctx, mgr, voter, &pool)
		if penaltyErr != nil {
			// An unreadable/inconsistent LP must not block the independent custody
			// and pool corrections. The security event records this fallback too.
			reason += "; asset-only penalty skipped: " + penaltyErr.Error()
		} else if !burned.IsZero() {
			reason += fmt.Sprintf("; asset-only whole-position penalty: units=%s", burned)
		}
		if err := mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(tx, "liquidity errata ownership fallback: "+reason)); err != nil {
			return err
		}
		mod, debitErr := debitLiquidityErrataPool(ctx, mgr, tx, coin.Asset, pool, requested)
		if debitErr != nil {
			return debitErr
		}
		mods = append(mods, mod)
	}
	return mgr.EventMgr().EmitEvent(ctx, NewEventErrata(tx.ID, mods))
}

// penalizeAssetOnlyLiquidity restores the former whole-position penalty only
// where an executed, plain external ADD used the sender as its LP key. This is
// deliberately not contribution accounting: an orphaned 1 BTC ADD can erase a
// prior 100 BTC position, including units added after a withdrawal/redeposit.
// Paired/XMR positions and separate affiliates retain the pool-funded fallback.
func penalizeAssetOnlyLiquidity(ctx cosmos.Context, mgr Manager, voter ObservedTxVoter, pool *Pool) (cosmos.Uint, error) {
	zero := cosmos.ZeroUint()
	tx := voter.Tx.Tx
	// IsDone() only compares outbound counts and accepts 0 <= 0. Require the
	// explicit pre-errata status instead; failed/dust refunds can have no actions.
	// Compromised-vault no-ops are also marked done, but never credit the vault.
	if voter.Tx.Status != common.Status_done || !voter.UpdatedVault {
		return zero, nil
	}
	// Refunds and tracked pending deposits do not prove a committed ADD.
	if len(voter.Actions) != 0 || len(voter.OutTxs) != 0 || voter.PendingReceipt != nil {
		return zero, nil
	}
	// The external chain must provide a sender that can identify its own LP.
	if tx.Chain.IsTHORChain() || tx.Chain.RequiresExplicitRefundAddress() ||
		tx.FromAddress.IsEmpty() || !tx.FromAddress.IsChain(tx.Chain) {
		return zero, nil
	}
	// Require one direct layer-1 coin from that chain before reading its asset.
	if len(tx.Coins) != 1 {
		return zero, nil
	}
	asset := tx.Coins[0].Asset
	if !asset.Equals(asset.GetLayer1Asset()) || !asset.GetChain().Equals(tx.Chain) {
		return zero, nil
	}
	// Apply the penalty only to the existing pool funded by this inbound.
	if pool.IsEmpty() || !pool.Asset.Equals(asset) {
		return zero, nil
	}
	// A paired ADD can share its sender with a separate asset-only position.
	// Require an empty pair field. Parse only the type/asset: an affiliate's
	// current THORName resolution must not select or protect the primary LP.
	// The memo parser drops everything after "|", so do the same here.
	base, _, _ := strings.Cut(tx.Memo, "|")
	parts := strings.SplitN(base, ":", 4)
	if len(parts) < 2 || (len(parts) > 2 && parts[2] != "") {
		return zero, nil
	}
	memo, err := ParseMemo(mgr.GetVersion(), strings.Join(parts[:2], ":"))
	// Successful plain ADD validation requires the resolved asset to match the
	// inbound coin. That coin identifies the executed pool even for short memos.
	// Do not repeat fuzzyAssetMatch here: it scans pools and a different token
	// can win after depths change. Still exclude synth/trade/secured memos, so
	// a savers ADD cannot penalize the sender's separate layer-1 position.
	if err != nil || !memo.IsType(TxAdd) || !memo.GetAsset().Equals(memo.GetAsset().GetLayer1Asset()) {
		return zero, nil
	}
	lp, err := mgr.Keeper().GetLiquidityProvider(ctx, pool.Asset, tx.FromAddress)
	if err != nil {
		return zero, err
	}
	if !lp.Asset.Equals(pool.Asset) || !lp.RuneAddress.IsEmpty() || !lp.AssetAddress.Equals(tx.FromAddress) {
		return zero, nil
	}
	if lp.Units.IsNil() || pool.LPUnits.IsNil() || lp.PendingRune.IsNil() || lp.PendingAsset.IsNil() {
		return zero, fmt.Errorf("LP or pool amounts are missing")
	}
	if lp.Units.IsZero() {
		return zero, nil // includes the keeper's missing-LP default; never write it
	}
	if lp.Units.GT(pool.LPUnits) {
		return zero, fmt.Errorf("LP units exceed pool units")
	}
	burned := lp.Units
	pool.LPUnits = pool.LPUnits.Sub(burned)
	lp.Units = zero
	lp.LastAddHeight = ctx.BlockHeight()
	if lp.PendingRune.IsZero() && lp.PendingAsset.IsZero() {
		mgr.Keeper().RemoveLiquidityProvider(ctx, lp)
	} else {
		mgr.Keeper().SetLiquidityProvider(ctx, lp)
	}
	return burned, nil
}

// debitLiquidityErrataPool caps only the pool adjustment, independently of the
// full custody debit. A pool shortfall need not cause a vault coin underflow;
// report it without inventing funds, a pool, or another vault deficit debit.
// Policy: an exhausted or removed pool has no further loss-absorbing balance.
// Keep the uncovered amount explicit in a security event; do not create negative
// pool balances, seize other pools' backing, or claim that the event restores
// solvency. The full custody debit already happened once, not just its remainder.
func debitLiquidityErrataPool(ctx cosmos.Context, mgr Manager, tx common.Tx, asset common.Asset, pool Pool, requestedAsset cosmos.Uint) (PoolMod, error) {
	assetDebit := cosmos.ZeroUint()
	if !pool.IsEmpty() {
		assetDebit = cosmos.MinUint(requestedAsset, pool.BalanceAsset)
		pool.BalanceAsset = pool.BalanceAsset.Sub(assetDebit)
		// An exact receipt may also have removed pending liability from this pool.
		if err := mgr.Keeper().SetPool(ctx, pool); err != nil {
			return PoolMod{}, err
		}
	}
	mod := NewPoolMod(asset, cosmos.ZeroUint(), false, assetDebit, false)
	if !assetDebit.Equal(requestedAsset) {
		msg := fmt.Sprintf("liquidity errata pool shortfall: pool=%s asset_requested=%s asset_debited=%s asset_uncovered=%s",
			asset, requestedAsset, assetDebit, requestedAsset.Sub(assetDebit))
		if err := mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(tx, msg)); err != nil {
			return PoolMod{}, err
		}
	}
	return mod, nil
}
