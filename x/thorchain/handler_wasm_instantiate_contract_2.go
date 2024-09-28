package thorchain

import (
	"fmt"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmInstantiateContract2Handler processes incoming MsgInstantiateContract2 messages from x/wasm
type WasmInstantiateContract2Handler struct {
	mgr Manager
}

// NewWasmInstantiateContract2Handler create a new instance of WasmInstantiateContract2Handler
func NewWasmInstantiateContract2Handler(mgr Manager) WasmInstantiateContract2Handler {
	return WasmInstantiateContract2Handler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmInstantiateContract2Handler
func (h WasmInstantiateContract2Handler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*wasmtypes.MsgInstantiateContract2)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgInstantiateContract2 failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgInstantiateContract2", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmInstantiateContract2Handler) validate(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract2) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.validateV137(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmInstantiateContract2Handler) validateV137(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract2) error {
	return nil
}

func (h WasmInstantiateContract2Handler) handle(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract2) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgInstantiateContract2", "from", msg.Sender)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return h.handleV137(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmInstantiateContract2Handler) handleV137(ctx cosmos.Context, msg wasmtypes.MsgInstantiateContract2) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgInstantiateContract2 while THORChain is halted")
	}
	_, err := h.mgr.WasmManager().InstantiateContract2(ctx, &msg)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
