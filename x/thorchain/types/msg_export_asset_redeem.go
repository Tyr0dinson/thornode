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
	address common.Address,
	signer cosmos.AccAddress,
) *MsgExportAssetRedeem {
	return &MsgExportAssetRedeem{
		Tx:      tx,
		Address: address,
		Signer:  signer,
	}
}

func (m *MsgExportAssetRedeem) ValidateBasic() error {
	if m.Address.IsEmpty() {
		return cosmos.ErrUnknownRequest("asset address cannot be empty")
	}
	if !m.Address.IsChain(common.THORChain) {
		return cosmos.ErrUnknownRequest("address must be thorchain")
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
