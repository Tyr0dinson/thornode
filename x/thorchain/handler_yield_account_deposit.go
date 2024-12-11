package thorchain

import (
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// YieldAccountDepositHandler is handler to process MsgYieldAccountDeposit
type YieldAccountDepositHandler struct {
	mgr Manager
}

// NewYieldAccountDepositHandler create a new instance of YieldAccountDepositHandler
func NewYieldAccountDepositHandler(mgr Manager) YieldAccountDepositHandler {
	return YieldAccountDepositHandler{
		mgr: mgr,
	}
}

// Run is the main entry point for YieldAccountDepositHandler
func (h YieldAccountDepositHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgYieldAccountDeposit)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgYieldAccountDeposit failed validation", "error", err)
		return nil, err
	}
	err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgYieldAccountDeposit", "error", err)
		return nil, err
	}
	return &cosmos.Result{}, err
}

func (h YieldAccountDepositHandler) validate(ctx cosmos.Context, msg MsgYieldAccountDeposit) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("3.1.0")):
		return h.validateV3_1_0(ctx, msg)
	default:
		return errBadVersion
	}
}

func (h YieldAccountDepositHandler) validateV3_1_0(ctx cosmos.Context, msg MsgYieldAccountDeposit) error {
	return msg.ValidateBasic()
}

func (h YieldAccountDepositHandler) handle(ctx cosmos.Context, msg MsgYieldAccountDeposit) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("3.1.0")):
		return h.handleV3_1_0(ctx, msg)
	default:
		return errBadVersion
	}
}

// handle process MsgYieldAccountDeposit
func (h YieldAccountDepositHandler) handleV3_1_0(ctx cosmos.Context, msg MsgYieldAccountDeposit) error {
	// If depositing the secured asset that matches the yield account, deposit

	// If depositing an asset that doesn't match the yield account, swap first

	return nil
}
