package thorchain

import (
	"context"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
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

func (ms msgServer) SudoContract(goCtx context.Context, msg *wasmtypes.MsgSudoContract) (*wasmtypes.MsgSudoContractResponse, error) {
	handler := NewWasmSudoContractHandler(ms.mgr)
	_, err := externalHandler(goCtx, handler, msg)
	if err != nil {
		return &wasmtypes.MsgSudoContractResponse{}, err
	}
	return &wasmtypes.MsgSudoContractResponse{}, nil
}
