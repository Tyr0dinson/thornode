package keeper

import sdk "github.com/cosmos/cosmos-sdk/types"

// MintToCov exposes mintTo to the external test package.
func (k Keeper) MintToCov(ctx sdk.Context, amount sdk.Coin, to string) error {
	return k.mintTo(ctx, amount, to)
}

// BurnFromCov exposes burnFrom to the external test package.
func (k Keeper) BurnFromCov(ctx sdk.Context, amount sdk.Coin, from string) error {
	return k.burnFrom(ctx, amount, from)
}
