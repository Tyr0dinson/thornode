package thorchain

import (
	"context"

	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// DummyWasmManager is VCUR implementation of slasher
type DummyWasmManager struct {
	keeper     keeper.Keeper
	wasmKeeper wasmkeeper.Keeper
	eventMgr   EventManager
}

// newDummyWasmManager create a new instance of Slasher
func NewDummyWasmManager() *DummyWasmManager {
	return &DummyWasmManager{}
}

func (s *DummyWasmManager) Create(
	ctx cosmos.Context,
	creator cosmos.AccAddress,
	wasmCode []byte,
) (wasmtypes.MsgStoreCodeResponse, error) {
	return wasmtypes.MsgStoreCodeResponse{}, nil
}

func (s *DummyWasmManager) Instantiate(ctx context.Context,
	codeID uint64,
	creator, admin cosmos.AccAddress,
	initMsg []byte,
	label string,
	deposit cosmos.Coins,
) (wasmtypes.MsgInstantiateContractResponse, error) {
	return wasmtypes.MsgInstantiateContractResponse{}, nil

}

func (s *DummyWasmManager) Instantiate2(
	ctx context.Context,
	codeID uint64,
	creator, admin cosmos.AccAddress,
	initMsg []byte,
	label string,
	deposit cosmos.Coins,
	salt []byte,
	fixMsg bool,
) (wasmtypes.MsgInstantiateContract2Response, error) {
	return wasmtypes.MsgInstantiateContract2Response{}, nil

}

func (s *DummyWasmManager) Migrate(ctx context.Context,
	contractAddress, caller cosmos.AccAddress,
	newCodeID uint64,
	msg []byte,
) (wasmtypes.MsgMigrateContractResponse, error) {
	return wasmtypes.MsgMigrateContractResponse{}, nil

}

func (s *DummyWasmManager) Sudo(ctx cosmos.Context, contractAddres, caller cosmos.AccAddress, msg []byte) (wasmtypes.MsgSudoContractResponse, error) {
	return wasmtypes.MsgSudoContractResponse{}, nil
}
