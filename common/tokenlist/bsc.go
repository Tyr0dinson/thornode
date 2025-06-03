package tokenlist

import (
	"encoding/json"

	"gitlab.com/thorchain/thornode/v3/common/tokenlist/bsctokens"
)

var bscTokenList EVMTokenList

func init() {
	if err := json.Unmarshal(bsctokens.BSCTokenListRaw, &bscTokenList); err != nil {
		panic(err)
	}
	if err := json.Unmarshal(bsctokens.BSCTokenListRawV3_2_0, &bscTokenListV3_2_0); err != nil {
		panic(err)
	}
}

func GetBSCTokenList() EVMTokenList {
	return bscTokenList
}
