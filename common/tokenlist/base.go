package tokenlist

import (
	"encoding/json"

	"gitlab.com/thorchain/thornode/v3/common/tokenlist/basetokens"
)

var baseTokenList TokenList

func init() {
	if err := json.Unmarshal(basetokens.BASETokenListRaw, &baseTokenList); err != nil {
		panic(err)
	}
}

func GetBASETokenList() TokenList {
	return baseTokenList
}
