package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// BatchModifyLimitSwapHandler is the handler to process MsgBatchModifyLimitSwap.
type BatchModifyLimitSwapHandler struct {
	mgr Manager
}

// NewBatchModifyLimitSwapHandler creates a new instance of BatchModifyLimitSwapHandler.
func NewBatchModifyLimitSwapHandler(mgr Manager) BatchModifyLimitSwapHandler {
	return BatchModifyLimitSwapHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for BatchModifyLimitSwapHandler.
func (h BatchModifyLimitSwapHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgBatchModifyLimitSwap)
	if !ok {
		return nil, errInvalidMessage
	}

	err := h.validate(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("MsgBatchModifyLimitSwap failed validation", "error", err)
		return nil, err
	}

	err = h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgBatchModifyLimitSwap", "error", err)
		return nil, err
	}

	return &cosmos.Result{}, err
}

func (h BatchModifyLimitSwapHandler) validate(ctx cosmos.Context, msg MsgBatchModifyLimitSwap) error {
	return msg.ValidateBasic()
}

func (h BatchModifyLimitSwapHandler) handle(ctx cosmos.Context, msg MsgBatchModifyLimitSwap) error {
	// Process each operation atomically - if any fails, the entire batch fails
	for i, op := range msg.Operations {
		// Create a synthetic single modify message for each operation
		singleMsg := NewMsgModifyLimitSwap(
			op.From,
			op.Source,
			op.Target,
			op.ModifiedTargetAmount,
			msg.Signer,
			common.EmptyAsset,
			cosmos.ZeroUint(),
		)

		// Reuse the single modify handler logic
		singleHandler := NewModifyLimitSwapHandler(h.mgr)
		if err := singleHandler.handle(ctx, *singleMsg); err != nil {
			return fmt.Errorf("operation %d failed: %w", i, err)
		}

		// Emit event for each operation
		modEvent := NewEventModifyLimitSwap(op.From, op.Source, op.Target, op.ModifiedTargetAmount)
		if err := h.mgr.EventMgr().EmitEvent(ctx, modEvent); err != nil {
			ctx.Logger().Error("fail to emit modEvent event", "error", err)
		}
	}

	return nil
}

// BatchModifyLimitSwapAnteHandler is the ante handler for MsgBatchModifyLimitSwap
func BatchModifyLimitSwapAnteHandler(ctx cosmos.Context, v semver.Version, k keeper.Keeper, msg MsgBatchModifyLimitSwap) (cosmos.Context, error) {
	return ctx, k.DeductModifyLimitSwapFee(ctx, msg.Signer, true)
}
