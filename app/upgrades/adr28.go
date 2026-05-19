package upgrades

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"sort"
	"text/tabwriter"

	"cosmossdk.io/math"
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
		return fmt.Errorf("get treasury module address: %w", err)
	}
	reserve, err := k.GetModuleAddress(types.ReserveName)
	if err != nil {
		return fmt.Errorf("get reserve module address: %w", err)
	}

	assets, err := assets(sdkCtx, k)
	if err != nil {
		return err
	}
	liabilities, err := liabilities(sdkCtx, k)
	if err != nil {
		return err
	}

	shortfall := sortedCoins(liabilities.SafeSub(assets...))

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if err := printCoins(w, "gross liabilities", shortfall); err != nil {
		return err
	}

	reserveLiquidity, err := liquidity(sdkCtx, k, reserve)
	if err != nil {
		return err
	}
	if err := printCoins(w, "reserve liquidity", reserveLiquidity); err != nil {
		return err
	}

	treasuryLiquidity, err := liquidity(sdkCtx, k, treasury)
	if err != nil {
		return err
	}
	if err := printCoins(w, "treasury liquidity", treasuryLiquidity); err != nil {
		return err
	}

	afterReserve, err := allocateLiquidity(w, "reserve liquidity", shortfall, reserveLiquidity)
	if err != nil {
		return err
	}

	afterTreasury, err := allocateLiquidity(w, "treasury liquidity", afterReserve, treasuryLiquidity)
	if err != nil {
		return err
	}

	remaining, err := allocateSynths(w, sdkCtx, k, afterTreasury)
	if err != nil {
		return err
	}

	if err := printCoins(w, "liability remaining", remaining); err != nil {
		return err
	}

	reserveRemaining := reserveLiquidity.SafeSub(shortfall...)
	if err := printCoins(w, "reserve remaining", reserveRemaining); err != nil {
		return err
	}

	treasuryRemaining := treasuryLiquidity.SafeSub(afterReserve...)
	if err := printCoins(w, "treasury remaining", treasuryRemaining); err != nil {
		return err
	}

	if err := w.Flush(); err != nil {
		return fmt.Errorf("flush ADR28 output: %w", err)
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
		return nil, fmt.Errorf("get switch manager: %w", err)
	}

	iterator := k.GetVaultIterator(sdkCtx)
	defer iterator.Close()

	total := common.Coins{}
	for ; iterator.Valid(); iterator.Next() {
		var vault types.Vault
		if err := k.Cdc().Unmarshal(iterator.Value(), &vault); err != nil {
			return nil, fmt.Errorf("unmarshal vault: %w", err)
		}

		address, err := vault.GetAddress(common.THORChain)
		if err != nil {
			return nil, fmt.Errorf("get THOR address for vault %s: %w", vault.PubKey, err)
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
			return nil, fmt.Errorf("unmarshal pool: %w", err)
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
			return nil, fmt.Errorf("unmarshal trade unit: %w", err)
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
			return nil, fmt.Errorf("unmarshal secured asset: %w", err)
		}
		if asset.Asset.IsEmpty() || asset.Depth.IsZero() {
			continue
		}

		total = total.Add(common.NewCoin(asset.Asset.GetLayer1Asset(), asset.Depth).WithDecimals(common.THORChainDecimals))
	}

	return total, nil
}

func liquidity(sdkCtx cosmos.Context, k keeper.Keeper, address common.Address) (common.Coins, error) {

	iterator := k.GetPoolIterator(sdkCtx)
	defer iterator.Close()

	total := common.Coins{}
	for ; iterator.Valid(); iterator.Next() {
		var pool types.Pool
		if err := k.Cdc().Unmarshal(iterator.Value(), &pool); err != nil {
			return nil, fmt.Errorf("unmarshal pool: %w", err)
		}
		if pool.IsEmpty() || pool.BalanceAsset.IsZero() {
			continue
		}
		if !pool.Asset.IsExternalL1Asset() {
			continue
		}

		synthSupply := k.GetTotalSupply(sdkCtx, pool.Asset.GetSyntheticAsset())
		lp, err := k.GetLiquidityProvider(sdkCtx, pool.Asset, address)
		if err != nil {
			return nil, fmt.Errorf("get liquidity provider for %s at %s: %w", pool.Asset, address, err)
		}
		if lp.Units.IsZero() {
			continue
		}

		err, assetRedeemValue := lp.GetAssetRedeemValue(pool, synthSupply)
		if err != nil {
			return nil, fmt.Errorf("get liquidity asset redeem value for %s at %s: %w", pool.Asset, address, err)
		}
		if assetRedeemValue.IsZero() {
			continue
		}

		total = total.Add(common.NewCoin(pool.Asset, assetRedeemValue).WithDecimals(pool.Decimals))
	}

	return total, nil
}

