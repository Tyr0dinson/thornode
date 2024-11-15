package thorchain

import (
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var _ WasmManager = &DummyWasmManager{}

// DummyWasmManager is VCUR implementation of slasher
type DummyWasmManager struct {
	wasmtypes.UnimplementedMsgServer
}

// newDummyWasmManager create a new instance of Slasher
func NewDummyWasmManager() *DummyWasmManager {
	return &DummyWasmManager{}
}

func (s *DummyWasmManager) StoreCode(ctx cosmos.Context, msg *wasmtypes.MsgStoreCode) (*wasmtypes.MsgStoreCodeResponse, error) {
	return &wasmtypes.MsgStoreCodeResponse{}, nil
}

func (s *DummyWasmManager) InstantiateContract(ctx cosmos.Context, msg *wasmtypes.MsgInstantiateContract) (*wasmtypes.MsgInstantiateContractResponse, error) {
	return &wasmtypes.MsgInstantiateContractResponse{}, nil
}

func (s *DummyWasmManager) InstantiateContract2(ctx cosmos.Context, msg *wasmtypes.MsgInstantiateContract2) (*wasmtypes.MsgInstantiateContract2Response, error) {
	return &wasmtypes.MsgInstantiateContract2Response{}, nil
}

func (s *DummyWasmManager) ExecuteContract(ctx cosmos.Context, contractAddr, senderAddr cosmos.AccAddress, msg []byte, coins cosmos.Coins) (*wasmtypes.MsgExecuteContractResponse, error) {
	return &wasmtypes.MsgExecuteContractResponse{}, nil
}

func (s *DummyWasmManager) MigrateContract(ctx cosmos.Context, msg *wasmtypes.MsgMigrateContract) (*wasmtypes.MsgMigrateContractResponse, error) {
	return &wasmtypes.MsgMigrateContractResponse{}, nil
}

func (s *DummyWasmManager) SudoContract(ctx cosmos.Context, msg *wasmtypes.MsgSudoContract) (*wasmtypes.MsgSudoContractResponse, error) {
	return &wasmtypes.MsgSudoContractResponse{}, nil
}
