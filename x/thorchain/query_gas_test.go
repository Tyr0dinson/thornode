package thorchain

import (
	"context"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type capturedQueryService struct {
	desc    *grpc.ServiceDesc
	handler interface{}
}

func (s *capturedQueryService) RegisterService(desc *grpc.ServiceDesc, handler interface{}) {
	s.desc = desc
	s.handler = handler
}

type capturedTransportStream struct {
	header metadata.MD
}

func (*capturedTransportStream) Method() string { return "/test.Query/Gas" }
func (s *capturedTransportStream) SetHeader(md metadata.MD) error {
	s.header = metadata.Join(s.header, md)
	return nil
}
func (s *capturedTransportStream) SendHeader(md metadata.MD) error {
	return s.SetHeader(md)
}
func (*capturedTransportStream) SetTrailer(metadata.MD) error { return nil }

func TestQueryGasServerReportsConsumedGasWhenRequested(t *testing.T) {
	capture := &capturedQueryService{}
	queryGasServer{Server: capture}.RegisterService(&grpc.ServiceDesc{
		ServiceName: "test.Query",
		Methods: []grpc.MethodDesc{{
			MethodName: "Gas",
			Handler: func(_ interface{}, ctx context.Context, _ func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
				handler := func(ctx context.Context, _ interface{}) (interface{}, error) {
					sdk.UnwrapSDKContext(ctx).GasMeter().ConsumeGas(37, "test query")
					return struct{}{}, nil
				}
				return interceptor(ctx, struct{}{}, &grpc.UnaryServerInfo{FullMethod: "/test.Query/Gas"}, handler)
			},
		}},
	}, struct{}{})

	stream := &capturedTransportStream{}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(types.QueryGasRequestMetadata, "true"))
	ctx = grpc.NewContextWithServerTransportStream(ctx, stream)
	queryContextInterceptor := func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		sdkCtx := sdk.Context{}.
			WithContext(ctx).
			WithGasMeter(storetypes.NewGasMeter(1_000))
		return handler(sdkCtx, req)
	}

	if _, err := capture.desc.Methods[0].Handler(capture.handler, ctx, func(interface{}) error { return nil }, queryContextInterceptor); err != nil {
		t.Fatal(err)
	}
	if got := stream.header.Get(types.QueryGasUsedMetadata); len(got) != 1 || got[0] != "37" {
		t.Fatalf("unexpected query gas metadata: %v", got)
	}
}

func TestQueryGasServerLeavesABCIQueriesUnchanged(t *testing.T) {
	capture := &capturedQueryService{}
	queryGasServer{Server: capture}.RegisterService(&grpc.ServiceDesc{
		ServiceName: "test.Query",
		Methods: []grpc.MethodDesc{{
			MethodName: "ABCI",
			Handler: func(_ interface{}, _ context.Context, _ func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
				if interceptor != nil {
					t.Fatal("ABCI query unexpectedly received an interceptor")
				}
				return struct{}{}, nil
			},
		}},
	}, struct{}{})

	if _, err := capture.desc.Methods[0].Handler(capture.handler, sdk.Context{}, func(interface{}) error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
}
