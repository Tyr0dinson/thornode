package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgIntentOpen{}
	_ sdk.HasValidateBasic = &MsgIntentOpen{}
	_ sdk.LegacyMsg        = &MsgIntentOpen{}

	_ sdk.Msg              = &MsgIntentCancel{}
	_ sdk.HasValidateBasic = &MsgIntentCancel{}
	_ sdk.LegacyMsg        = &MsgIntentCancel{}

	_ sdk.Msg              = &MsgIntentSolve{}
	_ sdk.HasValidateBasic = &MsgIntentSolve{}
	_ sdk.LegacyMsg        = &MsgIntentSolve{}
)

// NewMsgIntentOpen is a constructor function for MsgIntentOpen
func NewMsgIntentOpen(tx common.Tx, targetAsset common.Asset, targetAddr common.Address, highTarget, lowTarget cosmos.Uint, affAddr common.Address, affPts cosmos.Uint, signer cosmos.AccAddress) *MsgIntentOpen {
	return &MsgIntentOpen{
		Tx:                   tx,
		TargetAsset:          targetAsset,
		TargetAddress:        targetAddr,
		HighTarget:           highTarget,
		LowTarget:            lowTarget,
		AffiliateAddress:     affAddr,
		AffiliateBasisPoints: affPts,
		Signer:               signer,
	}
}

// GetSigners returns the signers for the message
func (m *MsgIntentOpen) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

// ValidateBasic runs stateless checks on the message
func (m *MsgIntentOpen) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.TargetAsset.IsEmpty() {
		return cosmos.ErrUnknownRequest("target asset cannot be empty")
	}
	if err := m.Tx.Valid(); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if m.HighTarget.IsZero() {
		return cosmos.ErrUnknownRequest("high target cannot be zero")
	}
	if m.LowTarget.IsZero() {
		return cosmos.ErrUnknownRequest("low target cannot be zero")
	}
	if m.HighTarget.LT(m.LowTarget) {
		return cosmos.ErrUnknownRequest("high target must be greater than or equal to low target")
	}
	if m.TargetAddress.IsEmpty() {
		return cosmos.ErrUnknownRequest("target address cannot be empty")
	}
	return nil
}

// NewMsgIntentCancel is a constructor function for MsgIntentCancel
func NewMsgIntentCancel(tx common.Tx, intentTxID common.TxID, signer cosmos.AccAddress) *MsgIntentCancel {
	return &MsgIntentCancel{
		Tx:         tx,
		IntentTxID: intentTxID,
		Signer:     signer,
	}
}

// GetSigners returns the signers for the message
func (m *MsgIntentCancel) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

// ValidateBasic runs stateless checks on the message
func (m *MsgIntentCancel) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if err := m.Tx.Valid(); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if m.IntentTxID.IsEmpty() {
		return cosmos.ErrUnknownRequest("intent tx id cannot be empty")
	}
	return nil
}

// NewMsgIntentSolve is a constructor function for MsgIntentSolve
func NewMsgIntentSolve(tx common.Tx, intentTxID common.TxID, signer cosmos.AccAddress) *MsgIntentSolve {
	return &MsgIntentSolve{
		Tx:         tx,
		IntentTxID: intentTxID,
		Signer:     signer,
	}
}

// GetSigners returns the signers for the message
func (m *MsgIntentSolve) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

// ValidateBasic runs stateless checks on the message
func (m *MsgIntentSolve) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if err := m.Tx.Valid(); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if m.IntentTxID.IsEmpty() {
		return cosmos.ErrUnknownRequest("intent tx id cannot be empty")
	}
	return nil
}
