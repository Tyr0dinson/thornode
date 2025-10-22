# ZEC Go Library

Low-level Zcash library for Go, providing comprehensive Zcash functionality through Rust FFI bindings. This library enables transparent and shielded transaction support with high performance and security.

## Overview

This package provides a comprehensive set of Zcash primitives and utilities, leveraging Rust's librustzcash through CGO/UniFFI bindings for optimal performance and security. It supports both transparent and shielded (Sapling/Orchard) transactions.

### Key Features

- **Full Zcash Protocol Support**: Transparent, Sapling, and Orchard transaction types
- **High Performance**: Rust-based cryptographic operations via FFI
- **Network Support**: Mainnet, Testnet, and Regtest networks
- **Account Management**: HD wallet derivation from mnemonics
- **Transaction Building**: Complete transaction construction and signing
- **Shielded Operations**: Private transaction support with witness building

## Installation

```go
import "gitlab.com/mayachain/mayanode/chain/zec/go/zec"
```

### Platform Support

This package includes CGO bindings that work on:

- Linux x64
- macOS x64/ARM64
- Windows x64 (manual build; the helper script supports Linux/macOS)

## Building the Rust library (features)

The Go package links against the Rust shared library in `lib/libzec.so` (or `.dylib` on macOS, `.dll` on Windows). Use the build helper in `chain/zec/build.sh` to produce the variant you need.

- Default build (full features):

```bash
cd chain/zec
# Default features are "uniffi shielded e2e" (runs E2E; Docker required)
./build.sh -t go
```

- Shielded-enabled build (Sapling/Orchard send/decrypt/witness available):

```bash
cd chain/zec
ZEC_FEATURES="uniffi shielded" ./build.sh -t go
```

Notes

- `ZEC_FEATURES` controls the Cargo features passed to `cargo build`. The default is `uniffi shielded e2e`.
- Include `shielded` to enable shielded functionality in the Rust crate; include `e2e` to run containerized E2E tests (implies `shielded`).
- The script copies the built shared library to `../../lib/libzec.so`, which is what the Go code links against via CGO settings in the project.

Feature behavior summary

- `uniffi` only: transparent flows; shielded functions are not compiled.
- `uniffi shielded`: transparent + shielded send/decrypt/witness available; no Docker E2E.
- `e2e` (implies `shielded`): same as above, and the helper runs Docker-backed E2E tests.

### Using with simulation (mocknet)

If you want simulation runs to use shielded functionality, build the shielded variant first (without E2E), then start simulation:

```bash
# 1) Build shielded variant of the library
cd chain/zec
ZEC_FEATURES="uniffi shielded" ./build.sh -t go

# 2) From the project root, run your simulation target
cd ../..  # back to repo root
make test-simulation
```

This ensures the simulation (which links `lib/sim/libzec.so`) uses the shielded-enabled library.

## Complete API Reference

### Core Functions

#### Library Initialization

```go
// Initialize the Zcash library (required before using any functions)
// Sets logging/config
func InitZec() error
```

#### Account Management

```go
// Derive Zcash account from BIP39 mnemonic
func DeriveAccountFromMnemonic(mnemonic string, network Network) (ZecAccount, error)

// Get output viewing key from vault public key
func GetOvk(vault []byte) ([]byte, error)
```

#### Address Validation

```go
// Validate any Zcash address for a specific network
func ValidateAddress(address string, network Network) error

// Check if address is a transparent address
func IsTransparentAddress(address string, network Network) bool

// Validate if address can be used as shielded source
func ValidateShieldedSource(address string, network Network) (bool, error)
```

#### Transaction Building

```go
// Build unsigned transaction with sighashes
func BuildPtx(vault []byte, ptx PartialTx, network Network) (PartialTx, error)

// Apply signatures to complete transaction
func ApplySignatures(vault []byte, ptx PartialTx, signatures [][]byte, network Network) ([]byte, error)

// Compute transaction ID from partial transaction
func ComputeTxid(vault []byte, ptx PartialTx, network Network) (string, error)
```

#### Note Decryption

```go
// Decrypt Sapling notes from transaction bytes
func DecryptNotesInTx(mnemonic string, network Network, txBytes []byte, minedHeight uint32) ([]DecryptedNote, error)

// Decrypt Orchard notes from transaction bytes
func DecryptOrchardNotesInTx(mnemonic string, network Network, txBytes []byte, minedHeight uint32) ([]OrchardDecryptedNote, error)
```

#### Witness Building

