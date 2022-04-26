package thorchain

import (
	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common"
)

func getDerivedAnchors(version semver.Version, a common.Asset) []common.Asset {
	if a.Equals(common.THORUSD) {
		if version.GTE(semver.MustParse("0.1.0")) {
			busd, _ := common.NewAsset("BNB.BUSD-BD1")
			usdc, _ := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
			usdt, _ := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
			dai, _ := common.NewAsset("ETH.DAI-0X6B175474E89094C44DA98B954EEDEAC495271D0F")
			ust, _ := common.NewAsset("TERRA.UST")
			return []common.Asset{busd, usdc, usdt, dai, ust}
		}
	}
	return []common.Asset{a}
}
