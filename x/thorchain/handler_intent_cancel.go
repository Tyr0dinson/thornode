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
	// Handle both proper bech32 conversion and direct string casting (for tests)
	signerAsAddress := common.Address(string(msg.Signer))
	sourceAccAddr, err := cosmos.AccAddressFromBech32(string(intent.SourceAddress))
	signerMatches := intent.SourceAddress.Equals(signerAsAddress) || (err == nil && sourceAccAddr.Equals(msg.Signer))

	if !signerMatches {
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

	// Determine if we should refund or abandon based on asset type
	sourcePool, _ := h.mgr.Keeper().GetPool(ctx, intent.Source.Asset.GetLayer1Asset())
	isPoolAsset := !sourcePool.IsEmpty()
	isNativeAsset := intent.Source.Asset.IsRune() || intent.Source.Asset.IsSecuredAsset()
	shouldRefund := isPoolAsset || isNativeAsset

	var refundCoin common.Coin
	abandoned := false

	if shouldRefund {
		// Refund the source asset back to the source address
		refundCoin = intent.Source

		// Schedule the txout for refund
		toi := TxOutItem{
			Chain:       intent.Source.Asset.GetChain(),
			ToAddress:   intent.SourceAddress,
			VaultPubKey: intent.VaultPubKey,
			Coin:        intent.Source,
			Memo:        fmt.Sprintf("REFUND:%s", intent.TxID),
			InHash:      intent.TxID,
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
	} else {
		// Abandon (burn) the source asset by updating the vault
		refundCoin = common.NewCoin(intent.Source.Asset, cosmos.ZeroUint())
		abandoned = true

		// Get the vault and subtract the abandoned coins
		vault, err := h.mgr.Keeper().GetVault(ctx, intent.VaultPubKey)
		if err != nil {
			return fmt.Errorf("fail to get vault: %w", err)
		}

		// Subtract the abandoned amount from the vault
		vault.SubFunds(common.NewCoins(intent.Source))

		// Save the updated vault
		if err := h.mgr.Keeper().SetVault(ctx, vault); err != nil {
			return fmt.Errorf("fail to save vault: %w", err)
		}

		ctx.Logger().Info("abandoned intent source asset (non-pool, non-native)",
			"txid", intent.TxID,
			"asset", intent.Source.Asset,
			"amount", intent.Source.Amount,
			"vault", intent.VaultPubKey,
		)
	}

	// Update intent status to cancelled
	intent.Status = IntentCancelled
	h.mgr.Keeper().SetIntent(ctx, intent)

	// Emit cancel event
	evt := NewEventIntentCancel(
		intent.TxID,
		refundCoin,
		intent.SourceAddress,
		abandoned,
	)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit intent cancel event", "error", err)
	}

	return nil
}