```go
// Build Sapling witness from blockchain data
func BuildWitnessFromBlocks(
    mnemonic string,
    network Network,
    diversifier []byte,
    value uint64,
    rseed []byte,
    zip212 bool,
    blocksTxs [][][]byte,
    branchIdAtAnchor BranchId,
    expectedRoot *[]byte,
) (WitnessParts, error)

// Build Orchard witness from blockchain data
func BuildOrchardWitnessFromBlocks(
    mnemonic string,
    network Network,
    diversifier []byte,
    value uint64,
    rseed []byte,
    rho []byte,
    cmx []byte,
    blocksTxs [][][]byte,
    branchIdAtAnchor BranchId,
    expectedRoot *[]byte,
) (OrchardWitnessParts, error)
```

#### Shielded Transactions

```go
// Send from Sapling shielded pool to transparent address
func SendFromSaplingToTransparent(
    mnemonic string,
    network Network,
    notesToSpend []SpendableNote,
    toTransparentAddress string,
    anchorBytes []byte,
    targetHeight uint32,
    memo *string,
) ([]byte, error)

// Send from Orchard shielded pool to transparent address
func SendFromOrchardToTransparent(
    mnemonic string,
    network Network,
    notesToSpend []OrchardSpendableNote,
    toTransparentAddress string,
    anchorBytes []byte,
    targetHeight uint32,
    memo *string,
) ([]byte, error)
```

### Utility Functions

```go
// Generate Zcash transparent address from Cosmos public key
func NewAddressWitnessPubKeyHash(pks string, params *NetworkParams) (string, error)

// Convert ZEC amount to zatoshis (big.Int)
func ZecToZats(zec float64) (*big.Int, error)

// Convert ZEC amount to zatoshis (cosmos.Uint)
func ZecToUint(zec float64) (cosmos.Uint, error)

// Calculate transaction fee based on input/output counts
func CalculateFee(inCount, outCount uint64) uint64

// Calculate transaction fee including memo overhead
func CalculateFeeWithMemo(inCount, outCount uint64, memo string) uint64
```

### Types

```go
// Network options
type Network uint
const (
    NetworkMain    Network = 1
    NetworkRegtest Network = 2
    NetworkTest    Network = 3
)

// Branch ID for network upgrades
type BranchId uint
const (
    BranchIdOverwinter BranchId = 1
    BranchIdSapling    BranchId = 2
    BranchIdBlossom    BranchId = 3
    BranchIdHeartwood  BranchId = 4
    BranchIdCanopy     BranchId = 5
    BranchIdNu5        BranchId = 6
)

// Zcash account with all address types
type ZecAccount struct {
    PrivateKey  []byte // 32-byte private key
    PublicKey   []byte // 33-byte compressed public key
    Index       uint32 // Account derivation index
    Transparent string // P2PKH transparent address
    Sapling     string // Sapling shielded address
    Orchard     string // Orchard shielded address
    Unified     string // Unified address
}

// UTXO for transaction inputs
type Utxo struct {
    Txid   string // Transaction ID
    Height uint32 // Block height
    Vout   uint32 // Output index
    Script string // Output script (hex)
    Value  uint64 // Amount in zatoshis
}

// Transaction output
type Output struct {
    Address string // Destination address
    Amount  uint64 // Amount in zatoshis
    Memo    string // Optional memo data
}

// Partial transaction for building/signing
type PartialTx struct {
    Height        uint32     // Current block height
    Txid          []byte     // Transaction ID (populated after building)
    Inputs        []Utxo     // Input UTXOs
    Outputs       []Output   // Transaction outputs
    Fee           uint64     // Transaction fee
    Sighashes     [][]byte   // Sighashes for signing (populated after building)
    ExpiryHeight  uint32     // Transaction expiry height
    Version       uint32     // Transaction version
}

// Decrypted Sapling note
type DecryptedNote struct {
    Diversifier []byte // 11-byte diversifier
    Value       uint64 // Note value in zatoshis
    Rseed       []byte // 32-byte randomness seed
    Zip212      bool   // ZIP-212 flag
}

// Decrypted Orchard note
type OrchardDecryptedNote struct {
    Diversifier []byte // 11-byte diversifier
    Value       uint64 // Note value in zatoshis
    Rseed       []byte // 32-byte randomness seed
    Rho         []byte // 32-byte rho value
    Cmx         []byte // 32-byte commitment
    AddrBytes   []byte // Address bytes
}

// Spendable Sapling note with witness
type SpendableNote struct {
    Diversifier []byte // 11-byte diversifier
    Value       uint64 // Note value in zatoshis
    Rseed       []byte // 32-byte randomness seed
    Zip212      bool   // ZIP-212 flag
    Witness     []byte // Merkle witness data
}

// Spendable Orchard note with witness
type OrchardSpendableNote struct {
    Diversifier []byte // 11-byte diversifier
    Value       uint64 // Note value in zatoshis
    Rseed       []byte // 32-byte randomness seed
    Rho         []byte // 32-byte rho value
    Witness     []byte // Merkle witness data
    Cmx         []byte // 32-byte commitment
    AddrBytes   []byte // Address bytes
}

// Witness components for Sapling
type WitnessParts struct {
    WitnessBytes []byte // Serialized witness
    AnchorBytes  []byte // Merkle tree anchor
}

// Witness components for Orchard
type OrchardWitnessParts struct {
    WitnessBytes []byte // Serialized witness
    AnchorBytes  []byte // Merkle tree anchor
}

// Network parameters
type NetworkParams struct {
    Name                        string
    Net                         Network
    Bech32mHRPs                 map[string]addressKind
    Base58Prefixes              map[string]addressKind
    P2PKHPrefix                 []byte
    P2SHPrefix                  []byte
    ShieldedSourceMarkerAddress string
}
```

