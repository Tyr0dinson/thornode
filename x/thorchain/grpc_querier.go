package thorchain

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

var _ types.QueryServer = queryServer{}

type queryServer struct {
	mgr Manager
	k   keeper.Keeper
}

func NewQueryServer(mgr Manager) types.QueryServer {
	return queryServer{mgr: mgr, k: mgr.Keeper()}
}

func (s queryServer) Pool(c context.Context, req *types.QueryPoolRequest) (*types.QueryPoolResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(c)

	asset, err := common.NewAsset(req.Asset)
	if err != nil {
		return nil, err
	}

	pool, err := s.k.GetPool(ctx, asset)
	if err != nil {
		return nil, err
	}

	return &types.QueryPoolResponse{Pool: &pool}, nil
}

func (s queryServer) Pools(c context.Context, req *types.QueryPoolsRequest) (*types.QueryPoolsResponse, error) {
	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "empty request")
	}

	ctx := sdk.UnwrapSDKContext(c)
	poolsStore := s.k.GetPoolStore(ctx)

	var pools []*Pool
	pageRes, err := query.Paginate(poolsStore, req.Pagination, func(key, value []byte) error {
		var pool Pool
		s.k.Cdc().MustUnmarshal(value, &pool)
		pools = append(pools, &pool)
		return nil
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "paginate: %v", err)
	}

	return &types.QueryPoolsResponse{Pools: pools, Pagination: pageRes}, nil
}
