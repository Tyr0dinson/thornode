package types

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const txOutFailIDFormat = "%d/%d/%s"

// TxOutFailVoterID derives the unique key for a TxOut failure voter.
func TxOutFailVoterID(height int64, index uint64, inHash common.TxID) string {
	return fmt.Sprintf(txOutFailIDFormat, height, index, inHash.String())
}

// NewTxOutFailVoter creates a voter record that tracks consensus for marking a
// TxOut item as failed/fatal.
func NewTxOutFailVoter(height int64, index uint64, inHash common.TxID) TxOutFailVoter {
	return TxOutFailVoter{
		ID:     TxOutFailVoterID(height, index, inHash),
		Height: height,
		Index:  index,
		InHash: inHash,
	}
}

func (m *TxOutFailVoter) GetSigners() []cosmos.AccAddress {
	addresses := make([]cosmos.AccAddress, 0, len(m.Signers))
	for _, signer := range m.Signers {
		addr, err := cosmos.AccAddressFromBech32(signer)
		if err != nil {
			continue
		}
		addresses = append(addresses, addr)
	}
	return addresses
}

// HasSigned returns true when the supplied signer already endorsed this voter.
func (m *TxOutFailVoter) HasSigned(signer cosmos.AccAddress) bool {
	for _, existing := range m.GetSigners() {
		if existing.Equals(signer) {
			return true
		}
	}
	return false
}

// Sign appends the signer to the voter; it returns false when the signer had
// already endorsed the voter.
func (m *TxOutFailVoter) Sign(signer cosmos.AccAddress) bool {
	if m.HasSigned(signer) {
		return false
	}
	m.Signers = append(m.Signers, signer.String())
	return true
}

// HasConsensus checks whether a simple majority of active node accounts that
// belong to the relevant vault have signed the voter.
func (m *TxOutFailVoter) HasConsensus(nas NodeAccounts) bool {
	var count int
	for _, signer := range m.GetSigners() {
		for _, na := range nas {
			if signer.Equals(na.NodeAddress) {
				count++
				break
			}
		}
	}
	return HasSimpleMajority(count, len(nas))
}

// Empty is true when the voter has not been initialised yet.
func (m *TxOutFailVoter) Empty() bool {
	return len(m.ID) == 0 || m.Height == 0
}

// String implements fmt.Stringer using the unique voter ID.
func (m *TxOutFailVoter) String() string {
	return m.ID
}
