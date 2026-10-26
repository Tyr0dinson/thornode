package types

import "fmt"

// Valid checks the parked inbound is well formed.
func (m *SolvencyGatedInbound) Valid() error {
	if m.Chain.IsEmpty() {
		return fmt.Errorf("chain cannot be empty")
	}
	if m.PubKey.IsEmpty() {
		return fmt.Errorf("pubkey cannot be empty")
	}
	if m.TxID.IsEmpty() {
		return fmt.Errorf("tx id cannot be empty")
	}
	if m.Height < 0 || m.ParkedAt < 0 {
		return fmt.Errorf("heights cannot be negative")
	}
	return nil
}

func (m *SolvencyGatedInbound) String() string {
	return fmt.Sprintf("%s %s height %d vault %s parked at %d", m.Chain, m.TxID, m.Height, m.PubKey, m.ParkedAt)
}
