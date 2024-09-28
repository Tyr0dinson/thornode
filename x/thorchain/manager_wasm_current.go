package thorchain

import (
	"context"
	"encoding/hex"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"

	errorsmod "cosmossdk.io/errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/errors"
)

var (
	_                        WasmManager = &WasmMgrVCUR{}
	MIMIR_WASM_GLOBAL_HALT               = "WASMHALTGLOBAL"
	MIMIR_WASM_CHECKSUM_HALT             = "WASMHALTCONTRACT"
	MIMIR_WASM_CONTRACT_HALT             = "WASMHALTCHECKSUM"
	WASM_PERMISSIONS                     = map[string](map[string]bool){}
)

// WasmMgrVCUR is VCUR implementation of slasher
type WasmMgrVCUR struct {
	wasmtypes.UnimplementedMsgServer
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

func (m *WasmMgrVCUR) Sudo(ctx cosmos.Context, msg *wasmtypes.MsgSudoContract) (*wasmtypes.MsgSudoContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}
	senderAddr, err := sdk.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "authority")
	}

	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	contractInfo := m.wasmKeeper.GetContractInfo(sdkCtx, contractAddr)
	codeInfo := m.wasmKeeper.GetCodeInfo(sdkCtx, contractInfo.CodeID)
	err = m.checkAuthorization(senderAddr, codeInfo.CodeHash)
	if err != nil {
		return nil, err
	}

	res, err := m.permissionedKeeper().Sudo(sdkCtx, contractAddr, msg.Msg)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgSudoContractResponse{
		Data: res,
	}, nil
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

	codeID, checksum, err := m.permissionedKeeper().Create(
		sdkCtx,
		senderAddr,
		msg.WASMByteCode,
		msg.InstantiatePermission,
	)
	if err != nil {
		return nil, err
	}

	err = m.checkAuthorization(senderAddr, checksum)
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

	codeInfo := m.wasmKeeper.GetCodeInfo(sdkCtx, msg.CodeID)
	err = m.checkAuthorization(senderAddr, codeInfo.CodeHash)
	if err != nil {
		return nil, err
	}

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

	codeInfo := m.wasmKeeper.GetCodeInfo(sdkCtx, msg.CodeID)
	err = m.checkAuthorization(senderAddr, codeInfo.CodeHash)
	if err != nil {
		return nil, err
	}

	var adminAddr sdk.AccAddress
	if msg.Admin != "" {
		if adminAddr, err = sdk.AccAddressFromBech32(msg.Admin); err != nil {
			return nil, errorsmod.Wrap(err, "admin")
		}
	}

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

	// The default `Messenger` configured in wasm keeper, used for routing sub-messages, uses
	// the app's `Router`. Therefore, any SDK submessages that call a contract will route
	// back through this code path and will be halted where necessary
	contractInfo := m.wasmKeeper.GetContractInfo(sdkCtx, contractAddr)
	codeInfo := m.wasmKeeper.GetCodeInfo(sdkCtx, contractInfo.CodeID)

	if err := m.checkContractHalt(sdkCtx, contractAddr); err != nil {
		return nil, err
	}

	if err := m.checkChecksumHalt(sdkCtx, codeInfo.CodeHash); err != nil {
		return nil, err
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

	codeInfo := m.wasmKeeper.GetCodeInfo(sdkCtx, msg.CodeID)
	err = m.checkAuthorization(senderAddr, codeInfo.CodeHash)
	if err != nil {
		return nil, err
	}

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

	m.maybeUnpin(sdkCtx, contractAddr)

	return &wasmtypes.MsgMigrateContractResponse{
		Data: data,
	}, nil
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

// SudoContract calls sudo on a contract.
func (m WasmMgrVCUR) SudoContract(
	ctx context.Context,
	msg *wasmtypes.MsgSudoContract,
) (*wasmtypes.MsgSudoContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	authorityAddr, err := sdk.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "authority")
	}

	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	contractInfo := m.wasmKeeper.GetContractInfo(sdkCtx, contractAddr)
	codeInfo := m.wasmKeeper.GetCodeInfo(sdkCtx, contractInfo.CodeID)
	err = m.checkAuthorization(authorityAddr, codeInfo.CodeHash)
	if err != nil {
		return nil, err
	}

	data, err := m.permissionedKeeper().Sudo(sdkCtx, contractAddr, msg.Msg)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgSudoContractResponse{Data: data}, nil
}

