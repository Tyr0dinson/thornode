package types

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// NewOperatorBlameRecord creates a new OperatorBlameRecord
func NewOperatorBlameRecord(addr cosmos.AccAddress) OperatorBlameRecord {
	return OperatorBlameRecord{
		OperatorAddress:             addr,
		KeygenBlameCountTotal:       0,
		KeysignBlameCountTotal:      0,
		KeygenBlameCountSinceChurn:  0,
		KeysignBlameCountSinceChurn: 0,
	}
}

// ResetSinceChurnCounters resets both since_churn counters to zero
func (r *OperatorBlameRecord) ResetSinceChurnCounters() {
	r.KeygenBlameCountSinceChurn = 0
	r.KeysignBlameCountSinceChurn = 0
}

// IsEmpty returns true if the record has no blame data
func (r *OperatorBlameRecord) IsEmpty() bool {
	return r.KeygenBlameCountTotal == 0 && r.KeysignBlameCountTotal == 0
}
