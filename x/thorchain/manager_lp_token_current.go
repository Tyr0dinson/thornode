package thorchain

import (
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

// LpTokenMgrVCUR is VCUR implementation of slasher
type LpTokenMgrVCUR struct {
	keeper   keeper.Keeper
	bank     bankkeeper.Keeper
	eventMgr EventManager
}

// newLpTokenMgrVCUR create a new instance of Slasher
func newLpTokenMgrVCUR(keeper keeper.Keeper, bank bankkeeper.Keeper, eventMgr EventManager) *LpTokenMgrVCUR {
	return &LpTokenMgrVCUR{
		keeper:   keeper,
		bank:     bank,
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
	coins := cosmos.NewCoins(cosmos.NewCoin(pool.NativeLpDenomString(), cosmos.Int(amount)))
	err = s.bank.MintCoins(ctx, ModuleName, coins)
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	err = s.bank.SendCoinsFromModuleToAccount(ctx, ModuleName, recipient, coins)
	if err != nil {
		return cosmos.ZeroUint(), err
	}

	if err != nil {
		return cosmos.ZeroUint(), err
	}

	s.keeper.SetPool(ctx, pool)
	s.keeper.SetLiquidityProvider(ctx, lp)
	return amount, nil
}
