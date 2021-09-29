package types

import (
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgSetNodeType is a constructor function for NewMsgSetNodeType
func NewMsgSetNodeType(nodeType NodeType, signer cosmos.AccAddress) *MsgSetNodeType {
	return &MsgSetNodeType{
		NodeType: nodeType,
		Signer:   signer,
	}
}

// Route should return the router key of the module
func (m *MsgSetNodeType) Route() string { return RouterKey }

// Type should return the action
func (m MsgSetNodeType) Type() string { return "set_node_type" }

// ValidateBasic runs stateless checks on the message
func (m *MsgSetNodeType) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.NodeType == NodeType_TypeUnknown {
		return cosmos.ErrUnknownRequest("can not set node type as unknown")
	}

	return nil
}

// GetSignBytes encodes the message for signing
func (m *MsgSetNodeType) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners defines whose signature is required
func (m *MsgSetNodeType) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
