package upgrades

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"sort"
	"text/tabwriter"

	errorsmod "cosmossdk.io/errors"
	"github.com/blang/semver"
	sdk "github.com/cosmos/cosmos-sdk/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var amountPrinter = message.NewPrinter(language.English)

type coinSpec struct {
	asset  string
	amount string
}

func Execute(ctx context.Context, k keeper.Keeper, _ bankkeeper.Keeper) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	if err := executeADR28(sdkCtx, k); err != nil {
		sdkCtx.Logger().Error("ADR28 execution failed", "error", err)
		return err
	}
	return nil
}

func executeADR28(sdkCtx cosmos.Context, k keeper.Keeper) error {
	treasury, err := k.GetModuleAddress(types.TreasuryName)
	if err != nil {
		return errorsmod.Wrap(err, "get treasury module address")
	}
	reserve, err := k.GetModuleAddress(types.ReserveName)
	if err != nil {
		return errorsmod.Wrap(err, "get reserve module address")
	}

	liabilities, err := liabilityShortfall(sdkCtx, k)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	runeRemoved, err := processLiabilities(w, sdkCtx, k, liabilities, treasury, reserve)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\nrune removed\t%s\n", formatAmount(runeRemoved)); err != nil {
		return errorsmod.Wrap(err, "print rune removed total")
	}

	if err := w.Flush(); err != nil {
		return errorsmod.Wrap(err, "flush ADR28 output")
	}

	return nil
}

func assets(sdkCtx cosmos.Context, k keeper.Keeper) (common.Coins, error) {
	thor1t8f467qdkpmuflgwvgvvlr86r0kldnnvtr9847, err := fixedCoins(
		coinSpec{"DOGE.DOGE", "201000002"},
		coinSpec{"GAIA.ATOM", "1880012100"},
		coinSpec{"SOL.SOL", "2942492147"},
		coinSpec{"TRON.TRX", "14737953600"},
		coinSpec{"XRP.XRP", "200000100"},
	)
	if err != nil {
		return nil, err
	}

	switchMgr, err := thorchain.GetSwitchManager(semver.Version{}, k, &thorchain.EventMgr{})
	if err != nil {
		return nil, errorsmod.Wrap(err, "get switch manager")
	}

	iterator := k.GetVaultIterator(sdkCtx)
	defer iterator.Close()

	total := common.Coins{}
	for ; iterator.Valid(); iterator.Next() {
		var vault types.Vault
		if err := k.Cdc().Unmarshal(iterator.Value(), &vault); err != nil {
			return nil, errorsmod.Wrap(err, "unmarshal vault")
		}

		address, err := vault.GetAddress(common.THORChain)
		if err != nil {
			return nil, errorsmod.Wrapf(err, "get THOR address for vault %s", vault.PubKey)
		}

		if address.String() == "thor1t8f467qdkpmuflgwvgvvlr86r0kldnnvtr9847" {
			total = total.Add(thor1t8f467qdkpmuflgwvgvvlr86r0kldnnvtr9847...)
			continue
		}

		total = total.Add(vault.Coins...)
	}

	total = total.NoneEmpty()

	for _, coin := range total {
		if switchMgr.IsSwitch(sdkCtx, coin.Asset) {
			total = total.SafeSub(coin)
		}
	}

	return total, nil
}

func liabilityShortfall(sdkCtx cosmos.Context, k keeper.Keeper) (common.Coins, error) {
	assets, err := assets(sdkCtx, k)
	if err != nil {
		return nil, err
	}
	liabilities, err := liabilities(sdkCtx, k)
	if err != nil {
		return nil, err
	}
	return sortedCoins(liabilities.SafeSub(assets...)), nil
}

func liabilities(sdkCtx cosmos.Context, k keeper.Keeper) (common.Coins, error) {
	pools, err := poolAssets(sdkCtx, k)
	if err != nil {
		return nil, err
	}
	trades, err := tradeAssets(sdkCtx, k)
	if err != nil {
		return nil, err
	}
	secured, err := securedAssets(sdkCtx, k)
	if err != nil {
		return nil, err
	}
	return pools.Add(trades...).Add(secured...), nil
}

