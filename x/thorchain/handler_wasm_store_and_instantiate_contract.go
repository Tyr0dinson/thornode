package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmStoreAndInstantiateContractHandler processes incoming MsgStoreAndInstantiateContract messages from x/wasm
type WasmStoreAndInstantiateContractHandler struct {
	mgr Manager
}

// NewWasmStoreAndInstantiateContractHandler create a new instance of WasmStoreAndInstantiateContractHandler
func NewWasmStoreAndInstantiateContractHandler(mgr Manager) WasmStoreAndInstantiateContractHandler {
	return WasmStoreAndInstantiateContractHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmStoreAndInstantiateContractHandler
func (h WasmStoreAndInstantiateContractHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*wasmtypes.MsgStoreAndInstantiateContract)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgStoreAndInstantiateContract failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgStoreAndInstantiateContract", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmStoreAndInstantiateContractHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgStoreAndInstantiateContract) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmStoreAndInstantiateContractHandler) validateV137(ctx cosmos.Context, msg wasmtypes.MsgStoreAndInstantiateContract) error {
	return nil
}

func (h WasmStoreAndInstantiateContractHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgStoreAndInstantiateContract) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgStoreAndInstantiateContract", "from", msg.Authority)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmStoreAndInstantiateContractHandler) handleV137(ctx cosmos.Context, msg wasmtypes.MsgStoreAndInstantiateContract) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgStoreAndInstantiateContract while THORChain is halted")
	}
	_, err := h.mgr.WasmManager().StoreAndInstantiateContract(ctx, &msg)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
