package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type SwitchMemo struct {
	MemoBase
	Address cosmos.AccAddress
}

func (m SwitchMemo) GetAccAddress() cosmos.AccAddress { return m.Address }

func NewSwitchMemo(addr cosmos.AccAddress) SwitchMemo {
	return SwitchMemo{
		MemoBase: MemoBase{TxType: common.TxSwitch},
		Address:  addr,
	}
}

func (p *parser) ParseSwitch() (SwitchMemo, error) {
	addr := p.getAccAddress(1, true, nil)
	return NewSwitchMemo(addr), p.Error()
}
