package types

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
)

func NewYieldAccount(addr common.Address, asset common.Asset) YieldAccount {
	return YieldAccount{
		Asset:        asset,
		AssetAddress: addr,
	}
}

func (ya YieldAccount) Key() string {
	return fmt.Sprintf("%s/%s", ya.AssetAddress.String(), ya.Asset.String())
}
