# Bitcoin FROST/Schnorr Signing

## Release Scope

Bitcoin FROST/Schnorr adds a Taproot signing engine for `BTCChain`.

The production Mimir is `EnableFrostBTC`.

- `EnableFrostBTC=0` or absent: new BTC vaults use the existing secp256k1 engine.
- `EnableFrostBTC=1`: new BTC vaults use FROST/Schnorr.
- Existing vaults always keep signing with the engine stored in their local keygen state.

Only `BTCChain` uses this feature. DOGE, LTC, BCH, ZEC, EVM chains, THORChain, and Ed25519 paths continue using their existing signing/address logic.

## Build And Shipping

The native engine lives in `schnorr/rust/libgoschnorr` and wraps Zcash FROST `frost-secp256k1-tr` for BIP340/Taproot.

The Go wrapper lives in `schnorr/go-wrappers/go-schnorr`.

Native libraries are bundled under:

- `schnorr/go-wrappers/includes/linux/amd64`
- `schnorr/go-wrappers/includes/linux/arm64`
- `schnorr/go-wrappers/includes/darwin/arm64`

Docker packages the Linux shared library into the Bifrost runtime image.

## Integration

Bifrost reads `EnableFrostBTC` through Mimir. When enabled, BTC keygen requests the Schnorr engine and stores `signing_engine=schnorr` in local keygen state.

Keysign routing is based on stored vault state. For the BTC upgrade path, this means old BTC vaults continue using GG20 and new BTC vaults use FROST/Schnorr:

- no `signing_engine`: GG20
- `signing_engine=gg20`: GG20
- `signing_engine=dkls`: DKLS
- `signing_engine=schnorr`: BTC FROST/Schnorr

Compared with GG20, the FROST/Schnorr engine keeps the same Bifrost-facing ceremony shape:

- same session request flow
- same signed P2P message envelope model
- same timeout and result broadcast model
- same blame output paths through `MsgTssPool` and `MsgTssKeysignFail`
- Schnorr message IDs are isolated with a `:schnorr` suffix

The BTC chain client uses BIP340 signatures for Schnorr vaults and builds Taproot key-path spends.

## BTC Address Migration

Before the upgrade, BTC vaults publish P2WPKH addresses and sign with GG20.

After `EnableFrostBTC=1`, the next BTC vault publishes a P2TR address and signs with FROST/Schnorr.

During the upgrade both vault types exist at the same time:

- old BTC vault: P2WPKH address, GG20 signing
- new BTC vault: P2TR address, FROST/Schnorr signing

Fund migration spends from the retiring P2WPKH vault into the new P2TR vault. Later FROST churns spend from P2TR to P2TR.

The address type is resolved from the vault signing engine, not from the current Mimir value. This keeps old BTC vaults spendable while new BTC vaults move to Taproot.

## Timing Results

7-node HCloud BTC regtest timing sample:

| Metric | Result |
| --- | ---: |
| GG20 keysign | ~1.03s |
| GG20 keyshare size | ~30.8 KB |
| FROST/Schnorr keygen | ~508ms |
| FROST/Schnorr keysign | ~508ms |
| FROST/Schnorr keyshare size | ~2.5 KB |

Observed Schnorr keygen samples were 506-514ms. Observed Schnorr keysign samples were 505-509ms. Observed GG20 migration keysign samples were 1029-1035ms.

## Blame

Schnorr blame keeps the existing GG20-facing failure outputs:

- keygen timeout blames the joining node
- keysign timeout blames the leader
- malformed/auth/result faults blame the sender or broadcaster
- native identifiable FROST errors map the reported party index to the THORChain pubkey

The on-chain failure outputs stay unchanged: keygen failure uses `MsgTssPool`; keysign failure uses `MsgTssKeysignFail`.
