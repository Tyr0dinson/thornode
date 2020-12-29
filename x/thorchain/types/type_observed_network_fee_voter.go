package types

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewObservedNetworkFeeVoter create a new instance of ObservedNetworkFeeVoter
func NewObservedNetworkFeeVoter(reportBlockHeight int64, chain common.Chain) ObservedNetworkFeeVoter {
	return ObservedNetworkFeeVoter{
		ReportBlockHeight: reportBlockHeight,
		Chain:             chain,
	}
}

func (f *ObservedNetworkFeeVoter) GetSigners() []cosmos.AccAddress {
	addrs := make([]cosmos.AccAddress, 0)
	for _, a := range f.Signers {
		addr, err := cosmos.AccAddressFromBech32(a)
		if err != nil {
			continue
		}
		addrs = append(addrs, addr)
	}
	return addrs
}

// HasSigned - check if given address has signed
func (f *ObservedNetworkFeeVoter) HasSigned(signer cosmos.AccAddress) bool {
	for _, sign := range f.GetSigners() {
		if sign.Equals(signer) {
			return true
		}
	}
	return false
}

// Sign this voter with given signer address
func (f *ObservedNetworkFeeVoter) Sign(signer cosmos.AccAddress) bool {
	if f.HasSigned(signer) {
		return false
	}
	f.Signers = append(f.Signers, signer.String())
	return true
}

// HasConsensus Determine if this errata has enough signers
func (f *ObservedNetworkFeeVoter) HasConsensus(nas NodeAccounts) bool {
	var count int
	for _, signer := range f.GetSigners() {
		if nas.IsNodeKeys(signer) {
			count++
		}
	}
	if HasSuperMajority(count, len(nas)) {
		return true
	}

	return false
}

// IsEmpty return true when chain is empty and block height is 0
func (f *ObservedNetworkFeeVoter) IsEmpty() bool {
	return f.Chain.IsEmpty() && f.ReportBlockHeight == 0
}

// String implement fmt.Stringer
func (f *ObservedNetworkFeeVoter) String() string {
	return fmt.Sprintf("%s-%d", f.Chain.String(), f.ReportBlockHeight)
}
