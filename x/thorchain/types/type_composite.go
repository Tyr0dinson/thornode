package types

import "gitlab.com/thorchain/thornode/common"

func (c Composite) IsEmpty() bool {
	return len(c.Name) == 0
}

func (c Composite) Assets() []common.Asset {
	assets := make([]common.Asset, len(c.Holdings))

	for i, coin := range c.Holdings {
		assets[i] = coin.Asset
	}

	return assets
}
