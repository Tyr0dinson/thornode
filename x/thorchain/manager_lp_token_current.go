package thorchain

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// LpTokenMgrVCUR is VCUR implementation of slasher
type LpTokenMgrVCUR struct {
	keeper   keeper.Keeper
	eventMgr EventManager
}

// newLpTokenMgrVCUR create a new instance of Slasher
func newLpTokenMgrVCUR(keeper keeper.Keeper, eventMgr EventManager) *LpTokenMgrVCUR {
	return &LpTokenMgrVCUR{
		keeper:   keeper,
		eventMgr: eventMgr,
	}
}

func (s *LpTokenMgrVCUR) Mint(
	ctx cosmos.Context,
	asset common.Asset,
	runeAddr, assetAddr common.Address,
	amount cosmos.Uint,
	recipient cosmos.AccAddress,
	txID common.TxID,
) (cosmos.Uint, error) {
	pool, err := s.keeper.GetPool(ctx, asset)
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	fetchAddr := runeAddr
	if fetchAddr.IsEmpty() {
		fetchAddr = assetAddr
	}
	lp, err := s.keeper.GetLiquidityProvider(ctx, asset, fetchAddr)
	if err != nil {
		return cosmos.ZeroUint(), ErrInternal(err, "fail to get liquidity provider")
	}

	if amount.GT(lp.Units) {
		return cosmos.ZeroUint(), ErrInternal(err, "excessive amount")
	}

	lp.Units = lp.Units.Sub(amount)
	pool.BankUnits = pool.BankUnits.Add(amount)
	pool.LPUnits = pool.LPUnits.Sub(amount)
	lpAsset, err := pool.GetLpAsset()
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	err = s.keeper.MintAndSendToAccount(ctx, recipient, common.NewCoin(lpAsset, amount))
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	s.keeper.SetPool(ctx, pool)
	s.keeper.SetLiquidityProvider(ctx, lp)
	return amount, nil
}
