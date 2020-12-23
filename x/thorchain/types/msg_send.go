package types

import (
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgSend - construct a msg to send coins from one account to another.
func NewMsgSend(fromAddr, toAddr cosmos.AccAddress, amount cosmos.Coins) *MsgSend {
	return &MsgSend{FromAddress: fromAddr, ToAddress: toAddr, Amount: amount}
}

// Route Implements Msg.
func (msg MsgSend) Route() string { return RouterKey }

// Type Implements Msg.
func (msg MsgSend) Type() string { return "send" }

// ValidateBasic Implements Msg.
func (msg MsgSend) ValidateBasic() error {
	if err := cosmos.VerifyAddressFormat(msg.FromAddress); err != nil {
		return cosmos.ErrInvalidAddress(msg.FromAddress.String())
	}

	if err := cosmos.VerifyAddressFormat(msg.ToAddress); err != nil {
		return cosmos.ErrInvalidAddress(msg.ToAddress.String())
	}

	if !msg.Amount.IsValid() {
		return cosmos.ErrInvalidCoins("coins must be valid")
	}

	if !msg.Amount.IsAllPositive() {
		return cosmos.ErrInvalidCoins("coins must be positive")
	}

	return nil
}

// GetSignBytes Implements Msg.
func (msg MsgSend) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(&msg))
}

// GetSigners Implements Msg.
func (msg MsgSend) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{msg.FromAddress}
}