func poolAssets(sdkCtx cosmos.Context, k keeper.Keeper) (common.Coins, error) {
	iterator := k.GetPoolIterator(sdkCtx)
	defer iterator.Close()

	total := common.Coins{}
	for ; iterator.Valid(); iterator.Next() {
		var pool types.Pool
		if err := k.Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return nil, errorsmod.Wrap(err, "unmarshal pool")
		}
		if pool.IsEmpty() || pool.BalanceAsset.IsZero() {
			continue
		}
		if !pool.Asset.IsExternalL1Asset() {
			continue
		}
		total = total.Add(common.NewCoin(pool.Asset, pool.BalanceAsset).WithDecimals(pool.Decimals))
	}

	return total, nil
}

func tradeAssets(sdkCtx cosmos.Context, k keeper.Keeper) (common.Coins, error) {
	iterator := k.GetTradeUnitIterator(sdkCtx)
	defer iterator.Close()

	total := common.Coins{}
	for ; iterator.Valid(); iterator.Next() {
		var unit types.TradeUnit
		if err := k.Cdc().Unmarshal(iterator.Value(), &unit); err != nil {
			return nil, errorsmod.Wrap(err, "unmarshal trade unit")
		}
		if unit.Asset.IsEmpty() || unit.Depth.IsZero() {
			continue
		}

		total = total.Add(common.NewCoin(unit.Asset.GetLayer1Asset(), unit.Depth).WithDecimals(common.THORChainDecimals))
	}

	return total, nil
}

func securedAssets(sdkCtx cosmos.Context, k keeper.Keeper) (common.Coins, error) {
	iterator := k.GetSecuredAssetIterator(sdkCtx)
	defer iterator.Close()

	total := common.Coins{}
	for ; iterator.Valid(); iterator.Next() {
		var asset types.SecuredAsset
		if err := k.Cdc().Unmarshal(iterator.Value(), &asset); err != nil {
			return nil, errorsmod.Wrap(err, "unmarshal secured asset")
		}
		if asset.Asset.IsEmpty() || asset.Depth.IsZero() {
			continue
		}

		total = total.Add(common.NewCoin(asset.Asset.GetLayer1Asset(), asset.Depth).WithDecimals(common.THORChainDecimals))
	}

	return total, nil
}

func processLiabilities(
	w *tabwriter.Writer,
	sdkCtx cosmos.Context,
	k keeper.Keeper,
	liabilities common.Coins,
	treasury common.Address,
	reserve common.Address,
) (cosmos.Uint, error) {
	if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		"asset", "liability", "treasury", "", "reserve", "", "synth", "", "", "remaining",
	); err != nil {
		return cosmos.ZeroUint(), errorsmod.Wrap(err, "print liability header")
	}
	if _, err := fmt.Fprintf(w, "\t\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n",
		"available", "consumed", "available", "consumed", "available", "consumed", "bps",
	); err != nil {
		return cosmos.ZeroUint(), errorsmod.Wrap(err, "print liability columns")
	}

	totalRuneRemoved := cosmos.ZeroUint()
	for _, liability := range sortedCoins(liabilities) {
		runeRemoved, err := processLiability(w, sdkCtx, k, liability, treasury, reserve)
		if err != nil {
			return totalRuneRemoved, err
		}
		totalRuneRemoved = totalRuneRemoved.Add(runeRemoved)
	}

	return totalRuneRemoved, nil
}

func processLiability(
	w *tabwriter.Writer,
	sdkCtx cosmos.Context,
	k keeper.Keeper,
	liability common.Coin,
	treasury common.Address,
	reserve common.Address,
) (cosmos.Uint, error) {
	if _, err := fmt.Fprintf(w, "%s\t", formatCoin(liability)); err != nil {
		return cosmos.ZeroUint(), errorsmod.Wrapf(err, "print liability row for %s", liability.Asset)
	}

	remaining := liability

	var err error
	totalRuneRemoved := cosmos.ZeroUint()
	remaining, runeRemoved, err := allocateLiquidity(w, sdkCtx, k, "treasury", remaining, treasury)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	totalRuneRemoved = totalRuneRemoved.Add(runeRemoved)

	remaining, runeRemoved, err = allocateLiquidity(w, sdkCtx, k, "reserve", remaining, reserve)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	totalRuneRemoved = totalRuneRemoved.Add(runeRemoved)

	remaining, err = allocateSynth(w, sdkCtx, k, remaining)
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	if _, err := fmt.Fprintf(w, "%s\n", formatAmount(remaining.Amount)); err != nil {
		return cosmos.ZeroUint(), errorsmod.Wrapf(err, "print remaining for %s", liability.Asset)
	}

	return totalRuneRemoved, nil
}

