package types

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	se "github.com/cosmos/cosmos-sdk/types/errors"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
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

	_ sdk.Msg              = &MsgIntentAuctionBid{}
	_ sdk.HasValidateBasic = &MsgIntentAuctionBid{}
	_ sdk.LegacyMsg        = &MsgIntentAuctionBid{}
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
		return se.ErrInvalidRequest.Wrap("target asset cannot be empty")
	}
	if err := m.Tx.Valid(); err != nil {
		return se.ErrInvalidRequest.Wrap(err.Error())
	}
	if m.HighTarget.IsZero() {
		return se.ErrInvalidRequest.Wrap("high target cannot be zero")
	}
	if m.LowTarget.IsZero() {
		return se.ErrInvalidRequest.Wrap("low target cannot be zero")
	}
	if m.HighTarget.LT(m.LowTarget) {
		return se.ErrInvalidRequest.Wrap("high target must be greater than or equal to low target")
	}
	if m.TargetAddress.IsEmpty() {
		return se.ErrInvalidRequest.Wrap("target address cannot be empty")
	}
	// Validate affiliate parameters
	if m.AffiliateAddress.IsEmpty() && !m.AffiliateBasisPoints.IsZero() {
		return se.ErrInvalidRequest.Wrap("affiliate address cannot be empty while affiliate basis points is non-zero")
	}
	if !m.AffiliateBasisPoints.IsZero() && m.AffiliateBasisPoints.GT(cosmos.NewUint(constants.MaxBasisPts)) {
		return se.ErrInvalidRequest.Wrap(fmt.Sprintf("affiliate basis points cannot exceed %d", constants.MaxBasisPts))
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
		return se.ErrInvalidRequest.Wrap(err.Error())
	}
	if m.IntentTxID.IsEmpty() {
		return se.ErrInvalidRequest.Wrap("intent tx id cannot be empty")
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
		return se.ErrInvalidRequest.Wrap(err.Error())
	}
	if m.IntentTxID.IsEmpty() {
		return se.ErrInvalidRequest.Wrap("intent tx id cannot be empty")
	}
	return nil
}

// NewMsgIntentAuctionBid is a constructor function for MsgIntentAuctionBid
func NewMsgIntentAuctionBid(tx common.Tx, intentTxID common.TxID, signer cosmos.AccAddress) *MsgIntentAuctionBid {
	return &MsgIntentAuctionBid{
		Tx:         tx,
		IntentTxID: intentTxID,
		Signer:     signer,
	}
}

// GetSigners returns the signers for the message
func (m *MsgIntentAuctionBid) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

// ValidateBasic runs stateless checks on the message
func (m *MsgIntentAuctionBid) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if err := m.Tx.Valid(); err != nil {
		return se.ErrInvalidRequest.Wrap(err.Error())
	}
	if m.IntentTxID.IsEmpty() {
		return se.ErrInvalidRequest.Wrap("intent tx id cannot be empty")
	}
	return nil
}
