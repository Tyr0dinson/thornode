package tokenlist

import (
	"encoding/json"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common/tokenlist/ethtokens"
)

var ethTokenListV128 EVMTokenList

func init() {
	if err := json.Unmarshal(ethtokens.ETHTokenListRawV128, &ethTokenListV128); err != nil {
		panic(err)
	}
}

func GetETHTokenList(version semver.Version) EVMTokenList {
	switch {
	case version.GTE(semver.MustParse("1.128.0")):
		return ethTokenListV128
	default:
		return EVMTokenList{}
	}
}
