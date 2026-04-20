// Package dashutil provides Dash-specific network parameters and helpers
// built on top of btcsuite/btcd. Dash uses Bitcoin-compatible single-byte
// base58check address encoding, so its chain params can be expressed as
// btcd's chaincfg.Params with Dash-specific prefix bytes.
package dashutil

import "github.com/btcsuite/btcd/chaincfg"

// MainNetParams parameters for Dash. Address prefixes and network magic are
// derived from chainparams.cpp in dashpay/dash.
var MainNetParams = chaincfg.Params{
	Name:        "dash-mainnet",
	Net:         0xbd6b0cbf, // start string bf0c6bbd on wire (little-endian)
	DefaultPort: "9999",

	// Address encoding parameters
	PubKeyHashAddrID: 0x4c, // starts with 'X'
	ScriptHashAddrID: 0x10, // starts with '7'
	PrivateKeyID:     0xcc, // WIF private keys start with '7' or 'X'

	// Dash does not use SegWit. Leave Bech32HRPSegwit empty so callers can
	// branch on an empty string the same way they do for DOGE/BCH.
	Bech32HRPSegwit: "",

	// BIP32 HD key magic ids (match Bitcoin-compatible xprv/xpub).
	HDPrivateKeyID: [4]byte{0x02, 0xfe, 0x52, 0xf8},
	HDPublicKeyID:  [4]byte{0x02, 0xfe, 0x52, 0xcc},

	// SLIP-0044 coin type
	HDCoinType: 5,
}

// TestNetParams for Dash testnet. Regtest/devnet share the same address
// prefix bytes as testnet — distinguish between them at the dashd daemon
// config level, not at address level.
var TestNetParams = chaincfg.Params{
	Name:        "dash-testnet",
	Net:         0xffcae2ce,
	DefaultPort: "19999",

	PubKeyHashAddrID: 0x8c, // starts with 'y'
	ScriptHashAddrID: 0x13, // starts with '8' or '9'
	PrivateKeyID:     0xef,

	Bech32HRPSegwit: "",

	HDPrivateKeyID: [4]byte{0x04, 0x35, 0x83, 0x94},
	HDPublicKeyID:  [4]byte{0x04, 0x35, 0x87, 0xcf},

	HDCoinType: 1,
}

// RegressionNetParams for dashd -regtest. Shares address prefix bytes with
// testnet so addresses round-trip across both; port and magic differ.
var RegressionNetParams = chaincfg.Params{
	Name:        "dash-regtest",
	Net:         0xdcb7c1fc,
	DefaultPort: "19899",

	PubKeyHashAddrID: 0x8c,
	ScriptHashAddrID: 0x13,
	PrivateKeyID:     0xef,

	Bech32HRPSegwit: "",

	HDPrivateKeyID: [4]byte{0x04, 0x35, 0x83, 0x94},
	HDPublicKeyID:  [4]byte{0x04, 0x35, 0x87, 0xcf},

	HDCoinType: 1,
}

func init() {
	// Register with btcd's chaincfg so DecodeAddress can look up params by
	// the prefix bytes. Ignore errors — they only occur on duplicate
	// registration which is fine if the package is imported multiple times.
	_ = chaincfg.Register(&MainNetParams)
	_ = chaincfg.Register(&TestNetParams)
	_ = chaincfg.Register(&RegressionNetParams)
}
