# BIP-341 wallet test vectors

`bip341-wallet-test-vectors.json` is an unmodified copy of
`bip-0341/wallet-test-vectors.json` from https://github.com/bitcoin/bips,
fetched on 2026-10-12.

- SHA-256: `403e19fb81dd1f31e745699216308f61fb403774b2aafa87b631b8f7c042d37f`
- License: BIP-341 is published under the BSD-3-Clause license (see the BIP header).

The vectors are Bitcoin-specific only in their `bc` address prefix. Taproot
tweaking, output scripts and the BIP-341 signature hash are identical on
Litecoin, so `taproot_test.go` runs them against this package and re-encodes
the address vectors with the `bc` HRP for comparison.
