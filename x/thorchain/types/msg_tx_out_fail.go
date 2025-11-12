package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"

	apitypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgTxOutFail{}
	_ sdk.HasValidateBasic = &MsgTxOutFail{}
)

// NewMsgTxOutFail constructs a new MsgTxOutFail instance.
func NewMsgTxOutFail(height int64, index uint64, inHash common.TxID, fatal bool, reason string, signer cosmos.AccAddress) *MsgTxOutFail {
	return &MsgTxOutFail{
		Height:     height,
		Index:      index,
		InHash:     inHash,
		Fatal:      fatal,
		FailReason: reason,
		Signer:     signer,
	}
}

// ValidateBasic performs stateless validation.
func (m *MsgTxOutFail) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress("signer cannot be empty")
	}
	if m.Height <= 0 {
		return cosmos.ErrUnknownRequest("height must be positive")
	}
	if m.FailReason == "" {
		return cosmos.ErrUnknownRequest("fail reason cannot be empty")
	}
	if m.InHash.IsEmpty() {
		return cosmos.ErrUnknownRequest("in hash cannot be empty")
	}
	if !m.Fatal {
		return cosmos.ErrUnknownRequest("fatal flag must be true for TxOut failures")
	}
	return nil
}

// GetSigners returns the required signer.
func (m *MsgTxOutFail) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

func MsgTxOutFailCustomGetSigners(msg proto.Message) ([][]byte, error) {
	m, ok := msg.(*apitypes.MsgTxOutFail)
	if !ok {
		return nil, fmt.Errorf("can't cast as MsgTxOutFail: %T", msg)
	}
	return [][]byte{m.Signer}, nil
}
