package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgWasmExec{}
	_ sdk.HasValidateBasic = &MsgWasmExec{}
	_ sdk.LegacyMsg        = &MsgWasmExec{}
)

// NewMsgWasmExec is a constructor function for MsgWasmExec
func NewMsgWasmExec(asset common.Asset, amount cosmos.Uint, contract, signer cosmos.AccAddress, tx common.Tx) *MsgWasmExec {
	return &MsgWasmExec{
		Tx:       tx,
		Asset:    asset,
		Amount:   amount,
		Contract: contract,
		Signer:   signer,
	}
}

// ValidateBasic runs stateless checks on the message
func (m *MsgWasmExec) ValidateBasic() error {
	if m.Asset.IsEmpty() {
		return cosmos.ErrUnknownRequest("asset cannot be empty")
	}
	if m.Asset.GetChain().IsTHORChain() {
		return cosmos.ErrUnknownRequest("asset cannot be THORChain asset")
	}
	if m.Amount.IsZero() {
		return cosmos.ErrUnknownRequest("amount cannot be zero")
	}
	if m.Contract.Empty() {
		return cosmos.ErrInvalidAddress(m.Contract.String())
	}
	if m.Signer.Empty() {
		return cosmos.ErrInvalidAddress(m.Signer.String())
	}
	if m.Tx.ID.IsEmpty() {
		return cosmos.ErrUnknownRequest("txID cannot be empty")
	}
	return nil
}

// GetSigners defines whose signature is required
func (m *MsgWasmExec) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
