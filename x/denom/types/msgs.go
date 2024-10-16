package types

import (
	"cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// constants
const (
	TypeMsgCreateDenom      = "create_denom"
	TypeMsgMintTokens       = "mint"
	TypeMsgBurnTokens       = "burn"
	TypeMsgForceTransfer    = "force_transfer"
	TypeMsgChangeDenomAdmin = "change_admin"
)

var (
	_ sdk.Msg = &MsgCreateDenom{}
)

// NewMsgCreateDenom creates a msg to create a new denom
func NewMsgCreateDenom(sender, id string) *MsgCreateDenom {
	return &MsgCreateDenom{
		Sender: sender,
		Id:     id,
	}
}

func (m MsgCreateDenom) Route() string { return RouterKey }
func (m MsgCreateDenom) Type() string  { return TypeMsgCreateDenom }
func (m MsgCreateDenom) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(m.Sender)
	if err != nil {
		return errors.Wrapf(sdkerrors.ErrInvalidAddress, "Invalid sender address (%s)", err)
	}

	_, err = GetTokenDenom(m.Sender, m.Id)
	if err != nil {
		return errors.Wrap(ErrInvalidDenom, err.Error())
	}

	return nil
}

func (m MsgCreateDenom) GetSigners() []sdk.AccAddress {
	sender, _ := sdk.AccAddressFromBech32(m.Sender)
	return []sdk.AccAddress{sender}
}

var _ sdk.Msg = &MsgMintTokens{}

// NewMsgMintTokens creates a message to mint tokens
func NewMsgMintTokens(sender string, amount sdk.Coin, recipient string) *MsgMintTokens {
	return &MsgMintTokens{
		Sender:    sender,
		Amount:    amount,
		Recipient: recipient,
	}
}

func (m MsgMintTokens) Route() string { return RouterKey }
func (m MsgMintTokens) Type() string  { return TypeMsgMintTokens }
func (m MsgMintTokens) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(m.Sender)
	if err != nil {
		return errors.Wrapf(sdkerrors.ErrInvalidAddress, "Invalid sender address (%s)", err)
	}

	if !m.Amount.IsValid() {
		return errors.Wrap(sdkerrors.ErrInvalidCoins, m.Amount.String())
	}

	return nil
}

func (m MsgMintTokens) GetSigners() []sdk.AccAddress {
	sender, _ := sdk.AccAddressFromBech32(m.Sender)
	return []sdk.AccAddress{sender}
}

var _ sdk.Msg = &MsgBurnTokens{}

// NewMsgBurnTokens creates a message to burn tokens
func NewMsgBurnTokens(sender string, amount sdk.Coin) *MsgBurnTokens {
	return &MsgBurnTokens{
		Sender: sender,
		Amount: amount,
	}
}

func (m MsgBurnTokens) Route() string { return RouterKey }
func (m MsgBurnTokens) Type() string  { return TypeMsgBurnTokens }
func (m MsgBurnTokens) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(m.Sender)
	if err != nil {
		return errors.Wrapf(sdkerrors.ErrInvalidAddress, "Invalid sender address (%s)", err)
	}

	if !m.Amount.IsValid() {
		return errors.Wrap(sdkerrors.ErrInvalidCoins, m.Amount.String())
	}

	return nil
}

func (m MsgBurnTokens) GetSigners() []sdk.AccAddress {
	sender, _ := sdk.AccAddressFromBech32(m.Sender)
	return []sdk.AccAddress{sender}
}

var _ sdk.Msg = &MsgChangeDenomAdmin{}

// NewMsgChangeDenomAdmin creates a message to burn tokens
func NewMsgChangeDenomAdmin(sender, denom, newAdmin string) *MsgChangeDenomAdmin {
	return &MsgChangeDenomAdmin{
		Sender:   sender,
		Denom:    denom,
		NewAdmin: newAdmin,
	}
}

func (m MsgChangeDenomAdmin) Route() string { return RouterKey }
func (m MsgChangeDenomAdmin) Type() string  { return TypeMsgChangeDenomAdmin }
func (m MsgChangeDenomAdmin) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(m.Sender)
	if err != nil {
		return errors.Wrapf(sdkerrors.ErrInvalidAddress, "Invalid sender address (%s)", err)
	}

	_, err = sdk.AccAddressFromBech32(m.NewAdmin)
	if err != nil {
		return errors.Wrapf(sdkerrors.ErrInvalidAddress, "Invalid address (%s)", err)
	}

	_, _, err = DeconstructDenom(m.Denom)
	if err != nil {
		return err
	}

	return nil
}

func (m MsgChangeDenomAdmin) GetSigners() []sdk.AccAddress {
	sender, _ := sdk.AccAddressFromBech32(m.Sender)
	return []sdk.AccAddress{sender}
}
