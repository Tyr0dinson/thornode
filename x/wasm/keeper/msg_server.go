package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/errors"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/CosmWasm/wasmd/x/wasm/types"
	"gitlab.com/thorchain/thornode/common/cosmos"
	tckeeper "gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

var (
	_                 types.MsgServer = msgServer{}
	MIMIR_GLOBAL_HALT                 = "WASMGLOBALHALT"
)

// grpc message server implementation
type msgServer struct {
	keeper   *wasmkeeper.PermissionedKeeper
	tcKeeper tckeeper.Keeper
}

// NewMsgServerImpl default constructor
func NewMsgServerImpl(k *wasmkeeper.PermissionedKeeper, tcKeeper tckeeper.Keeper) types.MsgServer {
	return &msgServer{keeper: k, tcKeeper: tcKeeper}
}

// StoreCode stores a new wasm code on chain
func (m msgServer) StoreCode(
	ctx context.Context,
	msg *types.MsgStoreCode,
) (*types.MsgStoreCodeResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	// TODO: Authorization

	codeID, checksum, err := m.keeper.Create(
		sdkCtx,
		senderAddr,
		msg.WASMByteCode,
		msg.InstantiatePermission,
	)
	if err != nil {
		return nil, err
	}

	if err := m.keeper.PinCode(sdkCtx, codeID); err != nil {
		return nil, err
	}

	return &types.MsgStoreCodeResponse{
		CodeID:   codeID,
		Checksum: checksum,
	}, nil
}

// InstantiateContract instantiate a new contract with classic sequence based address generation
func (m msgServer) InstantiateContract(
	ctx context.Context,
	msg *types.MsgInstantiateContract,
) (*types.MsgInstantiateContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	// TODO: Authorization

	var adminAddr sdk.AccAddress
	if msg.Admin != "" {
		if adminAddr, err = sdk.AccAddressFromBech32(msg.Admin); err != nil {
			return nil, errorsmod.Wrap(err, "admin")
		}
	}

	contractAddr, data, err := m.keeper.Instantiate(
		sdkCtx,
		msg.CodeID,
		senderAddr,
		adminAddr,
		msg.Msg,
		msg.Label,
		msg.Funds,
	)
	if err != nil {
		return nil, err
	}

	return &types.MsgInstantiateContractResponse{
		Address: contractAddr.String(),
		Data:    data,
	}, nil
}

// InstantiateContract2 instantiate a new contract with predicatable address generated
func (m msgServer) InstantiateContract2(
	ctx context.Context,
	msg *types.MsgInstantiateContract2,
) (*types.MsgInstantiateContract2Response, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	// TODO: Authorization

	var adminAddr sdk.AccAddress
	if msg.Admin != "" {
		if adminAddr, err = sdk.AccAddressFromBech32(msg.Admin); err != nil {
			return nil, errorsmod.Wrap(err, "admin")
		}
	}

	// TODO: Authorization
	contractAddr, data, err := m.keeper.Instantiate2(
		sdkCtx,
		msg.CodeID,
		senderAddr,
		adminAddr,
		msg.Msg,
		msg.Label,
		msg.Funds,
		msg.Salt,
		msg.FixMsg,
	)
	if err != nil {
		return nil, err
	}

	return &types.MsgInstantiateContract2Response{
		Address: contractAddr.String(),
		Data:    data,
	}, nil
}

func (m msgServer) ExecuteContract(
	ctx context.Context,
	msg *types.MsgExecuteContract,
) (*types.MsgExecuteContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}
	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	data, err := m.keeper.Execute(
		sdkCtx,
		contractAddr,
		senderAddr,
		msg.Msg,
		msg.Funds,
	)
	if err != nil {
		return nil, err
	}

	return &types.MsgExecuteContractResponse{
		Data: data,
	}, nil
}

func (m msgServer) MigrateContract(
	ctx context.Context,
	msg *types.MsgMigrateContract,
) (*types.MsgMigrateContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	// TODO: Authorization

	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	data, err := m.keeper.Migrate(
		sdkCtx,
		contractAddr,
		senderAddr,
		msg.CodeID,
		msg.Msg,
	)
	if err != nil {
		return nil, err
	}

	// TODO: Check if old code id contract count == 0 and unpin

	return &types.MsgMigrateContractResponse{
		Data: data,
	}, nil
}

func (m msgServer) UpdateAdmin(
	ctx context.Context,
	msg *types.MsgUpdateAdmin,
) (*types.MsgUpdateAdminResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}
	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}
	newAdminAddr, err := sdk.AccAddressFromBech32(msg.NewAdmin)
	if err != nil {
		return nil, errorsmod.Wrap(err, "new admin")
	}

	if err := m.keeper.UpdateContractAdmin(
		sdkCtx,
		contractAddr,
		senderAddr,
		newAdminAddr,
	); err != nil {
		return nil, err
	}

	return &types.MsgUpdateAdminResponse{}, nil
}

func (m msgServer) ClearAdmin(
	ctx context.Context,
	msg *types.MsgClearAdmin) (*types.MsgClearAdminResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}
	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	if err := m.keeper.ClearContractAdmin(
		sdkCtx,
		contractAddr,
		senderAddr,
	); err != nil {
		return nil, err
	}

	return &types.MsgClearAdminResponse{}, nil
}

func (m msgServer) UpdateInstantiateConfig(
	ctx context.Context,
	msg *types.MsgUpdateInstantiateConfig,
) (*types.MsgUpdateInstantiateConfigResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	if err := m.keeper.SetAccessConfig(
		sdkCtx,
		msg.CodeID,
		senderAddr,
		*msg.NewInstantiatePermission,
	); err != nil {
		return nil, err
	}

	return &types.MsgUpdateInstantiateConfigResponse{}, nil
}

