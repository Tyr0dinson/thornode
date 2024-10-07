package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// WasmExecHandler is to process native messages on THORChain
type WasmExecHandler struct {
	mgr Manager
}

// NewWasmExecHandler create a new instance of WasmExecHandler
func NewWasmExecHandler(mgr Manager) WasmExecHandler {
	return WasmExecHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmExecHandler
func (h WasmExecHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*wasmtypes.MsgExecuteContract)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgExec failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgExec", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmExecHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmExecHandler) validateV137(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) error {
	return nil
}

func (h WasmExecHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgExecuteContract", "from", msg.Sender, "contract", msg.Contract)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmExecHandler) handleV137(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgExecuteContract while THORChain is halted")
	}
	_, err := h.mgr.WasmManager().Sudo(ctx,
		cosmos.AccAddress(msg.Contract),
		cosmos.AccAddress(msg.Sender),
		msg.Msg)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
