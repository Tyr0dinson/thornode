package types

import (
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// NewNodeBlameRecord creates a new NodeBlameRecord
func NewNodeBlameRecord(addr cosmos.AccAddress) NodeBlameRecord {
	return NodeBlameRecord{
		NodeAddress:                 addr,
		KeygenBlameCountTotal:       0,
		KeysignBlameCountTotal:      0,
		KeygenBlameCountSinceChurn:  0,
		KeysignBlameCountSinceChurn: 0,
	}
}

// ResetSinceChurnCounters resets both since_churn counters to zero
func (r *NodeBlameRecord) ResetSinceChurnCounters() {
	r.KeygenBlameCountSinceChurn = 0
	r.KeysignBlameCountSinceChurn = 0
}

// IsEmpty returns true if the record has no blame data
func (r *NodeBlameRecord) IsEmpty() bool {
	return r.KeygenBlameCountTotal == 0 && r.KeysignBlameCountTotal == 0
}
