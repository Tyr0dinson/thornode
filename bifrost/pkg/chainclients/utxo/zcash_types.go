package utxo

import zectxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/zec-txscript"

// Re-export types from zec-txscript to maintain backward compatibility
type (
	Network       = zectxscript.Network
	NetworkParams = zectxscript.NetworkParams
)

// Re-export constants
const (
	NetworkMain    = zectxscript.NetworkMain
	NetworkRegtest = zectxscript.NetworkRegtest
	NetworkTest    = zectxscript.NetworkTest
)

// MainNetParams defines the Zcash mainnet parameters
var MainNetParams = zectxscript.NetworkParams{
	Name: "MainNet",
	Net:  zectxscript.NetworkMain,
	// "t1" - 0x1C, 0xB8 for Mainnet P2PKH prefix
	P2PKHPrefix: []byte{0x1C, 0xB8},
	// "t3" - 0x1C, 0xBD for Mainnet P2SH prefix
	P2SHPrefix: []byte{0x1C, 0xBD},
	// used as a marker for txs that have shielded source
	ShieldedSourceMarkerAddress: "t1RDCnMNpvVMfngtFkEYq38LxD9c7cQHGch",
}

// RegtestNetParams defines the Zcash regtest/testnet parameters
var RegtestNetParams = zectxscript.NetworkParams{
	Name: "RegtestNet",
	Net:  zectxscript.NetworkRegtest,
	// "tm" - 0x1D, 0x25 for Testnet/Regtest P2PKH prefix
	P2PKHPrefix: []byte{0x1D, 0x25},
	// "tn" - 0x1C, 0xBA for Testnet/Regtest P2SH prefix
	P2SHPrefix: []byte{0x1C, 0xBA},
	// used as a marker for txs that have shielded source
	ShieldedSourceMarkerAddress: "tmF1xjfhsSzhy55dmhorzTnKjtHhZmPKzts",
}
