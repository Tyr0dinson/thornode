package types

import (
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// NewTssKeysignFailVoter create a new instance of TssKeysignFailVoter
func NewTssKeysignFailVoter(id string) TssKeysignFailVoter {
	return TssKeysignFailVoter{
		ID:    id,
		State: TssKeysignFailVoterState_PendingVoter,
	}
}

func (m *TssKeysignFailVoter) GetSigners() []cosmos.AccAddress {
	addrs := make([]cosmos.AccAddress, 0)
	for _, a := range m.Signers {
		addr, err := cosmos.AccAddressFromBech32(a)
		if err != nil {
			continue
		}
		addrs = append(addrs, addr)
	}
	return addrs
}

// HasSigned - check if given address has signed
func (m *TssKeysignFailVoter) HasSigned(signer cosmos.AccAddress) bool {
	for _, sign := range m.GetSigners() {
		if sign.Equals(signer) {
			return true
		}
	}
	return false
}

// Sign this voter with given signer address
func (m *TssKeysignFailVoter) Sign(signer cosmos.AccAddress) bool {
	if m.HasSigned(signer) {
		return false
	}
	m.Signers = append(m.Signers, signer.String())
	return true
}

// AddReport records a reporter's vote together with the blame list that
// reporter submitted. With the blamed set removed from the voter id (Release
// 3.22.0) every reporter for one failure lands on this single voter, so each
// reporter's own (possibly divergent) blame list is accumulated here and
// combined on consensus by deriveConsensusBlame. It dedupes on signer, keeping
// Signers in lockstep with Reports so MemberSignerCount / HasConsensus are
// unaffected. Returns false if the signer already reported.
func (m *TssKeysignFailVoter) AddReport(signer cosmos.AccAddress, blamed []string) bool {
	if !m.Sign(signer) {
		return false
	}
	m.Reports = append(m.Reports, &TssKeysignFailReport{
		Signer: signer.String(),
		Blamed: blamed,
	})
	return true
}

// MemberSignerCount returns the number of signers that are members of the given node accounts
func (m *TssKeysignFailVoter) MemberSignerCount(nas NodeAccounts) int {
	count := 0
	for _, signer := range m.GetSigners() {
		for _, item := range nas {
			if signer.Equals(item.NodeAddress) {
				count++
				break
			}
		}
	}
	return count
}

// HasConsensus determine if this tss pool has enough signers
func (m *TssKeysignFailVoter) HasConsensus(nas NodeAccounts) bool {
	return HasSimpleMajority(m.MemberSignerCount(nas), len(nas))
}

// String implement fmt.Stringer , return's the ID
func (m *TssKeysignFailVoter) String() string {
	return m.ID
}

// String implement fmt.Stringer
func (m *TssKeysignFailReport) String() string {
	return m.Signer + ":" + strings.Join(m.Blamed, ",")
}
