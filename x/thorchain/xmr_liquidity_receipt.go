package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

func xmrLPOwner(lp LiquidityProvider) common.Address {
	if !lp.RuneAddress.IsEmpty() {
		return lp.RuneAddress
	}
	return lp.AssetAddress
}

// recordXMRLiquidity attributes each XMR contribution, including repeated staged
// deposits and affiliates. A completed batch assigns its exact units and paired
// RUNE proportionally; the final contribution owns integer rounding remainders.
// Called in the ADD transaction's cache, together with LP and pool writes.
func recordXMRLiquidity(ctx cosmos.Context, k keeper.Keeper, before, after LiquidityProvider, txID, runeTxID common.TxID, addAsset, runeAmount, units cosmos.Uint, pending bool) error {
	if (!before.AssetAddress.IsEmpty() && before.AssetAddress != after.AssetAddress) || (!before.RuneAddress.IsEmpty() && before.RuneAddress != after.RuneAddress) {
		return fmt.Errorf("XMR LP booking address changed")
	}
	owner := xmrLPOwner(after)
	ids, err := k.GetXMRLiquidityTxIDs(ctx, owner)
	if err != nil {
		return err
	}
	knownPending := cosmos.ZeroUint()
	pendingIDs := make([]common.TxID, 0, len(ids)+1)
	for _, id := range ids {
		receipts, getErr := k.GetXMRLiquidityReceipts(ctx, id)
		if getErr != nil {
			return getErr
		}
		found := false
		for _, receipt := range receipts {
			if receipt.Owner == owner && !receipt.Reverted && !receipt.Withdrawn {
				if receipt.Pending {
					knownPending = knownPending.Add(receipt.AssetAmount)
					pendingIDs = append(pendingIDs, id)
				}
				found = true
			}
		}
		if !found {
			return fmt.Errorf("missing pending XMR LP attribution")
		}
	}
	if !knownPending.Equal(before.PendingAsset) {
		return fmt.Errorf("pending XMR LP attribution does not match pending balance")
	}
	if !addAsset.IsZero() {
		receipts, getErr := k.GetXMRLiquidityReceipts(ctx, txID)
		if getErr != nil {
			return getErr
		}
		for _, receipt := range receipts {
			if receipt.Owner == owner {
				return fmt.Errorf("duplicate XMR LP contribution")
			}
		}
		receipts = append(receipts, kvTypes.XMRLiquidityReceipt{
			TxID: txID, Owner: owner, RuneAddress: after.RuneAddress, AssetAddress: after.AssetAddress,
			AssetAmount: addAsset, RuneAmount: cosmos.ZeroUint(), Units: cosmos.ZeroUint(),
			RuneDepositValue: cosmos.ZeroUint(), AssetDepositValue: cosmos.ZeroUint(), Pending: true,
		})
		if err := k.SetXMRLiquidityReceipts(ctx, txID, receipts); err != nil {
			return err
		}
		ids = append(ids, txID)
		pendingIDs = append(pendingIDs, txID)
	}
	if pending {
		return k.SetXMRLiquidityTxIDs(ctx, owner, ids)
	}
	totalAsset := knownPending.Add(addAsset)
	if totalAsset.IsZero() {
		return nil
	} // RUNE-only asymmetric addition.
	runeValue := common.SafeSub(after.RuneDepositValue, before.RuneDepositValue)
	assetValue := common.SafeSub(after.AssetDepositValue, before.AssetDepositValue)
	remainingUnits, remainingRune := units, runeAmount
	remainingRuneValue, remainingAssetValue := runeValue, assetValue
	for i, id := range pendingIDs {
		receipts, getErr := k.GetXMRLiquidityReceipts(ctx, id)
		if getErr != nil {
			return getErr
		}
		for j := range receipts {
			r := &receipts[j]
			if r.Owner != owner || !r.Pending || r.Reverted {
				continue
			}
			r.Units = common.GetUncappedShare(r.AssetAmount, totalAsset, units)
			r.RuneAmount = common.GetUncappedShare(r.AssetAmount, totalAsset, runeAmount)
			r.RuneDepositValue = common.GetUncappedShare(r.AssetAmount, totalAsset, runeValue)
			r.AssetDepositValue = common.GetUncappedShare(r.AssetAmount, totalAsset, assetValue)
			if i == len(pendingIDs)-1 {
				r.Units, r.RuneAmount = remainingUnits, remainingRune
				r.RuneDepositValue, r.AssetDepositValue = remainingRuneValue, remainingAssetValue
			}
			remainingUnits = remainingUnits.Sub(r.Units)
			remainingRune = remainingRune.Sub(r.RuneAmount)
			remainingRuneValue = remainingRuneValue.Sub(r.RuneDepositValue)
			remainingAssetValue = remainingAssetValue.Sub(r.AssetDepositValue)
			r.Pending = false
			r.RuneTxID = runeTxID
			r.RuneAddress, r.AssetAddress = after.RuneAddress, after.AssetAddress
		}
		if err := k.SetXMRLiquidityReceipts(ctx, id, receipts); err != nil {
			return err
		}
	}
	return k.SetXMRLiquidityTxIDs(ctx, owner, ids)
}

