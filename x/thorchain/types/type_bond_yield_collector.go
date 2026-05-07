package types

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// NewBondYieldCollector creates an empty collector for one node/provider pair.
func NewBondYieldCollector(nodeAddr, providerAddr cosmos.AccAddress) BondYieldCollector {
	return BondYieldCollector{
		NodeAddress:         nodeAddr,
		BondProviderAddress: providerAddr,
		RuneAmount:          cosmos.ZeroUint(),
	}
}

// IsEmpty reports whether the collector is missing either side of its key.
func (m BondYieldCollector) IsEmpty() bool {
	return m.NodeAddress.Empty() || m.BondProviderAddress.Empty()
}

// Key returns the stable keeper key segment for the node/provider pair.
func (m BondYieldCollector) Key() string {
	return fmt.Sprintf("%s/%s", m.NodeAddress.String(), m.BondProviderAddress.String())
}
