package types

import (
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// NewNetwork create a new instance Network it is empty though
func NewNetwork() Network {
	return Network{
		BondRewardRune: cosmos.ZeroUint(),
		TotalBondUnits: cosmos.ZeroUint(),
		ChainRewards:   []ChainReward{},
		ChainBondUnits: []ChainBondUnit{},
	}
}

// CalcNodeRewards calculate node rewards
func (m *Network) CalcNodeRewards(nodeUnits cosmos.Uint) cosmos.Uint {
	return common.GetUncappedShare(nodeUnits, m.TotalBondUnits, m.BondRewardRune)
}

// GetChainRewardAmount returns the reward amount for a specific chain
func (m *Network) GetChainRewardAmount(chainStr string) cosmos.Uint {
	requestedChain, err := common.NewChain(chainStr)
	if err != nil {
		return cosmos.ZeroUint()
	}
	for _, cr := range m.ChainRewards {
		chain, err := common.NewChain(cr.Chain)
		if err != nil {
			continue
		}
		if chain.Equals(requestedChain) {
			return cr.Amount
		}
	}
	return cosmos.ZeroUint()
}

// SetChainRewardAmount sets or updates the reward amount for a specific chain
func (m *Network) SetChainRewardAmount(chainStr string, amount cosmos.Uint) {
	requestedChain, err := common.NewChain(chainStr)
	if err != nil {
		return
	}
	for i, cr := range m.ChainRewards {
		chain, err := common.NewChain(cr.Chain)
		if err != nil {
			continue
		}
		if chain.Equals(requestedChain) {
			m.ChainRewards[i].Amount = amount
			return
		}
	}
	// Chain not found, append new entry
	m.ChainRewards = append(m.ChainRewards, ChainReward{
		Chain:  chainStr,
		Amount: amount,
	})
}

// GetChainBondUnitsAmount returns the bond units for a specific chain
func (m *Network) GetChainBondUnitsAmount(chainStr string) cosmos.Uint {
	requestedChain, err := common.NewChain(chainStr)
	if err != nil {
		return cosmos.ZeroUint()
	}
	for _, cbu := range m.ChainBondUnits {
		chain, err := common.NewChain(cbu.Chain)
		if err != nil {
			continue
		}
		if chain.Equals(requestedChain) {
			return cbu.Units
		}
	}
	return cosmos.ZeroUint()
}

// SetChainBondUnitsAmount sets or updates the bond units for a specific chain
func (m *Network) SetChainBondUnitsAmount(chainStr string, units cosmos.Uint) {
	requestedChain, err := common.NewChain(chainStr)
	if err != nil {
		return
	}
	for i, cbu := range m.ChainBondUnits {
		chain, err := common.NewChain(cbu.Chain)
		if err != nil {
			continue
		}
		if chain.Equals(requestedChain) {
			m.ChainBondUnits[i].Units = units
			return
		}
	}
	// Chain not found, append new entry
	m.ChainBondUnits = append(m.ChainBondUnits, ChainBondUnit{
		Chain: chainStr,
		Units: units,
	})
}
