package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"

	"gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgObservedTxIn{}
	_ sdk.HasValidateBasic = &MsgObservedTxIn{}
	_ sdk.LegacyMsg        = &MsgObservedTxIn{}
)

// NewMsgObservedTxIn is a constructor function for MsgObservedTxIn
func NewMsgObservedTxInQuorum(tx *common.QuorumTx, signer cosmos.AccAddress) *MsgObservedTxInQuorum {
	return &MsgObservedTxInQuorum{
		Tx:     tx,
		Signer: signer,
	}
}

// ValidateBasic implements HasValidateBasic
// ValidateBasic is now ran in the message service router handler for messages that
// used to be routed using the external handler and only when HasValidateBasic is implemented.
// No versioning is used there.
func (m *MsgObservedTxInQuorum) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}

	tx := m.Tx.Tx

	if err := tx.Valid(); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	obAddr, err := tx.ObservedPubKey.GetAddress(tx.Tx.Coins[0].Asset.GetChain())
	if err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if !tx.Tx.ToAddress.Equals(obAddr) {
		return cosmos.ErrUnknownRequest("request is not an inbound observed transaction")
	}
	if len(tx.Signers) > 0 {
		return cosmos.ErrUnknownRequest("signers must be empty")
	}
	if len(tx.OutHashes) > 0 {
		return cosmos.ErrUnknownRequest("out hashes must be empty")
	}
	if tx.Status != common.Status_incomplete {
		return cosmos.ErrUnknownRequest("status must be incomplete")
	}

	return nil
}

// GetSigners defines whose signature is required
func (m *MsgObservedTxInQuorum) GetSigners() []cosmos.AccAddress {
	// if m.Signer == nil {
	// 	return nil
	// }
	return []cosmos.AccAddress{m.Signer}
}

func MsgObservedTxInQuorumCustomGetSigners(m proto.Message) ([][]byte, error) {
	msg, ok := m.(*types.MsgObservedTxInQuorum)
	if !ok {
		return nil, fmt.Errorf("can't cast as MsgObservedTxInQuorum: %T", m)
	}
	// if msg.Signer == nil {
	// 	return nil, nil
	// }
	return [][]byte{msg.Signer}, nil
}
