package tokenlist

import (
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/poltokens"
)

var polTokenList EVMTokenList

func init() {
	mustUnmarshal(poltokens.POLTokenListRaw, &polTokenList)
}

func GetPOLTokenList() EVMTokenList {
	return polTokenList
}
