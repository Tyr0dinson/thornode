package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
)

type UpdateLPMemo struct {
	MemoBase
	NewAddress common.Address
}

func (m UpdateLPMemo) GetNewAddress() common.Address { return m.NewAddress }

func NewUpdateLPMemo(asset common.Asset, addr common.Address) UpdateLPMemo {
	return UpdateLPMemo{
		MemoBase:   MemoBase{TxType: TxAdd, Asset: asset},
		NewAddress: addr,
	}
}

func ParseUpdateLPMemo(asset common.Asset, parts []string) (UpdateLPMemo, error) {
	var err error
	addr := common.NoAddress

	if len(parts) != 3 {
		return UpdateLPMemo{}, fmt.Errorf("not enough parameters")
	}

	if len(parts) == 3 {
		addr, err = common.NewAddress(parts[2])
		if err != nil {

		}
	}
	return NewUpdateLPMemo(asset, addr), nil
}
