package thorchain

import (
	"context"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (ms msgServer) StoreCode(goCtx context.Context, msg *wasmtypes.MsgStoreCode) (*wasmtypes.MsgStoreCodeResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return runTracedHandler(
		ctx,
		ms.mgr.Tracer(),
		handlerTrace{path: pathWasm, msg: msg},
		func() (*wasmtypes.MsgStoreCodeResponse, error) {
			return NewWasmStoreCodeHandler(ms.mgr).Run(ctx, msg)
		},
	)
}

func (ms msgServer) InstantiateContract(goCtx context.Context, msg *wasmtypes.MsgInstantiateContract) (*wasmtypes.MsgInstantiateContractResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return runTracedHandler(
		ctx,
		ms.mgr.Tracer(),
		handlerTrace{path: pathWasm, msg: msg},
		func() (*wasmtypes.MsgInstantiateContractResponse, error) {
			return NewWasmInstantiateContractHandler(ms.mgr).Run(ctx, msg)
		},
	)
}

func (ms msgServer) InstantiateContract2(goCtx context.Context, msg *wasmtypes.MsgInstantiateContract2) (*wasmtypes.MsgInstantiateContract2Response, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return runTracedHandler(
		ctx,
		ms.mgr.Tracer(),
		handlerTrace{path: pathWasm, msg: msg},
		func() (*wasmtypes.MsgInstantiateContract2Response, error) {
			return NewWasmInstantiateContract2Handler(ms.mgr).Run(ctx, msg)
		},
	)
}

func (ms msgServer) ExecuteContract(goCtx context.Context, msg *wasmtypes.MsgExecuteContract) (*wasmtypes.MsgExecuteContractResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return runTracedHandler(
		ctx,
		ms.mgr.Tracer(),
		handlerTrace{path: pathWasm, msg: msg},
		func() (*wasmtypes.MsgExecuteContractResponse, error) {
			return NewWasmExecuteContractHandler(ms.mgr).Run(ctx, msg)
		},
	)
}

func (ms msgServer) MigrateContract(goCtx context.Context, msg *wasmtypes.MsgMigrateContract) (*wasmtypes.MsgMigrateContractResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return runTracedHandler(
		ctx,
		ms.mgr.Tracer(),
		handlerTrace{path: pathWasm, msg: msg},
		func() (*wasmtypes.MsgMigrateContractResponse, error) {
			return NewWasmMigrateContractHandler(ms.mgr).Run(ctx, msg)
		},
	)
}

func (ms msgServer) SudoContract(goCtx context.Context, msg *wasmtypes.MsgSudoContract) (*wasmtypes.MsgSudoContractResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return runTracedHandler(
		ctx,
		ms.mgr.Tracer(),
		handlerTrace{path: pathWasm, msg: msg},
		func() (*wasmtypes.MsgSudoContractResponse, error) {
			return NewWasmSudoContractHandler(ms.mgr).Run(ctx, msg)
		},
	)
}

func (ms msgServer) UpdateAdmin(goCtx context.Context, msg *wasmtypes.MsgUpdateAdmin) (*wasmtypes.MsgUpdateAdminResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return runTracedHandler(
		ctx,
		ms.mgr.Tracer(),
		handlerTrace{path: pathWasm, msg: msg},
		func() (*wasmtypes.MsgUpdateAdminResponse, error) {
			return NewWasmUpdateAdminHandler(ms.mgr).Run(ctx, msg)
		},
	)
}

func (ms msgServer) ClearAdmin(goCtx context.Context, msg *wasmtypes.MsgClearAdmin) (*wasmtypes.MsgClearAdminResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	return runTracedHandler(
		ctx,
		ms.mgr.Tracer(),
		handlerTrace{path: pathWasm, msg: msg},
		func() (*wasmtypes.MsgClearAdminResponse, error) {
			return NewWasmClearAdminHandler(ms.mgr).Run(ctx, msg)
		},
	)
}
