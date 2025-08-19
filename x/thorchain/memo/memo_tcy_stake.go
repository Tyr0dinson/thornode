package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
)

type TCYStakeMemo struct {
	MemoBase
}

func NewTCYStakeMemo() TCYStakeMemo {
	return TCYStakeMemo{
		MemoBase: MemoBase{TxType: common.TxTCYStake},
	}
}

func (p *parser) ParseTCYStakeMemo() (TCYStakeMemo, error) {
	return NewTCYStakeMemo(), p.Error()
}
