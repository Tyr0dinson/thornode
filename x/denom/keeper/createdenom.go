package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/x/denom/types"
)

func (k Keeper) CreateDenom(ctx sdk.Context, creatorAddr string, denomId string) (newTokenDenom string, err error) {
	denom, err := types.GetTokenDenom(creatorAddr, denomId)
	if err != nil {
		return "", err
	}

	_, found := k.bankKeeper.GetDenomMetaData(ctx, denom)
	if found {
		return "", types.ErrDenomExists
	}

	authorityMetadata := types.DenomAuthorityMetadata{
		Admin: creatorAddr,
	}
	err = k.SetAuthorityMetadata(ctx, denom, authorityMetadata)
	if err != nil {
		return "", err
	}

	k.addDenomFromCreator(ctx, creatorAddr, denom)
	return denom, nil
}
