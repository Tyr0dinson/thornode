# THORChain Monero (XMR) Integration — Security Audit Scope

Prepared 2026-07-09 and verified 2026-07-10 for audit-quote sizing
(Least Authority, https://leastauthority.com/).

Unless explicitly labeled as test LOC, counts use **production code only**: non-blank lines with
full-line `//` comments excluded, and test files/modules counted separately (see §4). Counts were
reproduced from these frozen sizing snapshots; neither revision is the final audit pin.

| Repo | Frozen sizing revision |
|---|---|
| thornode | [`aa0677b417ab6217dabfb5a0215319699f6152c2`](https://gitlab.com/thorchain/thornode/-/commit/aa0677b417ab6217dabfb5a0215319699f6152c2) |
| serai fork | [`4440559a67d94bd838bb5067dbe7df66481e2764`](https://gitlab.com/thorchain/devops/serai/-/commit/4440559a67d94bd838bb5067dbe7df66481e2764) |

> **Final audit target:** pin both repositories and recount the combined trees after the remaining
> work in [thornode !4811 — Expose XMR outbound transparency metadata](https://gitlab.com/thorchain/thornode/-/merge_requests/4811)
> and [thornode !4931](https://gitlab.com/thorchain/thornode/-/merge_requests/4931) is
> resolved. MR !4811 aggregates overlapping transparency, reliability, and churn work; include
> the corresponding signer-sidecar support (originating in
> [serai !9](https://gitlab.com/thorchain/devops/serai/-/merge_requests/9)), but do not add the
> MRs' LOC independently because that would double-count their overlap. Status checked
> 2026-07-10: thornode !4811 and !4931 were open; thornode !4934 and !4893 and serai !61 and !15
> were already merged. The previous “+400 LOC / under 5%” estimate is therefore retired.

---

## 1. Scope — thornode / Bifrost (Go)

### 1.1 Monero chain client — `bifrost/pkg/chainclients/monero/` (dedicated)

| File | Code LOC | Notes |
|---|---:|---|
| `client.go` | 2,345 | vault lifecycle, keysign via sidecar, broadcast, spent-ref apply/retry, consolidation, solvency reporting |
| `scanner.go` | 929 | block scanning, inbound observation, mempool, reorg + skipped-block handling, non-signer filtering |
| `vault_registration.go` | 90 | registers vaults with the sidecar (solo spend-key vs DKG membership) |
| `reorg_store.go` | 71 | persisted reorg tracking |
| `vault_address_store.go` | 55 | persisted vault-address cache |
| `vault_bootstrap.go` | 51 | single-member vault registration without DKG |
| `default_{mainnet,mocknet,testnet}.go` | 12 | network defaults |
| **Subtotal** | **3,553** | |

MR !4811 also adds `signed_tx_payload_store.go` (62 code LOC at reviewed head `ea3dba4383f5`) for
restart-safe signed-payload rebroadcast. It is in the final audit scope but excluded from the
frozen-snapshot subtotal above; the final tree must be recounted.

### 1.2 Sidecar HTTP boundary — `bifrost/pkg/chainclients/monero/sidecar/` (dedicated)

| File | Code LOC | Notes |
|---|---:|---|
| `client.go` | 560 | HTTP client for the Rust sidecar (DKG, sign, scanner, mark-spent) |
| `server.go` | 319 | local server exposing the FROST `Engine` surface (mirrors `Client`) |
| `types.go` | 199 | request/response types |
| `daemon_fallback.go` | 182 | optional direct-monerod fallback path |
| `adapter.go` | 161 | `FrostAdapter` bridging client to chain-client interfaces |
| `errors.go` | 33 | error taxonomy (retryable vs terminal) |
| **Subtotal** | **1,454** | |

### 1.3 Observer — spent-ref attestation import (dedicated)

| File | Code LOC | Notes |
|---|---:|---|
| `bifrost/observer/xmr_spent_refs.go` | 206 | authority checks for peer spent-ref sets, applied-latch, conflict tracking |

Shared observer files carrying XMR logic (`attestation_gossip.go`, `attestation_handlers_impl.go`,
`observe.go`, `generic_attestation.go`) are listed in §1.7.

### 1.4 FROST keygen/keysign glue — go-tss (dedicated)

| File | Code LOC | Notes |
|---|---:|---|
| `bifrost/tss/go-tss/tss/frost_keygen.go` | 1,124 | FROST DKG orchestration across the TSS p2p layer |
| `bifrost/tss/go-tss/tss/frost_keysign.go` | 1,264 | FROST signing coordination, canonical plan/session binding, and result quorum |
| `bifrost/tss/go-tss/tss/frost_transcript_verify.go` | 243 | round-transcript binding and equivocation checks |
| `bifrost/tss/go-tss/tss/frost_types.go` | 327 | message/state types |
| **Subtotal** | **2,958** | |

### 1.5 x/thorchain consensus layer (dedicated)

| File | Code LOC | Notes |
|---|---:|---|
| `x/thorchain/xmr_monovault.go` | 144 | monovault keygen scheduling/selection |
| `x/thorchain/keeper/v1/keeper_xmr_spent_refs.go` | 119 | consensus storage of spent-output refs |
| **Subtotal** | **263** | |

### 1.6 Common types (dedicated)

| File | Code LOC | Notes |
|---|---:|---|
| `common/xmr_address.go` | 434 | XMR address encode/decode/validation (base58, network prefixes, subaddresses) |

**Dedicated Go production total: 8,868 LOC**

### 1.7 XMR-specific changes inside shared files (partial-file scope)

Only the XMR/FROST functions and branch points in these otherwise shared files are in primary
scope. The previous lexical “XMR-term lines” counter was removed: it matched `frost` inside
`Bifrost`, missed Go CamelCase spent-ref identifiers, and counted unrelated EdDSA code. It was
therefore neither a reliable diff stat nor a defensible LOC floor. The final frozen tree should
carry a function/range-level manifest or a reviewed diff against its agreed baseline.

The minimum shared-file inventory at the sizing snapshot is:

- **Bifrost observation/signing/client:** `bifrost/signer/sign.go`,
  `bifrost/observer/{attestation_gossip,attestation_handlers_impl,observe,generic_attestation}.go`,
  `bifrost/thorclient/{thorchain,keysign,keys}.go`,
  `bifrost/thorclient/types/{tx_in,tx_out}.go`,
  `bifrost/pkg/chainclients/loadchains.go`,
  `bifrost/pkg/chainclients/shared/{types/types,signing/halt}.go`, and `cmd/bifrost/main.go`.
- **FROST transport/shared orchestration:** `bifrost/p2p/messages/{p2p_message,TssPhaseMessages}.go`,
  `bifrost/tss/recover_keyshares.go`, `bifrost/tss/keygen.go`,
  `bifrost/tss/go-tss/common/{types,tss_helper}.go`, and
  `bifrost/tss/go-tss/tss/{server,keygen,keysign,tss}.go`.
- **thornode consensus/keeper/query:** `x/thorchain/handler_tss.go`,
  `x/thorchain/handler_observed_tx_helpers.go`, `x/thorchain/handler_common_outbound.go`,
  `x/thorchain/handler.go`, `x/thorchain/helpers.go`,
  `x/thorchain/manager_{validator,network,slasher,txout}_current.go`,
  `x/thorchain/{querier,grpc_query}.go`, `x/thorchain/keeper/v1/keeper_vault.go`, and
  `x/thorchain/types/type_vault.go`, `x/thorchain/types/type_keygen.go`,
  `x/thorchain/types/msg_tss_pool.go`, `x/thorchain/types/msg_tss_pool_v2.go`, and
  `x/thorchain/types/tss_round_normalizer.go`.
- **Common/configuration:** `common/{address,chain,common,asset,pubkey,tx,type_observed_tx}.go`,
  `config/config.go`, and `constants/constants_mocknet.go`.

Additional small branch points exist in `x/thorchain/handler_add_liquidity.go`,
`x/thorchain/alias.go`, `x/thorchain/managers.go`, `x/thorchain/keeper/keeper.go`,
`x/thorchain/keeper/keeper_dummy.go`, `x/thorchain/keeper/v1/keeper.go`, and
`x/thorchain/keeper/v1/alias.go`.

**Provisional estimate of XMR-specific logic embedded in shared files: ≈ 1,500–2,000 LOC.**
This is a quote-sizing placeholder, not a measured final-tree count.

### 1.8 Protocol/API surface

The frozen snapshot has XMR/FROST surface in 12 `.proto` files (about 34 directly tagged schema
lines): `common.proto`, `msg_tss_pool.proto`, `query.proto`, `query_inbound_address.proto`,
`query_network.proto`, `query_outbound.proto`, `query_tx.proto`, `query_vault.proto`,
`type_keygen.proto`, `type_tss.proto`, `type_tx_out.proto`, and `type_vault.proto`. Together these
define `OutputRef`/`spent_output_refs`, XMR query routes/readiness, EdDSA/FROST vault and keyshare
fields, observed Monero height, and `XMRMonovaultKeygen`.

MR !4811 further carries `ObservedTx.xmr_tx_key` through `common.proto` and `query_tx.proto` and
adds `vault_pub_key_frost` to `query_outbound.proto`. Generated code (`*.pb.go`, gRPC gateway,
openapi) is excluded from all counts.

---

## 2. Scope — xmr-frost-signer sidecar (Rust, serai fork)

Whole crate: `xmr-frost-signer/`. Each source file's final top-level `#[cfg(test)]` module is
excluded here and counted in §4.

| File | Code LOC | Notes |
|---|---:|---|
| `build.rs` | 9 | embeds release/version metadata at build time |
| `main.rs` | 297 | process entry, router, task wiring |
| `config.rs` | 221 | configuration/env |
| `server.rs` | 71 | HTTP server/middleware |
| `auth.rs` | 55 | local API authentication |
| `error.rs` | 72 | `AppError` → HTTP mapping (retryable 404 vs terminal 400 semantics) |
| `types.rs` | 314 | API request/response types |
| `crypto.rs` | 97 | key/crypto helpers |
| `monero.rs` | 59 | daemon RPC helpers |
| `chain/mod.rs` | 84 | chain height/daemon status |
| `dkg/mod.rs` | 260 | FROST DKG (PedPoP) session handling |
| `dkg/state.rs` | 105 | DKG state persistence |
| `keys/mod.rs` | 222 | vault key management, backups |
| `scanner/mod.rs` | 829 | scanner API: register vault, sync, transfers, outputs, daemon-verified `mark_spent` with exact key-image coverage |
| `scanner/tracker.rs` | 1,656 | `VaultTracker`: sled-backed output/key-image tracking, spent-ref binding |
| `sign/mod.rs` | 1,108 | signing sessions: prepare (input selection/decoys), partial, combine, broadcast |
| `sign/state.rs` | 144 | signing session state |
| `sign/idempotency.rs` | 189 | idempotent session/replay handling |
| `sim.rs` | 532 | **mocknet-only** simulation hooks — optionally excluded |
| **Total** | **6,324** | **5,792 excluding `sim.rs`** |

---

## 3. Optional scope — vendored cryptography (serai fork, in-repo)

Serai workspace crates the sidecar links against. These are forked from upstream
`serai-dex/serai` (parts of which have prior third-party audit coverage upstream); a pragmatic
middle ground is auditing only the fork's diff vs upstream.

| Crate | Code LOC | Test LOC |
|---|---:|---:|
| `crypto/dkg` (+ `pedpop`, `dealer`, `recovery`) | 1,570 | 458 |
| `crypto/dleq` | 1,047 | 399 |
| `crypto/frost` (modular-frost, ed25519) | 810 | 672 |
| `crypto/dalek-ff-group` | 746 | — |
| `crypto/multiexp` | 410 | 198 |
| `crypto/schnorr` | 207 | 146 |
| `crypto/transcript` | 147 | 74 |
| `crypto/ciphersuite` | 69 | — |
| `common/std-shims` | 189 | — |
| `common/zalloc`, `common/env` | 46 | — |
| **Total** | **≈ 5,241** | **≈ 1,950** |

The sidecar links these crates directly (`transcript`, `dalek-ff-group`, `ciphersuite`, `dkg`/`pedpop`, `modular-frost`, `zalloc`, `env`) or transitively (`std-shims`, `dleq`, `multiexp`, `schnorr`). An earlier draft also listed `common/request` (307 LOC); it has no reverse dependency from the sidecar workspace and has been removed.

**External Monero dependency (outside the serai fork):** the sidecar also links `monero-wallet` and `monero-simple-request-rpc` from monero-oxide
(`https://github.com/monero-oxide/monero-oxide` @ `32e6b5fe5ba9e1ea3e68da882550005122a11d22`, feature `multisig`) — the CLSAG multisig, transaction
construction, and decoy-selection code that `sign/mod.rs` drives. It is an external upstream crate (not part of the serai fork) and is excluded from every
count above; the engagement should state explicitly whether this Monero cryptography is in or out of audit scope.

## 4. Test suites (context)

| Area | Test LOC |
|---|---:|
| Go — monero client (`*_test.go`, incl. e2e gates, security, keysign-failure, consolidation, spent-refs) | 5,954 |
| Go — monero/sidecar package | 1,032 |
| Go — observer XMR tests (spent-refs, committed catchup, outbound filter) | 646 |
| Go — FROST keygen + keysign (go-tss) | 1,705 |
| Go — x/thorchain + common XMR tests | 581 |
| **Go total** | **9,918** |
| Rust — inline `#[cfg(test)]` modules in the sidecar | **3,882** |
| **Both repositories** | **13,800** |

Beyond unit tests, `make test-simulation-cluster` exercises churn/migration end-to-end; its
default `TEST_SIMULATION_CLUSTER_STAGES` includes `xmr-post-churn`. Chainnet/stagenet compose
stacks also exist for live-network testing.

## 5. Sizing summary

| Bucket | Production LOC |
|---|---:|
| Go — dedicated XMR files (§1.1–1.6) | 8,868 |
| Go — XMR logic embedded in shared files (§1.7, provisional estimate) | 1,500–2,000 |
| Rust — xmr-frost-signer sidecar (§2) | 6,324 |
| **Primary scope** | **≈ 16,700–17,200** |
| Optional — vendored serai crypto (§3) | 5,241 |
| Tests, both repos (§4, context) | 13,800 |

## 6. Methodology & caveats

- LOC = non-blank lines with full-line `//` comments stripped; block comments are not stripped.
  Treat these as reproducible sizing counts rather than language-aware logical LOC. Reproduce with:
  `grep -c -v -E '^[[:space:]]*(//|$)' <file>`.
- Rust code/test split: every sidecar source file keeps its `#[cfg(test)]` module as the final
  top-level item (verified). Lines before that marker are production; lines from the marker onward
  are tests. `build.rs` has no inline test module and is counted as production.
- Shared-file scope in §1.7 is an inventory plus a provisional manual estimate. A final
  function/range-level manifest or reviewed baseline diff must replace that estimate when the
  audit commits are frozen.
- Snapshot totals exclude moving-MR additions such as !4811's `signed_tx_payload_store.go` and
  all growth in files that already exist. Re-run the counts on the final pinned trees; do not sum
  overlapping MR diffstats.
