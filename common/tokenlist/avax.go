package tokenlist

import (
	"encoding/json"

	"gitlab.com/thorchain/thornode/v3/common/tokenlist/avaxtokens"
)

var avaxTokenList TokenList

func init() {
	if err := json.Unmarshal(avaxtokens.AVAXTokenListRaw, &avaxTokenList); err != nil {
		panic(err)
	}
}

func GetAVAXTokenList() TokenList {
	return avaxTokenList
}
