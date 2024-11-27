package thorchain

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// WasmSudoContractHandler processes incoming MsgSudoContract messages from x/wasm
type WasmSudoContractHandler struct {
	mgr Manager
}

// NewWasmSudoContractHandler create a new instance of WasmSudoContractHandler
func NewWasmSudoContractHandler(mgr Manager) WasmSudoContractHandler {
	return WasmSudoContractHandler{
		mgr: mgr,
	}
}

// Run is the main entry of WasmSudoContractHandler
func (h WasmSudoContractHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*wasmtypes.MsgSudoContract)
	if !ok {
		return nil, errInvalidMessage
	}
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error("MsgSudoContract failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to process MsgSudoContract", "error", err)
		return nil, err
	}
	return result, nil
}

func (h WasmSudoContractHandler) validate(ctx cosmos.Context, msg wasmtypes.MsgSudoContract) error {
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("3.1.0")):
		return h.validateV3_1_0(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmSudoContractHandler) validateV3_1_0(_ cosmos.Context, _ wasmtypes.MsgSudoContract) error {
	// ValidateBasic is executed in message service router
	// Removed for clarity that it is not versioned
	return nil
}

func (h WasmSudoContractHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgSudoContract) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgSudoContract", "from", msg.Authority)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("3.1.0")):
		return h.handleV3_1_0(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmSudoContractHandler) handleV3_1_0(ctx cosmos.Context, msg wasmtypes.MsgSudoContract) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgSudoContract while THORChain is halted")
	}

	authorityAddr, err := cosmos.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "authority")
	}

	contractAddr, err := cosmos.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	_, err = h.mgr.WasmManager().SudoContract(ctx, contractAddr, authorityAddr, msg.Msg)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
