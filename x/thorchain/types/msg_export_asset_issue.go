package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgExportAssetIssue{}
	_ sdk.HasValidateBasic = &MsgExportAssetIssue{}
	_ sdk.LegacyMsg        = &MsgExportAssetIssue{}
)

func NewMsgExportAssetIssue(
	tx common.Tx,
	exportAsset common.Asset,
	nativeAsset common.Asset,
	address common.Address,
	signer cosmos.AccAddress,
) *MsgExportAssetIssue {
	return &MsgExportAssetIssue{
		Tx:          tx,
		ExportAsset: exportAsset,
		NativeAsset: nativeAsset,
		Address:     address,
		Signer:      signer,
	}
}

func (m *MsgExportAssetIssue) ValidateBasic() error {
	if m.ExportAsset.IsEmpty() {
		return cosmos.ErrUnknownRequest("export asset cannot be empty")
	}
	if m.ExportAsset.IsNative() {
		return cosmos.ErrUnknownRequest("export asset cannot be native")
	}
	if m.NativeAsset.IsEmpty() {
		return cosmos.ErrUnknownRequest("native asset cannot be empty")
	}
	if !m.NativeAsset.IsNative() {
		return cosmos.ErrUnknownRequest("native asset must be native")
	}
	if m.Address.IsEmpty() {
		return cosmos.ErrUnknownRequest("asset address cannot be empty")
	}
	if !m.Address.IsChain(m.ExportAsset.GetLayer1Asset().GetChain()) {
		return cosmos.ErrUnknownRequest("asset address does not match asset chain")
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.Tx.ID.IsEmpty() {
		return cosmos.ErrUnknownRequest("txID cannot be empty")
	}
	return nil
}

func (m *MsgExportAssetIssue) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
