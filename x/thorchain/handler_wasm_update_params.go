package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmUpdateParamsHandler processes incoming MsgUpdateParams messages from x/wasm
type WasmUpdateParamsHandler struct {
	mgr Manager
}

// NewWasmUpdateParamsHandler create a new instance of WasmUpdateParamsHandler
func NewWasmUpdateParamsHandler(mgr Manager) WasmUpdateParamsHandler {
	return WasmUpdateParamsHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmUpdateParamsHandler
func (h WasmUpdateParamsHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*wasmtypes.MsgUpdateParams)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgUpdateParams failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgUpdateParams", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmUpdateParamsHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgUpdateParams) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmUpdateParamsHandler) validateV137(ctx cosmos.Context, msg wasmtypes.MsgUpdateParams) error {
	return nil
}

func (h WasmUpdateParamsHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgUpdateParams) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgUpdateParams", "from", msg.Authority)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmUpdateParamsHandler) handleV137(ctx cosmos.Context, msg wasmtypes.MsgUpdateParams) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgUpdateParams while THORChain is halted")
	}
	_, err := h.mgr.WasmManager().UpdateParams(ctx, &msg)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