### Error Handling

```go
// ZEC-specific error type
type ZecError struct {
    err error
}

// Error variants (use with errors.Is())
var ErrZecErrorGenericError = fmt.Errorf("ZecErrorGenericError")
var ErrZecErrorInvalidVaultPubkey = fmt.Errorf("ZecErrorInvalidVaultPubkey")
var ErrZecErrorInvalidAddress = fmt.Errorf("ZecErrorInvalidAddress")
var ErrZecErrorInitError = fmt.Errorf("ZecErrorInitError")
var ErrZecErrorInvalidMemo = fmt.Errorf("ZecErrorInvalidMemo")
var ErrZecErrorInvalidAmount = fmt.Errorf("ZecErrorInvalidAmount")
var ErrZecErrorInvalidOvk = fmt.Errorf("ZecErrorInvalidOvk")
var ErrZecErrorScanError = fmt.Errorf("ZecErrorScanError")
var ErrZecErrorNonTransparentRefundAddress = fmt.Errorf("ZecErrorNonTransparentRefundAddress")
```

## Usage Examples

### Basic Initialization and Account Derivation

```go
package main

import (
    "fmt"
    "log"

    "gitlab.com/mayachain/mayanode/chain/zec/go/zec"
)

func main() {
    // Initialize the library
    if err := zec.InitZec(); err != nil {
        log.Fatalf("Failed to initialize ZEC: %v", err)
    }

    // Derive account from mnemonic
    mnemonic := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
    account, err := zec.DeriveAccountFromMnemonic(mnemonic, zec.NetworkRegtest)
    if err != nil {
        log.Fatalf("Failed to derive account: %v", err)
    }

    fmt.Printf("Transparent: %s\n", account.Transparent)
    fmt.Printf("Sapling: %s\n", account.Sapling)
    fmt.Printf("Orchard: %s\n", account.Orchard)
}
```

### Building and Signing Transactions

```go
package main

import (
    "encoding/hex"
    "fmt"
    "log"

    "gitlab.com/mayachain/mayanode/chain/zec/go/zec"
)

func main() {
    // Initialize library
    if err := zec.InitZec(); err != nil {
        log.Fatal(err)
    }

    // Derive account
    account, err := zec.DeriveAccountFromMnemonic(
        "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about",
        zec.NetworkRegtest,
    )
    if err != nil {
        log.Fatal(err)
    }

    // Create partial transaction
    ptx := zec.PartialTx{
        Height: 1000,
        Inputs: []zec.Utxo{{
            Txid:   "abcd1234...",
            Vout:   0,
            Script: "76a914...",
            Value:  100000000,
            Height: 999,
        }},
        Outputs: []zec.Output{{
            Address: "tmXYZ...",
            Amount:  99950000,
            Memo:    "",
        }},
        Fee:          50000,
        ExpiryHeight: 0,
    }

    // Build transaction (populates sighashes)
    builtPtx, err := zec.BuildPtx(account.PublicKey, ptx, zec.NetworkRegtest)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Built transaction with %d sighashes\n", len(builtPtx.Sighashes))

    // Sign sighashes (external signing process)
    signatures := make([][]byte, len(builtPtx.Sighashes))
    for i, sighash := range builtPtx.Sighashes {
        // Sign sighash with ECDSA (implementation not shown)
        sig := signECDSA(account.PrivateKey, sighash)
        signatures[i] = sig
    }

    // Apply signatures
    finalTx, err := zec.ApplySignatures(account.PublicKey, builtPtx, signatures, zec.NetworkRegtest)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Final transaction: %s\n", hex.EncodeToString(finalTx))
}
```

