//go:build mainnet

package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// Migrate21to22 drops one permanently undeliverable XRP outbound and makes its
// owner whole by restoring the secured XRP it was withdrawing.
//
// The secured XRP withdrawal in inbound EE959E9A...3306 scheduled 149.758826
// XRP to rNxp4h8apvRis6mJf9Sh8C6iRxfrDWN7AV. That destination requires a
// destination tag, so the outbound can never land: it has gone unobserved and
// reschedules every SigningTransactionPeriod, slashing no one (an unsigned
// item is only reassigned, not signed) and never consuming an XRP sequence, so
// dropping it cannot double-pay.
//
// The migration does two things, atomically (all or none):
//
//  1. Drop. It neutralises the live TxOut item for inbound EE959E9A...3306 by
//     setting its OutHash to BlankTxID, exactly as LackSigning marks an item it
//     has moved so bifrost never signs it and the slasher never reschedules it
//     again (manager_slasher_current.go). The item reschedules across heights
//     and vaults, so it is matched by inbound hash alone, scanned over a window
//     around the upgrade height rather than by a fixed height or vault.
//
//  2. Re-credit. The withdrawal burned the owner's secured-XRP shares and
//     debited the secured-XRP depth by the withdrawn amount. Dropping the
//     outbound leaves that asset in the vaults, so the migration mints the
//     owner's shares back and restores the depth by the same amount, the
//     mirror of the withdrawal. The owner is thor1ax6w7a5szst33r4zatdkfpzkfd35a2j4lmmaw5.
//
// Mainnet only; it logs loudly and never halts the upgrade.
const (
	// xrpStuckInHash is the inbound hash of the undeliverable secured-XRP
	// withdrawal whose outbound this migration drops.
	xrpStuckInHash = "EE959E9ACE62C68715D19A6BFCC5B9A80ED4663FFBB9499113968B2138FF3306"
	// xrpStuckToAddress is the XRP destination that requires a destination tag
	// (recorded for the re-credit event only; matching is by inbound hash).
	xrpStuckToAddress = "rNxp4h8apvRis6mJf9Sh8C6iRxfrDWN7AV"
	// xrpRecreditOwner is the THOR address whose secured XRP is restored. It is
	// also the SECURE- withdrawal's from_address.
	xrpRecreditOwner = "thor1ax6w7a5szst33r4zatdkfpzkfd35a2j4lmmaw5" //nolint:gosec // G101: a public THOR account address, not a credential
	// xrpRecreditAmount is the secured XRP to restore, in 1e8 units:
	// 150.00000000 XRP, the full amount the withdrawal debited from the owner's
	// secured-XRP shares and from the secured-XRP depth.
	//
	// The scheduled outbound was 149.758826 XRP (the debited amount minus a
	// ~0.241174 XRP outbound fee), but because the outbound never broadcast no
	// L1 send and no fee occurred and the vaults still hold the full backing, so
	// restoring the full 150.00000000 exactly reverses the withdrawal's depth
	// debit (Itz's decision 2026-11-12).
	xrpRecreditAmount = uint64(15_000_000_000)
)

// migrate21to22Mainnet performs the drop and the re-credit in a cached context
// and commits only if both succeed, so any failure writes nothing. Mainnet
// only; it logs loudly and never halts the upgrade.
func (m Migrator) migrate21to22Mainnet(ctx cosmos.Context) error {
	k := m.mgr.Keeper()
	cacheCtx, commit := ctx.CacheContext()

	dropped, err := dropXRPStuckOutbound(cacheCtx, k)
	if err != nil {
		ctx.Logger().Error("Migrate21to22 drop failed; no change applied", "error", err)
		return nil
	}
	if dropped == 0 {
		// Nothing to drop means the live item was not found in the scan window;
		// do not re-credit, or the owner would be paid without the outbound
		// being neutralised.
		ctx.Logger().Error("Migrate21to22 found no live XRP outbound to drop; no change applied",
			"in_hash", xrpStuckInHash)
		return nil
	}

	mintAmt, err := recreditSecuredXRP(cacheCtx, k, xrpRecreditOwner, xrpRecreditAmount)
	if err != nil {
		ctx.Logger().Error("Migrate21to22 secured XRP re-credit failed; no change applied", "error", err)
		return nil
	}

	commit()

	// Emit the re-credit event on the committed (parent) context: a cache
	// context's events are discarded by commit.
	asset := common.XRPAsset.GetSecuredAsset()
	depositEvent := NewEventSecuredAssetDeposit(
		cosmos.NewUint(xrpRecreditAmount),
		asset,
		// No L1 counterparty: this is a migration re-credit, not a deposit from
		// an asset-chain address, so the asset address is left empty rather than
		// naming the undeliverable destination.
		common.NoAddress,
		common.Address(xrpRecreditOwner),
		common.TxID(xrpStuckInHash),
	)
	if err := m.mgr.EventMgr().EmitEvent(ctx, depositEvent); err != nil {
		ctx.Logger().Error("Migrate21to22 fail to emit secured asset deposit event", "error", err)
	}

	ctx.Logger().Info("Migrate21to22 dropped undeliverable XRP outbound and restored secured XRP",
		"in_hash", xrpStuckInHash,
		"dropped_items", dropped,
		"owner", xrpRecreditOwner,
		"amount", cosmos.NewUint(xrpRecreditAmount).String(),
		"minted_shares", mintAmt.String())
	return nil
}

