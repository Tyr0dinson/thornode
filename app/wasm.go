package app

import (
	"context"
	"fmt"

	"github.com/CosmWasm/wasmd/x/wasm"
	wasmkeeper "github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v2/types"
	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cosmos/cosmos-sdk/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/cosmos/gogoproto/proto"
	apitypes "gitlab.com/thorchain/thornode/v3/api/types"
)

var wasmAcceptedQueries = AcceptedQueries{
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
})

// CustomWasmModule re-exposes the underlying module's methods,
// but prevents Services from being registered, as these
// should be registered and handled in x/thorchain
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

type (
	AcceptedQueries map[string]func() proto.Message
	grpcQuerierFn   func(ctx sdk.Context, request *wasmvmtypes.GrpcQuery) (proto.Message, error)
)

// Temporary patch to address concurrency issues. See https://github.com/CosmWasm/wasmd/issues/2051
func AcceptListGrpcQuerier(acceptList AcceptedQueries, queryRouter wasmkeeper.GRPCQueryRouter, codec codec.Codec) grpcQuerierFn {
	return func(ctx sdk.Context, request *wasmvmtypes.GrpcQuery) (proto.Message, error) {
		protoResponseFn, accepted := acceptList[request.Path]
		if !accepted {
			return nil, wasmvmtypes.UnsupportedRequest{Kind: fmt.Sprintf("'%s' path is not allowed from the contract", request.Path)}
		}

		handler := queryRouter.Route(request.Path)
		if handler == nil {
			return nil, wasmvmtypes.UnsupportedRequest{Kind: fmt.Sprintf("No route to query '%s'", request.Path)}
		}

		res, err := handler(ctx, &abci.RequestQuery{
			Data: request.Data,
			Path: request.Path,
		})
		if err != nil {
			return nil, err
		}

		protoResponse := protoResponseFn()
		// decode the query response into the expected protobuf message
		err = codec.Unmarshal(res.Value, protoResponse)
		if err != nil {
			return nil, err
		}

		return protoResponse, nil
	}
}
