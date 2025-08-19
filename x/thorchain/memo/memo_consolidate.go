package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
)

type ConsolidateMemo struct {
	MemoBase
}

// String implement fmt.Stringer
func (m ConsolidateMemo) String() string {
	return "consolidate"
}

// NewConsolidateMemo create a new instance of ConsolidateMemo
func NewConsolidateMemo() ConsolidateMemo {
	return ConsolidateMemo{
		MemoBase: MemoBase{TxType: common.TxConsolidate},
	}
}

// ParseConsolidateMemo try to parse the memo
func (p *parser) ParseConsolidateMemo() (ConsolidateMemo, error) {
	return NewConsolidateMemo(), nil
}
