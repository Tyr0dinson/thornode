package types

import "gitlab.com/thorchain/thornode/common"

func (c Composite) IsEmpty() bool {
	return len(c.Name) == 0 || len(c.Id) == 0
}

func (c Composite) Asset() common.Asset {
	asset, _ := common.NewCompositeAsset(c.Id)
	return asset
}

func (c Composite) Assets() []common.Asset {
	assets := make([]common.Asset, len(c.Holdings))

	for i, coin := range c.Holdings {
		assets[i] = coin.Asset
	}

	return assets
}
