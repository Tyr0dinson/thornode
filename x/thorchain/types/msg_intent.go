package types

import (
	"fmt"

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
		return cosmos.ErrUnknownRequest("target asset cannot be empty")
	}
	if err := m.Tx.Valid(); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if len(m.Tx.Coins) == 0 {
		return cosmos.ErrUnknownRequest("no coins in transaction")
	}
	if len(m.Tx.Coins) > 1 {
		return cosmos.ErrUnknownRequest("intent can only be created with one coin type")
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
	if !m.TargetAddress.IsChain(m.TargetAsset.GetChain()) {
		return cosmos.ErrUnknownRequest("target address is not the same chain as the target asset")
	}
	// Validate affiliate parameters
	if !m.AffiliateAddress.IsEmpty() && !m.AffiliateAddress.IsChain(common.THORChain) {
		return cosmos.ErrUnknownRequest("affiliate address must be a THOR address")
	}
	if m.AffiliateAddress.IsEmpty() && !m.AffiliateBasisPoints.IsZero() {
		return cosmos.ErrUnknownRequest("affiliate address cannot be empty while affiliate basis points is non-zero")
	}
	if !m.AffiliateBasisPoints.IsZero() && m.AffiliateBasisPoints.GT(cosmos.NewUint(MaxAffiliateFeeBasisPoints)) {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("affiliate fee basis points can't be more than %d", MaxAffiliateFeeBasisPoints))
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
func NewMsgIntentSolve(tx common.Tx, intentTxID common.TxID, destAddr common.Address, signer cosmos.AccAddress) *MsgIntentSolve {
	return &MsgIntentSolve{
		Tx:                 tx,
		IntentTxID:         intentTxID,
		DestinationAddress: destAddr,
		Signer:             signer,
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
	if len(m.Tx.Coins) == 0 {
		return cosmos.ErrUnknownRequest("no coins in transaction")
	}
	if len(m.Tx.Coins) > 1 {
		return cosmos.ErrUnknownRequest("solve can only provide one coin type")
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
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if m.IntentTxID.IsEmpty() {
		return cosmos.ErrUnknownRequest("intent tx id cannot be empty")
	}
	if len(m.Tx.Coins) == 0 {
		return cosmos.ErrUnknownRequest("no coins in bid transaction")
	}
	if len(m.Tx.Coins) > 1 {
		return cosmos.ErrUnknownRequest("bid can only provide one coin type")
	}
	return nil
}
