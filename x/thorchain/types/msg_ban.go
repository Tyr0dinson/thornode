package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/api/types"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

var (
	_ sdk.Msg = &MsgBan{}
	_ sdk.HasValidateBasic = &MsgBan{}
	_ sdk.LegacyMsg = &MsgBan{}
)

// NewMsgBan is a constructor function for NewMsgBan
func NewMsgBan(addr, signer cosmos.AccAddress) *MsgBan {
	return &MsgBan{
		NodeAddress: addr,
		Signer:      signer,
	}
}

// ValidateBasic implements HasValidateBasic
// ValidateBasic is now ran in the message service router for messages that
// used to be routed using the external handler and only when HasValidateBasic is implemented. 
// No versioning is used there.
// It does not need to be executed twice, so it is removed from the handler's validate.
// Alternatively, we can not implement HasValidateBasic and perform this validation
// directly in the msg server / handler. The sdk prefers this...
func (m *MsgBan) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.NodeAddress.Empty() {
		return cosmos.ErrInvalidAddress(m.NodeAddress.String())
	}
	return nil
}

// GetSigners return all the signer who signed this message
// Implements LegacyMsg.
func (m *MsgBan) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

func MsgBanCustomGetSigners(m proto.Message) ([][]byte, error) {
	return [][]byte{m.(*types.MsgBan).Signer}, nil
}
