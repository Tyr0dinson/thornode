package types

import (
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgMintComposite is a constructor function for NewMsgMintComposite
func NewMsgMintComposite(coin common.Coin, id string, signer cosmos.AccAddress) *MsgMintComposite {
	return &MsgMintComposite{
		Coin:        coin,
		CompositeId: id,
		Signer:      signer,
	}
}

// Route should return the route key of the module
func (m *MsgMintComposite) Route() string { return RouterKey }

// Type should return the action
func (m MsgMintComposite) Type() string { return "mint_composite" }

// ValidateBasic runs stateless checks on the message
func (m *MsgMintComposite) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if !m.Coin.Asset.IsSyntheticAsset() {
		return cosmos.ErrUnknownRequest("can only deposit synthetic assets to composite")
	}
	if len(m.CompositeId) == 0 {
		return cosmos.ErrUnknownRequest("composite id cannot be empty")
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (m *MsgMintComposite) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners defines whose signature is required
func (m *MsgMintComposite) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}

// NewMsgBurnComposite is a constructor function for NewMsgBurnComposite
func NewMsgBurnComposite(coin common.Coin, signer cosmos.AccAddress) *MsgBurnComposite {
	return &MsgBurnComposite{
		Coin:   coin,
		Signer: signer,
	}
}

// Route should return the route key of the module
func (m *MsgBurnComposite) Route() string { return RouterKey }

// Type should return the action
func (m MsgBurnComposite) Type() string { return "burn_composite" }

// ValidateBasic runs stateless checks on the message
func (m *MsgBurnComposite) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if !m.Coin.Asset.IsCompositeAsset() {
		return cosmos.ErrUnknownRequest("can only burn composite assets to synthetics")
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (m *MsgBurnComposite) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners defines whose signature is required
func (m *MsgBurnComposite) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
