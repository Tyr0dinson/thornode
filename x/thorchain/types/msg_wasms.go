package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/common/cosmos"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
)

var (
	_ sdk.Msg              = &MsgStoreCode{}
	_ sdk.HasValidateBasic = &MsgStoreCode{}
	_ sdk.LegacyMsg        = &MsgStoreCode{}
	_ sdk.Msg              = &MsgInstantiateContract{}
	_ sdk.HasValidateBasic = &MsgInstantiateContract{}
	_ sdk.LegacyMsg        = &MsgInstantiateContract{}
	_ sdk.Msg              = &MsgInstantiateContract2{}
	_ sdk.HasValidateBasic = &MsgInstantiateContract2{}
	_ sdk.LegacyMsg        = &MsgInstantiateContract2{}
	_ sdk.Msg              = &MsgMigrateContract{}
	_ sdk.HasValidateBasic = &MsgMigrateContract{}
	_ sdk.LegacyMsg        = &MsgMigrateContract{}
	_ sdk.Msg              = &MsgSudoContract{}
	_ sdk.HasValidateBasic = &MsgSudoContract{}
	_ sdk.LegacyMsg        = &MsgSudoContract{}
)

type RawContractMessage = wasmtypes.RawContractMessage

func (msg MsgStoreCode) Route() string {
	return RouterKey
}

func (msg MsgStoreCode) Type() string {
	return wasmtypes.MsgStoreCode{}.Type()
}

func (msg MsgStoreCode) ValidateBasic() error {
	return wasmtypes.MsgStoreCode{
		Sender:       msg.Sender,
		WASMByteCode: msg.WASMByteCode,
	}.ValidateBasic()
}

// GetSigners defines whose signature is required
func (m *MsgStoreCode) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{sdk.AccAddress(m.Sender)}
}

func (msg MsgInstantiateContract) Route() string {
	return RouterKey
}

func (msg MsgInstantiateContract) Type() string {
	return wasmtypes.MsgInstantiateContract{}.Type()
}

func (msg MsgInstantiateContract) ValidateBasic() error {
	return wasmtypes.MsgInstantiateContract{
		Sender: msg.Sender,
		Admin:  msg.Admin,
		CodeID: msg.CodeID,
		Label:  msg.Label,
		Msg:    msg.Msg,
		Funds:  msg.Funds,
	}.ValidateBasic()
}

func (m *MsgInstantiateContract) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{sdk.AccAddress(m.Sender)}
}

func (msg MsgInstantiateContract2) Route() string {
	return RouterKey
}

func (msg MsgInstantiateContract2) Type() string {
	return wasmtypes.MsgInstantiateContract2{}.Type()
}

func (msg MsgInstantiateContract2) ValidateBasic() error {
	return wasmtypes.MsgInstantiateContract2{
		Sender: msg.Sender,
		Admin:  msg.Admin,
		CodeID: msg.CodeID,
		Label:  msg.Label,
		Msg:    msg.Msg,
		Funds:  msg.Funds,
		Salt:   msg.Salt,
		FixMsg: msg.FixMsg,
	}.ValidateBasic()
}

func (m *MsgInstantiateContract2) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{sdk.AccAddress(m.Sender)}
}

func (msg MsgMigrateContract) Route() string {
	return RouterKey
}

func (msg MsgMigrateContract) Type() string {
	return wasmtypes.MsgMigrateContract{}.Type()
}

func (msg MsgMigrateContract) ValidateBasic() error {
	return wasmtypes.MsgMigrateContract{
		Sender:   msg.Sender,
		Contract: msg.Contract,
		CodeID:   msg.CodeID,
		Msg:      msg.Msg,
	}.ValidateBasic()
}

// GetSigners defines whose signature is required
func (m *MsgMigrateContract) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{sdk.AccAddress(m.Sender)}
}

func (msg MsgSudoContract) Route() string {
	return RouterKey
}

func (msg MsgSudoContract) Type() string {
	return wasmtypes.MsgSudoContract{}.Type()
}

func (msg MsgSudoContract) ValidateBasic() error {
	return wasmtypes.MsgSudoContract{
		Authority: msg.Sender,
		Contract:  msg.Contract,
		Msg:       msg.Msg,
	}.ValidateBasic()
}

func (m *MsgSudoContract) GetSigners() []cosmos.AccAddress {
	return []cosmos.AccAddress{sdk.AccAddress(m.Sender)}
}
