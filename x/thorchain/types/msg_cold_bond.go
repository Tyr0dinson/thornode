package types

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgColdBond create new MsgColdBond message
func NewMsgColdBond(operator cosmos.AccAddress, asset common.Asset, bond common.Coin, pk common.PubKey, signer cosmos.AccAddress) *MsgColdBond {
	return &MsgColdBond{
		Operator: operator,
		Asset:    asset,
		Bond:     bond,
		PubKey:   pk,
		Signer:   signer,
	}
}

// Route should return the router key of the module
func (m *MsgColdBond) Route() string { return RouterKey }

// Type should return the action
func (m MsgColdBond) Type() string { return "cold_bond" }

// ValidateBasic runs stateless checks on the message
func (m *MsgColdBond) ValidateBasic() error {
	if m.Operator.Empty() {
		return cosmos.ErrInvalidAddress("operator address cannot be empty")
	}
	if m.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest("asset cannot be empty")
	}
	if !m.Asset.IsGasAsset() {
		return cosmos.ErrUnknownRequest("asset must be a gas asset")
	}
	if m.Bond.IsEmpty() {
		return cosmos.ErrUnknownRequest("bond cannot be zero")
	}
	if !m.Bond.Asset.IsNativeRune() {
		return cosmos.ErrUnknownRequest("bond needs to be in rune")
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress("empty signer address")
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (m *MsgColdBond) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners defines whose signature is required
func (m *MsgColdBond) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
