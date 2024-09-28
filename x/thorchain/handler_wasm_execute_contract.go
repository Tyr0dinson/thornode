package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmExecuteContractHandler processes incoming MsgExecuteContract messages from x/wasm
type WasmExecuteContractHandler struct {
	mgr Manager
}

// NewWasmExecuteContractHandler create a new instance of WasmExecuteContractHandler
func NewWasmExecuteContractHandler(mgr Manager) WasmExecuteContractHandler {
	return WasmExecuteContractHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmExecuteContractHandler
func (h WasmExecuteContractHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*wasmtypes.MsgExecuteContract)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgExecuteContract failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgExecuteContract", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmExecuteContractHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmExecuteContractHandler) validateV137(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) error {
	return nil
}

func (h WasmExecuteContractHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgExecuteContract", "from", msg.Sender)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmExecuteContractHandler) handleV137(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgExecuteContract while THORChain is halted")
	}
	_, err := h.mgr.WasmManager().ExecuteContract(ctx, &msg)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
