package tokenlist

import (
	"encoding/json"

	"gitlab.com/thorchain/thornode/v3/common/tokenlist/avaxtokens"
)

var avaxTokenList EVMTokenList

func init() {
	if err := json.Unmarshal(avaxtokens.AVAXTokenListRaw, &avaxTokenList); err != nil {
		panic(err)
	}
	if err := json.Unmarshal(avaxtokens.AVAXTokenListRawV3_2_0, &avaxTokenListV3_2_0); err != nil {
		panic(err)
	}
}

func GetAVAXTokenList() EVMTokenList {
	return avaxTokenList
}
