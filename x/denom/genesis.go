package denom

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/x/denom/keeper"
	"gitlab.com/thorchain/thornode/x/denom/types"
)

// InitGenesis initializes the denom module's state from a provided genesis
// state.
func InitGenesis(ctx sdk.Context, k keeper.Keeper, genState types.GenesisState) {
	for _, genDenom := range genState.GetCustomDenoms() {
		creator, id, err := types.DeconstructDenom(genDenom.GetDenom())
		if err != nil {
			panic(err)
		}
		_, err = k.CreateDenom(ctx, creator, id)
		if err != nil {
			panic(err)
		}
		err = k.SetAuthorityMetadata(ctx, genDenom.GetDenom(), genDenom.GetAuthorityMetadata())
		if err != nil {
			panic(err)
		}
	}

}

// ExportGenesis returns the denom module's exported genesis.
func ExportGenesis(ctx sdk.Context, k keeper.Keeper) *types.GenesisState {
	genDenoms := []types.GenesisDenom{}
	iterator := k.GetAllDenomsIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		denom := string(iterator.Value())

		authorityMetadata, err := k.GetAuthorityMetadata(ctx, denom)
		if err != nil {
			panic(err)
		}

		genDenoms = append(genDenoms, types.GenesisDenom{
			Denom:             denom,
			AuthorityMetadata: authorityMetadata,
		})
	}

	return &types.GenesisState{
		CustomDenoms: genDenoms,
	}
}
