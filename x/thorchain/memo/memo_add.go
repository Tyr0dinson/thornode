package thorchain

import (
	"github.com/blang/semver"
	"gitlab.com/thorchain/thornode/common"
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

type AddLiquidityMemo struct {
	MemoBase
	Address              common.Address
	AffiliateAddress     common.Address
	AffiliateBasisPoints cosmos.Uint
}

func (m AddLiquidityMemo) GetDestination() common.Address { return m.Address }

func NewAddLiquidityMemo(asset common.Asset, addr, affAddr common.Address, affPts cosmos.Uint) AddLiquidityMemo {
	return AddLiquidityMemo{
		MemoBase:             MemoBase{TxType: TxAdd, Asset: asset},
		Address:              addr,
		AffiliateAddress:     affAddr,
		AffiliateBasisPoints: affPts,
	}
}

func ParseAddLiquidityMemo(ctx cosmos.Context, keeper keeper.Keeper, asset common.Asset, parts []string) (AddLiquidityMemo, error) {
	if keeper == nil {
		return ParseAddLiquidityMemoV1(ctx, keeper, asset, parts)
	}
	switch {
	case keeper.GetVersion().GTE(semver.MustParse("1.104.0")):
		return ParseAddLiquidityMemoV104(ctx, keeper, asset, parts)
	default:
		return ParseAddLiquidityMemoV1(ctx, keeper, asset, parts)
	}
}

func ParseAddLiquidityMemoV104(ctx cosmos.Context, keeper keeper.Keeper, asset common.Asset, parts []string) (AddLiquidityMemo, error) {
	var err error
	addr := common.NoAddress
	affAddr := common.NoAddress
	affPts := cosmos.ZeroUint()
	if len(parts) >= 3 && len(parts[2]) > 0 {
		if keeper == nil {
			addr, err = common.NewAddress(parts[2])
		} else {
			addr, err = FetchAddress(ctx, keeper, parts[2], asset.Chain)
		}
		if err != nil {
			return AddLiquidityMemo{}, err
		}
	}

	if len(parts) > 4 && len(parts[3]) > 0 && len(parts[4]) > 0 {
		if keeper == nil {
			affAddr, err = common.NewAddress(parts[3])
		} else {
			affAddr, err = FetchAddress(ctx, keeper, parts[3], common.THORChain)
		}
		if err != nil {
			return AddLiquidityMemo{}, err
		}
		affPts, err = ParseAffiliateBasisPoints(ctx, keeper, parts[4])
		if err != nil {
			return AddLiquidityMemo{}, err
		}
	}
	return NewAddLiquidityMemo(asset, addr, affAddr, affPts), nil
}
