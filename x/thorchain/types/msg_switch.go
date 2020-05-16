package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/common"
)

// MsgSwitch defines a MsgSwitch message
type MsgSwitch struct {
	Tx          common.Tx      `json:"tx"`
	Destination common.Address `json:"destination"`
	Signer      sdk.AccAddress `json:"signer"`
}

// NewMsgSwitch is a constructor function for NewMsgSwitch
func NewMsgSwitch(tx common.Tx, addr common.Address, signer sdk.AccAddress) MsgSwitch {
	return MsgSwitch{
		Tx:          tx,
		Destination: addr,
		Signer:      signer,
	}
}

// Route should return the cmname of the module
func (msg MsgSwitch) Route() string { return RouterKey }

// Type should return the action
func (msg MsgSwitch) Type() string { return "switch" }

// ValidateBasic runs stateless checks on the message
func (msg MsgSwitch) ValidateBasic() error {
	if msg.Signer.Empty() {
		return common.ErrInvalidAddress(msg.Signer.String())
	}
	if msg.Destination.IsEmpty() {
		return common.ErrInvalidAddress(msg.Destination.String())
	}
	if err := msg.Tx.IsValid(); err != nil {
		return common.ErrUnknownRequest(err.Error())
	}
	// cannot be more or less than one coin
	if len(msg.Tx.Coins) != 1 {
		return common.ErrInvalidCoins("must be only one coin (rune)")
	}
	if !msg.Tx.Coins[0].Asset.IsRune() {
		return common.ErrInvalidCoins("must be rune")
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (msg MsgSwitch) GetSignBytes() []byte {
	return sdk.MustSortJSON(ModuleCdc.MustMarshalJSON(msg))
}

// GetSigners defines whose signature is required
func (msg MsgSwitch) GetSigners() []sdk.AccAddress {
	return []sdk.AccAddress{msg.Signer}
}
