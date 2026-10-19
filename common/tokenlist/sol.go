package tokenlist

import (
	"encoding/json"

	"gitlab.com/thorchain/thornode/v3/common/tokenlist/soltokens"
)

// solTokenList holds the SPL Token (classic program) mints Bifrost may whitelist on
// Solana. Addresses are case-sensitive base58 mints; asset symbols are uppercased,
// so Bifrost maps a symbol back to its mint through this list.
var solTokenList EVMTokenList

func init() {
	err := json.Unmarshal(soltokens.SOLTokenListRaw, &solTokenList)
	if err != nil {
		panic(err)
	}
}

func GetSOLTokenList() EVMTokenList {
	return solTokenList
}
