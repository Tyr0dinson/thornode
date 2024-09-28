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

func (s *DummyWasmManager) Sudo(ctx cosmos.Context, msg *wasmtypes.MsgSudoContract) (*wasmtypes.MsgSudoContractResponse, error) {
	return &wasmtypes.MsgSudoContractResponse{}, nil
}
