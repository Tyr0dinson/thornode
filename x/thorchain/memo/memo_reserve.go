package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
)

type ReserveMemo struct {
	MemoBase
}

func (p *parser) ParseReserveMemo() (ReserveMemo, error) {
	return ReserveMemo{
		MemoBase: MemoBase{TxType: common.TxReserve},
	}, nil
}