// StoreAndInstantiateContract stores and instantiates the contract.
func (m WasmMgrVCUR) StoreAndInstantiateContract(
	ctx context.Context,
	msg *wasmtypes.MsgStoreAndInstantiateContract,
) (*wasmtypes.MsgStoreAndInstantiateContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	authorityAddr, err := sdk.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "authority")
	}

	var adminAddr sdk.AccAddress
	if msg.Admin != "" {
		if adminAddr, err = sdk.AccAddressFromBech32(msg.Admin); err != nil {
			return nil, errorsmod.Wrap(err, "admin")
		}
	}

	codeID, checksum, err := m.permissionedKeeper().Create(
		sdkCtx,
		authorityAddr,
		msg.WASMByteCode,
		msg.InstantiatePermission,
	)
	if err != nil {
		return nil, err
	}

	err = m.checkAuthorization(authorityAddr, checksum)
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
		msg.Msg,
		msg.Label,
		msg.Funds)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgStoreAndInstantiateContractResponse{
		Address: contractAddr.String(),
		Data:    data,
	}, nil
}

// StoreAndMigrateContract stores and migrates the contract.
func (m WasmMgrVCUR) StoreAndMigrateContract(
	ctx context.Context,
	msg *wasmtypes.MsgStoreAndMigrateContract,
) (*wasmtypes.MsgStoreAndMigrateContractResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if err := m.checkHalt(sdkCtx); err != nil {
		return nil, err
	}

	authorityAddr, err := sdk.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return nil, errorsmod.Wrap(err, "authority")
	}

	if err = msg.ValidateBasic(); err != nil {
		return nil, err
	}

	codeID, checksum, err := m.permissionedKeeper().Create(
		sdkCtx,
		authorityAddr,
		msg.WASMByteCode,
		msg.InstantiatePermission,
	)
	if err != nil {
		return nil, err
	}

	err = m.checkAuthorization(authorityAddr, checksum)
	if err != nil {
		return nil, err
	}

	if err := m.permissionedKeeper().PinCode(sdkCtx, codeID); err != nil {
		return nil, err
	}

	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	data, err := m.permissionedKeeper().Migrate(
		sdkCtx,
		contractAddr,
		authorityAddr,
		codeID,
		msg.Msg)
	if err != nil {
		return nil, err
	}

	m.maybeUnpin(sdkCtx, contractAddr)

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

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	contractInfo := m.wasmKeeper.GetContractInfo(sdkCtx, contractAddr)
	codeInfo := m.wasmKeeper.GetCodeInfo(sdkCtx, contractInfo.CodeID)
	err = m.checkAuthorization(senderAddr, codeInfo.CodeHash)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgUpdateContractLabelResponse{}, errors.ErrUnauthorized
}

func (m WasmMgrVCUR) checkHalt(ctx cosmos.Context) error {
	v, err := m.keeper.GetMimir(ctx, MIMIR_WASM_GLOBAL_HALT)
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() > v {
		return errors.ErrUnauthorized
	}
	return nil
}

func (m WasmMgrVCUR) checkContractHalt(ctx cosmos.Context, address sdk.AccAddress) error {
	v, err := m.keeper.GetMimir(ctx, MIMIR_WASM_CONTRACT_HALT+address.String())
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() > v {
		return wrapError(ctx, errors.ErrUnauthorized, "contract halted")
	}
	return nil
}

func (m WasmMgrVCUR) checkChecksumHalt(ctx cosmos.Context, checksum []byte) error {
	v, err := m.keeper.GetMimir(ctx, MIMIR_WASM_CHECKSUM_HALT+hex.EncodeToString(checksum))
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() > v {
		return wrapError(ctx, errors.ErrUnauthorized, "checksum halted")
	}
	return nil
}

func (m WasmMgrVCUR) permissionedKeeper() *wasmkeeper.PermissionedKeeper {
	return wasmkeeper.NewGovPermissionKeeper(m.wasmKeeper)
}

func (m WasmMgrVCUR) checkAuthorization(actor cosmos.AccAddress, checksum []byte) error {
	if isAdmin(actor) {
		return nil
	}
	if WASM_PERMISSIONS[actor.String()][hex.EncodeToString(checksum)] {
		return nil
	}

	return errNotAuthorized
}

func (m WasmMgrVCUR) maybeUnpin(ctx cosmos.Context, address cosmos.AccAddress) {
	contractInfo := m.wasmKeeper.GetContractInfo(ctx, address)
	var instanceCount int
	m.wasmKeeper.IterateContractsByCode(ctx, contractInfo.CodeID, func(address sdk.AccAddress) bool {
		instanceCount++
		return true
	})
	if instanceCount == 0 {
		m.permissionedKeeper().UnpinCode(ctx, contractInfo.CodeID)
	}
}
