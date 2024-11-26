package thorchain

import (
	"fmt"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/blang/semver"
	sdk "github.com/cosmos/cosmos-sdk/types"

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
	case version.GTE(semver.MustParse("3.1.0")):
		return h.validateV3_1_0(ctx, msg)
	default:
		return errInvalidVersion
	}
}

func (h WasmExecuteContractHandler) validateV3_1_0(_ cosmos.Context, _ wasmtypes.MsgExecuteContract) error {
	return nil
}

func (h WasmExecuteContractHandler) handle(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) (*cosmos.Result, error) {
	ctx.Logger().Info("receive MsgExecuteContract", "from", msg.Sender)
	version := h.mgr.GetVersion()
	switch {
	case version.GTE(semver.MustParse("3.1.0")):
		return h.handleV3_1_0(ctx, msg)
	default:
		return nil, errInvalidVersion
	}
}

func (h WasmExecuteContractHandler) handleV3_1_0(ctx cosmos.Context, msg wasmtypes.MsgExecuteContract) (*cosmos.Result, error) {
	if h.mgr.Keeper().IsChainHalted(ctx, common.THORChain) {
		return nil, fmt.Errorf("unable to use MsgExecuteContract while THORChain is halted")
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}
	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	_, err = h.mgr.WasmManager().ExecuteContract(ctx, contractAddr, senderAddr, msg.Msg, msg.Funds)
	if err != nil {
		return &cosmos.Result{}, err
	}

	return &cosmos.Result{}, nil
}
