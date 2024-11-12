package gaia

import "strings"

type CosmosAssetMapping struct {
	CosmosDenom     string
	CosmosDecimals  int
	THORChainSymbol string
}

// CosmosAssetMappings maps a Cosmos denom to a THORChain symbol and provides the asset decimals
// CHANGEME: define assets that should be observed by THORChain here. This also acts a whitelist.
var CosmosAssetMappings = []CosmosAssetMapping{
	{
		CosmosDenom:     "uatom",
		CosmosDecimals:  6,
		THORChainSymbol: "ATOM",
	},
	{
		CosmosDenom:     "ibc/4CC44260793F84006656DD868E017578F827A492978161DA31D7572BCB3F4289",
		CosmosDecimals:  6,
		THORChainSymbol: "KUJI",
	},
	{
		CosmosDenom:     "ibc/50A69DC508ACCADE2DAC4B8B09AA6D9C9062FCBFA72BB4C6334367DECD972B06",
		CosmosDecimals:  6,
		THORChainSymbol: "RKUJI",
	},
	{
		CosmosDenom:     "ibc/6BBBB4B63C51648E9B8567F34505A9D5D8BAAC4C31D768971998BE8C18431C26",
		CosmosDecimals:  6,
		THORChainSymbol: "FUZN",
	},
	{
		CosmosDenom:     "ibc/4363FD2EF60A7090E405B79A6C4337C5E9447062972028F5A99FB041B9571942",
		CosmosDecimals:  6,
		THORChainSymbol: "WINK",
	},
	{
		CosmosDenom:     "ibc/0B99C4EFF1BD05E56DEDEE1D88286DB79680C893724E0E7573BC369D79B5DDF3",
		CosmosDecimals:  6,
		THORChainSymbol: "NSTK",
	},
	{
		CosmosDenom:     "ibc/6C95083ADD352D5D47FB4BA427015796E5FEF17A829463AD05ECD392EB38D889",
		CosmosDecimals:  6,
		THORChainSymbol: "LVN",
	},
}

func GetAssetByCosmosDenom(denom string) (CosmosAssetMapping, bool) {
	for _, asset := range CosmosAssetMappings {
		if strings.EqualFold(asset.CosmosDenom, denom) {
			return asset, true
		}
	}
	return CosmosAssetMapping{}, false
}

func GetAssetByThorchainSymbol(symbol string) (CosmosAssetMapping, bool) {
	for _, asset := range CosmosAssetMappings {
		if strings.EqualFold(asset.THORChainSymbol, symbol) {
			return asset, true
		}
	}
	return CosmosAssetMapping{}, false
}
