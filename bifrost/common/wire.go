package common

import (
	"github.com/tendermint/go-amino"
	"github.com/tendermint/tendermint/crypto"
	"github.com/tendermint/tendermint/crypto/secp256k1"
)

// cdc global variable
var Cdc = amino.NewCodec()

func RegisterCodec(cdc *amino.Codec) {
	cdc.RegisterInterface((*Tx)(nil), nil)
	cdc.RegisterInterface((*Msg)(nil), nil)
	cdc.RegisterInterface((*crypto.PubKey)(nil), nil)
	cdc.RegisterInterface((*Account)(nil), nil)

	cdc.RegisterConcrete(StdTx{}, "auth/StdTx", nil)
	cdc.RegisterConcrete(SendMsg{}, "cosmos-sdk/Send", nil)
	cdc.RegisterConcrete(&AppAccount{}, "bnbchain/Account", nil)
	cdc.RegisterConcrete(secp256k1.PubKeySecp256k1{}, "", nil)
}

func init() {
	RegisterCodec(Cdc)
}
