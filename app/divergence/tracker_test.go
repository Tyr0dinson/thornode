package divergence

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func TestTrackerCountsOverlap(t *testing.T) {
	tr := NewTracker()
	sim := tr.WrapSimulate(func([]byte) (sdk.GasInfo, *sdk.Result, error) {
		return sdk.GasInfo{GasUsed: 7}, nil, nil
	})

	// outside a block
	gi, _, err := sim(nil)
	require.NoError(t, err)
	require.Equal(t, uint64(7), gi.GasUsed)
	tr.QueryStart()
	require.False(t, tr.InBlock())

	// inside a block
	tr.BeginBlock()
	require.True(t, tr.InBlock())
	_, _, _ = sim(nil)
	tr.QueryStart()
	tr.QueryStart()
	tr.EndBlock()
	require.False(t, tr.InBlock())

	o := tr.TakeBlock()
	require.Equal(t, Overlap{Simulate: 2, SimulateOverlap: 1, Query: 3, QueryOverlap: 2}, o)
	require.Equal(t, Overlap{}, tr.TakeBlock())

	// a Simulate running when the block starts
	done := tr.simulateStart()
	tr.BeginBlock()
	done()
	tr.EndBlock()
	require.Equal(t, uint64(1), tr.Totals().SimulateInflightAtBegin) // not yet taken
	o = tr.TakeBlock()
	require.Equal(t, uint64(1), o.SimulateInflightAtBegin)
	require.Equal(t, uint64(1), o.Simulate)
	require.Equal(t, uint64(0), o.SimulateOverlap)

	require.Equal(t, Overlap{Simulate: 3, SimulateOverlap: 1, SimulateInflightAtBegin: 1, Query: 3, QueryOverlap: 2}, tr.Totals())
}

func TestNilTrackerIsNoop(t *testing.T) {
	var tr *Tracker
	tr.BeginBlock()
	tr.EndBlock()
	tr.QueryStart()
	tr.simulateStart()()
	require.False(t, tr.InBlock())
	require.Equal(t, Overlap{}, tr.TakeBlock())
	require.Equal(t, Overlap{}, tr.Totals())

	called := false
	f := func([]byte) (sdk.GasInfo, *sdk.Result, error) { called = true; return sdk.GasInfo{}, nil, nil }
	_, _, _ = tr.WrapSimulate(f)(nil)
	require.True(t, called)
	srv := &fakeGRPCServer{}
	require.Equal(t, srv, tr.WrapGRPCServer(srv))
}

type fakeGRPCServer struct {
	desc *grpc.ServiceDesc
}

func (f *fakeGRPCServer) RegisterService(sd *grpc.ServiceDesc, _ interface{}) { f.desc = sd }

func TestWrapGRPCServerCountsQueries(t *testing.T) {
	tr := NewTracker()
	inner := &fakeGRPCServer{}
	srv := tr.WrapGRPCServer(inner)
	errBoom := errors.New("boom")
	sd := &grpc.ServiceDesc{
		ServiceName: "svc",
		Methods: []grpc.MethodDesc{{
			MethodName: "M",
			Handler: func(interface{}, context.Context, func(interface{}) error, grpc.UnaryServerInterceptor) (interface{}, error) {
				return "resp", errBoom
			},
		}},
	}
	srv.RegisterService(sd, nil)
	require.NotNil(t, inner.desc)
	require.Equal(t, "svc", inner.desc.ServiceName)
	require.Equal(t, "M", inner.desc.Methods[0].MethodName)

	tr.BeginBlock()
	resp, err := inner.desc.Methods[0].Handler(nil, context.Background(), nil, nil)
	require.Equal(t, "resp", resp)
	require.ErrorIs(t, err, errBoom)
	tr.EndBlock()
	_, _ = inner.desc.Methods[0].Handler(nil, context.Background(), nil, nil)
	require.Equal(t, Overlap{Query: 2, QueryOverlap: 1}, tr.TakeBlock())
}
