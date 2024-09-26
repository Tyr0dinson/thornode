package thorchain

import (
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

// LpRedeemHandler is handler to process MsgLpRedeem
type LpRedeemHandler struct {
	mgr Manager
}

// NewLpRedeemHandler create a new instance of LpRedeemHandler
func NewLpRedeemHandler(mgr Manager) LpRedeemHandler {
	return LpRedeemHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for LpRedeemHandler
func (h LpRedeemHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgLpRedeem)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgLpRedeem failed validation", "error", err)
		return nil, err
	}
	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgLpRedeem", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, err
}

func (h LpRedeemHandler) validate(ctx cosmos.Context, msg MsgLpRedeem) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h LpRedeemHandler) validateV137(ctx cosmos.Context, msg MsgLpRedeem) error {
	return msg.ValidateBasic()
}

func (h LpRedeemHandler) handle(ctx cosmos.Context, msg MsgLpRedeem) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return errBadVersion
	}
}

// handle process MsgLpRedeem
func (h LpRedeemHandler) handleV137(ctx cosmos.Context, msg MsgLpRedeem) error {
	_, err := h.mgr.LpTokenManager().Redeem(ctx, msg.Asset, msg.AccountAddress, msg.Amount, msg.TxID)
	if err != nil {
		ctx.Logger().Error("fail to handle LP Token Redeem", "error", err)
		return err
	}
	return nil
}
