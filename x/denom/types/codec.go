package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"
)

func RegisterCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(&MsgCreateDenom{}, "gitlab.com/thorchain/thornode/denom/create-denom", nil)
	cdc.RegisterConcrete(&MsgMintTokens{}, "gitlab.com/thorchain/thornode/denom/mint-tokens", nil)
	cdc.RegisterConcrete(&MsgBurnTokens{}, "gitlab.com/thorchain/thornode/denom/burn-tokens", nil)
	cdc.RegisterConcrete(&MsgChangeDenomAdmin{}, "gitlab.com/thorchain/thornode/denom/change-admin", nil)
}

func RegisterInterfaces(registry cdctypes.InterfaceRegistry) {
	registry.RegisterImplementations(
		(*sdk.Msg)(nil),
		&MsgCreateDenom{},
		&MsgMintTokens{},
		&MsgBurnTokens{},
		&MsgChangeDenomAdmin{},
	)
	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
