package thorchain

import (
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

var _ WasmManager = &WasmMgrVCUR{}

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
func (s *WasmMgrVCUR) Sudo(ctx cosmos.Context, contractAddres, caller cosmos.AccAddress, msg []byte) (wasmtypes.MsgSudoContractResponse, error) {
	return wasmtypes.MsgSudoContractResponse{}, nil
}
