//go:build stagenet
// +build stagenet

package cmd

const (
	Bech32PrefixAccAddr         = "sthor"
	Bech32PrefixAccPub          = "sthorpub"
	Bech32PrefixValAddr         = "sthorv"
	Bech32PrefixValPub          = "sthorvpub"
	Bech32PrefixConsAddr        = "sthorc"
	Bech32PrefixConsPub         = "sthorcpub"
	DenomRegex                  = `[a-zA-Z][a-zA-Z0-9:\\/\\\-\\_\\.]{2,127}`
	BaseChainCoinType    uint32 = 931
	BaseChainCoinPurpose uint32 = 44
	BaseChainHDPath      string = `m/44'/931'/0'/0/0`
)
