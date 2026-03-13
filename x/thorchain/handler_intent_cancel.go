package thorchain

import (
	"fmt"

	se "github.com/cosmos/cosmos-sdk/types/errors"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// IntentCancelHandler handles cancelling intents
type IntentCancelHandler struct {
	mgr Manager
}

// NewIntentCancelHandler creates a new instance of IntentCancelHandler
func NewIntentCancelHandler(mgr Manager) IntentCancelHandler {
	return IntentCancelHandler{
		mgr: mgr,
	}
}

// Run executes the intent cancel handler
func (h IntentCancelHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgIntentCancel)
	if !ok {
		return nil, errInvalidMessage
	}

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgIntentCancel failed validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process MsgIntentCancel", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h IntentCancelHandler) validate(ctx cosmos.Context, msg MsgIntentCancel) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Check if intent exists
	if !h.mgr.Keeper().IntentExists(ctx, msg.IntentTxID) {
		return se.ErrUnknownRequest.Wrapf("intent %s does not exist", msg.IntentTxID)
	}

	// Get the intent
	intent, err := h.mgr.Keeper().GetIntent(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get intent")
	}

	// Verify the intent is active
	if intent.Status != IntentActive {
		return se.ErrUnauthorized.Wrapf("intent is not active, status: %s", intent.Status.String())
	}

	// Only the source address can cancel the intent
	// Use the FromAddress from the observed tx (the actual user), not msg.Signer (the observer node)
	if !intent.SourceAddress.Equals(msg.Tx.FromAddress) {
		return se.ErrUnauthorized.Wrap("only the intent creator can cancel it")
	}

	return nil
}

func (h IntentCancelHandler) handle(ctx cosmos.Context, msg MsgIntentCancel) error {
	// Get the intent
	intent, err := h.mgr.Keeper().GetIntent(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get intent")
	}

	// Determine if we should refund directly based on asset type.
	// Use the same predicate as expiry: pool must exist AND be available.
	sourcePool, err := h.mgr.Keeper().GetPool(ctx, intent.Source.Asset.GetLayer1Asset())
	if err != nil {
		return fmt.Errorf("fail to get pool for cancel refund decision: %w", err)
	}
	isPoolAsset := !sourcePool.IsEmpty() && sourcePool.Status == PoolAvailable
	isNativeAsset := intent.Source.Asset.IsNative()
	shouldRefund := isPoolAsset || isNativeAsset

	if !shouldRefund {
		// Non-pool, non-native source asset: route to auction instead of
		// abandoning immediately. This gives the owner (or anyone) the
		// opportunity to bid gas-asset funds and recover the escrowed source.
		if err := h.mgr.IntentMgr().StartIntentAuction(ctx, intent); err != nil {
			return fmt.Errorf("fail to start auction for cancelled intent: %w", err)
		}

		// Emit cancel event indicating the intent moved to auction
		evt := NewEventIntentCancel(
			intent.TxID,
			common.NewCoin(intent.Source.Asset, cosmos.ZeroUint()),
			intent.SourceAddress,
			false,
		)
		if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
			return fmt.Errorf("fail to emit intent cancel event: %w", err)
		}

		return nil
	}

	// Refund the source asset back to the source address.
	// Include any coins sent with the cancel tx (same asset) in the refund,
	// but only for non-secured/trade assets. The deposit handler skips escrowing
	// secured/trade coins for cancel messages, so including them would create
	// units from nothing.
	refundCoin := intent.Source
	if !intent.Source.Asset.IsSecuredAsset() && !intent.Source.Asset.IsTradeAsset() {
		for _, coin := range msg.Tx.Coins {
			if coin.Asset.Equals(intent.Source.Asset) && !coin.Amount.IsZero() {
				refundCoin.Amount = refundCoin.Amount.Add(coin.Amount)
			}
		}
	}

	// For RUNE, use Asgard module (where intent RUNE is escrowed)
	// For other assets, default to Asgard
	moduleName := ""
	if intent.Source.Asset.IsRune() {
		moduleName = AsgardName
	}

	// Schedule the txout for refund
	toi := TxOutItem{
		Chain:      intent.Source.Asset.GetChain(),
		ToAddress:  intent.SourceAddress,
		Coin:       refundCoin,
		Memo:       fmt.Sprintf("REFUND:%s", intent.TxID),
		InHash:     intent.TxID,
		ModuleName: moduleName,
	}

	ok, err := h.mgr.TxOutStore().TryAddTxOutItem(ctx, h.mgr, toi, cosmos.ZeroUint())
	if err != nil {
		return fmt.Errorf("fail to add outbound tx: %w", err)
	}
	if !ok {
		return fmt.Errorf("fail to schedule refund txout")
	}

	ctx.Logger().Info("scheduled intent refund",
		"txid", intent.TxID,
		"asset", refundCoin.Asset,
		"amount", refundCoin.Amount,
		"to", intent.SourceAddress,
	)

	// Refund any pending solves for this intent.
	// Return the error to trigger CacheContext rollback and retry next block,
	// preventing solve records from being deleted without successful refunds.
	if err := h.mgr.IntentMgr().RefundIntentSolves(ctx, intent); err != nil {
		return fmt.Errorf("fail to refund intent solves on cancel: %w", err)
	}

	// Update intent status to cancelled
	intent.Status = IntentCancelled
	h.mgr.Keeper().SetIntent(ctx, intent)

	// Emit cancel event
	evt := NewEventIntentCancel(
		intent.TxID,
		refundCoin,
		intent.SourceAddress,
		false,
	)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		return fmt.Errorf("fail to emit intent cancel event: %w", err)
	}

	return nil
}