func allocateLiquidity(
	w *tabwriter.Writer,
	sdkCtx cosmos.Context,
	k keeper.Keeper,
	label string,
	liability common.Coin,
	address common.Address,
) (common.Coin, cosmos.Uint, error) {
	pool, err := k.GetPool(sdkCtx, liability.Asset)
	if err != nil {
		return liability, cosmos.ZeroUint(), errorsmod.Wrapf(err, "get pool for %s", liability.Asset)
	}

	available := cosmos.ZeroUint()
	consumed := cosmos.ZeroUint()
	runeRemoved := cosmos.ZeroUint()
	unitsConsumed := cosmos.ZeroUint()
	var lp types.LiquidityProvider

	if !pool.IsEmpty() && !pool.BalanceAsset.IsZero() && pool.Asset.IsExternalL1Asset() {
		synthSupply := k.GetTotalSupply(sdkCtx, pool.Asset.GetSyntheticAsset())
		lp, err = k.GetLiquidityProvider(sdkCtx, pool.Asset, address)
		if err != nil {
			return liability, cosmos.ZeroUint(), errorsmod.Wrapf(err, "get liquidity provider for %s at %s", pool.Asset, address)
		}
		if !lp.Units.IsZero() {
			pool.CalcUnits(synthSupply)
			poolUnits := pool.GetPoolUnits()
			if !poolUnits.IsZero() {
				if pool.Status == types.PoolStatus_Available && lp.RuneDepositValue.IsZero() && lp.AssetDepositValue.IsZero() {
					lp.RuneDepositValue = lp.RuneDepositValue.Add(common.GetSafeShare(lp.Units, poolUnits, pool.BalanceRune))
					lp.AssetDepositValue = lp.AssetDepositValue.Add(common.GetSafeShare(lp.Units, poolUnits, pool.BalanceAsset))
				}

				available = common.GetSafeShare(lp.Units, poolUnits, pool.BalanceAsset)
				runeAvailable := common.GetSafeShare(lp.Units, poolUnits, pool.BalanceRune)
				consumed = consumeAmount(liability.Amount, available)
				unitsConsumed = common.GetSafeShare(consumed, available, lp.Units)
				if !unitsConsumed.IsZero() {
					runeRemoved = consumeRune(available, consumed, runeAvailable)
				} else {
					consumed = cosmos.ZeroUint()
				}
			}
		}
	}

	pool.BalanceAsset = common.SafeSub(pool.BalanceAsset, consumed)
	pool.BalanceRune = common.SafeSub(pool.BalanceRune, runeRemoved)
	pool.LPUnits = common.SafeSub(pool.LPUnits, unitsConsumed)
	if err := k.SetPool(sdkCtx, pool); err != nil {
		return liability, cosmos.ZeroUint(), errorsmod.Wrapf(err, "set pool %s after %s rune deduction", pool.Asset, label)
	}
	if !unitsConsumed.IsZero() {
		lp.LastWithdrawHeight = sdkCtx.BlockHeight()
		lp.RuneDepositValue = common.SafeSub(lp.RuneDepositValue, common.GetSafeShare(unitsConsumed, lp.Units, lp.RuneDepositValue))
		lp.AssetDepositValue = common.SafeSub(lp.AssetDepositValue, common.GetSafeShare(unitsConsumed, lp.Units, lp.AssetDepositValue))
		lp.Units = common.SafeSub(lp.Units, unitsConsumed)
		if !lp.Units.Add(lp.PendingAsset).Add(lp.PendingRune).IsZero() {
			k.SetLiquidityProvider(sdkCtx, lp)
		} else {
			k.RemoveLiquidityProvider(sdkCtx, lp)
		}
	}
	if _, err := fmt.Fprintf(w, "%s\t%s\t", formatAmount(available), formatAmount(consumed)); err != nil {
		return liability, cosmos.ZeroUint(), errorsmod.Wrapf(err, "print %s allocation for %s", label, liability.Asset)
	}

	return common.NewCoin(liability.Asset, common.SafeSub(liability.Amount, consumed)), runeRemoved, nil
}

