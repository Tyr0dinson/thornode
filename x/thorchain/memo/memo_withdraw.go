package thorchain

import (
	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common"
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/constants"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

type WithdrawLiquidityMemo struct {
	MemoBase
	Amount               cosmos.Uint
	WithdrawalAsset      common.Asset
	AffiliateAddress     common.Address
	AffiliateBasisPoints cosmos.Uint
}

func (m WithdrawLiquidityMemo) GetAmount() cosmos.Uint           { return m.Amount }
func (m WithdrawLiquidityMemo) GetWithdrawalAsset() common.Asset { return m.WithdrawalAsset }

func NewWithdrawLiquidityMemo(asset common.Asset, amt cosmos.Uint, withdrawalAsset common.Asset, affAddr common.Address, affPts cosmos.Uint) WithdrawLiquidityMemo {
	return WithdrawLiquidityMemo{
		MemoBase:             MemoBase{TxType: TxWithdraw, Asset: asset},
		Amount:               amt,
		WithdrawalAsset:      withdrawalAsset,
		AffiliateAddress:     affAddr,
		AffiliateBasisPoints: affPts,
	}
}

func (p *parser) ParseWithdrawLiquidityMemo() (WithdrawLiquidityMemo, error) {
	switch {
	case p.version.GTE(semver.MustParse("1.121.0")):
		return p.ParseWithdrawLiquidityMemoV121()
	case p.version.GTE(semver.MustParse("1.116.0")):
		return p.ParseWithdrawLiquidityMemoV116()
	default:
		return ParseWithdrawLiquidityMemoV1(p.getAsset(1, true, common.EmptyAsset), p.parts)
	}
}

func (p *parser) ParseWithdrawLiquidityMemoV121() (WithdrawLiquidityMemo, error) {
	asset := p.getAsset(1, true, common.EmptyAsset)
	withdrawalBasisPts := p.getUintWithMaxValue(2, false, types.MaxWithdrawBasisPoints, types.MaxWithdrawBasisPoints)
	withdrawalAsset := p.getAsset(3, false, common.EmptyAsset)
	return NewWithdrawLiquidityMemo(asset, withdrawalBasisPts, withdrawalAsset, common.NoAddress, cosmos.ZeroUint()), p.Error()
}

func (p *parser) ParseWithdrawLiquidityMemoV116() (WithdrawLiquidityMemo, error) {
	asset := p.getAsset(1, true, common.EmptyAsset)
	withdrawalBasisPts := p.getUintWithMaxValue(2, false, types.MaxWithdrawBasisPoints, types.MaxWithdrawBasisPoints)
	withdrawalAsset := p.getAsset(3, false, common.EmptyAsset)
	affAddr := p.getAddressWithKeeper(4, false, common.NoAddress, common.THORChain)
	affPts := p.getUintWithMaxValue(5, false, 0, constants.MaxBasisPts)
	return NewWithdrawLiquidityMemo(asset, withdrawalBasisPts, withdrawalAsset, affAddr, affPts), p.Error()
}
