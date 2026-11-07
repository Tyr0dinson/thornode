package thorchain

import (
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// recordPendingLiquidity is optional errata evidence, never an admission gate.
// Only a direct external ADD's primary share can stage once per inbound; its
// affiliate runs with stage=false. Native/secured deposits cannot be orphaned by
// external-chain errata, and transformed swaps need source-pool loss accounting.
// Neither needs a receipt or an amount-rounding tolerance to remain usable.
func recordPendingLiquidity(ctx cosmos.Context, mgr Manager, lp *LiquidityProvider, id common.TxID, assetAmount cosmos.Uint) {
	if id.IsEmpty() || id.IsBlank() {
		return
	}
	k := mgr.Keeper()
	voter, err := k.GetObservedTxInVoter(ctx, id)
	if err != nil {
		ctx.Logger().Error("cannot record pending liquidity receipt", "tx_id", id, "error", err)
		return
	}
	batch := lp.PendingBatch
	if batch.IsEmpty() {
		batch = id
	}
	receipt := &types.LiquidityPendingReceipt{Asset: lp.Asset, Address: lp.GetAddress(), AssetAmount: assetAmount, BatchID: batch}
	if !voter.TxID.Equals(id) || !voter.Tx.Tx.ID.Equals(id) || !validPendingReceipt(receipt, voter.Tx.Tx) {
		return
	}
	if voter.PendingReceipt != nil {
		// Unexpected repeated execution under one inbound ID is ambiguous. Discard
		// its proof rather than overwrite another contribution or block the ADD.
		ctx.Logger().Error("duplicate pending liquidity receipt; using errata fallback", "tx_id", id)
		if err = mgr.EventMgr().EmitEvent(ctx, NewEventSecurity(voter.Tx.Tx, "duplicate pending liquidity receipt; using errata fallback")); err != nil {
			ctx.Logger().Error("fail to emit security event", "error", err)
		}
		voter.PendingReceipt = nil
	} else {
		voter.PendingReceipt = receipt
		// Pre-upgrade pending totals remain untracked claims. A new contribution
		// starts tracking only itself, never attributing the old total to this ID.
		lp.PendingBatch = batch
	}
	k.SetObservedTxInVoter(ctx, voter)
}

func validPendingReceipt(r *types.LiquidityPendingReceipt, tx common.Tx) bool {
	memoType, _ := StringToTxType(strings.SplitN(tx.Memo, ":", 2)[0])
	return r != nil && memoType == TxAdd && !tx.Chain.Equals(common.THORChain) &&
		!r.BatchID.IsEmpty() && !r.BatchID.IsBlank() && !r.Address.IsEmpty() &&
		r.Asset.Equals(r.Asset.GetLayer1Asset()) && tx.Chain.Equals(r.Asset.GetChain()) &&
		!r.AssetAmount.IsNil() && !r.AssetAmount.IsZero() &&
		len(tx.Coins) == 1 && tx.Coins[0].Asset.Equals(r.Asset) &&
		!tx.Coins[0].Amount.IsNil() && r.AssetAmount.LTE(tx.Coins[0].Amount)
}

// removePendingLiquidity validates before writing and never creates a missing
// LP: GetLiquidityProvider's absent-record default has no batch. Empty batches
// never match. PendingTxID is reporting metadata, not ownership evidence.
func removePendingLiquidity(ctx cosmos.Context, mgr Manager, tx common.Tx, receipt *types.LiquidityPendingReceipt) error {
	if !validPendingReceipt(receipt, tx) {
		return fmt.Errorf("pending receipt missing or invalid")
	}
	k := mgr.Keeper()
	lp, err := k.GetLiquidityProvider(ctx, receipt.Asset, receipt.Address)
	if err != nil {
		return err
	}
	if !lp.Asset.Equals(receipt.Asset) || !lp.GetAddress().Equals(receipt.Address) || !lp.PendingBatch.Equals(receipt.BatchID) {
		return fmt.Errorf("pending LP identity or batch does not match receipt")
	}
	pool, err := k.GetPool(ctx, receipt.Asset)
	if err != nil {
		return err
	}
	if pool.IsEmpty() || !pool.Asset.Equals(receipt.Asset) ||
		lp.PendingRune.IsNil() || lp.PendingAsset.IsNil() || pool.PendingInboundRune.IsNil() || pool.PendingInboundAsset.IsNil() ||
		receipt.AssetAmount.GT(lp.PendingAsset) ||
		receipt.AssetAmount.GT(pool.PendingInboundAsset) {
		return fmt.Errorf("pending balances do not cover receipt")
	}
	lp.PendingAsset = lp.PendingAsset.Sub(receipt.AssetAmount)
	pool.PendingInboundAsset = pool.PendingInboundAsset.Sub(receipt.AssetAmount)
	// Preserve the batch label even if its naming transaction was reversed.
	// Other receipts (and untracked old claims) still belong to this lifecycle.
	if lp.PendingRune.IsZero() && lp.PendingAsset.IsZero() {
		lp.PendingBatch = ""
		lp.PendingTxID = ""
	} else if lp.PendingTxID.Equals(tx.ID) {
		// No list is kept to recover a surviving transaction ID. Queries/completion
		// events omit that ID while pending value remains valid.
		lp.PendingTxID = ""
	}
	if err = k.SetPool(ctx, pool); err != nil {
		return err
	}
	// As with withdrawal, an LP with no remaining claim must release its address
	// pairing. Retaining the empty record would let an orphaned deposit bind a
	// victim's RUNE address to its sender with nothing left to withdraw to clear it.
	if lp.Units.IsZero() && lp.PendingRune.IsZero() && lp.PendingAsset.IsZero() {
		k.RemoveLiquidityProvider(ctx, lp)
	} else {
		k.SetLiquidityProvider(ctx, lp)
	}
	// Reuse the pending withdraw event Midgard already consumes; the paired errata
	// event carries a zero mod for this amount. Midgard clears the member's whole
	// pending total on it, so a partial removal under-reports until the next event.
	evt := NewEventPendingLiquidity(receipt.Asset, WithdrawPendingLiquidity, lp.RuneAddress, cosmos.ZeroUint(),
		lp.AssetAddress, receipt.AssetAmount, common.TxID(""), tx.ID)
	return mgr.EventMgr().EmitEvent(ctx, evt)
}
