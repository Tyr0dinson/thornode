package types

import (
	"strings"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

type BondAllocations []BondAllocation

func NewBondAllocation(name string, nodeAddress cosmos.AccAddress, type_ BondAllocationType) BondAllocation {
	return BondAllocation{
		THORName:    name,
		NodeAddress: nodeAddress,
		Type:        type_,
		Amount:      cosmos.ZeroUint(),
	}
}

func (b BondAllocations) GetTotalAmount() cosmos.Uint {
	var total cosmos.Uint
	for _, allocation := range b {
		total = total.Add(allocation.Amount)
	}
	return total
}

func (b BondAllocations) FilterByType(allocType BondAllocationType) BondAllocations {
	var result BondAllocations
	for _, allocation := range b {
		if allocation.Type == allocType {
			result = append(result, allocation)
		}
	}
	return result
}

func (b BondAllocations) FilterByTHORName(name string) BondAllocations {
	var result BondAllocations
	for _, allocation := range b {
		if strings.EqualFold(allocation.THORName, name) {
			result = append(result, allocation)
		}
	}
	return result
}

func (b BondAllocations) FilterByNodeAddress(addr cosmos.AccAddress) BondAllocations {
	var result BondAllocations
	for _, allocation := range b {
		if allocation.NodeAddress.Equals(addr) {
			result = append(result, allocation)
		}
	}
	return result
}

func (b BondAllocations) Set(alloc BondAllocation) BondAllocations {
	for i, allocation := range b {
		if allocation.Type == alloc.Type && allocation.NodeAddress.Equals(alloc.NodeAddress) {
			b[i] = alloc
			return b
		}
	}
	return append(b, alloc)
}
