package tokenlist

import (
	"encoding/json"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common/tokenlist/basetokens"
)

var baseTokenListV138 EVMTokenList

func init() {
	if err := json.Unmarshal(basetokens.BASETokenListRawV138, &baseTokenListV138); err != nil {
		panic(err)
	}
}

func GetBASETokenList(version semver.Version) EVMTokenList {
	switch {
	case version.GTE(semver.MustParse("2.138.0")):
		return baseTokenListV138
	default:
		return EVMTokenList{}
	}
}
