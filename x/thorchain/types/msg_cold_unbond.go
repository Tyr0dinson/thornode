package types

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgColdUnBond create new MsgColdUnBond message
func NewMsgColdUnBond(operator cosmos.AccAddress, amount cosmos.Uint, asset common.Asset, signer cosmos.AccAddress) *MsgColdUnBond {
	return &MsgColdUnBond{
		Operator: operator,
		Amount:   amount,
		Asset:    asset,
		Signer:   signer,
	}
}

// Route should return the router key of the module
func (m *MsgColdUnBond) Route() string { return RouterKey }

// Type should return the action
func (m MsgColdUnBond) Type() string { return "cold_unbond" }

// ValidateBasic runs stateless checks on the message
func (m *MsgColdUnBond) ValidateBasic() error {
	if m.Operator.Empty() {
		return cosmos.ErrInvalidAddress("operator address cannot be empty")
	}
	if m.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest("asset cannot be empty")
	}
	if m.Amount.IsZero() {
		return cosmos.ErrUnknownRequest("unbond amount cannot be zero")
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress("empty signer address")
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (m *MsgColdUnBond) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners defines whose signature is required
func (m *MsgColdUnBond) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
