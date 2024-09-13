package thorchain

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// addSwapDirect adds the swap directly to the swap queue (no order book) - segmented
// out into its own function to allow easier maintenance of original behavior vs order
// book behavior.
func (h DepositHandler) addSwapDirectV131(ctx cosmos.Context, msg MsgSwap) {
	if msg.Tx.Coins.IsEmpty() {
		return
	}
	amt := cosmos.ZeroUint()
	swapSourceAsset := msg.Tx.Coins[0].Asset

	// Check if affiliate fee should be paid out
	if !msg.AffiliateBasisPoints.IsZero() && msg.AffiliateAddress.IsChain(common.THORChain) {
		amt = common.GetSafeShare(
			msg.AffiliateBasisPoints,
			cosmos.NewUint(10000),
			msg.Tx.Coins[0].Amount,
		)
		msg.Tx.Coins[0].Amount = common.SafeSub(msg.Tx.Coins[0].Amount, amt)
	}

	// Queue the main swap
	if err := h.mgr.Keeper().SetSwapQueueItem(ctx, msg, 0); err != nil {
		ctx.Logger().Error("fail to add swap to queue", "error", err)
	}

	// Affiliate fee flow
	if !amt.IsZero() {
		toAddress, err := msg.AffiliateAddress.AccAddress()
		if err != nil {
			ctx.Logger().Error("fail to convert address into AccAddress", "msg", msg.AffiliateAddress, "error", err)
			return
		}

		memo, err := ParseMemoWithTHORNames(ctx, h.mgr.Keeper(), msg.Tx.Memo)
		if err != nil {
			ctx.Logger().Error("fail to parse swap memo", "memo", msg.Tx.Memo, "error", err)
			return
		}
		// since native transaction fee has been charged to inbound from address, thus for affiliated fee , the network doesn't need to charge it again
		coin := common.NewCoin(swapSourceAsset, amt)
		affThorname := memo.GetAffiliateTHORName()

		// PreferredAsset set, update the AffiliateCollector module
		if affThorname != nil && !affThorname.PreferredAsset.IsEmpty() && swapSourceAsset.IsRune() {
			h.updateAffiliateCollector(ctx, coin, msg, affThorname)
			return
		}

		// No PreferredAsset set, normal behavior
		sdkErr := h.mgr.Keeper().SendFromModuleToAccount(ctx, AsgardName, toAddress, common.NewCoins(coin))
		if sdkErr != nil {
			ctx.Logger().Error("fail to send native asset to affiliate", "msg", msg.AffiliateAddress, "error", err, "asset", swapSourceAsset)
		}
	}
}
