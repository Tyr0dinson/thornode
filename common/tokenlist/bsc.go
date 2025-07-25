package tokenlist

import (
	"encoding/json"

	"gitlab.com/thorchain/thornode/v3/common/tokenlist/bsctokens"
)

var bscTokenList TokenList

func init() {
	if err := json.Unmarshal(bsctokens.BSCTokenListRaw, &bscTokenList); err != nil {
		panic(err)
	}
}

func GetBSCTokenList() TokenList {
	return bscTokenList
}
