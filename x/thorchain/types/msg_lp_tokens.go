package types

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgLpMint is a constructor function for MsgLpMint
func NewMsgLpMint(
	tx common.Tx,
	asset common.Asset,
	runeAddr, assetAddr common.Address,
	amount cosmos.Uint,
	recipient, signer cosmos.AccAddress,
) *MsgLpMint {
	return &MsgLpMint{
		Tx:        tx,
		Asset:     asset,
		Amount:    amount,
		Recipient: recipient,
		Signer:    signer,
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
	if err := m.Tx.Valid(); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	// There is no dedicate pool for RUNE, because every pool will have RUNE, that's by design
	if m.Asset.IsRune() {
		return cosmos.ErrUnknownRequest("asset cannot be rune")
	}
	if len(m.Tx.Coins) != 0 {
		return cosmos.ErrUnknownRequest("not expecting coins")
	}
	if m.RuneAddress.IsEmpty() && m.AssetAddress.IsEmpty() {
		return cosmos.ErrUnknownRequest("rune address and asset address cannot be empty")
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
