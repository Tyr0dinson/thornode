# Bitcoin FROST Signing

## Release Scope

Bitcoin FROST adds a Taproot signing engine for `BTCChain`.

The production Mimir is `EnableFrostBTC`.

- `EnableFrostBTC=0` or absent: new BTC vaults use the existing secp256k1 engine.
- `EnableFrostBTC=1`: new BTC vaults use FROST.
- Existing vaults always keep signing with the engine stored in their local keygen state.

Only `BTCChain` uses this feature. DOGE, LTC, BCH, ZEC, EVM chains, THORChain, and Ed25519 paths continue using their existing signing/address logic.

## Build And Shipping

The native engine lives in `frost/rust/libgofrost` and wraps Zcash FROST `frost-secp256k1-tr` for BIP340/Taproot.

The Go wrapper lives in `frost/go-wrappers/go-frost`.

Native libraries are bundled under:

- `frost/go-wrappers/includes/linux/amd64`
- `frost/go-wrappers/includes/linux/arm64`
- `frost/go-wrappers/includes/darwin/arm64`

Docker packages the Linux shared library into the Bifrost runtime image.

## Integration

Bifrost reads `EnableFrostBTC` through Mimir. When enabled, BTC keygen requests the FROST engine and stores `signing_engine=frost` in local keygen state.

Keysign routing is based on stored vault state. For the BTC upgrade path, this means old BTC vaults continue using GG20 and new BTC vaults use FROST:

- `signing_engine=frost`: BTC FROST

Legacy GG20 vaults do not have a local engine descriptor. An absent `signing_engine` remains the legacy GG20 path.

Compared with GG20, the FROST engine keeps the same Bifrost-facing ceremony shape:

- same session request flow
- same signed P2P message envelope model
- same timeout and result broadcast model
- same blame output paths through `MsgTssPool` and `MsgTssKeysignFail`
- FROST message IDs are isolated with a `:frost` suffix

The BTC chain client uses BIP340 signatures for FROST vaults and builds Taproot key-path spends.

## BTC Address Migration

Before the upgrade, BTC vaults publish P2WPKH addresses and sign with GG20.

After `EnableFrostBTC=1`, the next BTC vault publishes a P2TR address and signs with FROST.

During the upgrade both vault types exist at the same time:

- old BTC vault: P2WPKH address, GG20 signing
- new BTC vault: P2TR address, FROST signing

Fund migration spends from the retiring P2WPKH vault into the new P2TR vault. Later FROST churns spend from P2TR to P2TR.

The address type is resolved from the vault signing engine, not from the current Mimir value. This keeps old BTC vaults spendable while new BTC vaults move to Taproot.

## Timing Results

7-node HCloud BTC regtest timing sample:

| Metric              |   Result |
| ------------------- | -------: |
| GG20 keysign        |   ~1.03s |
| GG20 keyshare size  | ~30.8 KB |
| FROST keygen        |   ~508ms |
| FROST keysign       |   ~508ms |
| FROST keyshare size |  ~2.5 KB |

Observed FROST keygen samples were 506-514ms. Observed FROST keysign samples were 505-509ms. Observed GG20 migration keysign samples were 1029-1035ms.

## Blame

FROST blame keeps the existing GG20-facing failure outputs:

- keygen timeout blames the joining node
- keysign timeout blames the leader
- malformed/auth/result faults blame the sender or broadcaster
- native identifiable FROST errors map the reported party index to the THORChain pubkey

The on-chain failure outputs stay unchanged: keygen failure uses `MsgTssPool`; keysign failure uses `MsgTssKeysignFail`.
