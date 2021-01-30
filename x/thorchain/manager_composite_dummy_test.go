package thorchain

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

type DummyCompManager struct{}

func NewDummyCompManger() *DummyCompManager {
	return &DummyCompManager{}
}

func (cmgr DummyCompManager) FindAvailableName(ctx cosmos.Context, assets []common.Asset) string {
	return ""
}
