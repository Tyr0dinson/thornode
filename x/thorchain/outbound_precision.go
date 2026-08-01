package thorchain

import "gitlab.com/thorchain/thornode/v3/common"

// outboundPrecision returns the decimal precision used to round an outbound.
// It uses the pool precision when set and otherwise falls back to the
// chain-defined precision for gas assets. For other assets, a zero pool
// precision retains its "no rounding" meaning.
func outboundPrecision(pool Pool, asset common.Asset) int64 {
	if pool.Decimals == 0 && asset.IsGasAsset() {
		return asset.GetChain().GetGasAssetDecimal()
	}

	return pool.Decimals
}
