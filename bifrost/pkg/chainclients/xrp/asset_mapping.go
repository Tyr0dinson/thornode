package xrp

import "strings"

type XrpAssetMapping struct {
	XrpDenom        string
	XrpDecimals     int64
	XrpIssuer       string
	THORChainSymbol string
}

// XrpAssetMappings maps an xrp denom to a THORChain symbol and provides the asset decimals
// CHANGEME: define assets that should be observed by THORChain here. This also acts a whitelist.
var XrpAssetMappings = []XrpAssetMapping{
	{
		XrpDenom:        "XRP",
		XrpDecimals:     6,
		THORChainSymbol: "XRP",
	},
}

func GetAssetByXrpDenom(denom string) (XrpAssetMapping, bool) {
	for _, asset := range XrpAssetMappings {
		if strings.EqualFold(asset.XrpDenom, denom) {
			return asset, true
		}
	}
	return XrpAssetMapping{}, false
}

func GetAssetByThorchainSymbol(symbol string) (XrpAssetMapping, bool) {
	for _, asset := range XrpAssetMappings {
		if strings.EqualFold(asset.THORChainSymbol, symbol) {
			return asset, true
		}
	}
	return XrpAssetMapping{}, false
}
