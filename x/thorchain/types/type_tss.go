package types

import (
	"sort"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewTssVoter create a new instance of TssVoter
func NewTssVoter(id string, pks []string, pool common.PubKey) TssVoter {
	return TssVoter{
		ID:         id,
		PubKeys:    pks,
		PoolPubKey: pool,
	}
}

func (tss TssVoter) GetChains() common.Chains {
	chains := make(common.Chains, 0)
	for _, c := range tss.Chains {
		chain, err := common.NewChain(c)
		if err != nil {
			continue
		}
		chains = append(chains, chain)
	}
	return chains
}

func (tss TssVoter) GetPubKeys() common.PubKeys {
	pubkeys := make(common.PubKeys, 0)
	for _, pk := range tss.PubKeys {
		pk, err := common.NewPubKey(pk)
		if err != nil {
			continue
		}
		pubkeys = append(pubkeys, pk)
	}
	return pubkeys
}

func (tss TssVoter) GetSigners() []cosmos.AccAddress {
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
func (tss TssVoter) HasSigned(signer cosmos.AccAddress) bool {
	for _, sign := range tss.GetSigners() {
		if sign.Equals(signer) {
			return true
		}
	}
	return false
}

// Sign this voter with given signer address
func (tss *TssVoter) Sign(signer cosmos.AccAddress, chains []string) bool {
	if tss.HasSigned(signer) {
		return false
	}
	for _, pk := range tss.GetPubKeys() {
		addr, err := pk.GetThorAddress()
		if addr.Equals(signer) && err == nil {
			tss.Signers = append(tss.Signers, signer.String())
			tss.Chains = append(tss.Chains, chains...)
			return true
		}
	}
	return false
}

// ConsensusChains - get a list of chains that have 2/3rds majority
func (tss *TssVoter) ConsensusChains() common.Chains {
	chainCount := make(map[common.Chain]int, 0)
	for _, chain := range tss.GetChains() {
		if _, ok := chainCount[chain]; !ok {
			chainCount[chain] = 0
		}
		chainCount[chain]++
	}

	chains := make(common.Chains, 0)
	for chain, count := range chainCount {
		if HasSuperMajority(count, len(tss.PubKeys)) {
			chains = append(chains, chain)
		}
	}

	// sort chains for consistency
	sort.SliceStable(chains, func(i, j int) bool {
		return chains[i].String() < chains[j].String()
	})

	return chains
}

// HasConsensus determine if this tss pool has enough signers
func (tss *TssVoter) HasConsensus() bool {
	return HasSuperMajority(len(tss.Signers), len(tss.PubKeys))
}

// Empty check whether TssVoter represent empty info
func (tss *TssVoter) IsEmpty() bool {
	return len(tss.ID) == 0 || len(tss.PoolPubKey) == 0 || len(tss.PubKeys) == 0
}

// String implement fmt.Stringer
func (tss *TssVoter) String() string {
	return tss.ID
}
