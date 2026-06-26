package constants

import "strings"

var assetSlipMinBpsRefs = []ConstantName{
	L1SlipMinBps,
	SynthSlipMinBps,
	TradeAccountsSlipMinBps,
	DerivedSlipMinBps,
	SecuredAssetSlipMinBps,
	StableSlipMinBps,
}

// AssetSlipMinBpsMimirKey returns the per-asset override key for a slip-min-bps
// class and an asset MimirString, e.g. L1SlipMinBps-BTC-BTC.
func AssetSlipMinBpsMimirKey(ref ConstantName, assetRef string) string {
	return ref.String() + "-" + assetRef
}

// SlipMinBpsMaxMimirKey returns the per-asset cap key for asset min-slip-bps
// mimirs, e.g. SlipMinBpsMax-BTC-BTC.
func SlipMinBpsMaxMimirKey(assetRef string) string {
	return SlipMinBpsMax.String() + "-" + assetRef
}

// AssetSlipMinBpsMimirRef returns the slip-min-bps class and asset reference
// suffix from a supported per-asset slip-min-bps mimir key.
func AssetSlipMinBpsMimirRef(key string) (ConstantName, string, bool) {
	upperKey := strings.ToUpper(key)
	for _, ref := range assetSlipMinBpsRefs {
		prefix := ref.String() + "-"
		upperPrefix := strings.ToUpper(prefix)
		if strings.HasPrefix(upperKey, upperPrefix) && len(key) > len(prefix) {
			return ref, key[len(prefix):], true
		}
	}
	return ConstantName(0), "", false
}

// AssetSlipMinBpsAssetRef returns the asset reference suffix from a supported
// per-asset slip-min-bps mimir key.
func AssetSlipMinBpsAssetRef(key string) (string, bool) {
	_, assetRef, ok := AssetSlipMinBpsMimirRef(key)
	return assetRef, ok
}

// IsAssetSlipMinBpsMimirKey returns true for supported per-asset slip-min-bps
// mimir keys such as L1SlipMinBps-BTC-BTC.
func IsAssetSlipMinBpsMimirKey(key string) bool {
	_, ok := AssetSlipMinBpsAssetRef(key)
	return ok
}

// IsSlipMinBpsMimirKey returns true for legacy class-level slip-min-bps mimirs
// and supported per-asset overrides. SlipMinBpsMax intentionally does not match.
func IsSlipMinBpsMimirKey(key string) bool {
	upperKey := strings.ToUpper(key)
	return strings.HasSuffix(upperKey, "SLIPMINBPS") || IsAssetSlipMinBpsMimirKey(key)
}