func allocateSynth(w *tabwriter.Writer, sdkCtx cosmos.Context, k keeper.Keeper, liability common.Coin) (common.Coin, error) {
	if liability.Asset.IsEmpty() {
		if err := printSynthAllocation(w, cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint()); err != nil {
			return liability, err
		}
		return liability, nil
	}

	synthAsset := liability.Asset.GetLayer1Asset().GetSyntheticAsset()
	saverPool, err := k.GetPool(sdkCtx, synthAsset)
	if err != nil {
		return liability, errorsmod.Wrapf(err, "get saver pool for %s", synthAsset)
	}
	if saverPool.IsEmpty() || saverPool.BalanceAsset.IsZero() {
		if err := printSynthAllocation(w, cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint()); err != nil {
			return liability, err
		}
		return liability, nil
	}
	if saverPool.LPUnits.IsZero() {
		if err := printSynthAllocation(w, cosmos.ZeroUint(), cosmos.ZeroUint(), cosmos.ZeroUint()); err != nil {
			return liability, err
		}
		return liability, nil
	}

	saverLPs, saverLPUnits, err := saverLiquidityProviders(sdkCtx, k, synthAsset)
	if err != nil {
		return liability, err
	}
	if !saverLPUnits.Equal(saverPool.LPUnits) {
		return liability, fmt.Errorf("saver LP units mismatch for %s: pool %s LPs %s", synthAsset, saverPool.LPUnits, saverLPUnits)
	}

	available := saverPool.BalanceAsset
	consumed := consumeAmount(liability.Amount, available)
	unitsConsumed := common.GetSafeShare(consumed, available, saverPool.LPUnits)
	if unitsConsumed.IsZero() {
		consumed = cosmos.ZeroUint()
	}
	bps := cosmos.ZeroUint()
	if !consumed.IsZero() {
		bps = consumed.Mul(cosmos.NewUint(10000)).Quo(available)
		remainingBalance := common.SafeSub(saverPool.BalanceAsset, consumed)
		burnCoin := common.NewCoin(synthAsset, consumed)
		if err := k.SendFromModuleToModule(sdkCtx, types.AsgardName, types.ModuleName, common.NewCoins(burnCoin)); err != nil {
			return liability, errorsmod.Wrapf(err, "move synth from asgard to module for burn %s", burnCoin)
		}
		if err := k.BurnFromModule(sdkCtx, types.ModuleName, burnCoin); err != nil {
			return liability, errorsmod.Wrapf(err, "burn synth %s", burnCoin)
		}
		consumeSaverLiquidityProviders(sdkCtx, k, saverLPs, saverPool.LPUnits, unitsConsumed)
		saverPool.BalanceAsset = remainingBalance
		saverPool.LPUnits = common.SafeSub(saverPool.LPUnits, unitsConsumed)
		if err := k.SetPool(sdkCtx, saverPool); err != nil {
			return liability, errorsmod.Wrapf(err, "set saver pool %s", synthAsset)
		}
	}
	if err := printSynthAllocation(w, available, consumed, bps); err != nil {
		return liability, err
	}

	return common.NewCoin(liability.Asset, common.SafeSub(liability.Amount, consumed)), nil
}

func saverLiquidityProviders(sdkCtx cosmos.Context, k keeper.Keeper, synthAsset common.Asset) ([]types.LiquidityProvider, cosmos.Uint, error) {
	iterator := k.GetLiquidityProviderIterator(sdkCtx, synthAsset)
	defer iterator.Close()

	saverLPs := []types.LiquidityProvider{}
	totalUnits := cosmos.ZeroUint()
	for ; iterator.Valid(); iterator.Next() {
		var lp types.LiquidityProvider
		if err := k.Cdc().Unmarshal(iterator.Value(), &lp); err != nil {
			return nil, cosmos.ZeroUint(), errorsmod.Wrapf(err, "unmarshal saver LP for %s", synthAsset)
		}
		if lp.Units.IsZero() {
			continue
		}
		saverLPs = append(saverLPs, lp)
		totalUnits = totalUnits.Add(lp.Units)
	}

	return saverLPs, totalUnits, nil
}

