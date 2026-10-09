package tokenlist

import (
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/avaxtokens"
)

var avaxTokenList EVMTokenList

func init() {
	mustUnmarshal(avaxtokens.AVAXTokenListRaw, &avaxTokenList)
}

func GetAVAXTokenList() EVMTokenList {
	return avaxTokenList
}
