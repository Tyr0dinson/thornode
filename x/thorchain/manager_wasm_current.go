package thorchain

import (
	"context"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/errors"
)

var (
	_                 WasmManager = &WasmMgrVCUR{}
	MIMIR_GLOBAL_HALT             = "WASMGLOBALHALT"
)

// WasmMgrVCUR is VCUR implementation of slasher
type WasmMgrVCUR struct {
	keeper     keeper.Keeper
	wasmKeeper wasmkeeper.Keeper
	eventMgr   EventManager
}

// newWasmMgrVCUR create a new instance of Slasher
func newWasmMgrVCUR(keeper keeper.Keeper, wasmKeeper wasmkeeper.Keeper, eventMgr EventManager) *WasmMgrVCUR {
	return &WasmMgrVCUR{
		keeper:     keeper,
		wasmKeeper: wasmKeeper,
		eventMgr:   eventMgr,
	}
}
func (s *WasmMgrVCUR) Sudo(ctx cosmos.Context, msg *wasmtypes.MsgSudoContract) (wasmtypes.MsgSudoContractResponse, error) {
	return wasmtypes.MsgSudoContractResponse{}, nil
}

// StoreCode stores a new wasm code on chain
func (m WasmMgrVCUR) StoreCode(
	ctx context.Context,
	msg *wasmtypes.MsgStoreCode,
) (*wasmtypes.MsgStoreCodeResponse, error) {
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

	codeID, checksum, err := m.permissionedKeeper().Create(
		sdkCtx,
		senderAddr,
		msg.WASMByteCode,
		msg.InstantiatePermission,
	)
	if err != nil {
		return nil, err
	}

	if err := m.permissionedKeeper().PinCode(sdkCtx, codeID); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgStoreCodeResponse{
		CodeID:   codeID,
		Checksum: checksum,
	}, nil
}

