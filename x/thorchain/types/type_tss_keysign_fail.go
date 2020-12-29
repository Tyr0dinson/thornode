package types

import (
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewTssKeysignFailVoter create a new instance of TssKeysignFailVoter
func NewTssKeysignFailVoter(id string, height int64) TssKeysignFailVoter {
	return TssKeysignFailVoter{
		ID:     id,
		Height: height,
	}
}

func (tss TssKeysignFailVoter) GetSigners() []cosmos.AccAddress {
	addrs := make([]cosmos.AccAddress, 0)
	for _, a := range tss.Signers {
		addr, err := cosmos.AccAddressFromBech32(a)
		if err != nil {
			continue
		}
		addrs = append(addrs, addr)
	}
	return addrs
}

// HasSigned - check if given address has signed
func (tss TssKeysignFailVoter) HasSigned(signer cosmos.AccAddress) bool {
	for _, sign := range tss.GetSigners() {
		if sign.Equals(signer) {
			return true
		}
	}
	return false
}

// Sign this voter with given signer address
func (tss *TssKeysignFailVoter) Sign(signer cosmos.AccAddress) bool {
	if tss.HasSigned(signer) {
		return false
	}
	tss.Signers = append(tss.Signers, signer.String())
	return true
}

// HasConsensus determine if this tss pool has enough signers
func (tss *TssKeysignFailVoter) HasConsensus(nas NodeAccounts) bool {
	var count int
	for _, signer := range tss.GetSigners() {
		if nas.IsNodeKeys(signer) {
			count++
		}
	}
	return HasSimpleMajority(count, len(nas))
}

// Empty to check whether this Voter is empty or not
func (tss *TssKeysignFailVoter) Empty() bool {
	return len(tss.ID) == 0 || tss.Height == 0
}

// String implement fmt.Stringer , return's the ID
func (tss *TssKeysignFailVoter) String() string {
	return tss.ID
}
