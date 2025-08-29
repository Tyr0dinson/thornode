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
	asset common.Asset,
	amount cosmos.Uint,
	address common.Address,
	signer cosmos.AccAddress,
) *MsgExportAssetIssue {
	return &MsgExportAssetIssue{
		Tx:      tx,
		Asset:   asset,
		Amount:  amount,
		Address: address,
		Signer:  signer,
	}
}

func (m *MsgExportAssetIssue) ValidateBasic() error {
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if err := m.Tx.Valid(); err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}
	if m.Tx.FromAddress.Equals(m.Tx.ToAddress) {
		return cosmos.ErrUnknownRequest("from and to address cannot be ")
	}
	if len(m.Tx.Coins) != 1 {
		return cosmos.ErrUnknownRequest("must provide exactly one coin")
	}
	if m.Tx.Coins.IsEmpty() {
		return cosmos.ErrUnknownRequest("export coin cannot be empty")
	}
	if !m.Tx.Coins[0].Asset.IsNative() {
		return cosmos.ErrUnknownRequest("must provide a native asset")
	}
	if !m.Tx.Coins[0].Asset.IsExportAsset() {
		return cosmos.ErrUnknownRequest("must provide an export asset")
	}
	if m.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest("issued asset cannot be empty")
	}
	if !m.Tx.Coins[0].Asset.HasAssetExport(m.Asset) {
		return cosmos.ErrUnknownRequest("asset is no valid export asset")
	}
	if m.Amount.IsZero() {
		return cosmos.ErrUnknownRequest("issued amount cannot be zero")
	}
	if m.Address.IsEmpty() {
		return cosmos.ErrUnknownRequest("address cannot be empty")
	}
	if !m.Address.IsChain(m.Asset.GetLayer1Asset().GetChain()) {
		return cosmos.ErrUnknownRequest("address does not match chain")
	}
	return nil
}

func (m *MsgExportAssetIssue) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
