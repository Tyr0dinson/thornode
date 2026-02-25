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

	// Block intent creation during ragnarok
	if h.mgr.Keeper().RagnarokInProgress(ctx) {
		return se.ErrUnauthorized.Wrap("cannot create intents during ragnarok")
	}

	// Check if intent already exists (prevent duplicate intents)
	if h.mgr.Keeper().IntentExists(ctx, msg.Tx.ID) {
		return se.ErrInvalidRequest.Wrapf("intent with txid %s already exists", msg.Tx.ID)
	}

	// Validate high >= low
	if msg.HighTarget.LT(msg.LowTarget) {
		return se.ErrInvalidRequest.Wrap("high target must be greater than or equal to low target")
	}

	// Validate coins array before accessing
	if len(msg.Tx.Coins) == 0 {
		return se.ErrInvalidRequest.Wrap("no coins in transaction")
	}

	// Reject amounts below the chain's dust threshold to prevent spam
	sourceCoin := msg.Tx.Coins[0]
	dustThreshold := sourceCoin.Asset.GetLayer1Asset().Chain.DustThreshold()
	if !dustThreshold.IsZero() && sourceCoin.Amount.LTE(dustThreshold) {
		return se.ErrInvalidRequest.Wrapf(
			"source amount %s is below dust threshold %s for %s",
			sourceCoin.Amount, dustThreshold, sourceCoin.Asset.GetLayer1Asset().Chain,
		)
	}

	if msg.Tx.Coins[0].Asset.Equals(msg.TargetAsset) {
		return cosmos.ErrUnknownRequest("source and target assets cannot be the same")
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

	// At least one side must be fee-chargeable (has active pool or is native to THORChain).
	canChargeSource := hasActiveSourcePool || sourceAsset.IsNative()
	canChargeTarget := hasActiveTargetPool || targetAsset.IsNative()
	if !canChargeSource && !canChargeTarget {
		return se.ErrInvalidRequest.Wrap("at least one of source or target asset must be a fee-chargeable asset (has active pool or is native)")
	}

	// Validate combined fee basis points won't consume entire outbound
	minFeeBps := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentMinFeeBasisPoints)
	affiliateBps := msg.AffiliateBasisPoints.BigInt().Int64()
	if minFeeBps+affiliateBps >= int64(constants.MaxBasisPts) {
		return se.ErrInvalidRequest.Wrapf("combined fee basis points (%d + %d = %d) must be less than %d",
			minFeeBps, affiliateBps, minFeeBps+affiliateBps, constants.MaxBasisPts)
	}

	return nil
}

func (h IntentOpenHandler) handle(ctx cosmos.Context, msg MsgIntentOpen) error {
	// Validate coins array before accessing
	if len(msg.Tx.Coins) == 0 {
		return se.ErrInvalidRequest.Wrap("no coins in transaction")
	}

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
		AffiliateBasisPoints: msg.AffiliateBasisPoints.BigInt().Int64(), // Safe: validated in ValidateBasic to be <= MaxBasisPts
	}

	// Save the intent
	h.mgr.Keeper().SetIntent(ctx, intent)

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
		intent.AffiliateAddress,
		intent.AffiliateBasisPoints,
	)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		return fmt.Errorf("fail to emit intent open event: %w", err)
	}

	return nil
}
