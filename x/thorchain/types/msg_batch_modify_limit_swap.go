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
	_ sdk.Msg              = &MsgBatchModifyLimitSwap{}
	_ sdk.HasValidateBasic = &MsgBatchModifyLimitSwap{}
	_ sdk.LegacyMsg        = &MsgBatchModifyLimitSwap{}
)

// NewModifyLimitSwapOperation creates a new ModifyLimitSwapOperation
func NewModifyLimitSwapOperation(from common.Address, source, target common.Coin, modifiedTargetAmount cosmos.Uint) ModifyLimitSwapOperation {
	return ModifyLimitSwapOperation{
		From:                 from,
		Source:               source,
		Target:               target,
		ModifiedTargetAmount: modifiedTargetAmount,
	}
}

// NewMsgBatchModifyLimitSwap is a constructor function for MsgBatchModifyLimitSwap
func NewMsgBatchModifyLimitSwap(operations []ModifyLimitSwapOperation, signer cosmos.AccAddress) *MsgBatchModifyLimitSwap {
	return &MsgBatchModifyLimitSwap{
		Operations: operations,
		Signer:     signer,
	}
}

// ValidateBasic runs stateless checks on the message
func (m *MsgBatchModifyLimitSwap) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if len(m.Operations) == 0 {
		return cosmos.ErrUnknownRequest("no operations provided")
	}
	for i, op := range m.Operations {
		if err := op.Source.Valid(); err != nil {
			return cosmos.ErrUnknownRequest(fmt.Sprintf("operation %d: %s", i, err.Error()))
		}
		if err := op.Target.Valid(); err != nil {
			return cosmos.ErrUnknownRequest(fmt.Sprintf("operation %d: %s", i, err.Error()))
		}
		if op.Source.Asset.Equals(op.Target.Asset) {
			return cosmos.ErrUnknownRequest(fmt.Sprintf("operation %d: source and target assets cannot be the same", i))
		}
		if !op.From.IsChain(op.Source.Asset.GetChain()) {
			return cosmos.ErrUnknownRequest(fmt.Sprintf("operation %d: from address and source asset do not match", i))
		}
		// For native assets, verify from matches signer
		if op.Source.Asset.GetChain().IsTHORChain() {
			frm, err := op.From.AccAddress()
			if err != nil {
				return cosmos.ErrUnknownRequest(fmt.Sprintf("operation %d: %s", i, err.Error()))
			}
			if !frm.Equals(m.Signer) {
				return cosmos.ErrUnknownRequest(fmt.Sprintf("operation %d: from and signer address must match when source asset is native", i))
			}
		}
	}
	return nil
}

// GetSigners defines whose signature is required
func (m *MsgBatchModifyLimitSwap) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

func MsgBatchModifyLimitSwapCustomGetSigners(m proto.Message) ([][]byte, error) {
	msg, ok := m.(*types.MsgBatchModifyLimitSwap)
	if !ok {
		return nil, fmt.Errorf("can't cast as MsgBatchModifyLimitSwap: %T", m)
	}
	return [][]byte{msg.Signer}, nil
}
