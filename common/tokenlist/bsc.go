package tokenlist

import (
	"encoding/json"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common/tokenlist/bsctokens"
)

var bscTokenListV106 EVMTokenList

func init() {
	if err := json.Unmarshal(bsctokens.BSCTokenListRawV106, &bscTokenListV106); err != nil {
		panic(err)
	}
}

func GetBSCTokenList(version semver.Version) EVMTokenList {
	switch {
	case version.GTE(semver.MustParse("1.106.0")):
		return bscTokenListV106
	default:
		return bscTokenListV106
	}
}
