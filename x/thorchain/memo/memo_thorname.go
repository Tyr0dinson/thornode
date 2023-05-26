package thorchain

import (
	"fmt"
	"strconv"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

type ManageTHORNameMemo struct {
	MemoBase
	Name           string
	Chain          common.Chain
	Address        common.Address
	PreferredAsset common.Asset
	Expire         int64
	Owner          cosmos.AccAddress
	SwapThreshold  cosmos.Uint
}

func (m ManageTHORNameMemo) GetName() string            { return m.Name }
func (m ManageTHORNameMemo) GetChain() common.Chain     { return m.Chain }
func (m ManageTHORNameMemo) GetAddress() common.Address { return m.Address }
func (m ManageTHORNameMemo) GetBlockExpire() int64      { return m.Expire }

func NewManageTHORNameMemo(name string, chain common.Chain, addr common.Address, expire int64, asset common.Asset, owner cosmos.AccAddress, swapThreshold cosmos.Uint) ManageTHORNameMemo {
	return ManageTHORNameMemo{
		MemoBase:       MemoBase{TxType: TxTHORName},
		Name:           name,
		Chain:          chain,
		Address:        addr,
		PreferredAsset: asset,
		Expire:         expire,
		Owner:          owner,
		SwapThreshold:  swapThreshold,
	}
}

func ParseManageTHORNameMemo(parts []string) (ManageTHORNameMemo, error) {
	var err error
	var name string
	var owner cosmos.AccAddress
	preferredAsset := common.EmptyAsset
	expire := int64(0)
	swapThreshold := cosmos.ZeroUint()

	if len(parts) < 4 {
		return ManageTHORNameMemo{}, fmt.Errorf("not enough parameters")
	}

	name = GetPart(parts, 1)
	chain, err := common.NewChain(GetPart(parts, 2))
	if err != nil {
		return ManageTHORNameMemo{}, err
	}

	addr, err := common.NewAddress(GetPart(parts, 3))
	if err != nil {
		return ManageTHORNameMemo{}, err
	}

	if len(parts) >= 5 {
		owner, err = cosmos.AccAddressFromBech32(GetPart(parts, 4))
		if err != nil {
			return ManageTHORNameMemo{}, err
		}
	}

	if len(parts) >= 6 {
		preferredAsset, err = common.NewAsset(GetPart(parts, 5))
		if err != nil {
			return ManageTHORNameMemo{}, err
		}
	}

	if len(parts) >= 7 {
		expire, err = strconv.ParseInt(GetPart(parts, 6), 10, 64)
		if err != nil {
			return ManageTHORNameMemo{}, err
		}
	}

	swapThresholdStr := GetPart(parts, 7)
	if swapThresholdStr != "" {
		swapThresholdUint64, err := strconv.ParseUint(swapThresholdStr, 10, 64)
		if err != nil {
			return ManageTHORNameMemo{}, err
		}
		swapThreshold = cosmos.NewUint(swapThresholdUint64)
	}

	return NewManageTHORNameMemo(name, chain, addr, expire, preferredAsset, owner, swapThreshold), nil
}
