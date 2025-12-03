package thorchain

import (
	"fmt"

	se "github.com/cosmos/cosmos-sdk/types/errors"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// IntentOpenHandler handles opening new intents
type IntentOpenHandler struct {
	mgr Manager
}

// NewIntentOpenHandler creates a new instance of IntentOpenHandler
func NewIntentOpenHandler(mgr Manager) IntentOpenHandler {
	return IntentOpenHandler{
		mgr: mgr,
	}
}

// Run executes the intent open handler
func (h IntentOpenHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgIntentOpen)
	if !ok {
		return nil, errInvalidMessage
	}

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgIntentOpen failed validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process MsgIntentOpen", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h IntentOpenHandler) validate(ctx cosmos.Context, msg MsgIntentOpen) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Check if intents are enabled
	if h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentsEnabled) <= 0 {
		return se.ErrUnauthorized.Wrap("intents feature is disabled")
	}

	// Validate high >= low
	if msg.HighTarget.LT(msg.LowTarget) {
		return se.ErrInvalidRequest.Wrap("high target must be greater than or equal to low target")
	}

	// Validate source coin
	if len(msg.Tx.Coins) == 0 {
		return se.ErrInvalidRequest.Wrap("no coins in transaction")
	}
	if len(msg.Tx.Coins) > 1 {
		return se.ErrInvalidRequest.Wrap("intent can only be created with one coin type")
	}

	sourceAsset := msg.Tx.Coins[0].Asset
	targetAsset := msg.TargetAsset

	// At least one asset must have an active pool or be native
	sourcePool, err := h.mgr.Keeper().GetPool(ctx, sourceAsset.GetLayer1Asset())
	if err != nil {
		return ErrInternal(err, "fail to get source pool")
	}
	targetPool, err := h.mgr.Keeper().GetPool(ctx, targetAsset.GetLayer1Asset())
	if err != nil {
		return ErrInternal(err, "fail to get target pool")
	}

	hasActiveSourcePool := !sourcePool.IsEmpty() && sourcePool.Status == PoolAvailable
	hasActiveTargetPool := !targetPool.IsEmpty() && targetPool.Status == PoolAvailable
	isSourceNative := sourceAsset.IsRune() || sourceAsset.IsSecuredAsset() || sourceAsset.IsTradeAsset()
	isTargetNative := targetAsset.IsRune() || targetAsset.IsSecuredAsset() || targetAsset.IsTradeAsset()

	if !hasActiveSourcePool && !hasActiveTargetPool && !isSourceNative && !isTargetNative {
		return se.ErrInvalidRequest.Wrap("at least one of source or target asset must have an active pool or be a native/secured/trade asset")
	}

	return nil
}

