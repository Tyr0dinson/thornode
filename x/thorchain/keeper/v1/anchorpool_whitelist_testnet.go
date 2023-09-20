//go:build testnet
// +build testnet

package keeperv1

import (
	"strings"

	"github.com/blang/semver"
)

var ANCHORPOOL_WHITELIST = []TorAnchors{
	TorAnchors{
		whitelist: []string{
			"TORANCHOR-AVAX-USDC-0XB97EF9EF8734C71904D8002F8B6BC66DD9C48A6E",
			"TORANCHOR-BNB-BUSD-BD1",
			"TORANCHOR-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48",
			"TORANCHOR-ETH-USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7",
		},
	},
}

type TorAnchors struct {
	whitelist []string
}

func (ta TorAnchors) HasAsset(pool string) bool {
	for _, anchorPool := range ta.whitelist {
		if strings.EqualFold(anchorPool, pool) {
			return true
		}
	}
	return false
}

func GetTorAnchors(version semver.Version) TorAnchors {
	switch {
	case version.GTE(semver.MustParse("1.122.0")):
		return ANCHORPOOL_WHITELIST[0]
	default:
		return ANCHORPOOL_WHITELIST[0]
	}
}
