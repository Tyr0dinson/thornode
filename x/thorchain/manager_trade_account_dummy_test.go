package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type DummyTradeAccountManager struct{}

func NewDummyTradeAccountManager() *DummyTradeAccountManager {
	return &DummyTradeAccountManager{}
}

func (d DummyTradeAccountManager) EndBlock(ctx cosmos.Context, keeper keeper.Keeper) error {
	return nil
}

func (d DummyTradeAccountManager) BalanceOf(_ cosmos.Context, _ common.Asset, _ cosmos.AccAddress) cosmos.Uint {
	return cosmos.ZeroUint()
}

func (d DummyTradeAccountManager) Deposit(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, _ common.TxID) (cosmos.Uint, error) {
	return cosmos.ZeroUint(), nil
}

func (d DummyTradeAccountManager) Withdrawal(ctx cosmos.Context, asset common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, _ common.TxID) (cosmos.Uint, error) {
	return cosmos.ZeroUint(), nil
}

// DummySecuredAssetManager is a dummy implementation for testing
type DummySecuredAssetManager struct{}

func NewDummySecuredAssetManager() *DummySecuredAssetManager {
	return &DummySecuredAssetManager{}
}

func (d DummySecuredAssetManager) EndBlock(ctx cosmos.Context, keeper keeper.Keeper) error {
	return nil
}

func (d DummySecuredAssetManager) Deposit(_ cosmos.Context, _ common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, _ common.TxID) (cosmos.Coin, error) {
	return cosmos.NewCoin("", cosmos.ZeroInt()), nil
}

func (d DummySecuredAssetManager) Withdraw(_ cosmos.Context, _ common.Asset, amount cosmos.Uint, owner cosmos.AccAddress, assetAddr common.Address, _ common.TxID) (common.Coin, error) {
	return common.NoCoin, nil
}

func (d DummySecuredAssetManager) BalanceOf(_ cosmos.Context, _ common.Asset, owner cosmos.AccAddress) cosmos.Uint {
	return cosmos.ZeroUint()
}

func (d DummySecuredAssetManager) GetSecuredAssetStatus(_ cosmos.Context, _ common.Asset) (keeper.SecuredAsset, cosmos.Uint, error) {
	return keeper.SecuredAsset{}, cosmos.ZeroUint(), nil
}

func (d DummySecuredAssetManager) GetShareSupply(_ cosmos.Context, _ common.Asset) cosmos.Uint {
	return cosmos.ZeroUint()
}

func (d DummySecuredAssetManager) CheckHalt(_ cosmos.Context) error {
	return nil
}
