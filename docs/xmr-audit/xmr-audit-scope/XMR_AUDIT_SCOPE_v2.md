# THORChain Monero (XMR) Integration - Updated Security Audit Scope

Prepared and verified 2026-08-18 for the Least Authority audit engagement.

This document defines the audit target as the exact pair of merged commits in the table below.
Open post-3.20 work is documented separately in §7 and is **not** part of that target unless the
parties approve a written scope addendum.

## Audit target - frozen merged revisions

| Repository | Branch | Frozen audit commit | Commit date |
|---|---|---|---|
| thornode | `develop` | [`879b57f0112f6ffe76039f7cf0d4ae58f9bb9345`](https://gitlab.com/thorchain/thornode/-/commit/879b57f0112f6ffe76039f7cf0d4ae58f9bb9345) | 2026-08-17 |
| serai fork | `develop` | [`a61521bcfc09f50b5c7eefade24bf050717371e0`](https://gitlab.com/thorchain/devops/serai/-/commit/a61521bcfc09f50b5c7eefade24bf050717371e0) | 2026-08-11 |

The commit hashes, not moving branch names or container tags, define the audit target. Any
change to either tree after these commits requires a reviewed delta or a newly frozen target.

### Changes from the previously shared scope

Relative to the official v1, this version:

- freezes the current merged targets at `thornode@879b57f0112f6ffe76039f7cf0d4ae58f9bb9345`
  and `serai@a61521bcfc09f50b5c7eefade24bf050717371e0`
- includes the now-merged thornode !4811 and !4931 changes, together with the current signer-side
  implementation and hardening
- updates all file inventories and LOC figures from those frozen commits
- updates the pinned Monero dependency introduced through serai !69 and
- documents thornode !4983 and serai !70 separately as open post-baseline work that is not part
  of the frozen audit target.

---

## 1. Primary scope - thornode / Bifrost (Go)

Unless a row is explicitly marked as test context, the counts below are production code LOC:
non-blank lines with full-line `//` comments excluded. Generated files are excluded.

### 1.1 Monero chain client - `bifrost/pkg/chainclients/monero/`

All production `.go` files directly in this directory are in scope.

| File | Code LOC | Security-relevant responsibility |
|---|---:|---|
| `client.go` | 3,723 | vault lifecycle, keygen/keysign, outbound construction and recovery, broadcast, spent-ref reconciliation, consolidation and solvency |
| `scanner.go` | 2,583 | canonical block/receipt scanning, mempool handling, reorg and rewind behavior, non-signer filtering and observation delivery |
| `signed_tx_payload_store.go` | 1,941 | durable signed-outbound payload state, replay/rebroadcast and terminal cleanup |
| `reorg_store.go` | 841 | durable reorg/errata state and recovery ordering |
| `pending_internal_inbound_store.go` | 460 | durable pending internal inbound state and replay |
| `vault_registration.go` | 108 | sidecar vault enrollment and DKG membership |
| `vault_address_store.go` | 55 | persisted vault-address cache |
| `vault_bootstrap.go` | 51 | single-member/mocknet vault bootstrap |
| `default_{mainnet,mocknet,testnet}.go` | 12 | network defaults |
| **Subtotal** | **9,774** | |

### 1.2 Go sidecar boundary - `bifrost/pkg/chainclients/monero/sidecar/`

All production `.go` files in this directory are in scope.

| File | Code LOC | Security-relevant responsibility |
|---|---:|---|
| `client.go` | 1,437 | authenticated HTTP contract for DKG, signing, scanner state, output/spent-ref operations and recovery |
| `types.go` | 421 | request/response schema and capability fields |
| `server.go` | 319 | local FROST engine server surface |
| `adapter.go` | 209 | bridge between the chain client and FROST engine |
| `daemon_fallback.go` | 193 | optional direct-monerod fallback behavior |
| `errors.go` | 91 | retryable, terminal and contract error taxonomy |
| **Subtotal** | **2,670** | |

### 1.3 Observer spent-ref state and certificates

| File | Code LOC | Security-relevant responsibility |
|---|---:|---|
| `bifrost/observer/xmr_spent_refs.go` | 367 | authority, conflict and outcome handling for peer spent-ref imports |
| `bifrost/observer/xmr_spent_ref_certificates.go` | 786 | durable certificate construction, validation, persistence and replay |
| **Subtotal** | **1,153** | |

### 1.4 FROST DKG/keysign and evidence

| File | Code LOC | Security-relevant responsibility |
|---|---:|---|
| `bifrost/tss/go-tss/tss/frost_keygen.go` | 1,280 | distributed key generation orchestration, result consensus and recovery |
| `bifrost/tss/go-tss/tss/frost_keysign.go` | 1,958 | signing sessions, canonical plans, party/result consensus and recovery |
| `bifrost/tss/go-tss/tss/frost_signed_transcript.go` | 142 | authenticated signed-transcript representation |
| `bifrost/tss/go-tss/tss/frost_transcript_verify.go` | 311 | transcript/session binding and equivocation validation |
| `bifrost/tss/go-tss/tss/frost_types.go` | 353 | FROST message and state types |
| `bifrost/tss/go-tss/tss/frost_keygen_faultinject_{mainnet,mocknet}.go` | 21 | build-tagged production/mocknet fault-injection switch |
| `bifrost/p2p/messages/frost_evidence.go` | 225 | canonical FROST evidence encoding and validation |
| **Subtotal** | **4,290** | |

### 1.5 Consensus and common dedicated files

| File | Code LOC | Security-relevant responsibility |
|---|---:|---|
| `x/thorchain/xmr_monovault.go` | 135 | XMR-capable vault selection and monovault maintenance rules |
| `x/thorchain/keeper/v1/keeper_xmr_spent_refs.go` | 115 | consensus persistence of spent-output references |
| `common/xmr_address.go` | 434 | XMR address parsing, base58 encoding, network prefixes and subaddresses |
| **Subtotal** | **684** | |

**Dedicated thornode production total: 18,571 LOC.**

### 1.6 XMR/FROST logic in shared production files

The XMR/FROST functions, branches and adjacent invariants in the following shared files are in
primary scope. Reviewers may read the whole file for context unrelated chain implementations are
not intended as new audit scope. This inventory defines the shared-file scope no separate
partial-file LOC is assigned.

- **Observation, attestation and scanner persistence:**
  `bifrost/blockscanner/scanner_storage.go`,
  `bifrost/observer/{attestation_gossip,attestation_handlers,attestation_handlers_impl,generic_attestation,observe,observer_storage,peer_manager}.go`.
- **P2P and FROST transport/orchestration:**
  `bifrost/p2p/{communication,message_id_subscriber,party_coordinator}.go`,
  `bifrost/p2p/messages/{TssPhaseMessages,p2p_message}.go`,
  `bifrost/tss/{encrypt_keyshares,keysign_error,recover_keyshares}.go`,
  `bifrost/tss/go-tss/common/types.go`, and `bifrost/tss/go-tss/tss/server.go`.
- **Bifrost signing, chain loading and thornode client:**
  `bifrost/signer/{sign,thorchain_block_scanner}.go`,
  `bifrost/pkg/chainclients/loadchains.go`,
  `bifrost/pkg/chainclients/shared/{types/types,signing/halt}.go`,
  `bifrost/thorclient/{keysign,thorchain}.go`,
  `bifrost/thorclient/types/{errata_block,tx_in,tx_out}.go`, and `cmd/bifrost/main.go`.
- **Consensus, vault rotation, outbound and queries:**
  `x/thorchain/{grpc_query,handler_add_liquidity,handler_common_outbound,handler_observed_tx_helpers,handler_tss,handler_tss_keysign,helpers,manager_network_current,manager_slasher_current,manager_txout_current,manager_validator_current,migration_helpers,querier}.go`,
  `x/thorchain/keeper/{keeper,keeper_dummy}.go`,
  `x/thorchain/keeper/v1/{alias,keeper,keeper_vault}.go`, and
  `x/thorchain/types/{msg_observed_tx_quorum,msg_observed_txin,msg_observed_txout,msg_tss_pool,msg_tss_pool_v2,type_keygen,type_observed_tx,type_tx_out,type_vault}.go`.
- **Common/configuration:**
  `common/{address,asset,chain,common,pubkey,tx,type_observed_tx}.go`,
  `config/config.go`, and `constants/{constants,constants_mocknet}.go`.

Small aliases and manager wiring in `x/thorchain/{alias,managers}.go` are included where they
select XMR/FROST behavior.

### 1.7 Protocol and API surface

The frozen target has XMR/FROST fields or routes in these 12 source schemas:

`proto/thorchain/v1/common/common.proto` and
`proto/thorchain/v1/types/{msg_tss_pool,query,query_inbound_address,query_network,query_outbound,query_tx,query_vault,type_keygen,type_tss,type_tx_out,type_vault}.proto`.

The review surface includes:

- `OutputRef`/`spent_output_refs` and the `/thorchain/xmr/spent_refs/{vault_pub_key}` query
- `ObservedTx.xmr_tx_key` and its query representation, now present in the baseline through
  thornode !4811
- XMR readiness, inbound view-key and observed-height exposure
- EdDSA/FROST vault pubkeys and keyshare backups and
- `XMRMonovaultKeygen` request/result fields.

Generated protobuf, gRPC gateway, Pulsar and OpenAPI files are build artifacts and are excluded
from production LOC. They remain useful for checking generated-wire compatibility.

### 1.8 High-priority security boundaries

The primary review should trace these invariants across Go, Rust and consensus boundaries:

1. XMR address/network validation, amount conversion, transaction identity, tx-key disclosure,
   and the distinction between wire memos and THORChain semantic memos.
2. Scanner canonicality, receipt identity, reorg/rewind limits, errata ordering, mempool behavior,
   output ownership and key-image/spent-ref coverage.
3. Crash consistency and idempotency of pending inbound, signed payload, scanner, DKG, signing,
   attestation and spent-ref state across restart and replay.
4. Sidecar authentication, schema/capability negotiation, network identity, timeout/error
   classification and fail-closed behavior at the Go/Rust HTTP boundary.
5. FROST party membership, session/plan binding, transcript verification, P2P framing,
   result quorum, evidence/blame and recovery from missing or keyless members.
6. Consensus handling of monovault keygen, churn/rotation, spent refs, outbound attribution,
   slashing/jailing and repeated success/failure votes.

At the frozen thornode baseline, Bifrost contains a temporary network-wide skip for
`XMRMonovaultKeygen` in `bifrost/signer/sign.go`. The audit should evaluate both the guarded
current behavior and the safety conditions required before that guard is removed. The independent
Mimir-based replacement proposed by thornode !4983 is outside the baseline (§7.1).

---

## 2. Primary scope - `xmr-frost-signer` (Rust, serai fork)

Whole crate: `xmr-frost-signer/` at the frozen serai commit. The final top-level
`#[cfg(test)]` module in each source file is counted separately in §4 `build.rs` is production.

| File | Production LOC | Security-relevant responsibility |
|---|---:|---|
| `build.rs` | 9 | release/version metadata |
| `src/main.rs` | 409 | process startup, task/router wiring and shutdown |
| `src/config.rs` | 328 | configuration, network identity and secrets |
| `src/server.rs` | 195 | HTTP server, middleware and request controls |
| `src/auth.rs` | 55 | local API authentication |
| `src/error.rs` | 110 | retryable/terminal HTTP error contract |
| `src/types.rs` | 396 | API schema, capability and state types |
| `src/crypto.rs` | 97 | key and cryptographic helpers |
| `src/monero.rs` | 59 | Monero daemon RPC helpers |
| `src/chain/mod.rs` | 199 | chain height, daemon and canonical-location checks |
| `src/dkg/mod.rs` | 437 | PedPoP/FROST DKG sessions, expiry and finalize behavior |
| `src/dkg/state.rs` | 105 | durable DKG state |
| `src/keys/mod.rs` | 473 | vault keys, enrollment, backup/restore and replacement rules |
| `src/scanner/mod.rs` | 1,425 | scanner API, vault registration, canonical outputs, mark-spent and recovery |
| `src/scanner/tracker.rs` | 4,702 | sled-backed output/key-image tracking, continuity, expiry, quarantine and restart safety |
| `src/sign/mod.rs` | 1,525 | prepare/partial/combine/broadcast, durable plans and input/decoy handling |
| `src/sign/state.rs` | 202 | durable signing session state |
| `src/sign/idempotency.rs` | 204 | request replay and idempotent result handling |
| `src/sim.rs` | 554 | mocknet-only simulation hooks |
| **Total** | **11,484** | **10,930 excluding `sim.rs`** |

---

## 3. Optional cryptography and pinned Monero dependency

### 3.1 Optional in-repository Serai cryptography

These workspace crates are linked directly or transitively by the sidecar. A pragmatic optional
review is the THORChain fork delta against the corresponding upstream Serai code.

| Crate/group | Production LOC | Test LOC |
|---|---:|---:|
| `crypto/dkg` (+ `pedpop`, `dealer`, `recovery`) | 1,126 | 902 |
| `crypto/dleq` | 837 | 609 |
| `crypto/frost` | 810 | 672 |
| `crypto/dalek-ff-group` | 746 | - |
| `crypto/multiexp` | 267 | 341 |
| `crypto/schnorr` | 153 | 200 |
| `crypto/transcript` | 221 | - |
| `crypto/ciphersuite` | 69 | - |
| `common/std-shims` | 189 | - |
| `common/zalloc`, `common/env` | 46 | - |
| **Total** | **4,464** | **2,724** |

`common/request` is intentionally excluded. It remains a workspace member and contains 276
production LOC at the frozen commit, but the sidecar has no reverse dependency on it.

### 3.2 Pinned Monero implementation - separate dependency scope

The sidecar's `Cargo.toml` pins `monero-wallet` and `monero-simple-request-rpc` to
[`6309206da16b6acf4de9017446f1529a341aa0df`](https://gitlab.com/thorchain/devops/serai/-/commit/6309206da16b6acf4de9017446f1529a341aa0df),
the isolated vendor history produced by serai !69. This code supplies transaction construction,
multisig/CLSAG behavior, decoy selection and daemon RPC used by the signer.

Their source is outside the frozen Serai source tree and is not included in the LOC totals or
primary line-by-line audit scope. Because the pinned code contains THORChain-specific changes to
security-critical functionality, a separate review of those changes is recommended if the
engagement scope permits.

---

## 4. Test suites and executable context

Tests are audit context, not production LOC. The exact count below covers dedicated directories
and explicitly named XMR/FROST tests additional assertions in shared test files are available to
reviewers but are not included in the total.

| Area | Test LOC |
|---|---:|
| Go - Monero client directory | 12,513 |
| Go - Monero sidecar boundary directory | 3,372 |
| Go - observer spent-ref/certificate and XMR filter tests | 2,442 |
| Go - go-tss FROST tests | 2,969 |
| Go - P2P FROST evidence/frame/budget tests | 1,026 |
| Go - explicitly named x/thorchain and common XMR/spent-ref tests | 810 |
| **Go subtotal** | **23,132** |
| Rust - inline `#[cfg(test)]` modules in `xmr-frost-signer` | **8,119** |
| **Named/dedicated test total** | **31,251** |

Relevant executable context includes:

- `make test-simulation-cluster` its default `TEST_SIMULATION_CLUSTER_STAGES` contains
  `xmr-post-churn`
- XMR simulation actors in `test/simulation/actors/core/` for churn readiness, keygen jail,
  post-churn behavior, spent refs and vault balances and
- chainnet/stagenet Docker Compose stacks and `scripts/test-xmr-compose-contract.sh` for
  deployment-contract validation.

---

## 5. Sizing summary

| Bucket | Production LOC |
|---|---:|
| thornode - dedicated XMR/FROST files (§1.1–§1.5) | 18,571 |
| Rust - `xmr-frost-signer` (§2) | 11,484 |
| **Primary dedicated-code floor** | **30,055** |
| Primary floor excluding mocknet-only `sim.rs` | 29,501 |
| thornode - XMR/FROST branches in shared files (§1.6) | included no separate LOC assigned |
| Optional in-repo Serai cryptography (§3.1) | 4,464 |
| Named/dedicated tests (§4, context) | 31,251 test LOC |

The commit pair and path/function inventory define the scope LOC is only a reproducible sizing
aid. The primary floor does not assign separate LOC to the shared-file branches in §1.6.

---

## 6. Explicit exclusions and assumptions

- Open MR heads in §7 are excluded from the frozen baseline.
- Generated protobuf, Pulsar, gRPC gateway and OpenAPI output is excluded from production LOC.
- Non-XMR chain clients and unrelated branches of shared multi-chain files are excluded, except
  where needed to validate isolation or shared invariants.
- Test code, simulation actors, Docker/CI files and documentation are review context rather than
  production LOC.
- Optional Serai cryptography in §3.1 is included only if the engagement selected that option.
- The pinned Monero vendor source in §3.2 is dependency context, not line-by-line primary scope,
  unless approved through an addendum.
- Container tags such as `:develop` are not reproducible audit pins. Deployment verification must
  use an image built from the frozen source and, for release, an immutable digest.
- Configuration, daemon/network topology, TLS/loopback assumptions, bearer-secret handling and
  operational keygen activation are deployment assumptions that should be confirmed during
  kickoff even when deployment manifests are not themselves primary source scope.

---

## 7. Open post-baseline XMR work - separate and excluded

Status and heads in this section were checked 2026-08-18. These MRs are relevant follow-up work,
but they are not contained in both frozen baseline commits and must not be silently mixed into the
audit package.

### 7.1 thornode !4983 - post-3.20 defense-in-depth hardening

[thornode !4983](https://gitlab.com/thorchain/thornode/-/merge_requests/4983) is open and Draft.

- Head: [`e97860c770e3b3875596b9b76575db0b41833211`](https://gitlab.com/thorchain/thornode/-/commit/e97860c770e3b3875596b9b76575db0b41833211)
- Diff against its merge base/current `develop`: 84 files, 9,834 insertions, 742 deletions.
- Current role: post-3.20 follow-up not part of release 3.20 or this frozen baseline.

Security-relevant changes include:

- replay-safe terminal TSS keysign-failure voters, a persisted-consensus-state change requiring a
  coordinated future version boundary
- durable and ordered XMR errata cleanup, attestation/spent-ref replay, bounded state-sync and peer
  calls
- FROST P2P resource, frame, queue, cancellation, party and result-lifecycle controls
- sidecar schema/capability and network-identity checks, exact outbound-hash precedence,
  effective scan birthdays, wire-memo preservation/recovery and stronger fail-closed behavior
- explicit non-mocknet bearer requirements and Compose contract checks and
- `HaltMonovaultKeygen`, an independent operational control intended to replace the baseline's
  hard-coded network-wide keygen skip.

The MR pipeline was successful at the checked head, but the MR remained Draft and was not
mergeable as a final target. It requires a matching, versioned sidecar deployment and coordinated
activation.

### 7.2 serai !70 - companion recovery contracts

[serai !70](https://gitlab.com/thorchain/devops/serai/-/merge_requests/70) is open and Draft.

- Head: [`cc6db7ffe8a637f16809f4566290365b54d9e2eb`](https://gitlab.com/thorchain/devops/serai/-/commit/cc6db7ffe8a637f16809f4566290365b54d9e2eb)
- Diff against `serai@a61521bc…`: 4 files, 344 insertions, 85 deletions.
- Files: `xmr-frost-signer/README.md`, `src/scanner/mod.rs`, `src/scanner/tracker.rs`,
  and `src/types.rs`.

The MR requires an imported outbound wire memo to exactly match authenticated Monero transaction
Extra, rejects malformed/ambiguous/oversized/non-UTF-8 Extra data, preserves the earliest valid
vault scan birthday, returns `effective_birthday`, and atomically clears stale scanner checkpoints
when an earlier bounded birthday is accepted.

These contracts are intentionally paired with thornode !4983's wire-memo and effective-birthday
handling. They must not be deployed or audited as a replacement sidecar against the unmodified
frozen thornode baseline.

### 7.3 Procedure if the open MRs are added

If the engagement is expanded to include either open MR:

1. obtain written agreement that the work is an addendum rather than an unchanged target
2. freeze compatible final heads for **both** repositories after rebase/merge
3. verify the cross-repository schema, memo, birthday, auth and activation contracts
4. recount the combined final trees rather than summing overlapping MR diffstats and
5. provide Least Authority with a baseline-to-new-target diff and immutable deployment artifact.

---

## 8. Reproduction methodology

- File inventories were read from the commit trees with `git ls-tree -r --name-only <commit>`
  counts do not depend on the working tree.
- Go/production LOC is reproduced per file with:
  `git show <commit>:<file> | grep -c -v -E '^[[:space:]]*(//|$)'`.
- LOC excludes blank lines and full-line `//` comments. It does not strip block comments and is
  therefore a sizing metric rather than language-aware logical LOC.
- Rust sidecar production/test split uses the final top-level `#[cfg(test)]` marker in every
  `src/*.rs` file: lines before it are production lines from it onward are tests. `build.rs` has
  no inline test module and is production. `src/sim.rs` is shown separately because it is
  mocknet-only.
- Optional Rust crate test files under `tests/` or `src/tests/` are test LOC final inline
  `#[cfg(test)]` modules are split the same way as the sidecar.
- Open-MR diffstats use three-dot diffs from the target branch to the checked MR head. They are
  descriptive only and are not added to baseline totals.
