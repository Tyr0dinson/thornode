package thorchain

import (
	"strconv"

	"gitlab.com/thorchain/thornode/common"
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"
)

type AddLiquidityMemo struct {
	MemoBase
	Address              common.Address
	AffiliateAddress     common.Address
	AffiliateBasisPoints cosmos.Uint
	Sender               common.Address // only used for hvn/mnr chains
}

func (m AddLiquidityMemo) GetDestination() common.Address { return m.Address }
func (m AddLiquidityMemo) GetSender() common.Address      { return m.Sender }

func NewAddLiquidityMemo(asset common.Asset, addr, affAddr common.Address, affPts cosmos.Uint, sender common.Address) AddLiquidityMemo {
	return AddLiquidityMemo{
		MemoBase:             MemoBase{TxType: TxAdd, Asset: asset},
		Address:              addr,
		AffiliateAddress:     affAddr,
		AffiliateBasisPoints: affPts,
		Sender:               sender,
	}
}

func ParseAddLiquidityMemo(ctx cosmos.Context, keeper keeper.Keeper, asset common.Asset, parts []string) (AddLiquidityMemo, error) {
	var err error
	addr := common.NoAddress
	affAddr := common.NoAddress
	sender := common.NoAddress
	affPts := cosmos.ZeroUint()
	if len(parts) >= 3 && len(parts[2]) > 0 {
		firstAddr, secondAddr := ExtractAddresses(asset.Chain, parts[2])
		// first address is lp associative address.
		if len(firstAddr) > 0 {
			if keeper == nil {
				addr, err = common.NewAddress(firstAddr)
			} else {
				addr, err = FetchAddress(ctx, keeper, firstAddr, asset.Chain)
			}
		}
		// second address is for sender if it exist
		if len(secondAddr) > 0 {
			sender, err = common.NewAddress(secondAddr)
			if err != nil {
				return AddLiquidityMemo{}, err
			}
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
		pts, err := strconv.ParseUint(parts[4], 10, 64)
		if err != nil {
			return AddLiquidityMemo{}, err
		}
		affPts = cosmos.NewUint(pts)
	}
	return NewAddLiquidityMemo(asset, addr, affAddr, affPts, sender), nil
}
