package thorchain

import (
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
	WASM_PERMISSIONS                     = map[string](map[string]bool){
		"tthor1zf3gsk7edzwl9syyefvfhle37cjtql35h6k85m": {
			"f9b2efce508ab3594d41ad0d7bd645014b1e145df64083308d37c780de7390b0": true,
		},
	}
)

// WasmMgrVCUR is VCUR implementation of slasher
type WasmMgrVCUR struct {
	keeper     keeper.Keeper
	wasmKeeper wasmkeeper.Keeper
	eventMgr   EventManager
}

// newWasmMgrVCUR create a new instance of Slasher
func newWasmMgrVCUR(ctx cosmos.Context, keeper keeper.Keeper, wasmKeeper wasmkeeper.Keeper, eventMgr EventManager) *WasmMgrVCUR {
	// We don't use wasmtypes.Params for permissioning.
	// This just ensures that there is data available for wasmkeeper.Keeper.GetParams to retrieve
	wasmKeeper.SetParams(ctx, wasmtypes.Params{
		CodeUploadAccess: wasmtypes.AccessConfig{
			Permission: 1,
			Addresses:  []string{},
		},
		InstantiateDefaultPermission: 1,
	})

	return &WasmMgrVCUR{
		keeper:     keeper,
		wasmKeeper: wasmKeeper,
		eventMgr:   eventMgr,
	}
}

// StoreCode stores a new wasm code on chain
func (m WasmMgrVCUR) StoreCode(
	ctx cosmos.Context,
	msg *wasmtypes.MsgStoreCode,
) (*wasmtypes.MsgStoreCodeResponse, error) {
	if err := m.checkHalt(ctx); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	codeID, checksum, err := m.permissionedKeeper().Create(
		ctx,
		senderAddr,
		msg.WASMByteCode,
		msg.InstantiatePermission,
	)

	if err != nil {
		return nil, err
	}

	if err := m.checkChecksumHalt(ctx, checksum); err != nil {
		return nil, err
	}

	if err := m.checkAuthorization(senderAddr, checksum); err != nil {
		return nil, err
	}

	if err := m.permissionedKeeper().PinCode(ctx, codeID); err != nil {
		return nil, err
	}

	return &wasmtypes.MsgStoreCodeResponse{
		CodeID:   codeID,
		Checksum: checksum,
	}, nil
}

// InstantiateContract instantiate a new contract with classic sequence based address generation
func (m WasmMgrVCUR) InstantiateContract(
	ctx cosmos.Context,
	msg *wasmtypes.MsgInstantiateContract,
) (*wasmtypes.MsgInstantiateContractResponse, error) {
	if err := m.checkHalt(ctx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	codeInfo := m.wasmKeeper.GetCodeInfo(ctx, msg.CodeID)
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
		ctx,
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
	ctx cosmos.Context,
	msg *wasmtypes.MsgInstantiateContract2,
) (*wasmtypes.MsgInstantiateContract2Response, error) {
	if err := m.checkHalt(ctx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	codeInfo := m.wasmKeeper.GetCodeInfo(ctx, msg.CodeID)
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
		ctx,
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
	ctx cosmos.Context,
	msg *wasmtypes.MsgExecuteContract,
) (*wasmtypes.MsgExecuteContractResponse, error) {
	if err := m.checkHalt(ctx); err != nil {
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
	contractInfo := m.wasmKeeper.GetContractInfo(ctx, contractAddr)
	codeInfo := m.wasmKeeper.GetCodeInfo(ctx, contractInfo.CodeID)

	if err := m.checkContractHalt(ctx, contractAddr); err != nil {
		return nil, err
	}

	if err := m.checkChecksumHalt(ctx, codeInfo.CodeHash); err != nil {
		return nil, err
	}

	data, err := m.permissionedKeeper().Execute(
		ctx,
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
	ctx cosmos.Context,
	msg *wasmtypes.MsgMigrateContract,
) (*wasmtypes.MsgMigrateContractResponse, error) {
	if err := m.checkHalt(ctx); err != nil {
		return nil, err
	}

	if err := msg.ValidateBasic(); err != nil {
		return nil, err
	}

	senderAddr, err := sdk.AccAddressFromBech32(msg.Sender)
	if err != nil {
		return nil, errorsmod.Wrap(err, "sender")
	}

	codeInfo := m.wasmKeeper.GetCodeInfo(ctx, msg.CodeID)
	err = m.checkAuthorization(senderAddr, codeInfo.CodeHash)
	if err != nil {
		return nil, err
	}

	contractAddr, err := sdk.AccAddressFromBech32(msg.Contract)
	if err != nil {
		return nil, errorsmod.Wrap(err, "contract")
	}

	contractInfo := m.wasmKeeper.GetContractInfo(ctx, contractAddr)
	if contractInfo.Admin == "" || contractInfo.Admin != msg.Sender {
		return nil, errNotAuthorized
	}

	data, err := m.permissionedKeeper().Migrate(
		ctx,
		contractAddr,
		senderAddr,
		msg.CodeID,
		msg.Msg,
	)
	if err != nil {
		return nil, err
	}

	m.maybeUnpin(ctx, contractAddr)

	return &wasmtypes.MsgMigrateContractResponse{
		Data: data,
	}, nil
}

// SudoContract calls sudo on a contract.
func (m WasmMgrVCUR) SudoContract(
	ctx cosmos.Context,
	msg *wasmtypes.MsgSudoContract,
) (*wasmtypes.MsgSudoContractResponse, error) {
	if err := m.checkHalt(ctx); err != nil {
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

	contractInfo := m.wasmKeeper.GetContractInfo(ctx, contractAddr)
	codeInfo := m.wasmKeeper.GetCodeInfo(ctx, contractInfo.CodeID)
	err = m.checkAuthorization(authorityAddr, codeInfo.CodeHash)
	if err != nil {
		return nil, err
	}

	data, err := m.permissionedKeeper().Sudo(ctx, contractAddr, msg.Msg)
	if err != nil {
		return nil, err
	}

	return &wasmtypes.MsgSudoContractResponse{Data: data}, nil
}

func (m WasmMgrVCUR) checkHalt(ctx cosmos.Context) error {
	v, err := m.keeper.GetMimir(ctx, MIMIR_WASM_GLOBAL_HALT)
	if err != nil {
		return err
	}
	if v != -1 && ctx.BlockHeight() > v {
		return errorsmod.Wrap(errors.ErrUnauthorized, "wasm halted")
	}
	return nil
}

func (m WasmMgrVCUR) checkContractHalt(ctx cosmos.Context, address sdk.AccAddress) error {
	v, err := m.keeper.GetMimir(ctx, MIMIR_WASM_CONTRACT_HALT+address.String())
	if err != nil {
		return err
	}
	if v != -1 && ctx.BlockHeight() > v {
		return errorsmod.Wrap(errors.ErrUnauthorized, "contract halted")
	}
	return nil
}

func (m WasmMgrVCUR) checkChecksumHalt(ctx cosmos.Context, checksum []byte) error {
	v, err := m.keeper.GetMimir(ctx, MIMIR_WASM_CHECKSUM_HALT+hex.EncodeToString(checksum))
	if err != nil {
		return err
	}
	if v != -1 && ctx.BlockHeight() > v {
		return errorsmod.Wrap(errors.ErrUnauthorized, "checksum halted")
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
	actorStr := actor.String()
	checksumStr := hex.EncodeToString(checksum)
	if WASM_PERMISSIONS[actorStr][checksumStr] {
		return nil
	}

	return errorsmod.Wrapf(errNotAuthorized, "%s:%s", actorStr, checksumStr)
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