func (h IntentOpenHandler) handle(ctx cosmos.Context, msg MsgIntentOpen) error {
	// Get the source coin
	sourceCoin := msg.Tx.Coins[0]

	// Escrow secured/trade assets by withdrawing them from the user
	// This burns the coins/reduces units and decreases pool depth
	// When refunding, the txout system will call Deposit() to restore them
	if sourceCoin.Asset.IsSecuredAsset() {
		_, err := h.mgr.SecuredAssetManager().Withdraw(ctx, sourceCoin.Asset, sourceCoin.Amount, msg.Signer, common.NoAddress, msg.Tx.ID)
		if err != nil {
			return fmt.Errorf("fail to escrow secured asset for intent: %w", err)
		}
		ctx.Logger().Info("escrowed secured asset for intent", "asset", sourceCoin.Asset, "amount", sourceCoin.Amount)
	} else if sourceCoin.Asset.IsTradeAsset() {
		_, err := h.mgr.TradeAccountManager().Withdrawal(ctx, sourceCoin.Asset, sourceCoin.Amount, msg.Signer, common.NoAddress, msg.Tx.ID)
		if err != nil {
			return fmt.Errorf("fail to escrow trade asset for intent: %w", err)
		}
		ctx.Logger().Info("escrowed trade asset for intent", "asset", sourceCoin.Asset, "amount", sourceCoin.Amount)
	}

	// Get configuration values
	ttlBlocks := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentTTLBlocks)
	minFeeBps := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentMinFeeBasisPoints)

	// Create the intent
	intent := Intent{
		TxID:                 msg.Tx.ID,
		Source:               sourceCoin,
		SourceAddress:        msg.Tx.FromAddress,
		TargetAsset:          msg.TargetAsset,
		TargetAddress:        msg.TargetAddress,
		HighTarget:           msg.HighTarget,
		LowTarget:            msg.LowTarget,
		CreateHeight:         ctx.BlockHeight(),
		ExpiryHeight:         ctx.BlockHeight() + ttlBlocks,
		Status:               IntentActive,
		MinFeeBasisPoints:    minFeeBps,
		AffiliateAddress:     msg.AffiliateAddress,
		AffiliateBasisPoints: msg.AffiliateBasisPoints.BigInt().Int64(),
	}

	// Save the intent
	h.mgr.Keeper().SetIntent(ctx, intent)

	// Handle affiliate fee if applicable
	if !msg.AffiliateBasisPoints.IsZero() && !msg.AffiliateAddress.IsEmpty() {
		sourceAsset := sourceCoin.Asset
		targetAsset := msg.TargetAsset

		// Check which asset can pay affiliate fee (must have L1 pool or be RUNE)
		sourcePool, _ := h.mgr.Keeper().GetPool(ctx, sourceAsset.GetLayer1Asset())
		targetPool, _ := h.mgr.Keeper().GetPool(ctx, targetAsset.GetLayer1Asset())

		canChargeSource := sourceAsset.IsRune() || (!sourcePool.IsEmpty() && sourcePool.Status == PoolAvailable)
		canChargeTarget := targetAsset.IsRune() || (!targetPool.IsEmpty() && targetPool.Status == PoolAvailable)

		switch {
		case canChargeSource:
			// Charge affiliate fee on source asset using skimAffiliateFees
			feeAmount := common.GetSafeShare(msg.AffiliateBasisPoints, cosmos.NewUint(10000), sourceCoin.Amount)
			feeCoin := common.NewCoin(sourceAsset, feeAmount)

			if !feeCoin.IsEmpty() && feeCoin.Amount.GT(cosmos.ZeroUint()) {
				affFee, err := skimAffiliateFees(ctx, h.mgr, msg.Tx, msg.Signer, feeCoin, msg.Tx.Memo)
				if err != nil {
					ctx.Logger().Error("fail to process intent affiliate fee", "error", err)
				} else if !affFee.IsZero() {
					// Deduct affiliate fee from escrowed source amount
					intent.Source.Amount = common.SafeSub(intent.Source.Amount, affFee)
					h.mgr.Keeper().SetIntent(ctx, intent)
					ctx.Logger().Info("processed intent affiliate fee on source",
						"asset", sourceAsset, "amount", affFee, "affiliate", msg.AffiliateAddress)
				}
			}
		case canChargeTarget:
			// Target asset can pay affiliate fee - will be handled at solve time
			// The affiliate info is already stored in the intent for use when solving
			ctx.Logger().Info("intent affiliate fee will be charged on target at solve time",
				"target_asset", targetAsset, "affiliate", msg.AffiliateAddress, "bps", msg.AffiliateBasisPoints)
		default:
			// Neither asset qualifies - skip affiliate fee
			ctx.Logger().Info("skipping intent affiliate fee - no eligible asset",
				"source", sourceAsset, "target", targetAsset)
		}
	}

	// Emit event
	evt := NewEventIntentOpen(
		msg.Tx.ID,
		sourceCoin,
		msg.Tx.FromAddress,
		msg.TargetAsset,
		msg.TargetAddress,
		msg.HighTarget,
		msg.LowTarget,
		intent.ExpiryHeight,
		intent.MinFeeBasisPoints,
	)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit intent open event", "error", err)
	}

	return nil
}
