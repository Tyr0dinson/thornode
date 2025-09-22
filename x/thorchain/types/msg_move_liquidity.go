package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgMoveLiquidity{}
	_ sdk.HasValidateBasic = &MsgMoveLiquidity{}
	_ sdk.LegacyMsg        = &MsgMoveLiquidity{}
)

// NewMsgMoveLiquidity is a constructor function for MsgMoveLiquidity
func NewMsgMoveLiquidity(tx common.Tx, asset common.Asset, amount cosmos.Uint, newRuneAddr, newAssetAddr common.Address, signer cosmos.AccAddress) *MsgMoveLiquidity {
	return &MsgMoveLiquidity{
		TxIn:            tx,
		Asset:           asset,
		Amount:          amount,
		NewAssetAddress: newAssetAddr,
		NewRuneAddress:  newRuneAddr,
		Signer:          signer,
	}
}

// ValidateBasic runs stateless checks on the message
func (m *MsgMoveLiquidity) ValidateBasic() error {
	// here we can't call m.TxIn.Valid , because we allow user to send rebond request without any coins in it
	// m.TxIn.Valid will reject this kind request , which result rebond to fail
	if m.TxIn.ID.IsEmpty() {
		return cosmos.ErrUnknownRequest("tx id cannot be empty")
	}
	if m.TxIn.FromAddress.IsEmpty() {
		return cosmos.ErrInvalidAddress("tx from address cannot be empty")
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest("asset cannot be empty")
	}
	// There is no dedicate pool for RUNE, because every pool will have RUNE, that's by design
	if m.Asset.IsRune() {
		return cosmos.ErrUnknownRequest("asset cannot be rune")
	}
	if !m.Asset.Equals(m.Asset.GetLayer1Asset()) {
		return cosmos.ErrUnknownRequest("asset is no L1 asset")
	}
	if !m.NewRuneAddress.IsEmpty() && !m.NewRuneAddress.IsChain(common.THORChain) {
		return cosmos.ErrUnknownRequest("rune address does not match THORChain")
	}
	if !m.NewAssetAddress.IsEmpty() && !m.NewAssetAddress.IsChain(m.Asset.GetChain()) {
		return cosmos.ErrUnknownRequest("asset address does not match asset chain")
	}
	if m.NewRuneAddress.IsEmpty() && m.NewAssetAddress.IsEmpty() {
		return cosmos.ErrUnknownRequest("no address provided")
	}
	return nil
}

// GetSigners defines whose signature is required
func (m *MsgMoveLiquidity) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
