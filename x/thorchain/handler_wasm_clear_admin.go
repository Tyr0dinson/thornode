package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// WasmClearAdminHandler is to process native messages on THORChain
type WasmClearAdminHandler struct {
	mgr Manager
}

// NewWasmClearAdminHandler create a new instance of WasmClearAdminHandler
func NewWasmClearAdminHandler(mgr Manager) WasmClearAdminHandler {
	return WasmClearAdminHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmClearAdminHandler
func (h WasmClearAdminHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*wasmtypes.MsgClearAdmin)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgClearAdmin failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgClearAdmin", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmClearAdminHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgClearAdmin) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmClearAdminHandler) validateV137(ctx cosmos.Context, msg wasmtypes.MsgClearAdmin) error {
	return nil
}

func (h WasmClearAdminHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgClearAdmin) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgClearAdmin", "from", msg.Sender)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmClearAdminHandler) handleV137(ctx cosmos.Context, msg wasmtypes.MsgClearAdmin) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgClearAdmin while THORChain is halted")
	}
	_, err := h.mgr.WasmManager().ClearAdmin(ctx, &msg)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
