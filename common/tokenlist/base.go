package tokenlist

import (
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/basetokens"
)

var baseTokenList EVMTokenList

func init() {
	mustUnmarshal(basetokens.BASETokenListRaw, &baseTokenList)
}

func GetBASETokenList() EVMTokenList {
	return baseTokenList
}
