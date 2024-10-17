package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/x/denom/types"
)

var _ types.QueryServer = Keeper{}

func (k Keeper) DenomAdmin(ctx context.Context, req *types.QueryDenomAdminRequest) (*types.QueryDenomAdminResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	admin, err := k.GetAdmin(sdkCtx, req.GetDenom())
	if err != nil {
		return nil, err
	}

	return &types.QueryDenomAdminResponse{Admin: admin.String()}, nil
}

func (k Keeper) DenomsFromCreator(ctx context.Context, req *types.QueryDenomsFromCreatorRequest) (*types.QueryDenomsFromCreatorResponse, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	denoms := k.getDenomsFromCreator(sdkCtx, req.GetCreator())
	return &types.QueryDenomsFromCreatorResponse{Denoms: denoms}, nil
}
