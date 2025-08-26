package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgExportAssetBind{}
	_ sdk.HasValidateBasic = &MsgExportAssetBind{}
	_ sdk.LegacyMsg        = &MsgExportAssetBind{}
)

func NewMsgExportAssetBind(
	tx common.Tx,
	asset common.Asset,
	amount cosmos.Uint,
	signer cosmos.AccAddress,
) *MsgExportAssetBind {
	return &MsgExportAssetBind{
		Tx:     tx,
		Asset:  asset,
		Amount: amount,
		Signer: signer,
	}
}

func (m *MsgExportAssetBind) ValidateBasic() error {
	if m.Tx.ID.IsEmpty() {
		return cosmos.ErrUnknownRequest("txID cannot be empty")
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest("asset cannot be empty")
	}

	if m.Amount.IsZero() {
		return cosmos.ErrUnknownRequest("fee and asset amounts are zero")
	}

	if !common.IsSupportedExportAsset(m.Asset) {
		return cosmos.ErrUnknownRequest("unsupported export asset")
	}

	return nil
}

func (m *MsgExportAssetBind) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
