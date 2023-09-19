package keeperv1

import (
	"strings"

	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

var WHITELIST = []string{
	"TorAnchor-TERRA-LUNA",
	"TorAnchor-GAIA-ATOM",
	"TorAnchor-BNB-BNB",
	"TorAnchor-BSC-BNB",
	"TorAnchor-BTC-BTC",
	"TorAnchor-LTC-LTC",
	"TorAnchor-BCH-BCH",
	"TorAnchor-DOGE-DOGE",
	"TorAnchor-ETH-ETH",
	"TorAnchor-AVAX-AVAX",
	"TorAnchor-BNB-RUNE-67C",
	"TorAnchor-BNB-RUNE-B1A",
	"TorAnchor-THOR-RUNE",
	"TorAnchor-ETH-RUNE-0x3155ba85d5f96b2d030a4966af206230e46849cb",
	"TorAnchor-ETH-RUNE-0xd601c6A3a36721320573885A8d8420746dA3d7A0",
	"TorAnchor-THOR-TOR",
	"TorAnchor-THOR-BTC",
	"TorAnchor-BNB-BUSD-BD1",
	"TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
}

func (k KVStore) IsInHardCodedList(ctx cosmos.Context, poolId string) bool {
	if k.GetVersion().GTE(semver.MustParse("1.122.0")) {
		for _, pool := range WHITELIST {
			if strings.EqualFold(pool, poolId) {
				return true
			}
		}
		return false
	}
	return true
}