func printCoins(w *tabwriter.Writer, label string, coins common.Coins) error {
	coins = sortedCoins(coins)
	if _, err := fmt.Fprintf(w, "%s (%d assets)\n", label, len(coins)); err != nil {
		return fmt.Errorf("print %s header: %w", label, err)
	}

	for _, coin := range coins {
		if _, err := fmt.Fprintf(w, "%s.%s\t%s\n", coin.Asset.Chain, coin.Asset.Ticker, formatAmount(coin.Amount)); err != nil {
			return fmt.Errorf("print %s row for %s: %w", label, coin.Asset, err)
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return fmt.Errorf("print %s footer: %w", label, err)
	}
	return nil
}

func allocateLiquidity(w *tabwriter.Writer, label string, coins, liquidity common.Coins) (common.Coins, error) {
	coins = sortedCoins(coins)
	remaining := common.NewCoins()
	if _, err := fmt.Fprintf(w, "%s consumption (%d assets)\n", label, len(coins)); err != nil {
		return nil, fmt.Errorf("print %s consumption header: %w", label, err)
	}
	if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n",
		"asset",
		"consumed",
		"remaining",
	); err != nil {
		return nil, fmt.Errorf("print %s consumption columns: %w", label, err)
	}
	for _, coin := range coins {
		remainingCoin, err := allocateLiquidityCoin(label, w, coin, liquidity.GetCoin(coin.Asset))
		if err != nil {
			return nil, err
		}
		remaining = remaining.Add(remainingCoin)
	}

	if _, err := fmt.Fprintln(w); err != nil {
		return nil, fmt.Errorf("print %s consumption footer: %w", label, err)
	}
	return remaining, nil
}

func allocateLiquidityCoin(label string, w *tabwriter.Writer, coin, liquidity common.Coin) (common.Coin, error) {
	if coin.IsEmpty() {
		return coin, nil
	}
	if liquidity.IsEmpty() || liquidity.Amount.IsZero() {
		return coin, nil
	}

	consumed := coin.Amount
	if liquidity.Amount.LT(consumed) {
		consumed = liquidity.Amount
	}
	remainingLiquidity := common.SafeSub(liquidity.Amount, consumed)

	if _, err := fmt.Fprintf(w, "%s.%s\t%s\t%s\n",
		coin.Asset.Chain,
		coin.Asset.Ticker,
		formatAmount(consumed),
		formatAmount(remainingLiquidity),
	); err != nil {
		return coin, fmt.Errorf("print %s allocation row for %s: %w", label, coin.Asset, err)
	}

	return common.NewCoin(coin.Asset, common.SafeSub(coin.Amount, consumed)), nil
}

func allocateSynths(w *tabwriter.Writer, sdkCtx cosmos.Context, k keeper.Keeper, coins common.Coins) (common.Coins, error) {
	coins = sortedCoins(coins)
	remaining := common.NewCoins()
	if _, err := fmt.Fprintf(w, "synth consumption (%d assets)\n", len(coins)); err != nil {
		return nil, fmt.Errorf("print synth consumption header: %w", err)
	}
	if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
		"asset",
		"consumed",
		"remaining",
		"bps",
	); err != nil {
		return nil, fmt.Errorf("print synth consumption columns: %w", err)
	}
	for _, coin := range coins {
		remainingCoin, err := allocateSynth(sdkCtx, k, w, coin)
		if err != nil {
			return nil, err
		}
		remaining = remaining.Add(remainingCoin)
	}

	if _, err := fmt.Fprintln(w); err != nil {
		return nil, fmt.Errorf("print synth consumption footer: %w", err)
	}
	return remaining, nil
}

func allocateSynth(sdkCtx cosmos.Context, k keeper.Keeper, w *tabwriter.Writer, coin common.Coin) (common.Coin, error) {
	if coin.IsEmpty() {
		return coin, nil
	}

	pool, err := k.GetPool(sdkCtx, coin.Asset.GetLayer1Asset())
	if err != nil {
		return coin, fmt.Errorf("get pool for %s: %w", coin.Asset, err)
	}
	if pool.IsEmpty() || pool.BalanceAsset.IsZero() {
		return coin, nil
	}

	currentSupply := k.GetTotalSupply(sdkCtx, pool.Asset.GetSyntheticAsset())

	if currentSupply.IsZero() {
		return coin, nil
	}

	newSupply := common.SafeSub(currentSupply, coin.Amount)
	consumed := currentSupply.Sub(newSupply)

	if _, err := fmt.Fprintf(w, "%s.%s\t%s\t%s\t%s\n",
		coin.Asset.Chain,
		coin.Asset.Ticker,
		formatAmount(consumed),
		formatAmount(newSupply),
		consumed.Mul(math.NewUint(10000)).Quo(currentSupply),
	); err != nil {
		return coin, fmt.Errorf("print synth allocation row for %s: %w", coin.Asset, err)
	}

	return common.NewCoin(coin.Asset, common.SafeSub(coin.Amount, consumed)), nil
}

func sortedCoins(coins common.Coins) common.Coins {
	sort.SliceStable(coins, func(i, j int) bool {
		return coins[i].Asset.String() < coins[j].Asset.String()
	})
	return coins.NoneEmpty()
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
		return common.NoCoin, fmt.Errorf("parse fixed asset %s: %w", spec.asset, err)
	}
	parsedAmount, err := cosmos.ParseUint(spec.amount)
	if err != nil {
		return common.NoCoin, fmt.Errorf("parse fixed amount for %s: %w", spec.asset, err)
	}
	return common.NewCoin(parsedAsset, parsedAmount).WithDecimals(common.THORChainDecimals), nil
}
