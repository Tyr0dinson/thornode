package tokenlist

import (
	"gitlab.com/thorchain/thornode/v3/common"
)

type TokenList struct {
	Name   string         `json:"name"`
	Tokens []common.Token `json:"tokens"`
}

// GetTokenList returns all available tokens for external asset matching for a
// particular.
//
// NOTE: These tokens are NOT necessarily the same tokens that are whitelisted for each
// chain - whitelisting happens in each chain's bifrost chain client.
func GetTokenList(chain common.Chain) TokenList {
	switch chain {
	case common.ETHChain:
		return GetETHTokenList()
	case common.AVAXChain:
		return GetAVAXTokenList()
	case common.BSCChain:
		return GetBSCTokenList()
	case common.BASEChain:
		return GetBASETokenList()
	case common.TRONChain:
		return GetTRONTokenList()
	default:
		return TokenList{}
	}
}
