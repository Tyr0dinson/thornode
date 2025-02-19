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
	_ sdk.Msg              = &MsgObservedTxOutQuorum{}
	_ sdk.HasValidateBasic = &MsgObservedTxOutQuorum{}
	_ sdk.LegacyMsg        = &MsgObservedTxOutQuorum{}
)

// NewMsgObservedTxOutQuorum is a constructor function for MsgObservedTxOutQuorum
func NewMsgObservedTxOutQuorum(tx *common.QuorumTx, signer cosmos.AccAddress) *MsgObservedTxOutQuorum {
	return &MsgObservedTxOutQuorum{
		Tx:     tx,
		Signer: signer,
	}
}

// ValidateBasic implements HasValidateBasic
// ValidateBasic is now ran in the message service router handler for messages that
// used to be routed using the external handler and only when HasValidateBasic is implemented.
// No versioning is used there.
func (m *MsgObservedTxOutQuorum) ValidateBasic() error {
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
	if !tx.Tx.FromAddress.Equals(obAddr) {
		return cosmos.ErrUnknownRequest("Request is not an outbound observed transaction")
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
func (m *MsgObservedTxOutQuorum) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

func MsgObservedTxOutQuorumCustomGetSigners(m proto.Message) ([][]byte, error) {
	msg, ok := m.(*types.MsgObservedTxOutQuorum)
	if !ok {
		return nil, fmt.Errorf("can't cast as MsgObservedTxOutQuorum: %T", m)
	}
	return [][]byte{msg.Signer}, nil
}
