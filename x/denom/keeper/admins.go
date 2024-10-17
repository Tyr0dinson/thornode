package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/x/denom/types"
)

// GetAdmin returns the authority metadata for a specific denom
func (k Keeper) GetAdmin(ctx sdk.Context, denom string) (sdk.AccAddress, error) {
	bz := k.GetDenomPrefixStore(ctx, denom).Get([]byte(types.DenomAdminKey))
	return bz, nil
}

// SetAdmin stores authority metadata for a specific denom
func (k Keeper) SetAdmin(ctx sdk.Context, denom string, addr sdk.AccAddress) error {
	store := k.GetDenomPrefixStore(ctx, denom)
	store.Set([]byte(types.DenomAdminKey), addr)
	return nil
}