// UpdateParams updates the module parameters
func (m msgServer) UpdateParams(
	ctx context.Context,
	req *types.MsgUpdateParams,
) (*types.MsgUpdateParamsResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check added for completeness
	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &types.MsgUpdateParamsResponse{}, errors.ErrUnauthorized
}

// Codes are pinned automatically when stored
func (m msgServer) PinCodes(
	ctx context.Context,
	req *types.MsgPinCodes,
) (*types.MsgPinCodesResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check added for completeness
	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &types.MsgPinCodesResponse{}, errors.ErrUnauthorized
}

// Codes are unpinned automatically when migrated away leaving 0
func (m msgServer) UnpinCodes(
	ctx context.Context,
	req *types.MsgUnpinCodes,
) (*types.MsgUnpinCodesResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check added for completeness
	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &types.MsgUnpinCodesResponse{}, errors.ErrUnauthorized
}

// SudoContract calls sudo on a contract.
func (m msgServer) SudoContract(
	ctx context.Context,
	req *types.MsgSudoContract,
) (*types.MsgSudoContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := req.ValidateBasic(); err != nil {
		return nil, err
	}

	// TODO: Authorization
	// authority := m.keeper.GetAuthority()
	// if authority != req.Authority {
	// 	return nil, errorsmod.Wrapf(types.ErrInvalid, "invalid authority; expected %s, got %s", authority, req.Authority)
	// }

	contractAddr, err := sdk.AccAddressFromBech32(req.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	data, err := m.keeper.Sudo(
		sdkCtx,
		contractAddr,
		req.Msg,
	)
	if err != nil {
		return nil, err
	}

	return &types.MsgSudoContractResponse{Data: data}, nil
}

// StoreAndInstantiateContract stores and instantiates the contract.
func (m msgServer) StoreAndInstantiateContract(
	ctx context.Context,
	req *types.MsgStoreAndInstantiateContract,
) (*types.MsgStoreAndInstantiateContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := req.ValidateBasic(); err != nil {
		return nil, err
	}

	authorityAddr, err := sdk.AccAddressFromBech32(req.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "authority")
	}

	var adminAddr sdk.AccAddress
	if req.Admin != "" {
		if adminAddr, err = sdk.AccAddressFromBech32(req.Admin); err != nil {
			return nil, errorsmod.Wrap(err, "admin")
		}
	}

	codeID, _, err := m.keeper.Create(
		sdkCtx,
		authorityAddr,
		req.WASMByteCode,
		req.InstantiatePermission,
	)
	if err != nil {
		return nil, err
	}

	if err := m.keeper.PinCode(sdkCtx, codeID); err != nil {
		return nil, err
	}

	contractAddr, data, err := m.keeper.Instantiate(
		sdkCtx,
		codeID,
		authorityAddr,
		adminAddr,
		req.Msg,
		req.Label,
		req.Funds)
	if err != nil {
		return nil, err
	}

	return &types.MsgStoreAndInstantiateContractResponse{
		Address: contractAddr.String(),
		Data:    data,
	}, nil
}

// AddCodeUploadParamsAddresses adds addresses to code upload params
func (m msgServer) AddCodeUploadParamsAddresses(
	ctx context.Context,
	req *types.MsgAddCodeUploadParamsAddresses,
) (*types.MsgAddCodeUploadParamsAddressesResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check added for completeness
	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &types.MsgAddCodeUploadParamsAddressesResponse{}, errors.ErrUnauthorized
}

// RemoveCodeUploadParamsAddresses removes addresses to code upload params
func (m msgServer) RemoveCodeUploadParamsAddresses(
	ctx context.Context,
	req *types.MsgRemoveCodeUploadParamsAddresses,
) (*types.MsgRemoveCodeUploadParamsAddressesResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check added for completeness
	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &types.MsgRemoveCodeUploadParamsAddressesResponse{}, errors.ErrUnauthorized
}

// StoreAndMigrateContract stores and migrates the contract.
func (m msgServer) StoreAndMigrateContract(
	ctx context.Context,
	req *types.MsgStoreAndMigrateContract,
) (*types.MsgStoreAndMigrateContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	authorityAddr, err := sdk.AccAddressFromBech32(req.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "authority")
	}

	if err = req.ValidateBasic(); err != nil {
		return nil, err
	}

	codeID, checksum, err := m.keeper.Create(
		sdkCtx,
		authorityAddr,
		req.WASMByteCode,
		req.InstantiatePermission,
	)
	if err != nil {
		return nil, err
	}

	if err := m.keeper.PinCode(sdkCtx, codeID); err != nil {
		return nil, err
	}

	contractAddr, err := sdk.AccAddressFromBech32(req.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	// TODO: Check if old code id contract count == 0 and unpin

	data, err := m.keeper.Migrate(
		sdkCtx,
		contractAddr,
		authorityAddr,
		codeID,
		req.Msg)
	if err != nil {
		return nil, err
	}

	return &types.MsgStoreAndMigrateContractResponse{
		CodeID:   codeID,
		Checksum: checksum,
		Data:     data,
	}, nil
}

func (m msgServer) UpdateContractLabel(
	ctx context.Context,
	msg *types.MsgUpdateContractLabel,
) (*types.MsgUpdateContractLabelResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &types.MsgUpdateContractLabelResponse{}, errors.ErrUnauthorized
}

func (m msgServer) checkHalt(ctx cosmos.Context) error {
	v, err := m.tcKeeper.GetMimir(ctx, MIMIR_GLOBAL_HALT)
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() > v {
		return errors.ErrUnauthorized
	}
	return nil
}
