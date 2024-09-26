package thorchain

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

type DummyLpTokenManager struct{}

func NewDummyLpTokenManager() *DummyLpTokenManager {
	return &DummyLpTokenManager{}
}

func (d DummyLpTokenManager) Mint(ctx cosmos.Context,
	asset common.Asset,
	accAddr common.Address,
	amount cosmos.Uint,
	recipient cosmos.AccAddress,
	txID common.TxID,
) (cosmos.Uint, error) {
	return cosmos.ZeroUint(), nil
}

func (d DummyLpTokenManager) Redeem(ctx cosmos.Context,
	asset common.Asset,
	accAddr common.Address,
	amount cosmos.Uint,
	txID common.TxID,
) (cosmos.Uint, error) {
	return cosmos.ZeroUint(), nil
}
