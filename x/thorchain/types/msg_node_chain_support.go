package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgAddNodeChainSupport{}
	_ sdk.HasValidateBasic = &MsgAddNodeChainSupport{}
	_ sdk.LegacyMsg        = &MsgAddNodeChainSupport{}
)

func NewMsgAddNodeChainSupport(chain common.Chain, signer cosmos.AccAddress) *MsgAddNodeChainSupport {
	return &MsgAddNodeChainSupport{
		Chain:  chain,
		Signer: signer,
	}
}

func (m *MsgAddNodeChainSupport) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.Chain.IsEmpty() {
		return cosmos.ErrUnknownRequest("chain can't be empty")
	}
	return nil
}

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
