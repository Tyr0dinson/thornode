package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

var (
	_ sdk.Msg              = &MsgYieldAccountDeposit{}
	_ sdk.HasValidateBasic = &MsgYieldAccountDeposit{}
	_ sdk.LegacyMsg        = &MsgYieldAccountDeposit{}
)

// NewMsgYieldAccountDeposit is a constructor function for MsgYieldAccountDeposit
func NewMsgYieldAccountDeposit(asset common.Asset, addr common.Address, amount cosmos.Uint, signer cosmos.AccAddress, tx common.Tx) *MsgYieldAccountDeposit {
	return &MsgYieldAccountDeposit{
		Tx:             tx,
		AccountAsset:   asset,
		AccountAddress: addr,
		DepositAmount:  amount,
		Signer:         signer,
	}
}

// ValidateBasic runs stateless checks on the message
func (m *MsgYieldAccountDeposit) ValidateBasic() error {
	if m.AccountAsset.IsEmpty() {
		return cosmos.ErrUnknownRequest("asset cannot be empty")
	}
	if m.DepositAmount.IsZero() {
		return cosmos.ErrUnknownRequest("amount cannot be zero")
	}
	if m.AccountAddress.IsEmpty() {
		return cosmos.ErrInvalidAddress(m.AccountAddress.String())
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
func (m *MsgYieldAccountDeposit) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{m.Signer}
}
