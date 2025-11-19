package thorchain

import (
	se "github.com/cosmos/cosmos-sdk/types/errors"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// IntentSolveHandler handles solving intents
type IntentSolveHandler struct {
	mgr Manager
}

// NewIntentSolveHandler creates a new instance of IntentSolveHandler
func NewIntentSolveHandler(mgr Manager) IntentSolveHandler {
	return IntentSolveHandler{
		mgr: mgr,
	}
}

// Run executes the intent solve handler
func (h IntentSolveHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgIntentSolve)
	if !ok {
		return nil, errInvalidMessage
	}

	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgIntentSolve failed validation", "error", err)
		return nil, err
	}

	if err := h.handle(ctx, *msg); err != nil {
		ctx.Logger().Error("fail to process MsgIntentSolve", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, nil
}

func (h IntentSolveHandler) validate(ctx cosmos.Context, msg MsgIntentSolve) error {
	if err := msg.ValidateBasic(); err != nil {
		return err
	}

	// Check if intents are enabled
	if h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentsEnabled) <= 0 {
		return se.ErrUnauthorized.Wrap("intents feature is disabled")
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

	// Check if intent has expired
	if ctx.BlockHeight() >= intent.ExpiryHeight {
		return se.ErrUnauthorized.Wrapf("intent has expired at height %d", intent.ExpiryHeight)
	}

	// Validate solve transaction has exactly one coin (the target asset)
	if len(msg.Tx.Coins) == 0 {
		return se.ErrInvalidRequest.Wrap("no coins in solve transaction")
	}
	if len(msg.Tx.Coins) > 1 {
		return se.ErrInvalidRequest.Wrap("solve can only provide one coin type")
	}

	// Verify the coin is the target asset
	if !msg.Tx.Coins[0].Asset.Equals(intent.TargetAsset) {
		return se.ErrInvalidRequest.Wrapf("solve asset %s does not match intent target asset %s", msg.Tx.Coins[0].Asset, intent.TargetAsset)
	}

	// Verify the offered amount meets minimum (at least low target)
	offeredAmount := msg.Tx.Coins[0].Amount
	if offeredAmount.LT(intent.LowTarget) {
		return se.ErrInvalidRequest.Wrapf("offered amount %s is less than low target %s", offeredAmount, intent.LowTarget)
	}

	return nil
}

func (h IntentSolveHandler) handle(ctx cosmos.Context, msg MsgIntentSolve) error {
	// Get the intent
	intent, err := h.mgr.Keeper().GetIntent(ctx, msg.IntentTxID)
	if err != nil {
		return ErrInternal(err, "fail to get intent")
	}

	// Get the offered amount
	offeredAmount := msg.Tx.Coins[0].Amount

	// Calculate match height using linear interpolation
	// match_height = current_height + ((high - offered) * slide_blocks / (high - low))
	currentHeight := ctx.BlockHeight()
	slideBlocks := h.mgr.Keeper().GetConfigInt64(ctx, constants.IntentPriceSlideBlocks)

	var matchHeight int64
	switch {
	case offeredAmount.GTE(intent.HighTarget):
		// Offer meets or exceeds high target - execute immediately
		matchHeight = currentHeight
	case intent.HighTarget.Equal(intent.LowTarget):
		// High and low are equal - any valid offer executes immediately
		matchHeight = currentHeight
	default:
		// Calculate interpolated match height
		// Using: match_height = current_height + ((high - offered) * slide_blocks / (high - low))
		highMinusOffered := common.SafeSub(intent.HighTarget, offeredAmount)
		highMinusLow := common.SafeSub(intent.HighTarget, intent.LowTarget)

		// Calculate the delay: (high - offered) * slide_blocks / (high - low)
		delay := highMinusOffered.Mul(cosmos.NewUint(uint64(slideBlocks)))
		delay = delay.Quo(highMinusLow)

		matchHeight = currentHeight + int64(delay.Uint64())

		// Cap match height at expiry height
		if matchHeight > intent.ExpiryHeight {
			matchHeight = intent.ExpiryHeight
		}
	}

	// Save the solve bid for matching in EndBlock
	solve := IntentSolve{
		SolveTxID:     msg.Tx.ID,
		IntentTxID:    msg.IntentTxID,
		SolverAddress: msg.Tx.FromAddress,
		TargetCoin:    msg.Tx.Coins[0],
		VaultPubKey:   intent.VaultPubKey,
		SubmitHeight:  ctx.BlockHeight(),
		MatchHeight:   matchHeight,
	}
	h.mgr.Keeper().SetIntentSolve(ctx, solve)

	ctx.Logger().Info("intent solve bid scheduled",
		"intent_txid", msg.IntentTxID,
		"solve_txid", msg.Tx.ID,
		"match_height", matchHeight,
		"offered_amount", offeredAmount,
	)

	// Emit solve event
	evt := NewEventIntentSolve(
		msg.Tx.ID,
		msg.IntentTxID,
		msg.Tx.FromAddress,
		msg.Tx.Coins[0].Amount,
		matchHeight,
	)
	if err := h.mgr.EventMgr().EmitEvent(ctx, evt); err != nil {
		ctx.Logger().Error("fail to emit intent solve event", "error", err)
	}

	return nil
}
