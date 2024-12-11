package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// YieldAccountMgrVCUR is VCUR implementation of YieldAccountMgr
type YieldAccountMgrVCUR struct {
	keeper   keeper.Keeper
	eventMgr EventManager
}

// newYieldAccountMgrVCUR create a new instance of YieldAccountMgr
func newYieldAccountMgrVCUR(keeper keeper.Keeper, eventMgr EventManager) *YieldAccountMgrVCUR {
	return &YieldAccountMgrVCUR{
		keeper:   keeper,
		eventMgr: eventMgr,
	}
}

func (s *YieldAccountMgrVCUR) Deposit(ctx cosmos.Context, address common.Address, asset common.Asset, amount cosmos.Uint) error {
	return nil
}

func (s *YieldAccountMgrVCUR) EndBlock(ctx cosmos.Context, keeper keeper.Keeper) error {
	return nil
}
