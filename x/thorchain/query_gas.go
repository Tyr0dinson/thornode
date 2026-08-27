package thorchain

import (
	"context"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	gogogrpc "github.com/cosmos/gogoproto/grpc"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// queryGasServer decorates THORChain's query service registration. The normal
// ABCI query path remains unchanged. Direct gRPC queries can opt in to receive
// the SDK gas meter's final consumption as response metadata, which the
// regression runner records alongside its existing query assertions.
type queryGasServer struct {
	gogogrpc.Server
}

func (s queryGasServer) RegisterService(sd *grpc.ServiceDesc, handler interface{}) {
	wrapped := *sd
	wrapped.Methods = make([]grpc.MethodDesc, len(sd.Methods))
	for i, method := range sd.Methods {
		inner := method.Handler
		wrapped.Methods[i] = grpc.MethodDesc{
			MethodName: method.MethodName,
			Handler: func(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
				if interceptor == nil {
					return inner(srv, ctx, dec, nil)
				}

				trackGas := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
					return interceptor(ctx, req, info, func(queryCtx context.Context, queryReq interface{}) (resp interface{}, err error) {
						defer reportQueryGas(queryCtx)
						return handler(queryCtx, queryReq)
					})
				}
				return inner(srv, ctx, dec, trackGas)
			},
		}
	}
	s.Server.RegisterService(&wrapped, handler)
}

func reportQueryGas(ctx context.Context) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok || len(md.Get(types.QueryGasRequestMetadata)) == 0 {
		return
	}

	gasUsed := sdk.UnwrapSDKContext(ctx).GasMeter().GasConsumed()
	_ = grpc.SetHeader(ctx, metadata.Pairs(types.QueryGasUsedMetadata, strconv.FormatUint(gasUsed, 10)))
}
