package thorchain

import (
	"fmt"
	"strconv"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

type ManageTHORNameMemo struct {
	MemoBase
	Name    string
	Chain   common.Chain
	Address common.Address
	Expire  int64
	Owner   cosmos.AccAddress
}

func (m ManageTHORNameMemo) GetName() string            { return m.Name }
func (m ManageTHORNameMemo) GetChain() common.Chain     { return m.Chain }
func (m ManageTHORNameMemo) GetAddress() common.Address { return m.Address }
func (m ManageTHORNameMemo) GetBlockExpire() int64      { return m.Expire }

func NewManageTHORNameMemo(name string, chain common.Chain, addr common.Address, expire int64, owner cosmos.AccAddress) ManageTHORNameMemo {
	return ManageTHORNameMemo{
		MemoBase: MemoBase{TxType: TxTHORName},
		Name:     name,
		Chain:    chain,
		Address:  addr,
		Expire:   expire,
		Owner:    owner,
	}
}

func ParseManageTHORNameMemo(parts []string) (ManageTHORNameMemo, error) {
	var err error
	var name string
	var owner cosmos.AccAddress
	expire := int64(0)

	if len(parts) < 4 {
		return ManageTHORNameMemo{}, fmt.Errorf("not enough parameters")
	}

	name = parts[1]
	chain, err := common.NewChain(parts[2])
	if err != nil {
		return ManageTHORNameMemo{}, err
	}

	addr, err := common.NewAddress(parts[3])
	if err != nil {
		return ManageTHORNameMemo{}, err
	}

	if len(parts) >= 5 {
		owner, err = cosmos.AccAddressFromBech32(parts[4])
		if err != nil {
			return ManageTHORNameMemo{}, err
		}
	}

	if len(parts) >= 6 {
		expire, err = strconv.ParseInt(parts[5], 10, 64)
		if err != nil {
			return ManageTHORNameMemo{}, err
		}
	}

	return NewManageTHORNameMemo(name, chain, addr, expire, owner), nil
}
