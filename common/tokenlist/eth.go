package tokenlist

import (
	"encoding/json"

	"gitlab.com/thorchain/thornode/v3/common/tokenlist/ethtokens"
)

var ethTokenList EVMTokenList

func init() {
	if err := json.Unmarshal(ethtokens.ETHTokenListRaw, &ethTokenList); err != nil {
		panic(err)
	}
	if err := json.Unmarshal(ethtokens.ETHTokenListRawV3_2_0, &ethTokenListV3_2_0); err != nil {
		panic(err)
	}
}

func GetETHTokenList() EVMTokenList {
	return ethTokenList
}
