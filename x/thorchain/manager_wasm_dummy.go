package thorchain

import (
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

var (
	_ WasmManager = &DummyWasmManager{}
)

// DummyWasmManager is VCUR implementation of slasher
type DummyWasmManager struct {
	wasmtypes.UnimplementedMsgServer
	keeper     keeper.Keeper
	wasmKeeper wasmkeeper.Keeper
	eventMgr   EventManager
}

// newDummyWasmManager create a new instance of Slasher
func NewDummyWasmManager() *DummyWasmManager {
	return &DummyWasmManager{}
}

func (s *DummyWasmManager) Sudo(ctx cosmos.Context, msg *wasmtypes.MsgSudoContract) (wasmtypes.MsgSudoContractResponse, error) {
	return wasmtypes.MsgSudoContractResponse{}, nil
}
