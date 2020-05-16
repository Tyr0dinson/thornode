package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/common"
)

// MsgNoOp defines a no op message
type MsgNoOp struct {
	ObservedTx ObservedTx     `json:"observed_tx"`
	Signer     sdk.AccAddress `json:"signer"`
}

// NewMsgNoOp is a constructor function for MsgNoOp
func NewMsgNoOp(ObservedTx ObservedTx, signer sdk.AccAddress) MsgNoOp {
	return MsgNoOp{
		ObservedTx: ObservedTx,
		Signer:     signer,
	}
}

// Route should return the pooldata of the module
func (msg MsgNoOp) Route() string { return RouterKey }

// Type should return the action
func (msg MsgNoOp) Type() string { return "set_noop" }

// ValidateBasic runs stateless checks on the message
func (msg MsgNoOp) ValidateBasic() error {
	if err := msg.ObservedTx.Valid(); err != nil {
		return common.ErrInvalidCoins(err.Error())
	}
	if msg.Signer.Empty() {
		return common.ErrInvalidAddress(msg.Signer.String())
	}
	return nil
}

// GetSignBytes encodes the message for signing
func (msg MsgNoOp) GetSignBytes() []byte {
	return sdk.MustSortJSON(ModuleCdc.MustMarshalJSON(msg))
}

// GetSigners defines whose signature is required
func (msg MsgNoOp) GetSigners() []sdk.AccAddress {
	return []sdk.AccAddress{msg.Signer}
}
