package types

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewErrataTxVoter create a new instance of ErrataTxVoter
func NewErrataTxVoter(txID common.TxID, chain common.Chain) ErrataTxVoter {
	return ErrataTxVoter{
		TxID:  txID,
		Chain: chain,
	}
}

func (errata *ErrataTxVoter) GetSigners() []cosmos.AccAddress {
	signers := make([]cosmos.AccAddress, 0)
	for _, str := range errata.Signers {
		signer, err := cosmos.AccAddressFromBech32(str)
		if err != nil {
			continue
		}
		signers = append(signers, signer)
	}
	return signers
}

// HasSigned - check if given address has signed
func (errata *ErrataTxVoter) HasSigned(signer cosmos.AccAddress) bool {
	for _, sign := range errata.GetSigners() {
		if sign.Equals(signer) {
			return true
		}
	}
	return false
}

// Sign this voter with the given signer address, if the given signer is already signed , it return false
// otherwise it add the given signer to the signers list and return true
func (errata *ErrataTxVoter) Sign(signer cosmos.AccAddress) bool {
	if errata.HasSigned(signer) {
		return false
	}
	errata.Signers = append(errata.Signers, signer.String())
	return true
}

// HasConsensus determine if this errata has enough signers
func (errata *ErrataTxVoter) HasConsensus(nas NodeAccounts) bool {
	var count int
	for _, signer := range errata.GetSigners() {
		if nas.IsNodeKeys(signer) {
			count++
		}
	}
	if HasSuperMajority(count, len(nas)) {
		return true
	}

	return false
}

// Empty check whether TxID or Chain is empty
func (errata *ErrataTxVoter) Empty() bool {
	if errata.TxID.IsEmpty() || errata.Chain.IsEmpty() {
		return true
	}
	return false
}

// String implement fmt.Stinger , return a string representation of errata tx voter
func (errata *ErrataTxVoter) String() string {
	return fmt.Sprintf("%s-%s", errata.Chain.String(), errata.TxID.String())
}
