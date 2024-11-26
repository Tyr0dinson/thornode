package app

import (
	"github.com/CosmWasm/wasmd/x/wasm"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/cosmos/cosmos-sdk/types/module"
	apitypes "gitlab.com/thorchain/thornode/v3/api/types"
)

var wasmAcceptedQueries = wasmkeeper.AcceptedQueries{
	"/types.Query/Pool": &apitypes.QueryPoolResponse{},
}

// CustomWasmModule re-exposes the underlying module's methods,
// but prevents Services from being registered, as these
// should be registered and handled in x/thorchain
// N.b. we can't use app.MsgServiceRouter as the router arguemnt to wasm.NewKeeper is a concrete type of baseapp.MsgServiceRouter,
// and cosmwasm submessages rely on the ability to call back into eg MsgExecuteContract
type CustomWasmModule struct {
	*wasm.AppModule
}

// NewCustomWasmModule creates a new CustomWasmModule object
func NewCustomWasmModule(
	module *wasm.AppModule,
) CustomWasmModule {
	return CustomWasmModule{module}
}

func (am CustomWasmModule) RegisterServices(cfg module.Configurator) {
}
