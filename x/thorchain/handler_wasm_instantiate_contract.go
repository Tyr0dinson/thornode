package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmInstantiateContractHandler processes incoming MsgInstantiateContract messages from x/wasm
type WasmInstantiateContractHandler struct {
	mgr Manager
}

// NewWasmInstantiateContractHandler create a new instance of WasmInstantiateContractHandler
func NewWasmInstantiateContractHandler(mgr Manager) WasmInstantiateContractHandler {
	return WasmInstantiateContractHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmInstantiateContractHandler
func (h WasmInstantiateContractHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*wasmtypes.MsgInstantiateContract)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgInstantiateContract failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgInstantiateContract", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmInstantiateContractHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmInstantiateContractHandler) validateV137(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract) error {
	return nil
}

func (h WasmInstantiateContractHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgInstantiateContract", "from", msg.Sender)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmInstantiateContractHandler) handleV137(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgInstantiateContract while THORChain is halted")
	}
	_, err := h.mgr.WasmManager().InstantiateContract(ctx, &msg)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
