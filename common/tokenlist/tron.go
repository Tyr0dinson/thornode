package tokenlist

import (
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/trontokens"
)

var tronTokenList EVMTokenList

func init() {
	mustUnmarshal(trontokens.TRONTokenListRaw, &tronTokenList)
}

func GetTRONTokenList() EVMTokenList {
	return tronTokenList
}
