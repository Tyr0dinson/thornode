package thorchain

import (
	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
)

type DummyLPManager struct{}

func NewDummyLPManager() *DummyLPManager {
	return &DummyLPManager{}
}

func (*DummyLPManager) EndBlock(ctx cosmos.Context, mgr Manager, version semver.Version, constAccessor constants.ConstantValues) error {
	return kaboom
}
