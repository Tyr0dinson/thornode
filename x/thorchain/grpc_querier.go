package thorchain

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

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
	ctx := sdk.UnwrapSDKContext(c)
	return queryPool(ctx, req, s.mgr)
}

func (s queryServer) Pools(c context.Context, req *types.QueryPoolsRequest) (*types.QueryPoolsResponse, error) {
	ctx := sdk.UnwrapSDKContext(c)
	return queryPools(ctx, req, s.mgr)
}
