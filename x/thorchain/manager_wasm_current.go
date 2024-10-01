package thorchain

import (
	"context"

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

func (s *WasmMgrVCUR) Create(
	ctx cosmos.Context,
	creator cosmos.AccAddress,
	wasmCode []byte,
) (wasmtypes.MsgStoreCodeResponse, error) {
}

func (s *WasmMgrVCUR) Instantiate(ctx context.Context,
	codeID uint64,
	creator, admin cosmos.AccAddress,
	initMsg []byte,
	label string,
	deposit cosmos.Coins,
) (wasmtypes.MsgInstantiateContractResponse, error) {
}

func (s *WasmMgrVCUR) Instantiate2(
	ctx context.Context,
	codeID uint64,
	creator, admin cosmos.AccAddress,
	initMsg []byte,
	label string,
	deposit cosmos.Coins,
	salt []byte,
	fixMsg bool,
) (wasmtypes.MsgInstantiateContract2Response, error) {
}

func (s *WasmMgrVCUR) Migrate(ctx context.Context,
	contractAddress, caller cosmos.AccAddress,
	newCodeID uint64,
	msg []byte,
) (wasmtypes.MsgMigrateContractResponse, error) {
}

func (s *WasmMgrVCUR) Sudo(ctx cosmos.Context, contractAddres, caller cosmos.AccAddress, msg []byte) (wasmtypes.MsgSudoContractResponse, error) {
	return wasmtypes.MsgSudoContractResponse{}, nil
}
