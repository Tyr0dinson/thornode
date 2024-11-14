package app

import (
	"errors"

	"github.com/CosmWasm/wasmd/x/wasm/keeper"
	wasmvmtypes "github.com/CosmWasm/wasmvm/v2/types"
	abci "github.com/cometbft/cometbft/abci/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/gogoproto/proto"
	apitypes "gitlab.com/thorchain/thornode/api/types"
)

func NewWasmQueryHandler(app *THORChainApp) keeper.QueryPlugins {
	return keeper.DefaultQueryPlugins(
		app.BankKeeper, app.StakingKeeper, nil, nil, app.WasmKeeper,
	).Merge(&keeper.QueryPlugins{
		Grpc: func(ctx sdk.Context, request *wasmvmtypes.GrpcQuery) (proto.Message, error) {
			switch request.Path {
			case "/types.Query/Pool":
				res, err := app.GRPCQueryRouter().Route(request.Path)(ctx, &abci.RequestQuery{
					Data: request.Data,
					Path: request.Path,
				})
				if err != nil {
					return nil, err
				}
				var val apitypes.QueryPoolResponse
				proto.Unmarshal(res.Value, &val)
				return &val, nil
			default:
				return nil, errors.New("unsupported request")
			}
		},
	})
}
