//go:build mocknet
// +build mocknet

package common

var AssetExports = map[Asset]map[Chain]Token{
	VULT: {
		ETHChain: {
			Symbol:   "VULT",
			Address:  "0xb788144df611029c60b859df47e79b7726c4deba",
			Decimals: 18,
		},
	},
	RUJI: {
		ETHChain: {
			Symbol:   "RUJI",
			Address:  "0xb788144df611029c60b859df47e79b7726c4deba",
			Decimals: 18,
		},
		BSCChain: {
			Symbol:   "BSCChain",
			Address:  "0xb788144df611029c60b859df47e79b7726c4deba",
			Decimals: 18,
		},
	},
	NTRN: {
		GAIAChain: {
			Symbol:   "NTRN",
			Address:  "ibc/5DB84693CB3F67A2D25D133C177B06786F423D6D1AFF2B8E080F0753B2E4D585",
			Decimals: 6,
		},
	},
}
