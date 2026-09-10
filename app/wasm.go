package app

import (
	"context"

	"github.com/CosmWasm/wasmd/x/wasm"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v3/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/gogoproto/proto"
	apitypes "gitlab.com/thorchain/thornode/v3/api/types"
)

var wasmAcceptedQueries = wasmkeeper.AcceptedQueries{
	"/types.Query/Network":           func() proto.Message { return &apitypes.QueryNetworkResponse{} },
	"/types.Query/LiquidityProvider": func() proto.Message { return &apitypes.QueryLiquidityProviderResponse{} },
	"/types.Query/MimirWithKey":      func() proto.Message { return &apitypes.QueryMimirWithKeyResponse{} },
	"/types.Query/Node":              func() proto.Message { return &apitypes.QueryNodeResponse{} },
	"/types.Query/OutboundFee":       func() proto.Message { return &apitypes.QueryOutboundFeeResponse{} },
	"/types.Query/Pool":              func() proto.Message { return &apitypes.QueryPoolResponse{} },
	"/types.Query/QuoteSwap":         func() proto.Message { return &apitypes.QueryQuoteSwapResponse{} },
	"/types.Query/SecuredAsset":      func() proto.Message { return &apitypes.QuerySecuredAssetResponse{} },
	"/types.Query/OraclePrice":       func() proto.Message { return &apitypes.QueryOraclePriceResponse{} },
	"/types.Query/SwapQueue":         func() proto.Message { return &apitypes.QuerySwapQueueResponse{} },
}

// Support slightly larger wasm files
var WasmMaxSize = 2_624_000

var WasmGasRegister = wasmtypes.NewWasmGasRegister(wasmtypes.WasmGasRegisterConfig{
	InstanceCost:               wasmtypes.DefaultInstanceCost,
	InstanceCostDiscount:       wasmtypes.DefaultInstanceCostDiscount,
	CompileCost:                wasmtypes.DefaultCompileCost * 100,
	GasMultiplier:              wasmtypes.DefaultGasMultiplier,
	EventPerAttributeCost:      wasmtypes.DefaultPerAttributeCost,
	CustomEventCost:            wasmtypes.DefaultPerCustomEventCost,
	EventAttributeDataCost:     wasmtypes.DefaultEventAttributeDataCost,
	EventAttributeDataFreeTier: wasmtypes.DefaultEventAttributeDataFreeTier,
	ContractMessageDataCost:    wasmtypes.DefaultContractMessageDataCost,
	// Wasm compile/store cost 100x due to auto-pinning
	// Contracts are stored in memory my default, rather than on disk
	// Standard contract ~ 20m gas to store, vs 50-200k for a regular contract execution
	UncompressCost: wasmvmtypes.UFraction{Numerator: 15, Denominator: 1},
	// Preserve the previous release's lack of a separate pin/unpin gas charge.
	PinCodeCost:   0,
	UnpinCodeCost: 0,
})

// CustomWasmModule re-exposes the underlying module's methods,
// but registers only migrations. Message and query services remain handled
// in x/thorchain.
type CustomWasmModule struct {
	*wasm.AppModule
	migrator wasmkeeper.Migrator
}

// NewCustomWasmModule creates a new CustomWasmModule object
func NewCustomWasmModule(
	module *wasm.AppModule,
	migrator wasmkeeper.Migrator,
) CustomWasmModule {
	return CustomWasmModule{module, migrator}
}

func (am CustomWasmModule) RegisterServices(cfg module.Configurator) {
	for i, migration := range []module.MigrationHandler{
		am.migrator.Migrate1to2,
		am.migrator.Migrate2to3,
		am.migrator.Migrate3to4,
	} {
		if err := cfg.RegisterMigration(wasmtypes.ModuleName, uint64(i+1), migration); err != nil {
			panic(err)
		}
	}
}

// x/wasm uses bankkeeper.IsSendEnabledCoins to check the movement of
// funds when instantiating, executing, sudoing, and executing submsgs
// WasmBankKeeper bypasses the IsSendEnabledCoins check to allow funds
// to be transferred for these actions, without affecting the behaviour
// of x/bank MsgSend
type WasmBankKeeper struct {
	wasmtypes.BankKeeper
}

func NewWasmBankKeeper(keeper wasmtypes.BankKeeper) WasmBankKeeper {
	return WasmBankKeeper{keeper}
}

func (c WasmBankKeeper) IsSendEnabledCoins(ctx context.Context, coins ...sdk.Coin) error {
	return nil
}
