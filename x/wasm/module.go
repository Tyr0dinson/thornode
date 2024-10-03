package wasm

import (
	"cosmossdk.io/core/appmodule"

	"github.com/cosmos/cosmos-sdk/baseapp"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/types/module"

	"github.com/CosmWasm/wasmd/x/wasm"
	"github.com/CosmWasm/wasmd/x/wasm/exported"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	"github.com/CosmWasm/wasmd/x/wasm/simulation"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"

	tckeeper "gitlab.com/thorchain/thornode/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
	"gitlab.com/thorchain/thornode/x/wasm/keeper"
)

var (
	_ module.AppModuleBasic      = AppModuleBasic{}
	_ module.AppModuleSimulation = AppModule{}
)

// AppModuleBasic defines the basic application module used by the wasm module.
type AppModuleBasic struct {
	wasm.AppModuleBasic
}

// ____________________________________________________________________________
var _ appmodule.AppModule = AppModule{}

// AppModule implements an application module for the wasm module.
type AppModule struct {
	wasm.AppModule
	tcKeeper tckeeper.Keeper
	keeper   *wasmkeeper.Keeper
	ss       exported.Subspace
}

// NewAppModule creates a new AppModule object
func NewAppModule(
	cdc codec.Codec,
	keeper *wasmkeeper.Keeper,
	validatorSetSource wasmkeeper.ValidatorSetSource,
	ak wasmtypes.AccountKeeper,
	bk simulation.BankKeeper,
	tk tckeeper.Keeper,
	router *baseapp.MsgServiceRouter,
	ss exported.Subspace,
) AppModule {
	wasmmodule := wasm.NewAppModule(cdc, keeper, validatorSetSource, ak, bk, router, ss)
	return AppModule{
		AppModule: wasmmodule,
		keeper:    keeper,
		ss:        ss,
	}
}

func (am AppModule) RegisterServices(cfg module.Configurator) {
	wasmtypes.RegisterMsgServer(cfg.MsgServer(), keeper.NewMsgServerImpl(
		wasmkeeper.NewGovPermissionKeeper(am.keeper),
		am.tcKeeper,
	))

	wasmtypes.RegisterQueryServer(cfg.QueryServer(), wasmkeeper.Querier(am.keeper))

	m := wasmkeeper.NewMigrator(*am.keeper, am.ss)
	err := cfg.RegisterMigration(types.ModuleName, 1, m.Migrate1to2)
	if err != nil {
		panic(err)
	}
	err = cfg.RegisterMigration(types.ModuleName, 2, m.Migrate2to3)
	if err != nil {
		panic(err)
	}
	err = cfg.RegisterMigration(types.ModuleName, 3, m.Migrate3to4)
	if err != nil {
		panic(err)
	}
}
