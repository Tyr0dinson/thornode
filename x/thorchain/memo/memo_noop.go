package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
)

type NoOpMemo struct {
	MemoBase
	Action string
}

// String implement fmt.Stringer
func (m NoOpMemo) String() string {
	if len(m.Action) == 0 {
		return "noop"
	}
	return fmt.Sprintf("noop:%s", m.Action)
}

// NewNoOpMemo create a new instance of NoOpMemo
func NewNoOpMemo(action string) NoOpMemo {
	return NoOpMemo{
		MemoBase: MemoBase{TxType: common.TxNoOp},
		Action:   action,
	}
}

// ParseNoOpMemo try to parse the memo
func (p *parser) ParseNoOpMemo() (NoOpMemo, error) {
	return NewNoOpMemo(p.get(1)), p.Error()
}
