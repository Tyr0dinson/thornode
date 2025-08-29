package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgExportAssetRedeem{}
	_ sdk.HasValidateBasic = &MsgExportAssetRedeem{}
	_ sdk.LegacyMsg        = &MsgExportAssetRedeem{}
)

func NewMsgExportAssetRedeem(
	tx common.Tx,
	asset common.Asset,
	amount cosmos.Uint,
	address common.Address,
	signer cosmos.AccAddress,
) *MsgExportAssetRedeem {
	return &MsgExportAssetRedeem{
		Tx:      tx,
		Asset:   asset,
		Amount:  amount,
		Address: address,
		Signer:  signer,
	}
}

func (m *MsgExportAssetRedeem) ValidateBasic() error {
	if m.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest("redeemed asset cannot be empty")
	}
	if !m.Asset.IsNative() {
		return cosmos.ErrUnknownRequest("redeemed asset must be native")
	}
	if m.Amount.IsZero() {
		return cosmos.ErrUnknownRequest("redeemed amount cannot be zero")
	}
	if m.Address.IsEmpty() {
		return cosmos.ErrUnknownRequest("address cannot be empty")
	}
	if !m.Address.IsChain(m.Asset.GetLayer1Asset().GetChain()) {
		return cosmos.ErrUnknownRequest("address does not match asset chain")
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.Tx.ID.IsEmpty() {
		return cosmos.ErrUnknownRequest("txID cannot be empty")
	}
	return nil
}

func (m *MsgExportAssetRedeem) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
