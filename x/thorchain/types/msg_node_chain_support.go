package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgAddNodeChainSupport{}
	_ sdk.HasValidateBasic = &MsgAddNodeChainSupport{}
	_ sdk.LegacyMsg        = &MsgAddNodeChainSupport{}
	_ sdk.Msg              = &MsgRemoveNodeChainSupport{}
	_ sdk.HasValidateBasic = &MsgRemoveNodeChainSupport{}
	_ sdk.LegacyMsg        = &MsgRemoveNodeChainSupport{}
)

// NewMsgAddNodeChainSupport is a constructor function for MsgAddNodeChainSupport
func NewMsgAddNodeChainSupport(chain string, signer cosmos.AccAddress) *MsgAddNodeChainSupport {
	return &MsgAddNodeChainSupport{
		Chain:  chain,
		Signer: signer,
	}
}

// ValidateBasic implements HasValidateBasic
func (m *MsgAddNodeChainSupport) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if len(m.Chain) == 0 {
		return fmt.Errorf("chain cannot be empty")
	}
	return nil
}

// GetSigners return all the signer who signed this message
func (m *MsgAddNodeChainSupport) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

func MsgAddNodeChainSupportCustomGetSigners(m proto.Message) ([][]byte, error) {
	msg, ok := m.(*types.MsgAddNodeChainSupport)
	if !ok {
		return nil, fmt.Errorf("can't cast as MsgAddNodeChainSupport: %T", m)
	}
	return [][]byte{msg.Signer}, nil
}

// NewMsgRemoveNodeChainSupport is a constructor function for MsgRemoveNodeChainSupport
func NewMsgRemoveNodeChainSupport(chain string, signer cosmos.AccAddress) *MsgRemoveNodeChainSupport {
	return &MsgRemoveNodeChainSupport{
		Chain:  chain,
		Signer: signer,
	}
}

// ValidateBasic implements HasValidateBasic
func (m *MsgRemoveNodeChainSupport) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if len(m.Chain) == 0 {
		return fmt.Errorf("chain cannot be empty")
	}
	return nil
}

// GetSigners return all the signer who signed this message
func (m *MsgRemoveNodeChainSupport) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

func MsgRemoveNodeChainSupportCustomGetSigners(m proto.Message) ([][]byte, error) {
	msg, ok := m.(*types.MsgRemoveNodeChainSupport)
	if !ok {
		return nil, fmt.Errorf("can't cast as MsgRemoveNodeChainSupport: %T", m)
	}
	return [][]byte{msg.Signer}, nil
}