// InstantiateContract instantiate a new contract with classic sequence based address generation
func (m WasmMgrVCUR) InstantiateContract(
	ctx context.Context,
	msg *wasmtypes.MsgInstantiateContract,
) (*wasmtypes.MsgInstantiateContractResponse, error) {
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

	contractAddr, data, err := m.permissionedKeeper().Instantiate(
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

	return &wasmtypes.MsgInstantiateContractResponse{
		Address: contractAddr.String(),
		Data:    data,
	}, nil
}

// InstantiateContract2 instantiate a new contract with predicatable address generated
func (m WasmMgrVCUR) InstantiateContract2(
	ctx context.Context,
	msg *wasmtypes.MsgInstantiateContract2,
) (*wasmtypes.MsgInstantiateContract2Response, error) {
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
	contractAddr, data, err := m.permissionedKeeper().Instantiate2(
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

	return &wasmtypes.MsgInstantiateContract2Response{
		Address: contractAddr.String(),
		Data:    data,
	}, nil
}

func (m WasmMgrVCUR) ExecuteContract(
	ctx context.Context,
	msg *wasmtypes.MsgExecuteContract,
) (*wasmtypes.MsgExecuteContractResponse, error) {
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

	data, err := m.permissionedKeeper().Execute(
		sdkCtx,
		contractAddr,
		senderAddr,
		msg.Msg,
		msg.Funds,
	)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgExecuteContractResponse{
		Data: data,
	}, nil
}

func (m WasmMgrVCUR) MigrateContract(
	ctx context.Context,
	msg *wasmtypes.MsgMigrateContract,
) (*wasmtypes.MsgMigrateContractResponse, error) {
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

	data, err := m.permissionedKeeper().Migrate(
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

	return &wasmtypes.MsgMigrateContractResponse{
		Data: data,
	}, nil
}

func (m WasmMgrVCUR) UpdateAdmin(
	ctx context.Context,
	msg *wasmtypes.MsgUpdateAdmin,
) (*wasmtypes.MsgUpdateAdminResponse, error) {
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

	if err := m.permissionedKeeper().UpdateContractAdmin(
		sdkCtx,
		contractAddr,
		senderAddr,
		newAdminAddr,
	); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgUpdateAdminResponse{}, nil
}

func (m WasmMgrVCUR) ClearAdmin(
	ctx context.Context,
	msg *wasmtypes.MsgClearAdmin) (*wasmtypes.MsgClearAdminResponse, error) {
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

	if err := m.permissionedKeeper().ClearContractAdmin(
		sdkCtx,
		contractAddr,
		senderAddr,
	); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgClearAdminResponse{}, nil
}

func (m WasmMgrVCUR) UpdateInstantiateConfig(
	ctx context.Context,
	msg *wasmtypes.MsgUpdateInstantiateConfig,
) (*wasmtypes.MsgUpdateInstantiateConfigResponse, error) {
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

	if err := m.permissionedKeeper().SetAccessConfig(
		sdkCtx,
		msg.CodeID,
		senderAddr,
		*msg.NewInstantiatePermission,
	); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgUpdateInstantiateConfigResponse{}, nil
}

// UpdateParams updates the module parameters
func (m WasmMgrVCUR) UpdateParams(
	ctx context.Context,
	req *wasmtypes.MsgUpdateParams,
) (*wasmtypes.MsgUpdateParamsResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check added for completeness
	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgUpdateParamsResponse{}, errors.ErrUnauthorized
}

// Codes are pinned automatically when stored
func (m WasmMgrVCUR) PinCodes(
	ctx context.Context,
	req *wasmtypes.MsgPinCodes,
) (*wasmtypes.MsgPinCodesResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check added for completeness
	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgPinCodesResponse{}, errors.ErrUnauthorized
}

// Codes are unpinned automatically when migrated away leaving 0
func (m WasmMgrVCUR) UnpinCodes(
	ctx context.Context,
	req *wasmtypes.MsgUnpinCodes,
) (*wasmtypes.MsgUnpinCodesResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check added for completeness
	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgUnpinCodesResponse{}, errors.ErrUnauthorized
}

// SudoContract calls sudo on a contract.
func (m WasmMgrVCUR) SudoContract(
	ctx context.Context,
	req *wasmtypes.MsgSudoContract,
) (*wasmtypes.MsgSudoContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := req.ValidateBasic(); err != nil {
		return nil, err
	}

	// TODO: Authorization
	// authority := m.permissionedKeeper().GetAuthority()
	// if authority != req.Authority {
	// 	return nil, errorsmod.Wrapf(types.ErrInvalid, "invalid authority; expected %s, got %s", authority, req.Authority)
	// }

	contractAddr, err := sdk.AccAddressFromBech32(req.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	data, err := m.permissionedKeeper().Sudo(
		sdkCtx,
		contractAddr,
		req.Msg,
	)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgSudoContractResponse{Data: data}, nil
}

// StoreAndInstantiateContract stores and instantiates the contract.
func (m WasmMgrVCUR) StoreAndInstantiateContract(
	ctx context.Context,
	req *wasmtypes.MsgStoreAndInstantiateContract,
) (*wasmtypes.MsgStoreAndInstantiateContractResponse, error) {
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

	codeID, _, err := m.permissionedKeeper().Create(
		sdkCtx,
		authorityAddr,
		req.WASMByteCode,
		req.InstantiatePermission,
	)
	if err != nil {
		return nil, err
	}

	if err := m.permissionedKeeper().PinCode(sdkCtx, codeID); err != nil {
		return nil, err
	}

	contractAddr, data, err := m.permissionedKeeper().Instantiate(
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

	return &wasmtypes.MsgStoreAndInstantiateContractResponse{
		Address: contractAddr.String(),
		Data:    data,
	}, nil
}

// AddCodeUploadParamsAddresses adds addresses to code upload params
func (m WasmMgrVCUR) AddCodeUploadParamsAddresses(
	ctx context.Context,
	req *wasmtypes.MsgAddCodeUploadParamsAddresses,
) (*wasmtypes.MsgAddCodeUploadParamsAddressesResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check added for completeness
	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgAddCodeUploadParamsAddressesResponse{}, errors.ErrUnauthorized
}

// RemoveCodeUploadParamsAddresses removes addresses to code upload params
func (m WasmMgrVCUR) RemoveCodeUploadParamsAddresses(
	ctx context.Context,
	req *wasmtypes.MsgRemoveCodeUploadParamsAddresses,
) (*wasmtypes.MsgRemoveCodeUploadParamsAddressesResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Check added for completeness
	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgRemoveCodeUploadParamsAddressesResponse{}, errors.ErrUnauthorized
}

// StoreAndMigrateContract stores and migrates the contract.
func (m WasmMgrVCUR) StoreAndMigrateContract(
	ctx context.Context,
	req *wasmtypes.MsgStoreAndMigrateContract,
) (*wasmtypes.MsgStoreAndMigrateContractResponse, error) {
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

	codeID, checksum, err := m.permissionedKeeper().Create(
		sdkCtx,
		authorityAddr,
		req.WASMByteCode,
		req.InstantiatePermission,
	)
	if err != nil {
		return nil, err
	}

	if err := m.permissionedKeeper().PinCode(sdkCtx, codeID); err != nil {
		return nil, err
	}

	contractAddr, err := sdk.AccAddressFromBech32(req.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	// TODO: Check if old code id contract count == 0 and unpin

	data, err := m.permissionedKeeper().Migrate(
		sdkCtx,
		contractAddr,
		authorityAddr,
		codeID,
		req.Msg)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgStoreAndMigrateContractResponse{
		CodeID:   codeID,
		Checksum: checksum,
		Data:     data,
	}, nil
}

func (m WasmMgrVCUR) UpdateContractLabel(
	ctx context.Context,
	msg *wasmtypes.MsgUpdateContractLabel,
) (*wasmtypes.MsgUpdateContractLabelResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgUpdateContractLabelResponse{}, errors.ErrUnauthorized
}

func (m WasmMgrVCUR) checkHalt(ctx cosmos.Context) error {
	v, err := m.keeper.GetMimir(ctx, MIMIR_GLOBAL_HALT)
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() > v {
		return errors.ErrUnauthorized
	}
	return nil
}

func (m WasmMgrVCUR) permissionedKeeper() *wasmkeeper.PermissionedKeeper {
	return wasmkeeper.NewGovPermissionKeeper(m.wasmKeeper)
}
