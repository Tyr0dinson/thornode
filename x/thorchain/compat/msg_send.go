package compat

import (
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// NewMsgSend - construct a msg to send coins from one account to another.
func NewMsgSend(fromAddr, toAddr string, amount cosmos.Coins) *MsgSend {
	return &MsgSend{FromAddress: fromAddr, ToAddress: toAddr, Amount: amount}
}

// Route Implements Msg.
func (m *MsgSend) Route() string { return RouterKey }

// Type Implements Msg.
func (m MsgSend) Type() string { return "send" }

// ValidateBasic Implements Msg.
func (m *MsgSend) ValidateBasic() error {
	if _, err := cosmos.AccAddressFromBech32(m.FromAddress); err != nil {
		return cosmos.ErrInvalidAddress(m.FromAddress)
	}

	if _, err := cosmos.AccAddressFromBech32(m.ToAddress); err != nil {
		return cosmos.ErrInvalidAddress(m.ToAddress)
	}

	if !m.Amount.IsValid() {
		return cosmos.ErrInvalidCoins("coins must be valid")
	}

	if !m.Amount.IsAllPositive() {
		return cosmos.ErrInvalidCoins("coins must be positive")
	}

	return nil
}

// GetSignBytes Implements Msg.
func (m *MsgSend) GetSignBytes() []byte {
	return cosmos.MustSortJSON(ModuleCdc.MustMarshalJSON(m))
}

// GetSigners Implements Msg.
func (m *MsgSend) GetSigners() []cosmos.AccAddress {
	add, err := cosmos.AccAddressFromBech32(m.FromAddress)
	if err == nil {
		return []cosmos.AccAddress{add}
	}

	return []cosmos.AccAddress{}
}