### Shielded Transaction Flow

```go
package main

import (
    "encoding/hex"
    "fmt"
    "log"

    "gitlab.com/mayachain/mayanode/chain/zec/go/zec"
)

func main() {
    // Initialize and derive account
    if err := zec.InitZec(); err != nil {
        log.Fatal(err)
    }

    mnemonic := "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
    account, err := zec.DeriveAccountFromMnemonic(mnemonic, zec.NetworkRegtest)
    if err != nil {
        log.Fatal(err)
    }

    // Assume we have a shielding transaction's raw bytes
    shieldTxBytes, _ := hex.DecodeString("0400008085202f89...")
    anchorHeight := uint32(1500)

    // Decrypt received Sapling notes
    notes, err := zec.DecryptNotesInTx(mnemonic, zec.NetworkRegtest, shieldTxBytes, anchorHeight)
    if err != nil {
        log.Fatal(err)
    }

    if len(notes) == 0 {
        log.Fatal("No notes found")
    }

    note := notes[0]
    fmt.Printf("Decrypted note: %d zatoshis\n", note.Value)

    // Build witness from block data
    blocksTxs := [][][]byte{} // Fetch from blockchain
    witnessParts, err := zec.BuildWitnessFromBlocks(
        mnemonic,
        zec.NetworkRegtest,
        note.Diversifier,
        note.Value,
        note.Rseed,
        note.Zip212,
        blocksTxs,
        zec.BranchIdNu5,
        nil, // Let Rust derive anchor
    )
    if err != nil {
        log.Fatal(err)
    }

    // Create spendable note
    spendableNote := zec.SpendableNote{
        Diversifier: note.Diversifier,
        Value:       note.Value,
        Rseed:       note.Rseed,
        Zip212:      note.Zip212,
        Witness:     witnessParts.WitnessBytes,
    }

    // Send from Sapling to transparent
    targetHeight := anchorHeight + 50
    unshieldTx, err := zec.SendFromSaplingToTransparent(
        mnemonic,
        zec.NetworkRegtest,
        []zec.SpendableNote{spendableNote},
        account.Transparent,
        witnessParts.AnchorBytes,
        targetHeight,
        nil, // No memo
    )
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Unshield transaction: %s\n", hex.EncodeToString(unshieldTx))
}
```

### Fee Calculation

```go
package main

import (
    "fmt"

    "gitlab.com/mayachain/mayanode/chain/zec/go/zec"
)

func main() {
    // Calculate fee for transaction with 2 inputs, 2 outputs
    fee := zec.CalculateFee(2, 2)
    fmt.Printf("Base fee: %d zatoshis\n", fee)

    // Calculate fee including memo
    feeWithMemo := zec.CalculateFeeWithMemo(2, 2, "Hello Zcash!")
    fmt.Printf("Fee with memo: %d zatoshis\n", feeWithMemo)

    // Convert ZEC to zatoshis
    amount, err := zec.ZecToUint(0.001)
    if err != nil {
        panic(err)
    }
    fmt.Printf("0.001 ZEC = %s zatoshis\n", amount.String())
}
```

## Network Parameters

Pre-defined network parameters are available:

```go
// Mainnet parameters
var MainNetParams = zec.NetworkParams{
    Name: "MainNet",
    Net:  zec.NetworkMain,
    // ... (address prefixes, etc.)
}

// Regtest parameters
var RegtestNetParams = zec.NetworkParams{
    Name: "RegtestNet",
    Net:  zec.NetworkRegtest,
    // ... (address prefixes, etc.)
}
```

## Architecture

This library provides three levels of functionality:

1. **FFI Functions** - Direct Rust bindings for cryptographic operations
2. **Go Utilities** - Address generation, fee calculation, type conversions
3. **Network Support** - Multi-network compatibility with proper parameter handling

The library automatically handles:

- Cryptographic proving parameter loading
- Memory management for FFI calls
- Error propagation from Rust code
- Type marshaling between Go and Rust

## Constants

```go
const ZatoshisPerZEC int64 = 100000000    // 1 ZEC = 100M zatoshis
const BaseRelayFee = 10000                // Minimum relay fee (10k zats)
const AddMarginalFee = 5000               // Marginal fee per input/output (5k zats)
const PkhOutputSize = 34                  // Size of P2PKH output
```

## Platform Support

- ✅ Go 1.19+
- ✅ Linux (x64, ARM64)
- ✅ macOS (x64, ARM64)
- ✅ Windows (x64)

## License

MIT
