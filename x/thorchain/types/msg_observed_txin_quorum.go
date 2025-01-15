package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgObservedTxInQuorum{}
	_ sdk.HasValidateBasic = &MsgObservedTxInQuorum{}
	_ sdk.LegacyMsg        = &MsgObservedTxInQuorum{}
)

// ValidateBasic implements HasValidateBasic
// ValidateBasic is now ran in the message service router handler for messages that
// used to be routed using the external handler and only when HasValidateBasic is implemented.
// No versioning is used there.
func (m *MsgObservedTxInQuorum) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if len(m.Txs) == 0 {
		return cosmos.ErrUnknownRequest("Txs cannot be empty")
	}
	for _, tx := range m.Txs {
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
		if tx.Status != Status_incomplete {
			return cosmos.ErrUnknownRequest("status must be incomplete")
		}
	}

	return nil
}

// GetSigners defines whose signature is required
func (m *MsgObservedTxInQuorum) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
