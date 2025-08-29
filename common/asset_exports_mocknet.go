//go:build mocknet
// +build mocknet

package common

var VULT = Asset{Chain: THORChain, Symbol: "VULT", Ticker: "VULT"}

var AssetExports = map[Asset]map[Chain]Token{
	RUJI: {
		GAIAChain: {
			Symbol:   "RUJI",
			Address:  "uruji",
			Decimals: 6,
		},
		ETHChain: {
			Symbol:   "RUJI",
			Address:  "0xRUJI",
			Decimals: 18,
		},
	},
	VULT: {
		BASEChain: {
			Symbol:   "VULT",
			Address:  "0xVULT",
			Decimals: 18,
		},
		ETHChain: {
			Symbol:   "VULT",
			Address:  "0xVULT",
			Decimals: 18,
		},
	},
}
