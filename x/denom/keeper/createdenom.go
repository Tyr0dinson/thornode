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
	admin, err := sdk.AccAddressFromBech32(creatorAddr)
	if err != nil {
		return "", err
	}

	err = k.SetAdmin(ctx, denom, admin)
	if err != nil {
		return "", err
	}

	k.addDenomFromCreator(ctx, creatorAddr, denom)
	return denom, nil
}
