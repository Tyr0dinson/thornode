package compat

import (
	"github.com/cosmos/cosmos-sdk/codec"
	cdctypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/types/msgservice"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

var (
	amino     = codec.NewLegacyAmino()
	ModuleCdc = codec.NewAminoCodec(amino)
)

func init() {
	RegisterCodec(amino)
}

// RegisterCodec register the msg types for amino
func RegisterCodec(cdc *codec.LegacyAmino) {
	cdc.RegisterConcrete(&MsgSend{}, "cosmos-sdk/MsgSend", nil)
}

// RegisterInterfaces register the types
func RegisterInterfaces(registry cdctypes.InterfaceRegistry) {
	registry.RegisterImplementations((*cosmos.Msg)(nil), &MsgSend{})

	// TODO: is this right?
	msgservice.RegisterMsgServiceDesc(registry, &_Msg_serviceDesc)
}
