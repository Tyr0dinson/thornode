package types

import (
	"errors"
	"fmt"
	"math/big"

	proto "github.com/cosmos/gogoproto/proto"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var _ proto.Message = &LiquidityProvider{}

// LiquidityProviders a list of liquidity providers
type LiquidityProviders []LiquidityProvider

// Valid check whether lp represent valid information
func (m *LiquidityProvider) Valid() error {
	if m.LastAddHeight == 0 {
		return errors.New("last add liquidity height cannot be empty")
	}
	if m.AssetAddress.IsEmpty() && m.RuneAddress.IsEmpty() {
		return errors.New("asset address and rune address cannot be empty")
	}
	return nil
}

func (lp LiquidityProvider) GetAddress() common.Address {
	if !lp.RuneAddress.IsEmpty() {
		return lp.RuneAddress
	}
	return lp.AssetAddress
}

// Key return a string which can be used to identify lp
func (lp LiquidityProvider) Key() string {
	return fmt.Sprintf("%s/%s", lp.Asset.String(), lp.GetAddress().String())
}

func (lp LiquidityProvider) HasLiquidityBuckets() bool {
	return !normalizeUint(lp.LegacyUnits).IsZero() || !normalizeUint(lp.PlainUnits).IsZero()
}

func (lp LiquidityProvider) HasMixedLiquidityBuckets() bool {
	return !lp.GetLegacyUnits().IsZero() && !lp.GetPlainUnits().IsZero()
}

func (lp *LiquidityProvider) EnsureLiquidityBuckets() {
	if lp.Asset.IsSyntheticAsset() {
		return
	}
	lp.LegacyUnits = normalizeUint(lp.LegacyUnits)
	lp.PlainUnits = normalizeUint(lp.PlainUnits)
	if !lp.HasLiquidityBuckets() {
		lp.LegacyUnits = lp.Units
	} else if lp.GetPlainUnits().IsZero() {
		lp.LegacyUnits = lp.Units
	}
	lp.SyncUnits()
}

func (lp *LiquidityProvider) SyncUnits() {
	if lp.Asset.IsSyntheticAsset() {
		return
	}
	lp.Units = lp.LegacyUnits.Add(lp.PlainUnits)
}

func (lp LiquidityProvider) GetLegacyUnits() cosmos.Uint {
	if lp.Asset.IsSyntheticAsset() || !lp.HasLiquidityBuckets() || lp.GetPlainUnits().IsZero() {
		return lp.Units
	}
	return normalizeUint(lp.LegacyUnits)
}

func (lp LiquidityProvider) GetPlainUnits() cosmos.Uint {
	if lp.Asset.IsSyntheticAsset() || !lp.HasLiquidityBuckets() {
		return cosmos.ZeroUint()
	}
	return normalizeUint(lp.PlainUnits)
}

func (lp LiquidityProvider) GetTotalUnits() cosmos.Uint {
	if lp.Asset.IsSyntheticAsset() || !lp.HasLiquidityBuckets() || lp.GetPlainUnits().IsZero() {
		return lp.Units
	}
	return lp.GetLegacyUnits().Add(lp.GetPlainUnits())
}

func (lp LiquidityProvider) GetRuneRedeemValue(pool Pool, synthSupply cosmos.Uint) (error, cosmos.Uint) {
	if !lp.Asset.Equals(pool.Asset) {
		return fmt.Errorf("LP and Pool assets do not match (%s, %s)", lp.Asset.String(), pool.Asset.String()), cosmos.ZeroUint()
	}

	pool.CalcUnits(synthSupply)
	if pool.UsesLiquidityBuckets() {
		runeValue := common.GetSafeShare(lp.GetLegacyUnits(), pool.GetLegacyPoolUnits(), pool.GetLegacyBalanceRune())
		runeValue = runeValue.Add(common.GetSafeShare(lp.GetPlainUnits(), pool.GetPlainPoolUnits(), pool.GetPlainBalanceRune()))
		return nil, runeValue
	}

	bigInt := &big.Int{}
	lpUnits := lp.Units.BigInt()
	poolRuneDepth := pool.BalanceRune.BigInt()
	num := bigInt.Mul(lpUnits, poolRuneDepth)
	denom := pool.GetPoolUnits().BigInt()
	if len(denom.Bits()) == 0 {
		return nil, cosmos.ZeroUint()
	}
	result := bigInt.Quo(num, denom)
	return nil, cosmos.NewUintFromBigInt(result)
}

func (lp LiquidityProvider) GetAssetRedeemValue(pool Pool, synthSupply cosmos.Uint) (error, cosmos.Uint) {
	if !lp.Asset.Equals(pool.Asset) {
		return fmt.Errorf("LP and Pool assets do not match (%s, %s)", lp.Asset.String(), pool.Asset.String()), cosmos.ZeroUint()
	}

	pool.CalcUnits(synthSupply)
	if pool.UsesLiquidityBuckets() {
		assetValue := common.GetSafeShare(lp.GetLegacyUnits(), pool.GetLegacyPoolUnits(), pool.GetLegacyBalanceAsset())
		assetValue = assetValue.Add(common.GetSafeShare(lp.GetPlainUnits(), pool.GetPlainPoolUnits(), pool.GetPlainBalanceAsset()))
		return nil, assetValue
	}

	bigInt := &big.Int{}
	lpUnits := lp.Units.BigInt()
	poolAssetDepth := pool.BalanceAsset.BigInt()
	num := bigInt.Mul(lpUnits, poolAssetDepth)
	denom := pool.GetPoolUnits().BigInt()
	if len(denom.Bits()) == 0 {
		return nil, cosmos.ZeroUint()
	}
	result := bigInt.Quo(num, denom)
	return nil, cosmos.NewUintFromBigInt(result)
}

func (lp LiquidityProvider) GetLuviDepositValue(pool Pool) (error, cosmos.Uint) {
	if !lp.Asset.Equals(pool.Asset) {
		return fmt.Errorf("LP and Pool assets do not match (%s, %s)", lp.Asset.String(), pool.Asset.String()), cosmos.ZeroUint()
	}

	bigInt := &big.Int{}
	runeDeposit := lp.RuneDepositValue.MulUint64(1e8).BigInt()
	assetDeposit := lp.AssetDepositValue.MulUint64(1e8).BigInt()
	num := bigInt.Mul(runeDeposit, assetDeposit)
	num = bigInt.Sqrt(num)
	denom := lp.Units.BigInt()
	if len(denom.Bits()) == 0 {
		return nil, cosmos.ZeroUint()
	}
	result := bigInt.Quo(num, denom)
	return nil, cosmos.NewUintFromBigInt(result)
}

func (lp LiquidityProvider) GetLuviRedeemValue(runeRedeemValue, assetRedeemValue cosmos.Uint) (error, cosmos.Uint) {
	bigInt := &big.Int{}
	runeValue := runeRedeemValue.MulUint64(1e8).BigInt()
	assetValue := assetRedeemValue.MulUint64(1e8).BigInt()
	num := bigInt.Mul(runeValue, assetValue)
	num = bigInt.Sqrt(num)
	denom := lp.Units.BigInt()
	if len(denom.Bits()) == 0 {
		return nil, cosmos.ZeroUint()
	}
	result := bigInt.Quo(num, denom)
	return nil, cosmos.NewUintFromBigInt(result)
}

func (lp LiquidityProvider) GetSaversAssetRedeemValue(pool Pool) cosmos.Uint {
	bigInt := &big.Int{}
	lpUnits := lp.Units.BigInt()
	saversDepth := pool.BalanceAsset.BigInt()
	num := bigInt.Mul(lpUnits, saversDepth)
	denom := pool.LPUnits.BigInt()
	if len(denom.Bits()) == 0 {
		return cosmos.ZeroUint()
	}
	result := bigInt.Quo(num, denom)
	return cosmos.NewUintFromBigInt(result)
}
