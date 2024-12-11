package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
)

type YieldAccountDepositMemo struct {
	MemoBase
	Address common.Address
}

func (m YieldAccountDepositMemo) GetDestination() common.Address { return m.Address }

func NewYieldAccountDepositMemo(addr common.Address) YieldAccountDepositMemo {
	return YieldAccountDepositMemo{
		MemoBase: MemoBase{TxType: TxYieldAccountDeposit},
		Address:  addr,
	}
}

func (p *parser) ParseYieldAccountDeposit() (YieldAccountDepositMemo, error) {
	addr := p.getAddress(1, true, common.NoAddress)
	return NewYieldAccountDepositMemo(addr), p.Error()
}
