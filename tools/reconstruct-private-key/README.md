# Reconstruct Private Key Tool

This tool reconstructs a private key from multiple TSS (Threshold Signature Scheme) key shares.

## Purpose

In emergency situations where you need to recover assets controlled by a TSS vault, this tool allows you to combine multiple key shares to reconstruct the full private key. This should only be used when absolutely necessary, as it defeats the security model of threshold cryptography.

**Supports both ECDSA and EDDSA keys:**
- **ECDSA (secp256k1)**: Used for BTC, ETH, and most other chains
- **EDDSA (Ed25519)**: Used for Solana (SOL) addresses

## Requirements

- Multiple keyshare files (`localstate-*.json`) from different nodes
- At least `threshold + 1` shares (where threshold is calculated as `ceil(2/3 * participants) - 1`)
- All shares must be from the same keygen ceremony (same vault)

## Usage

### Basic Usage

```bash
go run tools/reconstruct-private-key/main.go -dir /path/to/keyshares
```

### Options

- `-dir <path>`: Directory containing keyshare files (default: current directory)
- `-output <file>`: Output file for private key (default: stdout)
- `-format <format>`: Output format - `hex`, `wif`, or `raw` (default: `hex`)

### Examples

1. **Reconstruct from keyshares in current directory:**
   ```bash
   go run tools/reconstruct-private-key/main.go
   ```

2. **Reconstruct and save to file:**
   ```bash
   go run tools/reconstruct-private-key/main.go -dir ./keyshares -output private-key.txt
   ```

3. **Reconstruct with specific format:**
   ```bash
   go run tools/reconstruct-private-key/main.go -format wif -output key.wif
   ```

## How It Works

1. **Load Keyshares**: The tool scans for all `localstate-*.json` files in the specified directory
2. **Validate**: Checks that all shares are from the same keygen (same participant set)
3. **Calculate Threshold**: Determines the minimum number of shares needed (threshold + 1)
4. **Verify Sufficiency**: Ensures you have enough shares to reconstruct the key
5. **Reconstruct**: Uses Lagrange interpolation to combine shares into the full private key
6. **Verify**: Derives the public key from the reconstructed private key and compares it with the expected public key
7. **Generate Addresses**: Generates addresses for all supported chains using the same public key

## Address Generation

**Important**: THORNode does NOT use different derivation paths for different chains. Instead:

- **Same Public Key**: All chains use the exact same public key (from the TSS keygen)
- **Chain-Specific Encoding**: Each chain uses its own address encoding format:
  - **BTC/LTC**: Bech32 (native segwit) addresses
  - **EVM Chains** (ETH, BSC, AVAX, BASE): Ethereum-style 0x addresses
  - **Cosmos Chains** (THOR, GAIA, NOBLE): Bech32 addresses with chain-specific prefixes
  - **UTXO Chains** (DOGE, BCH): Legacy address formats
  - **Other**: Chain-specific formats (XRP, SOL, TRON, ZEC)

The tool automatically generates addresses for **all supported chains** using the reconstructed public key. This matches exactly how THORNode vaults work - one public key, many chain addresses.

## Security Warnings

⚠️ **CRITICAL**: This tool reconstructs the full private key, which defeats the security model of TSS.

- **Never use in production** unless absolutely necessary for emergency recovery
- **Delete the private key file immediately** after use
- **Never share the reconstructed private key** with anyone
- **Use secure storage** if you must save the key temporarily
- **Consider the security implications** before using this tool

## File Format

The tool expects keyshare files in the format:
- Filename: `localstate-<pubkey>.json`
- Format: JSON containing `KeygenLocalState` structure

You can obtain these files using the `recover-keyshare-backup` tool or by copying them from node storage.

The tool automatically detects whether keyshares are **ECDSA** or **EDDSA** by examining the share data structure. Both types are supported:
- **ECDSA keyshares**: Contain `ECDSAPub` field
- **EDDSA keyshares**: Contain `EDDSAPub` field (used for Solana)

## Troubleshooting

### "Insufficient shares"
- You need at least `threshold + 1` shares
- Check that you have keyshare files from enough different nodes
- Verify all files are from the same vault/keygen

### "Public key mismatch"
- This indicates an error in reconstruction
- Verify all shares are from the same keygen ceremony
- Check that shares are not corrupted

### "Failed to load keyshare"
- Verify the file is valid JSON
- Check file permissions
- Ensure the file contains valid `LocalPartySaveData`

## Technical Details

The reconstruction uses Lagrange interpolation:
- Each share contains a value `Xi` (the private key share) and a `ShareID` (the party index)
- The private key is computed as: `sum(Xi * L_i(0)) mod n`
- Where `L_i(0)` is the Lagrange coefficient for party i at point 0
- The curve order `n` is used as the modulus
