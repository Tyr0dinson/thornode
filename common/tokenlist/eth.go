package tokenlist

import (
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/ethtokens"
)

var ethTokenList EVMTokenList

func init() {
	mustUnmarshal(ethtokens.ETHTokenListRaw, &ethTokenList)
}

func GetETHTokenList() EVMTokenList {
	return ethTokenList
}
