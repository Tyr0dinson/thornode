package tokenlist

import (
	"encoding/json"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common/tokenlist/basetokens"
)

var baseTokenListV137 EVMTokenList

func init() {
	if err := json.Unmarshal(basetokens.BASETokenListRawV137, &baseTokenListV137); err != nil {
		panic(err)
	}
}

func GetBASETokenList(version semver.Version) EVMTokenList {
	switch {
	case version.GTE(semver.MustParse("2.137.0")):
		return baseTokenListV137
	default:
		return EVMTokenList{}
	}
}
