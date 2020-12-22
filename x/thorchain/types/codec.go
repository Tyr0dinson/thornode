package types

import (
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	"gitlab.com/thorchain/thornode/common/cosmos"
	// this line is used by starport scaffolding # 1
)

var (
	amino     = codec.NewLegacyAmino()
	ModuleCdc = codec.NewAminoCodec(amino)
)

func RegisterCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(MsgBan{}, "thorchain/MsgBan", nil)
	cdc.RegisterConcrete(MsgAddLiquidity{}, "thorchain/MsgAddLiquidity", nil)
}

func RegisterInterfaces(registry cdctypes.InterfaceRegistry) {
	registry.RegisterImplementations((*cosmos.Msg)(nil), &MsgBan{})
	registry.RegisterImplementations((*cosmos.Msg)(nil), &MsgAddLiquidity{})
}
