package app

import (
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/std"
	"github.com/cosmos/cosmos-sdk/x/auth/legacy/legacytx"
)

type EncodingConfig struct {
	InterfaceRegistry types.InterfaceRegistry
	Marshaler         codec.Marshaler
	TxConfig          client.TxConfig
	Amino             *codec.LegacyAmino
}

// MakeEncodingConfig creates an EncodingConfig
func MakeEncodingConfig() EncodingConfig {
	cdc := codec.NewLegacyAmino()
	interfaceRegistry := types.NewInterfaceRegistry()
	marshaler := codec.NewAminoCodec(cdc)
	cfg := EncodingConfig{
		InterfaceRegistry: interfaceRegistry,
		Marshaler:         marshaler,
		TxConfig:          legacytx.StdTxConfig{Cdc: cdc},
		Amino:             cdc,
	}
	std.RegisterLegacyAminoCodec(cfg.Amino)
	std.RegisterInterfaces(cfg.InterfaceRegistry)
	ModuleBasics.RegisterLegacyAminoCodec(cfg.Amino)
	ModuleBasics.RegisterInterfaces(cfg.InterfaceRegistry)
	return cfg
}
