package types

import (
	"cosmossdk.io/math"
	"fmt"
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
	assetAmount math.Uint,
	gasAmount math.Uint,
	signer cosmos.AccAddress,
) *MsgExportAssetBind {
	return &MsgExportAssetBind{
		Tx:          tx,
		Asset:       asset,
		AssetAmount: assetAmount,
		GasAmount:   gasAmount,
		Signer:      signer,
	}
}

func (m *MsgExportAssetBind) ValidateBasic() error {
	if m.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest("asset cannot be empty")
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.Tx.ID.IsEmpty() {
		return cosmos.ErrUnknownRequest("txID cannot be empty")
	}
	if m.AssetAmount.IsZero() && m.GasAmount.IsZero() {
		return cosmos.ErrUnknownRequest("fee and asset amounts are zero")
	}

	assetStr := fmt.Sprintf("THOR.%s", m.Asset.Symbol)
	exportAsset, err := common.NewAsset(assetStr)
	if err != nil {
		return cosmos.ErrUnknownRequest(err.Error())
	}

	exports, found := common.AssetExports[exportAsset]
	if !found {
		return cosmos.ErrUnknownRequest("unknown asset")
	}

	_, found = exports[m.Tx.Chain]
	if !found && !m.Tx.Chain.Equals(common.THORChain) {
		return cosmos.ErrUnknownRequest("chain not supported")
	}

	feeCoin := m.Tx.Coins.GetCoin(m.Asset.Chain.GetGasAsset())
	if !feeCoin.Amount.Equal(m.GasAmount) {
		return cosmos.ErrUnknownRequest("fee coin and amount dont match")
	}

	assetCoin := m.Tx.Coins.GetCoin(m.Asset)
	if !assetCoin.Amount.Equal(m.AssetAmount) {
		return cosmos.ErrUnknownRequest("asset coin and amount dont match")
	}

	return nil
}

func (m *MsgExportAssetBind) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