// dropXRPStuckOutbound neutralises every live (unsigned) TxOut item for the
// undeliverable inbound hash by setting its OutHash to BlankTxID, the same
// marker LackSigning uses to stop a moved item being signed or rescheduled. It
// matches by inbound hash only, scanning a window of TxOut heights around the
// upgrade height because the item reschedules across heights and vaults. It
// returns the number of items neutralised.
func dropXRPStuckOutbound(ctx cosmos.Context, k keeper.Keeper) (int, error) {
	target, err := common.NewTxID(xrpStuckInHash)
	if err != nil {
		return 0, fmt.Errorf("parse stuck in hash %q: %w", xrpStuckInHash, err)
	}

	// A standard outbound sits at the height of its last reschedule until it is
	// re-examined a SigningTransactionPeriod later, so the live item is within
	// one period behind the current height. LackSigning re-places it at the
	// current height rounded up to RescheduleCoalesceBlocks, so it can also sit
	// at most that rounding ahead. Scan a generous period behind and the
	// coalesce window ahead, so the exact upgrade height does not matter.
	period := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	if period <= 0 {
		period = 300
	}
	coalesce := k.GetConfigInt64(ctx, constants.RescheduleCoalesceBlocks)
	if coalesce < 0 {
		coalesce = 0
	}
	start := ctx.BlockHeight() - 3*period
	if start < 1 {
		start = 1
	}
	end := ctx.BlockHeight() + coalesce + 1

	dropped := 0
	for height := start; height <= end; height++ {
		txs, err := k.GetTxOut(ctx, height)
		if err != nil {
			return dropped, fmt.Errorf("get txout at height %d: %w", height, err)
		}
		if txs == nil || len(txs.TxArray) == 0 {
			continue
		}
		modified := false
		for i, toi := range txs.TxArray {
			if !toi.InHash.Equals(target) {
				continue
			}
			// Match the specific undeliverable XRP outbound precisely, not just
			// by inbound hash, so no unrelated outbound of the same inbound is
			// ever neutralised.
			if !toi.Chain.Equals(common.XRPChain) ||
				!toi.Coin.Asset.Equals(common.XRPAsset) ||
				!toi.ToAddress.Equals(common.Address(xrpStuckToAddress)) {
				continue
			}
			if !toi.OutHash.IsEmpty() {
				// Already signed, observed, or a slot LackSigning already
				// marked when it moved the item; leave it untouched.
				continue
			}
			txs.TxArray[i].OutHash = common.BlankTxID
			modified = true
			dropped++
			ctx.Logger().Info("Migrate21to22 neutralised XRP outbound item",
				"height", height, "in_hash", toi.InHash.String(),
				"to_address", toi.ToAddress.String(), "vault", toi.VaultPubKey.String(),
				"coin", toi.Coin.String())
		}
		if modified {
			if err := k.SetTxOut(ctx, txs); err != nil {
				return dropped, fmt.Errorf("set txout at height %d: %w", height, err)
			}
		}
	}
	return dropped, nil
}

// recreditSecuredXRP mints secured-XRP shares back to owner and restores the
// secured-XRP depth by amount (1e8 units), the mirror of the burn-and-debit the
// original withdrawal performed. It returns the minted share amount, or an
// error (writing nothing) when the owner is unparsable, the secured asset is
// missing, or the mint fails, so the caller can abandon the whole batch.
func recreditSecuredXRP(ctx cosmos.Context, k keeper.Keeper, ownerStr string, amount uint64) (cosmos.Uint, error) {
	owner, err := cosmos.AccAddressFromBech32(ownerStr)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("parse secured XRP owner %q: %w", ownerStr, err)
	}

	asset := common.XRPAsset.GetSecuredAsset()
	pool, err := k.GetSecuredAsset(ctx, asset)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("get secured asset %s: %w", asset, err)
	}

	add := cosmos.NewUint(amount)
	shareSupply := k.GetTotalSupply(ctx, asset)

	// Mirror SecuredAssetMgr.calcMintAmt: shares are proportional to the
	// restored amount at the current depth, or 1:1 when the pool is empty.
	var mintAmt cosmos.Uint
	switch {
	case shareSupply.IsZero() || pool.Depth.IsZero():
		mintAmt = add
	default:
		mintAmt = common.GetUncappedShare(add, pool.Depth, shareSupply)
	}

	coin := common.NewCoin(asset, mintAmt)
	if err := k.MintAndSendToAccount(ctx, owner, coin); err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("mint secured XRP to %s: %w", ownerStr, err)
	}

	pool.Depth = pool.Depth.Add(add)
	k.SetSecuredAsset(ctx, pool)

	return mintAmt, nil
}
