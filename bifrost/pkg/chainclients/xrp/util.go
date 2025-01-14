package xrp

import (
	"fmt"
	"math/big"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
)

func fromXrpToThorchain(c common.Coin) (common.Coin, error) {
	cosmosAsset, exists := GetAssetByXrpDenom(c.Asset.Symbol.String())
	if !exists {
		return common.NoCoin, fmt.Errorf("asset does not exist / not whitelisted by client")
	}

	decimals := cosmosAsset.XrpDecimals
	amount := c.Amount.BigInt()
	var exp big.Int
	// Decimals are more than native THORChain, so divide...
	if decimals > common.THORChainDecimals {
		decimalDiff := int64(decimals - common.THORChainDecimals)
		amount.Quo(amount, exp.Exp(big.NewInt(10), big.NewInt(decimalDiff), nil))
	} else if decimals < common.THORChainDecimals {
		// Decimals are less than native THORChain, so multiply...
		decimalDiff := int64(common.THORChainDecimals - decimals)
		amount.Mul(amount, exp.Exp(big.NewInt(10), big.NewInt(decimalDiff), nil))
	}
	return common.Coin{
		Asset:    c.Asset,
		Amount:   sdkmath.NewUintFromBigInt(amount),
		Decimals: decimals,
	}, nil
}

func fromThorchainToXrp(coin common.Coin) (common.Coin, error) {
	asset, exists := GetAssetByThorchainSymbol(coin.Asset.Symbol.String())
	if !exists {
		return common.Coin{}, fmt.Errorf("asset (%s) does not exist / not whitelisted by client", coin.Asset)
	}

	decimals := asset.XrpDecimals
	amount := coin.Amount.BigInt()
	var exp big.Int
	if decimals > common.THORChainDecimals {
		// Decimals are more than native THORChain, so multiply...
		decimalDiff := int64(decimals - common.THORChainDecimals)
		amount.Mul(amount, exp.Exp(big.NewInt(10), big.NewInt(decimalDiff), nil))
	} else if decimals < common.THORChainDecimals {
		// Decimals are less than native THORChain, so divide...
		decimalDiff := int64(common.THORChainDecimals - decimals)
		amount.Quo(amount, exp.Exp(big.NewInt(10), big.NewInt(decimalDiff), nil))
	}
	return common.Coin{
		Asset: coin.Asset,
		Amount: sdkmath.NewUintFromBigInt(amount),
		Decimals: decimals,
	}, nil
}
