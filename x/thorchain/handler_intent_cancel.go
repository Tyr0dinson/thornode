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

	// Determine if we should refund or abandon based on asset type.
	sourcePool, err := h.mgr.Keeper().GetPool(ctx, intent.Source.Asset.GetLayer1Asset())
	if err != nil {
		return fmt.Errorf("fail to get pool for cancel refund decision: %w", err)
	}
	isPoolAsset := !sourcePool.IsEmpty()
	isNativeAsset := intent.Source.Asset.IsRune() || intent.Source.Asset.IsSecuredAsset() || intent.Source.Asset.IsTradeAsset()
	shouldRefund := isPoolAsset || isNativeAsset

	var refundCoin common.Coin
	abandoned := false

	if shouldRefund {
		// Refund the source asset back to the source address
		// Include any coins sent with the cancel tx (same asset) in the refund,
		// but only for non-secured/trade assets. The deposit handler skips escrowing
		// secured/trade coins for cancel messages, so including them would create
		// units from nothing.
		refundCoin = intent.Source
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
	} else {
		// Abandon (burn) the source asset by updating the vault
		refundCoin = common.NewCoin(intent.Source.Asset, cosmos.ZeroUint())
		abandoned = true

		// Get active vaults and find the one with sufficient balance and closest amount
		vaults, err := h.mgr.Keeper().GetAsgardVaultsByStatus(ctx, ActiveVault)
		if err != nil {
			return fmt.Errorf("fail to get active vaults: %w", err)
		}
		if len(vaults) == 0 {
			return fmt.Errorf("no active vaults available")
		}

		// Find vault with sufficient balance and closest amount to the source coin
		var selectedVault *Vault
		var closestDiff cosmos.Uint
		for i := range vaults {
			vaultBalance := vaults[i].GetCoin(intent.Source.Asset).Amount

			// Check if vault has sufficient balance
			if vaultBalance.GTE(intent.Source.Amount) {
				diff := common.SafeSub(vaultBalance, intent.Source.Amount)

				// Select this vault if it's the first valid one, has a closer balance,
				// or has the same balance but lower PubKey (deterministic tiebreaker)
				if selectedVault == nil || diff.LT(closestDiff) ||
					(diff.Equal(closestDiff) && vaults[i].PubKey.String() < selectedVault.PubKey.String()) {
					selectedVault = &vaults[i]
					closestDiff = diff
				}
			}
		}

		if selectedVault == nil {
			return fmt.Errorf("no vault found with sufficient balance for asset %s (need %s)", intent.Source.Asset, intent.Source.Amount)
		}

		// Subtract the abandoned amount from the selected vault
		selectedVault.SubFunds(common.NewCoins(intent.Source))

		// Save the updated vault
		if err = h.mgr.Keeper().SetVault(ctx, *selectedVault); err != nil {
			return fmt.Errorf("fail to save vault: %w", err)
		}

		ctx.Logger().Info("abandoned intent source asset (non-pool, non-native)",
			"txid", intent.TxID,
			"asset", intent.Source.Asset,
			"amount", intent.Source.Amount,
			"vault", selectedVault.PubKey,
		)

		// Mark the cancel tx voter as done since no outbound is scheduled.
		// Without this, the voter created in handler_deposit.go stays open forever
		// because TxIntentCancel.HasOutbound() returns true (for the refund path).
		voter, err := h.mgr.Keeper().GetObservedTxInVoter(ctx, msg.Tx.ID)
		if err != nil {
			return fmt.Errorf("fail to get voter for cancel tx: %w", err)
		}
		voter.SetDone()
		h.mgr.Keeper().SetObservedTxInVoter(ctx, voter)
	}

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
		abandoned,
	)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		return fmt.Errorf("fail to emit intent cancel event: %w", err)
	}

	return nil
}
