package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"gitlab.com/thorchain/thornode/common/cosmos"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
)

var (
	_ sdk.Msg              = &MsgSudoContract{}
	_ sdk.HasValidateBasic = &MsgSudoContract{}
	_ sdk.LegacyMsg        = &MsgSudoContract{}
)

type RawContractMessage = wasmtypes.RawContractMessage

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