func reverseXMRLiquidity(ctx cosmos.Context, mgr Manager, txID common.TxID) error {
	k := mgr.Keeper()
	receipts, err := k.GetXMRLiquidityReceipts(ctx, txID)
	if err != nil {
		return err
	}
	if len(receipts) == 0 {
		// This is a fresh XMR hard cutover: every successful booking atomically
		// writes attribution. A rejected/refunded ADD has no LP effect to undo.
		// Do not use this assumption to migrate a pre-receipt XMR deployment.
		return nil
	}
	pool, err := k.GetPool(ctx, common.XMRAsset)
	if err != nil {
		return err
	}
	runeRemoved, assetRemoved := cosmos.ZeroUint(), cosmos.ZeroUint()
	for i := range receipts {
		r := &receipts[i]
		if r.Reverted {
			continue
		}
		if r.Withdrawn {
			return fmt.Errorf("XMR LP receipt was withdrawn before errata")
		}
		lp, getErr := k.GetLiquidityProvider(ctx, common.XMRAsset, r.Owner)
		if getErr != nil {
			return getErr
		}
		if xmrLPOwner(lp) != r.Owner || lp.RuneAddress != r.RuneAddress || (!r.AssetAddress.IsEmpty() && lp.AssetAddress != r.AssetAddress) {
			return fmt.Errorf("XMR LP booking owner mismatch")
		}
		if r.Pending {
			if lp.PendingAsset.LT(r.AssetAmount) || pool.PendingInboundAsset.LT(r.AssetAmount) {
				return fmt.Errorf("XMR pending receipt exceeds pending balance")
			}
			lp.PendingAsset = lp.PendingAsset.Sub(r.AssetAmount)
			pool.PendingInboundAsset = pool.PendingInboundAsset.Sub(r.AssetAmount)
			ids, getErr := k.GetXMRLiquidityTxIDs(ctx, r.Owner)
			if getErr != nil {
				return getErr
			}
			remaining := make([]common.TxID, 0, len(ids))
			for _, id := range ids {
				if !id.Equals(txID) {
					remaining = append(remaining, id)
				}
			}
			if err := k.SetXMRLiquidityTxIDs(ctx, r.Owner, remaining); err != nil {
				return err
			}
			if lp.PendingAsset.IsZero() && lp.PendingRune.IsZero() {
				lp.PendingTxID = common.TxID("")
			}
		} else {
			// A pre-errata withdrawal is not recoverable by this accounting-only
			// correction. Fail closed instead of taking another LP's units or
			// inventing a refund. Deployment remains an XMR hard cutover.
			if lp.Units.LT(r.Units) || pool.LPUnits.LT(r.Units) || pool.BalanceAsset.LT(r.AssetAmount) || pool.BalanceRune.LT(r.RuneAmount) {
				return fmt.Errorf("XMR booked effect no longer available for reversal")
			}
			lp.Units = lp.Units.Sub(r.Units)
			pool.LPUnits = pool.LPUnits.Sub(r.Units)
			pool.BalanceAsset = pool.BalanceAsset.Sub(r.AssetAmount)
			pool.BalanceRune = pool.BalanceRune.Sub(r.RuneAmount)
			lp.RuneDepositValue = common.SafeSub(lp.RuneDepositValue, r.RuneDepositValue)
			lp.AssetDepositValue = common.SafeSub(lp.AssetDepositValue, r.AssetDepositValue)
			// Retain the real paired RUNE as pending, never as a new payout.
			lp.PendingRune = lp.PendingRune.Add(r.RuneAmount)
			pool.PendingInboundRune = pool.PendingInboundRune.Add(r.RuneAmount)
			if !r.RuneAmount.IsZero() {
				lp.PendingTxID = r.RuneTxID
			}
			runeRemoved = runeRemoved.Add(r.RuneAmount)
			assetRemoved = assetRemoved.Add(r.AssetAmount)
		}
		lp.LastAddHeight = ctx.BlockHeight()
		k.SetLiquidityProvider(ctx, lp)
		r.Reverted = true
		ids, getErr := k.GetXMRLiquidityTxIDs(ctx, r.Owner)
		if getErr != nil {
			return getErr
		}
		remaining := make([]common.TxID, 0, len(ids))
		for _, id := range ids {
			if !id.Equals(txID) {
				remaining = append(remaining, id)
			}
		}
		if err := k.SetXMRLiquidityTxIDs(ctx, r.Owner, remaining); err != nil {
			return err
		}
	}
	if err := k.SetPool(ctx, pool); err != nil {
		return err
	}
	if err := k.SetXMRLiquidityReceipts(ctx, txID, receipts); err != nil {
		return err
	}
	return mgr.EventMgr().EmitEvent(ctx, NewEventErrata(txID, PoolMods{NewPoolMod(common.XMRAsset, runeRemoved, false, assetRemoved, false)}))
}

// Retire attribution before a legitimate withdrawal, so a later deposit cannot
// make old units look available again. This deliberately does not attempt to
// claw back a previously paid withdrawal: that requires separate recovery policy.
func retireXMRLiquidity(ctx cosmos.Context, k keeper.Keeper, lp LiquidityProvider, pendingWithdrawal bool) error {
	if !lp.Asset.Equals(common.XMRAsset) {
		return nil
	}
	owner := xmrLPOwner(lp)
	ids, err := k.GetXMRLiquidityTxIDs(ctx, owner)
	if err != nil {
		return err
	}
	remaining := make([]common.TxID, 0, len(ids))
	for _, id := range ids {
		receipts, err := k.GetXMRLiquidityReceipts(ctx, id)
		if err != nil {
			return err
		}
		for i := range receipts {
			if receipts[i].Owner == owner && !receipts[i].Reverted {
				if receipts[i].Pending == pendingWithdrawal {
					receipts[i].Withdrawn = true
				} else {
					remaining = append(remaining, id)
				}
			}
		}
		if err := k.SetXMRLiquidityReceipts(ctx, id, receipts); err != nil {
			return err
		}
	}
	return k.SetXMRLiquidityTxIDs(ctx, owner, remaining)
}
