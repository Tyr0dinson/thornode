package types

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgLpMint is a constructor function for MsgLpMint
func NewMsgLpMint(
	asset common.Asset,
	accAddr common.Address,
	amount cosmos.Uint,
	recipient, signer cosmos.AccAddress,
	txID common.TxID,
) *MsgLpMint {
	return &MsgLpMint{
		TxID:           txID,
		Asset:          asset,
		AccountAddress: accAddr,
		Amount:         amount,
		Recipient:      recipient,
		Signer:         signer,
	}
}

// Route should return the pooldata of the module
func (m *MsgLpMint) Route() string { return RouterKey }

// Type should return the action
func (m MsgLpMint) Type() string { return "lp_mint" }

// ValidateBasic runs stateless checks on the message
func (m *MsgLpMint) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest("pool asset cannot be empty")
	}
	if m.Asset.IsRune() {
		return cosmos.ErrUnknownRequest("asset cannot be rune")
	}
	if m.AccountAddress.IsEmpty() {
		return cosmos.ErrUnknownRequest("account address cannot be empty")
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (m *MsgLpMint) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners defines whose signature is required
func (m *MsgLpMint) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

// NewMsgLpRedeem is a constructor function for MsgLpRedeem
func NewMsgLpRedeem(
	asset common.Asset,
	accAddr common.Address,
	amount cosmos.Uint,
	signer cosmos.AccAddress,
	txID common.TxID,
) *MsgLpRedeem {
	return &MsgLpRedeem{
		AccountAddress: accAddr,
		TxID:           txID,
		Asset:          asset,
		Amount:         amount,
		Signer:         signer,
	}
}

// Route should return the pooldata of the module
func (m *MsgLpRedeem) Route() string { return RouterKey }

// Type should return the action
func (m MsgLpRedeem) Type() string { return "lp_redeem" }

// ValidateBasic runs stateless checks on the message
func (m *MsgLpRedeem) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest("pool asset cannot be empty")
	}
	// There is no dedicate pool for RUNE, because every pool will have RUNE, that's by design
	if m.Asset.IsRune() {
		return cosmos.ErrUnknownRequest("asset cannot be rune")
	}
	if m.AccountAddress.IsEmpty() {
		return cosmos.ErrUnknownRequest("account address cannot be empty")
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (m *MsgLpRedeem) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners defines whose signature is required
func (m *MsgLpRedeem) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
