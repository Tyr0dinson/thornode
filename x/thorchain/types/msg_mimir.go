package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	
	"google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/api/types"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

var (
	_ sdk.Msg = &MsgSend{}
	_ sdk.HasValidateBasic = &MsgSend{}
	_ sdk.LegacyMsg = &MsgSend{}
)

// NewMsgMimir is a constructor function for MsgMimir
func NewMsgMimir(key string, value int64, signer cosmos.AccAddress) *MsgMimir {
	return &MsgMimir{
		Key:    key,
		Value:  value,
		Signer: signer,
	}
}

// ValidateBasic implements HasValidateBasic
// ValidateBasic is now ran in the message service router handler for messages that
// used to be routed using the external handler and only when HasValidateBasic is implemented. 
// No versioning is used there.
// It does not need to be executed twice, so it is removed from the handler's validate.
// Alternatively, we can not implement HasValidateBasic and perform this validation
// directly in the msg server / handler. The sdk prefers this...
func (m *MsgMimir) ValidateBasic() error {
	if m.Key == "" {
		return cosmos.ErrUnknownRequest("key cannot be empty")
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	return nil
}

// GetSigners defines whose signature is required
func (m *MsgMimir) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

func MsgMimirCustomGetSigners(m proto.Message) ([][]byte, error) {
	return [][]byte{m.(*types.MsgMimir).Signer}, nil
}
