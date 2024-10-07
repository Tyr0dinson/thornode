package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// WasmMigrateContractHandler is to process native messages on THORChain
type WasmMigrateContractHandler struct {
	mgr Manager
}

// NewWasmMigrateContractHandler create a new instance of WasmMigrateContractHandler
func NewWasmMigrateContractHandler(mgr Manager) WasmMigrateContractHandler {
	return WasmMigrateContractHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmMigrateContractHandler
func (h WasmMigrateContractHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*wasmtypes.MsgMigrateContract)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgMigrateContract failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgMigrateContract", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmMigrateContractHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgMigrateContract) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmMigrateContractHandler) validateV137(ctx cosmos.Context, msg wasmtypes.MsgMigrateContract) error {
	return nil
}

func (h WasmMigrateContractHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgMigrateContract) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgMigrateContract", "from", msg.Sender)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmMigrateContractHandler) handleV137(ctx cosmos.Context, msg wasmtypes.MsgMigrateContract) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgMigrateContract while THORChain is halted")
	}
	_, err := h.mgr.WasmManager().MigrateContract(ctx, &msg)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
