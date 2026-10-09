package app

import (
	"context"
	"errors"
	"testing"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/runtime"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

func TestNewAnteHandlerRequirementsCov(t *testing.T) {
	a := newAppCov(t, dbm.NewMemDB(), t.TempDir())
	nodeConfig := wasmtypes.DefaultNodeConfig()

	opts := HandlerOptions{}
	steps := []struct {
		want string
		set  func()
	}{
		{"account keeper is required", func() { opts.AccountKeeper = a.AccountKeeper }},
		{"bank keeper is required", func() { opts.BankKeeper = a.BankKeeper }},
		{"sign mode handler is required", func() { opts.SignModeHandler = a.TxConfig().SignModeHandler() }},
		{"wasm config is required", func() { opts.NodeConfig = &nodeConfig }},
		{"wasm keeper is required", func() { opts.WasmKeeper = &a.WasmKeeper }},
		{"thorchain keeper is required", func() { opts.THORChainKeeper = a.ThorchainKeeper }},
		{"wasm store service is required", func() {
			opts.TXCounterStoreService = runtime.NewKVStoreService(a.GetKey(wasmtypes.StoreKey))
		}},
	}
	for _, step := range steps {
		h, err := NewAnteHandler(opts)
		require.ErrorContains(t, err, step.want)
		require.Nil(t, h)
		step.set()
	}

	opts.SigGasConsumer = SigGasConsumer
	h, err := NewAnteHandler(opts)
	require.NoError(t, err)
	require.NotNil(t, h)
}

func TestBlockLogDecoratorCov(t *testing.T) {
	ctx := sdk.Context{}.WithContext(context.Background())
	called := false
	var gotSimulate bool
	next := func(c sdk.Context, _ sdk.Tx, simulate bool) (sdk.Context, error) {
		called = true
		gotSimulate = simulate
		return c, errors.New("next-err")
	}
	_, err := blockLogDecorator{}.AnteHandle(ctx, nil, true, next)
	require.True(t, called)
	require.True(t, gotSimulate)
	require.EqualError(t, err, "next-err")
}

var errUnregisteredCov = errors.New("trading is halted")

func TestABCIParityErrorCov(t *testing.T) {
	require.NoError(t, abciParityError(nil))

	// explicit grpc status is preserved
	explicit := grpcstatus.Error(codes.NotFound, "explicit")
	require.Equal(t, explicit, abciParityError(explicit))

	// unregistered errors become InvalidArgument with the ABCI path's message
	err := abciParityError(errUnregisteredCov)
	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.InvalidArgument, st.Code())
	require.Equal(t, "trading is halted: invalid request", st.Message())

	for _, tc := range []struct {
		err  error
		code codes.Code
	}{
		{errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "bad"), codes.InvalidArgument},
		{errorsmod.Wrap(sdkerrors.ErrUnauthorized, "nope"), codes.Unauthenticated},
		{errorsmod.Wrap(sdkerrors.ErrKeyNotFound, "missing"), codes.NotFound},
		{errorsmod.Wrap(sdkerrors.ErrInsufficientFunds, "poor"), codes.Unknown},
	} {
		st, ok := grpcstatus.FromError(abciParityError(tc.err))
		require.True(t, ok)
		require.Equal(t, tc.code, st.Code(), tc.err.Error())
		require.Equal(t, tc.err.Error(), st.Message())
	}
}

// recordingServerCov captures the service descriptor registered with it.
type recordingServerCov struct {
	desc    *grpc.ServiceDesc
	handler interface{}
}

func (r *recordingServerCov) RegisterService(sd *grpc.ServiceDesc, h interface{}) {
	r.desc = sd
	r.handler = h
}

func TestABCIParityServerCov(t *testing.T) {
	inner := &recordingServerCov{}
	srv := abciParityServer{inner}

	var calls int
	sd := &grpc.ServiceDesc{
		ServiceName: "cov.Service",
		Methods: []grpc.MethodDesc{
			{
				MethodName: "Fail",
				Handler: func(_ interface{}, _ context.Context, _ func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
					calls++
					return nil, errUnregisteredCov
				},
			},
			{
				MethodName: "Ok",
				Handler: func(_ interface{}, _ context.Context, _ func(interface{}) error, _ grpc.UnaryServerInterceptor) (interface{}, error) {
					calls++
					return "resp", nil
				},
			},
		},
	}
	srv.RegisterService(sd, "handler")
	require.NotNil(t, inner.desc)
	require.Equal(t, "handler", inner.handler)
	require.Equal(t, "cov.Service", inner.desc.ServiceName)
	require.Len(t, inner.desc.Methods, 2)
	// the original descriptor is not modified
	require.NotSame(t, sd, inner.desc)

	resp, err := inner.desc.Methods[0].Handler(nil, context.Background(), nil, nil)
	require.Nil(t, resp)
	st, ok := grpcstatus.FromError(err)
	require.True(t, ok)
	require.Equal(t, codes.InvalidArgument, st.Code())

	resp, err = inner.desc.Methods[1].Handler(nil, context.Background(), nil, nil)
	require.NoError(t, err)
	require.Equal(t, "resp", resp)
	require.Equal(t, 2, calls)

	// the app wires its gRPC services through the parity wrapper
	a := newAppCov(t, dbm.NewMemDB(), t.TempDir())
	gsrv := grpc.NewServer()
	a.RegisterGRPCServerWithSkipCheckHeader(gsrv, true)
	require.NotEmpty(t, gsrv.GetServiceInfo())
}
