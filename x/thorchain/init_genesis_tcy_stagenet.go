//go:build stagenet
// +build stagenet

package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// setupInitialTCYBalances mints the full stagenet TCY supply and
// distributes it to the Treasury (SNode 1) and the TCYClaiming module,
// matching the Migrate4to5 behaviour. Called from InitGenesis so a
// fresh chain boots with TCY already in flight — no post-boot upgrade
// required to activate the claim flow.
//
// Totals:
//
//	210,000,000 TCY (8 decimals) minted to x/thorchain
//	  1,800,000 TCY → sthor1wzegxe0rjvfpk337ggmtlj8d93vc446zsadvxv (treasury / SNode 1)
//	208,200,000 TCY → TCYClaiming module (draws for MsgTCYClaim)
func setupInitialTCYBalances(ctx cosmos.Context, k keeper.Keeper) error {
	totalAmount := cosmos.NewUint(210_000_000_00000000)
	treasuryAmount := cosmos.NewUint(1_800_000_00000000)
	claimingAmount := totalAmount.Sub(treasuryAmount)

	totalCoin := common.NewCoin(common.TCY, totalAmount)
	if err := k.MintToModule(ctx, ModuleName, totalCoin); err != nil {
		return err
	}

	claimingCoin := common.NewCoin(common.TCY, claimingAmount)
	if err := k.SendFromModuleToModule(ctx, ModuleName, TCYClaimingName, common.NewCoins(claimingCoin)); err != nil {
		return err
	}

	treasuryCoin := common.NewCoin(common.TCY, treasuryAmount)
	treasuryAddr, err := common.NewAddress("sthor1wzegxe0rjvfpk337ggmtlj8d93vc446zsadvxv")
	if err != nil {
		return err
	}
	treasuryAccAddr, err := treasuryAddr.AccAddress()
	if err != nil {
		return err
	}
	if err := k.SendFromModuleToAccount(ctx, ModuleName, treasuryAccAddr, common.NewCoins(treasuryCoin)); err != nil {
		return err
	}

	return nil
}
