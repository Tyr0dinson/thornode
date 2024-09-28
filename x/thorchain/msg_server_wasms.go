package thorchain

import (
	"context"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"github.com/cosmos/cosmos-sdk/types/errors"
)

func (ms msgServer) StoreCode(goCtx context.Context, msg *wasmtypes.MsgStoreCode) (*wasmtypes.MsgStoreCodeResponse, error) {
	handler := NewWasmStoreCodeHandler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgStoreCodeResponse{}, err
	}
	return &wasmtypes.MsgStoreCodeResponse{}, nil
}

func (ms msgServer) InstantiateContract(goCtx context.Context, msg *wasmtypes.MsgInstantiateContract) (*wasmtypes.MsgInstantiateContractResponse, error) {
	handler := NewWasmInstantiateContractHandler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgInstantiateContractResponse{}, err
	}
	return &wasmtypes.MsgInstantiateContractResponse{}, nil
}

func (ms msgServer) InstantiateContract2(goCtx context.Context, msg *wasmtypes.MsgInstantiateContract2) (*wasmtypes.MsgInstantiateContract2Response, error) {
	handler := NewWasmInstantiateContract2Handler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgInstantiateContract2Response{}, err
	}
	return &wasmtypes.MsgInstantiateContract2Response{}, nil
}

func (ms msgServer) ExecuteContract(goCtx context.Context, msg *wasmtypes.MsgExecuteContract) (*wasmtypes.MsgExecuteContractResponse, error) {
	handler := NewWasmExecuteContractHandler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgExecuteContractResponse{}, err
	}
	return &wasmtypes.MsgExecuteContractResponse{}, nil
}

func (ms msgServer) MigrateContract(goCtx context.Context, msg *wasmtypes.MsgMigrateContract) (*wasmtypes.MsgMigrateContractResponse, error) {
	handler := NewWasmMigrateContractHandler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgMigrateContractResponse{}, err
	}
	return &wasmtypes.MsgMigrateContractResponse{}, nil
}

func (ms msgServer) UpdateAdmin(goCtx context.Context, msg *wasmtypes.MsgUpdateAdmin) (*wasmtypes.MsgUpdateAdminResponse, error) {
	// Contract admin permissioning handled in Wasm Manager
	return &wasmtypes.MsgUpdateAdminResponse{}, errors.ErrUnauthorized
}

func (ms msgServer) ClearAdmin(goCtx context.Context, msg *wasmtypes.MsgClearAdmin) (*wasmtypes.MsgClearAdminResponse, error) {
	// Contract admin permissioning handled in Wasm Manager
	return &wasmtypes.MsgClearAdminResponse{}, errors.ErrUnauthorized
}

func (ms msgServer) UpdateInstantiateConfig(goCtx context.Context, msg *wasmtypes.MsgUpdateInstantiateConfig) (*wasmtypes.MsgUpdateInstantiateConfigResponse, error) {
	// Contract creation permissioning handled in Wasm Manager
	return &wasmtypes.MsgUpdateInstantiateConfigResponse{}, errors.ErrUnauthorized
}

func (ms msgServer) UpdateParams(goCtx context.Context, msg *wasmtypes.MsgUpdateParams) (*wasmtypes.MsgUpdateParamsResponse, error) {
	handler := NewWasmUpdateParamsHandler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgUpdateParamsResponse{}, err
	}
	return &wasmtypes.MsgUpdateParamsResponse{}, nil
}

func (ms msgServer) SudoContract(goCtx context.Context, msg *wasmtypes.MsgSudoContract) (*wasmtypes.MsgSudoContractResponse, error) {
	handler := NewWasmSudoContractHandler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgSudoContractResponse{}, err
	}
	return &wasmtypes.MsgSudoContractResponse{}, nil
}

func (ms msgServer) PinCodes(goCtx context.Context, msg *wasmtypes.MsgPinCodes) (*wasmtypes.MsgPinCodesResponse, error) {
	// Code pinning handled automatically in Wasm Manager
	return &wasmtypes.MsgPinCodesResponse{}, errors.ErrUnauthorized
}

func (ms msgServer) UnpinCodes(goCtx context.Context, msg *wasmtypes.MsgUnpinCodes) (*wasmtypes.MsgUnpinCodesResponse, error) {
	// Code pinning handled automatically in Wasm Manager
	return &wasmtypes.MsgUnpinCodesResponse{}, errors.ErrUnauthorized
}

func (ms msgServer) StoreAndInstantiateContract(goCtx context.Context, msg *wasmtypes.MsgStoreAndInstantiateContract) (*wasmtypes.MsgStoreAndInstantiateContractResponse, error) {
	handler := NewWasmStoreAndInstantiateContractHandler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgStoreAndInstantiateContractResponse{}, err
	}
	return &wasmtypes.MsgStoreAndInstantiateContractResponse{}, nil
}

func (ms msgServer) RemoveCodeUploadParamsAddresses(goCtx context.Context, msg *wasmtypes.MsgRemoveCodeUploadParamsAddresses) (*wasmtypes.MsgRemoveCodeUploadParamsAddressesResponse, error) {
	// Contract storage permissioning handled in Wasm Manager
	return &wasmtypes.MsgRemoveCodeUploadParamsAddressesResponse{}, errors.ErrUnauthorized
}

func (ms msgServer) AddCodeUploadParamsAddresses(goCtx context.Context, msg *wasmtypes.MsgAddCodeUploadParamsAddresses) (*wasmtypes.MsgAddCodeUploadParamsAddressesResponse, error) {
	// Contract storage permissioning handled in Wasm Manager
	return &wasmtypes.MsgAddCodeUploadParamsAddressesResponse{}, errors.ErrUnauthorized
}

func (ms msgServer) StoreAndMigrateContract(goCtx context.Context, msg *wasmtypes.MsgStoreAndMigrateContract) (*wasmtypes.MsgStoreAndMigrateContractResponse, error) {
	handler := NewWasmStoreAndMigrateContractHandler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgStoreAndMigrateContractResponse{}, err
	}
	return &wasmtypes.MsgStoreAndMigrateContractResponse{}, nil
}

func (ms msgServer) UpdateContractLabel(goCtx context.Context, msg *wasmtypes.MsgUpdateContractLabel) (*wasmtypes.MsgUpdateContractLabelResponse, error) {
	handler := NewWasmUpdateContractLabelHandler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgUpdateContractLabelResponse{}, err
	}
	return &wasmtypes.MsgUpdateContractLabelResponse{}, nil
}
