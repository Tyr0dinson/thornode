package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// WasmUpdateContractLabelHandler is to process native messages on THORChain
type WasmUpdateContractLabelHandler struct {
	mgr Manager
}

// NewWasmUpdateContractLabelHandler create a new instance of WasmUpdateContractLabelHandler
func NewWasmUpdateContractLabelHandler(mgr Manager) WasmUpdateContractLabelHandler {
	return WasmUpdateContractLabelHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmUpdateContractLabelHandler
func (h WasmUpdateContractLabelHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*wasmtypes.MsgUpdateContractLabel)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgUpdateContractLabel failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgUpdateContractLabel", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmUpdateContractLabelHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgUpdateContractLabel) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmUpdateContractLabelHandler) validateV137(ctx cosmos.Context, msg wasmtypes.MsgUpdateContractLabel) error {
	return nil
}

func (h WasmUpdateContractLabelHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgUpdateContractLabel) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgUpdateContractLabel", "from", msg.Sender)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmUpdateContractLabelHandler) handleV137(ctx cosmos.Context, msg wasmtypes.MsgUpdateContractLabel) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgUpdateContractLabel while THORChain is halted")
	}
	_, err := h.mgr.WasmManager().UpdateContractLabel(ctx, &msg)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
