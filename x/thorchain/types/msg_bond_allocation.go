package types

import (
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgBondAllocation create a new instance of MsgBondAllocation
func NewMsgBondAllocation(name string, type_ BondAllocationType, amount cosmos.Uint, signer cosmos.AccAddress) *MsgBondAllocation {
	return &MsgBondAllocation{
		Name:     name,
		BondType: type_,
		Amount:   amount,
		Signer:   signer,
	}
}

// Route should return the Route of the module
func (m *MsgBondAllocation) Route() string { return RouterKey }

// Type should return the action
func (m MsgBondAllocation) Type() string { return "bond_allocation" }

// ValidateBasic runs stateless checks on the message
func (m *MsgBondAllocation) ValidateBasic() error {
	// validate n
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (m *MsgBondAllocation) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners defines whose signature is required
func (m *MsgBondAllocation) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