func consumeSaverLiquidityProviders(
	sdkCtx cosmos.Context,
	k keeper.Keeper,
	saverLPs []types.LiquidityProvider,
	poolUnits cosmos.Uint,
	unitsConsumed cosmos.Uint,
) {
	remainingUnitsConsumed := unitsConsumed
	remainingPoolUnits := poolUnits
	for _, lp := range saverLPs {
		if remainingUnitsConsumed.IsZero() {
			return
		}

		lpUnits := lp.Units
		lpUnitsConsumed := common.GetSafeShare(remainingUnitsConsumed, remainingPoolUnits, lpUnits)
		if lpUnitsConsumed.GT(remainingUnitsConsumed) {
			lpUnitsConsumed = remainingUnitsConsumed
		}
		if lpUnitsConsumed.GT(lpUnits) {
			lpUnitsConsumed = lpUnits
		}
		remainingPoolUnits = common.SafeSub(remainingPoolUnits, lpUnits)
		if lpUnitsConsumed.IsZero() {
			continue
		}

		lp.LastWithdrawHeight = sdkCtx.BlockHeight()
		lp.RuneDepositValue = common.SafeSub(lp.RuneDepositValue, common.GetSafeShare(lpUnitsConsumed, lpUnits, lp.RuneDepositValue))
		lp.AssetDepositValue = common.SafeSub(lp.AssetDepositValue, common.GetSafeShare(lpUnitsConsumed, lpUnits, lp.AssetDepositValue))
		lp.Units = common.SafeSub(lp.Units, lpUnitsConsumed)
		if !lp.Units.Add(lp.PendingAsset).Add(lp.PendingRune).IsZero() {
			k.SetLiquidityProvider(sdkCtx, lp)
		} else {
			k.RemoveLiquidityProvider(sdkCtx, lp)
		}
		remainingUnitsConsumed = common.SafeSub(remainingUnitsConsumed, lpUnitsConsumed)
	}
}

func consumeAmount(liability, available cosmos.Uint) cosmos.Uint {
	consumed := liability
	if available.LT(consumed) {
		consumed = available
	}
	return consumed
}

func consumeRune(available, consumed, runeAvailable cosmos.Uint) cosmos.Uint {
	if available.IsZero() || consumed.IsZero() || runeAvailable.IsZero() {
		return cosmos.ZeroUint()
	}
	return common.GetSafeShare(consumed, available, runeAvailable)
}

func printSynthAllocation(w *tabwriter.Writer, available, consumed, bps cosmos.Uint) error {
	if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t", formatAmount(available), formatAmount(consumed), bps); err != nil {
		return errorsmod.Wrap(err, "print synth allocation")
	}
	return nil
}

func sortedCoins(coins common.Coins) common.Coins {
	sort.SliceStable(coins, func(i, j int) bool {
		return coins[i].Asset.String() < coins[j].Asset.String()
	})
	return coins.NoneEmpty()
}

func formatCoin(coin common.Coin) string {
	return fmt.Sprintf("%s.%s\t%s", coin.Asset.Chain, coin.Asset.Ticker, formatAmount(coin.Amount))
}

func formatAmount(amount cosmos.Uint) string {
	scale := big.NewInt(common.One)
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(new(big.Int).Set(amount.BigInt()), scale, fraction)

	formattedWhole := whole.String()
	if whole.IsUint64() {
		formattedWhole = amountPrinter.Sprintf("%d", whole.Uint64())
	}

	return fmt.Sprintf("%s.%0*d", formattedWhole, common.THORChainDecimals, fraction)
}

func fixedCoins(specs ...coinSpec) (common.Coins, error) {
	coins := make(common.Coins, 0, len(specs))
	for _, spec := range specs {
		coin, err := fixedCoin(spec)
		if err != nil {
			return nil, err
		}
		coins = append(coins, coin)
	}
	return coins, nil
}

func fixedCoin(spec coinSpec) (common.Coin, error) {
	parsedAsset, err := common.NewAsset(spec.asset)
	if err != nil {
		return common.NoCoin, errorsmod.Wrapf(err, "parse fixed asset %s", spec.asset)
	}
	parsedAmount, err := cosmos.ParseUint(spec.amount)
	if err != nil {
		return common.NoCoin, errorsmod.Wrapf(err, "parse fixed amount for %s", spec.asset)
	}
	return common.NewCoin(parsedAsset, parsedAmount).WithDecimals(common.THORChainDecimals), nil
}
