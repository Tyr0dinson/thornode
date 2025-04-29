package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// LpDepositHandler is handler to process MsgLpDeposit
type LpDepositHandler struct {
	mgr Manager
}

// NewLpDepositHandler create a new instance of LpDepositHandler
func NewLpDepositHandler(mgr Manager) LpDepositHandler {
	return LpDepositHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for LpDepositHandler
func (h LpDepositHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgLpDeposit)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgLpDeposit failed validation", "error", err)
		return nil, err
	}
	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgLpDeposit", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, err
}

func (h LpDepositHandler) validate(ctx cosmos.Context, msg MsgLpDeposit) error {
	// Add validation, eg to ensure that Asset.Asset.IsSecuredAsset()
	return msg.ValidateBasic()
}

func (h LpDepositHandler) handle(ctx cosmos.Context, msg MsgLpDeposit) error {
	handler := NewAddLiquidityHandler(h.mgr)
	// Instead of supporting multiple MsgDeposit in 1 tx, we call the AddLiquidity handler twice,
	// for a more precise dual-execution that doesn't affect other MsgDeposit handled routes

	handler.Run(ctx, &MsgAddLiquidity{
		Asset:        msg.Asset.Asset,
		AssetAmount:  msg.Asset.Amount,
		AssetAddress: common.Address(msg.Signer),
		RuneAddress:  common.Address(msg.Signer),
		// AddLiquidityHandler must be updated to burn secured assets from signer's accoun during deposit (and opposite MsgLpWithdraw must mint and transfer to signer)
		// ... rest
	})
	handler.Run(ctx, &MsgAddLiquidity{
		RuneAmount:   msg.Rune,
		AssetAddress: common.Address(msg.Signer),
		RuneAddress:  common.Address(msg.Signer),
		// ... rest
	})

	return nil
}
