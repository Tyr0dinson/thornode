package thorchain

import (
	"fmt"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// WasmStoreCodeHandler is to process native messages on THORChain
type WasmStoreCodeHandler struct {
	mgr Manager
}

// NewWasmStoreCodeHandler create a new instance of WasmStoreCodeHandler
func NewWasmStoreCodeHandler(mgr Manager) WasmStoreCodeHandler {
	return WasmStoreCodeHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmStoreCodeHandler
func (h WasmStoreCodeHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*MsgStoreCode)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgStoreCode failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgStoreCode", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmStoreCodeHandler) validate(ctx cosmos.Context, msg MsgStoreCode) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmStoreCodeHandler) validateV137(ctx cosmos.Context, msg MsgStoreCode) error {
	// ValidateBasic is executed in message service router
	// Removed for clarity that it is not versioned

	return nil
}

func (h WasmStoreCodeHandler) handle(ctx cosmos.Context, msg MsgStoreCode) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgStoreCode", "from", msg.GetSigners()[0])
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmStoreCodeHandler) handleV137(ctx cosmos.Context, msg MsgStoreCode) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgStoreCode while THORChain is halted")
	}
	_, err := h.mgr.WasmManager().Create(ctx, cosmos.AccAddress(msg.Sender), msg.WASMByteCode)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
