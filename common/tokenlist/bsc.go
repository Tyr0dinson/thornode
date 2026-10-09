package tokenlist

import (
	"gitlab.com/thorchain/thornode/v3/common/tokenlist/bsctokens"
)

var bscTokenList EVMTokenList

func init() {
	mustUnmarshal(bsctokens.BSCTokenListRaw, &bscTokenList)
}

func GetBSCTokenList() EVMTokenList {
	return bscTokenList
}
