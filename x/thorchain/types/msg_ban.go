package types

import (
	"github.com/google/uuid"
	cosmos "gitlab.com/thorchain/thornode/common/cosmos"
)

var _ cosmos.Msg = &MsgBan{}

// NewMsgBan is a constructor function for NewMsgBan
func NewMsgBan(addr, signer cosmos.AccAddress) *MsgBan {
	return &MsgBan{
		Id:          uuid.New().String(),
		NodeAddress: addr,
		Signer:      signer,
	}
}

// Route should return the name of the module
func (msg *MsgBan) Route() string { return RouterKey }

// Type should return the action
func (msg *MsgBan) Type() string { return "ban" }

// ValidateBasic runs stateless checks on the message
func (msg *MsgBan) ValidateBasic() error {
	if msg.Signer.Empty() {
		return cosmos.ErrInvalidAddress(msg.Signer.String())
	}
	if msg.NodeAddress.Empty() {
		return cosmos.ErrInvalidAddress(msg.NodeAddress.String())
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (msg *MsgBan) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(msg))
}

func (msg *MsgBan) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{msg.Signer}
}
