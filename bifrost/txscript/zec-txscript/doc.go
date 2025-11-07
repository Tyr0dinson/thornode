// Package txscript implements Zcash transparent transaction script processing.
// This package provides script signing and verification for Zcash transparent addresses.
// It follows the pattern of other UTXO chain txscript packages (BCH, LTC, DOGE).
//
// For transparent addresses, Zcash uses Bitcoin-compatible P2PKH and P2SH scripts,
// but with Zcash-specific address encoding using 2-byte version prefixes:
//   - Mainnet P2PKH: 0x1CB8 (t1xxx addresses)
//   - Mainnet P2SH:  0x1CBD (t3xxx addresses)
//   - Testnet P2PKH: 0x1D25 (tmxxx addresses)
//   - Testnet P2SH:  0x1CBA (tnxxx addresses)
//
// This minimal implementation includes:
//   - Address extraction from scriptPubKey with Zcash encoding
//   - ECDSA transaction signing with witness support
//   - Script construction utilities
//   - TSS (Threshold Signature Scheme) integration via Signable interface
package txscript
