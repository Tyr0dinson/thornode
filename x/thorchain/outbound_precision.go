package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// outboundPrecision returns the decimal precision used to round an outbound.
// Native THORChain assets use the full internal precision. External assets use
// the pool precision when set, with a chain-defined fallback for gas assets.
// For other external assets, a zero pool precision retains its "no rounding"
// meaning.
func outboundPrecision(pool Pool, asset common.Asset) int64 {
	// Native assets are represented on THORChain at its full internal
	// precision. A pool's external-chain precision must not truncate RUNE,
	// synth, trade, secured, or derived-asset amounts.
	if asset.IsNative() {
		return cosmos.DefaultCoinDecimals
	}

	if pool.Decimals == 0 && asset.IsGasAsset() {
		return asset.GetChain().GetGasAssetDecimal()
	}

	return pool.Decimals
}
