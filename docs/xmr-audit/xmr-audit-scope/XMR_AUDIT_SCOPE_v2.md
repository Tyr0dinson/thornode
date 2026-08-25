# THORChain Monero (XMR) Integration — Updated Security Audit Scope

Prepared and verified 2026-08-25 for the Least Authority audit engagement.

**Status:** Working draft for review.

This document defines the audit target as the exact pair of post-3.20 MR heads below. The two
revisions are a paired Thornode/Sidecar target and must be reviewed and deployed together.

## Audit target — frozen paired MR revisions

| Repository | Source branch | Frozen audit commit | Commit date |
|---|---|---|---|
| thornode | `boonew/xmr-post-3.20-defense-in-depth-2026-07-23` ([!4983](https://gitlab.com/thorchain/thornode/-/merge_requests/4983)) | [`f398fd844931244470d520dee9827b99a3ac2c98`](https://gitlab.com/thorchain/thornode/-/commit/f398fd844931244470d520dee9827b99a3ac2c98) | 2026-08-25 |
| serai fork | `zly/xmr-post-3.20-recovery-contracts` ([!70](https://gitlab.com/thorchain/devops/serai/-/merge_requests/70)) | [`6844c8179b822309c174c25bd03bcdc0847bf6b1`](https://gitlab.com/thorchain/devops/serai/-/commit/6844c8179b822309c174c25bd03bcdc0847bf6b1) | 2026-08-24 |

The commit hashes, not moving branch names, MR status or container tags, define the audit target.
Both MRs were open and no longer Draft when checked on 2026-08-25. Any rebase, merge-time change
or later commit requires a reviewed delta or a newly frozen target.

### Changes from the previously shared scope

Relative to the official v1, this version:

- freezes the paired post-3.20 targets at `thornode@f398fd844931244470d520dee9827b99a3ac2c98`
  and `serai@6844c8179b822309c174c25bd03bcdc0847bf6b1`;
- includes the merged thornode !4811 and !4931 work, the current signer hardening, and the
  additional defense-in-depth and recovery-contract changes in thornode !4983 and serai !70;
- updates all file inventories and LOC figures from those frozen commits;
- updates the pinned Monero dependency introduced through serai !69; and
- records the merge, deployment and coordinated-activation conditions for the two MRs.

---

## 1. Primary scope — thornode / Bifrost (Go)

Unless a row is explicitly marked as test context, the counts below are production code LOC:
non-blank lines with full-line `//` comments excluded. Generated files are excluded.

### 1.1 Monero chain client — `bifrost/pkg/chainclients/monero/`

All production `.go` files directly in this directory are in scope.

| File | Code LOC | Security-relevant responsibility |
|---|---:|---|
| `client.go` | 4,138 | vault lifecycle, keygen/keysign, outbound construction and recovery, broadcast, spent-ref reconciliation, consolidation and solvency |
| `scanner.go` | 2,618 | canonical block/receipt scanning, mempool handling, reorg and rewind behavior, non-signer filtering and observation delivery |
| `signed_tx_payload_store.go` | 1,960 | durable signed-outbound payload state, replay/rebroadcast and terminal cleanup |
| `reorg_store.go` | 841 | durable reorg/errata state and recovery ordering |
| `pending_internal_inbound_store.go` | 460 | durable pending internal inbound state and replay |
| `vault_registration.go` | 108 | sidecar vault enrollment and DKG membership |
| `vault_address_store.go` | 55 | persisted vault-address cache |
| `vault_bootstrap.go` | 51 | single-member/mocknet vault bootstrap |
| `default_{mainnet,mocknet,testnet}.go` | 12 | network defaults |
| **Subtotal** | **10,243** | |

### 1.2 Go sidecar boundary — `bifrost/pkg/chainclients/monero/sidecar/`

All production `.go` files in this directory are in scope.

| File | Code LOC | Security-relevant responsibility |
|---|---:|---|
| `client.go` | 1,601 | authenticated HTTP contract for DKG, signing, scanner state, output/spent-ref operations and recovery |
| `types.go` | 435 | request/response schema and capability fields |
| `server.go` | 319 | local FROST engine server surface |
| `adapter.go` | 209 | bridge between the chain client and FROST engine |
| `daemon_fallback.go` | 193 | optional direct-monerod fallback behavior |
| `errors.go` | 105 | retryable, terminal and contract error taxonomy |
| **Subtotal** | **2,862** | |

### 1.3 Observer spent-ref state and certificates

| File | Code LOC | Security-relevant responsibility |
|---|---:|---|
| `bifrost/observer/xmr_spent_refs.go` | 545 | authority, conflict and outcome handling for peer spent-ref imports |
| `bifrost/observer/xmr_spent_ref_certificates.go` | 794 | durable certificate construction, validation, persistence and replay |
| **Subtotal** | **1,339** | |

### 1.4 FROST DKG/keysign, evidence and P2P controls

| File | Code LOC | Security-relevant responsibility |
|---|---:|---|
| `bifrost/tss/go-tss/tss/frost_keygen.go` | 1,394 | distributed key generation orchestration, early-phase traffic retention, result consensus and recovery |
| `bifrost/tss/go-tss/tss/frost_keysign.go` | 2,065 | signing sessions, canonical plans, early-phase traffic retention, party/result consensus and recovery |
| `bifrost/tss/go-tss/tss/frost_signed_transcript.go` | 142 | authenticated signed-transcript representation |
| `bifrost/tss/go-tss/tss/frost_transcript_verify.go` | 311 | transcript/session binding and equivocation validation |
| `bifrost/tss/go-tss/tss/frost_types.go` | 353 | FROST message and state types |
| `bifrost/tss/go-tss/tss/frost_keygen_faultinject_{mainnet,mocknet}.go` | 21 | build-tagged production/mocknet fault-injection switch |
| `bifrost/p2p/messages/frost_evidence.go` | 232 | canonical FROST evidence encoding and validation |
| `bifrost/p2p/frost_broadcast_scheduler.go` | 163 | bounded and fair FROST broadcast scheduling |
| `bifrost/p2p/frost_ingress_budget.go` | 218 | ceremony, node and frame resource budgets |
| `bifrost/p2p/tss_reader_limiter.go` | 60 | per-peer TSS stream reader limits |
| **Subtotal** | **4,959** | |

### 1.5 Consensus and common dedicated files

| File | Code LOC | Security-relevant responsibility |
|---|---:|---|
| `x/thorchain/xmr_monovault.go` | 135 | XMR-capable vault selection and monovault maintenance rules |
| `x/thorchain/keeper/v1/keeper_xmr_spent_refs.go` | 115 | consensus persistence of spent-output references |
| `common/xmr_address.go` | 434 | XMR address parsing, base58 encoding, network prefixes and subaddresses |
| **Subtotal** | **684** | |

**Dedicated thornode production total: 20,087 LOC.**

### 1.6 XMR/FROST logic in shared production files

The XMR/FROST functions, branches and adjacent invariants in the following shared files are in
primary scope. Reviewers may read the whole file for context; unrelated chain implementations are
not intended as new audit scope. This inventory defines the shared-file scope; no separate
partial-file LOC is assigned.

- **Observation, attestation and scanner persistence:**
  `bifrost/blockscanner/{blockscanner,scanner_storage}.go`,
  `bifrost/observer/{attestation_batcher,attestation_gossip,attestation_handlers,attestation_handlers_impl,attestation_state_init,generic_attestation,observe,observer_storage,peer_manager}.go`.
- **P2P and FROST transport/orchestration:**
  `bifrost/p2p/{communication,message_id_subscriber,party_coordinator,stream_helper}.go`,
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
  `x/thorchain/{grpc_query,handler_add_liquidity,handler_common_outbound,handler_errata_tx_helpers,handler_observed_tx_helpers,handler_tss,handler_tss_keysign,helpers,manager_network_current,manager_slasher_current,manager_txout_current,manager_validator_current,migration_helpers,querier}.go`,
  `x/thorchain/keeper/{keeper,keeper_dummy}.go`,
  `x/thorchain/keeper/v1/{alias,keeper,keeper_tss_keysign_fail,keeper_vault}.go`, and
  `x/thorchain/types/{msg_errata,msg_errata_quorum,msg_observed_tx_quorum,msg_observed_txin,msg_observed_txout,msg_tss_pool,msg_tss_pool_v2,type_keygen,type_observed_tx,type_tx_out,type_vault}.go`,
  and `tools/events/{info,security}.go`.
- **Common/configuration:**
  `common/{address,asset,chain,common,pubkey,tx,type_observed_tx}.go`,
  `config/config.go`, and `constants/{constants,constants_mocknet,mimir_strings}.go`.

Small aliases and manager wiring in `x/thorchain/{alias,managers}.go` are included where they
select XMR/FROST behavior.

### 1.7 Protocol and API surface

The frozen target has XMR/FROST fields, events, messages or routes in these 17 source schemas:

`proto/thorchain/v1/common/common.proto` and
`proto/thorchain/v1/types/{msg_tss_keysign_fail,msg_tss_pool,query,query_inbound_address,query_network,query_outbound,query_tx,query_vault,tx,type_errata_tx_voter,type_events,type_keygen,type_tss,type_tss_keysign,type_tx_out,type_vault}.proto`.

The review surface includes:

- `OutputRef`/`spent_output_refs` and the `/thorchain/xmr/spent_refs/{vault_pub_key}` query;
- `ObservedTx.xmr_tx_key`, `xmr_mined_height` and their query representations;
- deterministic XMR errata external-height/outcome state;
- XMR readiness, inbound view-key and observed-height exposure;
- EdDSA/FROST vault pubkeys and keyshare backups; and
- `XMRMonovaultKeygen` request/result fields, terminal `TssKeysignFailVoter.completed` state, and
  keysign-failure event data.

Generated protobuf, gRPC gateway, Pulsar and OpenAPI files are build artifacts and are excluded
from production LOC. They remain useful for checking generated-wire compatibility.

### 1.8 High-priority security boundaries

The primary review should trace these invariants across Go, Rust and consensus boundaries:

1. XMR address/network validation, amount conversion, transaction identity, tx-key disclosure,
   and the distinction between wire memos and THORChain semantic memos.
2. Scanner canonicality, receipt identity, bounded rewind and effective-birthday behavior,
   durable/ordered errata cleanup including nonstandard memos, independent mined-height
   observation quorum, mempool behavior, output ownership and key-image/spent-ref coverage.
3. Crash consistency and idempotency of pending inbound, signed payload, scanner, DKG, signing,
   attestation and spent-ref state across restart and replay.
4. Sidecar authentication, schema/capability negotiation, network identity, timeout/error
   classification and fail-closed behavior at the Go/Rust HTTP boundary.
5. FROST party membership, session/plan binding, transcript verification, P2P framing and resource
   budgets, fair/bounded queues, early-phase message retention, cancellation, result quorum,
   evidence/blame and recovery from missing or keyless members.
6. Consensus handling of monovault keygen, churn/rotation, spent refs, outbound attribution,
   slashing/jailing, terminal keysign-failure voters and repeated success/failure votes.

The frozen Thornode target retains the temporary network-wide `XMRMonovaultKeygen` skip in
`bifrost/signer/sign.go` and adds the independent `HaltMonovaultKeygen` Mimir control. The audit
should evaluate both controls, their ordering, and the safety conditions required before the
hard-coded skip is removed.

---

## 2. Primary scope — `xmr-frost-signer` (Rust, serai fork)

Whole crate: `xmr-frost-signer/` at the frozen serai commit. The final top-level
`#[cfg(test)]` module in each source file is counted separately in §4; `build.rs` is production.

| File | Production LOC | Security-relevant responsibility |
|---|---:|---|
| `build.rs` | 9 | release/version metadata |
| `src/main.rs` | 409 | process startup, task/router wiring and shutdown |
| `src/config.rs` | 328 | configuration, network identity and secrets |
| `src/server.rs` | 230 | HTTP server, middleware, request controls and daemon-network reporting |
| `src/auth.rs` | 55 | local API authentication |
| `src/error.rs` | 110 | retryable/terminal HTTP error contract |
| `src/types.rs` | 399 | API schema, capability and state types, including effective scan birthday and daemon network |
| `src/crypto.rs` | 97 | key and cryptographic helpers |
| `src/monero.rs` | 59 | Monero daemon RPC helpers |
| `src/chain/mod.rs` | 199 | chain height, daemon and canonical-location checks |
| `src/dkg/mod.rs` | 437 | PedPoP/FROST DKG sessions, expiry and finalize behavior |
| `src/dkg/state.rs` | 105 | durable DKG state |
| `src/keys/mod.rs` | 473 | vault keys, enrollment, backup/restore and replacement rules |
| `src/scanner/mod.rs` | 1,435 | scanner API, vault registration, canonical outputs, exact wire-memo validation, mark-spent and recovery |
| `src/scanner/tracker.rs` | 4,720 | sled-backed output/key-image tracking, effective birthdays, continuity, expiry, quarantine and restart safety |
| `src/sign/mod.rs` | 1,525 | prepare/partial/combine/broadcast, durable plans and input/decoy handling |
| `src/sign/state.rs` | 202 | durable signing session state |
| `src/sign/idempotency.rs` | 204 | request replay and idempotent result handling |
| `src/sim.rs` | 554 | mocknet-only simulation hooks |
| **Total** | **11,550** | **10,996 excluding `sim.rs`** |

---

## 3. Optional cryptography and pinned Monero dependency

### 3.1 Optional in-repository Serai cryptography

These workspace crates are linked directly or transitively by the sidecar. A pragmatic optional
review is the THORChain fork delta against the corresponding upstream Serai code.

| Crate/group | Production LOC | Test LOC |
|---|---:|---:|
| `crypto/dkg` (+ `pedpop`, `dealer`, `recovery`) | 1,570 | 458 |
| `crypto/dleq` | 1,047 | 399 |
| `crypto/frost` | 810 | 672 |
| `crypto/dalek-ff-group` | 746 | — |
| `crypto/multiexp` | 410 | 198 |
| `crypto/schnorr` | 207 | 146 |
| `crypto/transcript` | 147 | 74 |
| `crypto/ciphersuite` | 69 | — |
| `common/std-shims` | 189 | — |
| `common/zalloc`, `common/env` | 46 | — |
| **Total** | **5,241** | **1,947** |

`common/request` is intentionally excluded. It remains a workspace member and contains 276
production LOC at the frozen commit, but the sidecar has no reverse dependency on it.

### 3.2 Pinned Monero implementation — separate dependency scope

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
and explicitly named XMR/FROST tests; additional assertions in shared test files are available to
reviewers but are not included in the total.

| Area | Test LOC |
|---|---:|
| Go — Monero client directory | 13,537 |
| Go — Monero sidecar boundary directory | 3,946 |
| Go — observer spent-ref/certificate, XMR barrier and filter tests | 2,972 |
| Go — go-tss FROST tests | 3,572 |
| Go — P2P FROST evidence/frame/budget/ingress/lifecycle tests | 3,307 |
| Go — explicitly named x/thorchain and common XMR/spent-ref tests | 810 |
| **Go subtotal** | **28,144** |
| Rust — inline `#[cfg(test)]` modules in `xmr-frost-signer` | **8,367** |
| **Named/dedicated test total** | **36,511** |

Relevant executable context includes:

- `make test-simulation-cluster`; its default `TEST_SIMULATION_CLUSTER_STAGES` contains
  `xmr-post-churn`;
- XMR simulation actors in `test/simulation/actors/core/` for churn readiness, keygen jail,
  post-churn behavior, spent refs and vault balances; and
- chainnet/stagenet Docker Compose stacks and `scripts/test-xmr-compose-contract.sh` for
  deployment-contract validation.

---

## 5. Sizing summary

| Bucket | Production LOC |
|---|---:|
| thornode — dedicated XMR/FROST files (§1.1–§1.5) | 20,087 |
| Rust — `xmr-frost-signer` (§2) | 11,550 |
| **Primary dedicated-code floor** | **31,637** |
| Primary floor excluding mocknet-only `sim.rs` | 31,083 |
| thornode — XMR/FROST branches in shared files (§1.6) | included; no separate LOC assigned |
| Optional in-repo Serai cryptography (§3.1) | 5,241 |
| Named/dedicated tests (§4, context) | 36,511 test LOC |

The commit pair and path/function inventory define the scope; LOC is only a reproducible sizing
aid. The primary floor does not assign separate LOC to the shared-file branches in §1.6.

---

## 6. Explicit exclusions and assumptions

- Changes after the exact !4983 and !70 heads pinned above are excluded until reviewed and added
  through an updated audit target.
- Generated protobuf, Pulsar, gRPC gateway and OpenAPI output is excluded from production LOC.
- Non-XMR chain clients and unrelated branches of shared multi-chain files are excluded, except
  where needed to validate isolation or shared invariants.
- Test code, simulation actors, Docker/CI files and documentation are review context rather than
  production LOC.
- Optional Serai cryptography in §3.1 is included only if the engagement selected that option.
- The pinned Monero vendor source in §3.2 is dependency context, not line-by-line primary scope.
- Container tags such as `:develop` are not reproducible audit pins. Deployment verification must
  use an image built from the frozen source and, for release, an immutable digest.
- Configuration, daemon/network topology, TLS/loopback assumptions, bearer-secret handling and
  operational keygen activation are deployment assumptions that should be confirmed during
  kickoff even when deployment manifests are not themselves primary source scope.

---

## 7. Open MR, merge and activation status

Status and heads in this section were checked 2026-08-25. The exact !4983 and !70 heads below are
included in the audit target. Both MRs are open and no longer Draft. They form a paired
cross-repository contract and must not be reviewed or deployed independently.

### 7.1 thornode !4983 — post-3.20 defense-in-depth hardening

[thornode !4983](https://gitlab.com/thorchain/thornode/-/merge_requests/4983) is open, mergeable
and awaiting approval.

- Head: [`f398fd844931244470d520dee9827b99a3ac2c98`](https://gitlab.com/thorchain/thornode/-/commit/f398fd844931244470d520dee9827b99a3ac2c98)
- Current role: included post-3.20 defense-in-depth audit target; not part of release 3.20.

Security-relevant changes include:

- replay-safe terminal TSS keysign-failure voters, a persisted-consensus-state change requiring a
  coordinated future version boundary, with persistence errors propagated to callers;
- durable and ordered XMR errata cleanup, explicit handling of unknown outcomes and nonstandard
  memos, independent mined-height quorum, churn-safe retry, attestation/spent-ref replay, scalable
  bounded state-sync and peer calls;
- FROST P2P resource, frame, queue, cancellation, party and result-lifecycle controls;
- retention of valid early FROST keygen and keysign phase traffic;
- sidecar schema/capability and network-identity checks, exact outbound-hash precedence,
  effective scan birthdays, wire-memo preservation/recovery and stronger fail-closed behavior;
- end-to-end verification that Thornode and the Sidecar agree on the Monero daemon network;
- explicit non-mocknet bearer requirements and Compose contract checks; and
- `HaltMonovaultKeygen`, an independent operational control intended to replace the hard-coded
  network-wide keygen skip.

The MR had no reported conflicts or unresolved discussions. Its current merge-result pipeline was
running when checked. The pinned head remains the source under audit; any subsequent change
requires a delta review and updated pin.
Release additionally requires a matching versioned sidecar, immutable image digest, fresh Sidecar
state at the schema-4 cutover, and coordinated activation of the persisted-consensus-state change.

### 7.2 serai !70 — companion recovery contracts

[serai !70](https://gitlab.com/thorchain/devops/serai/-/merge_requests/70) is open, mergeable and
no longer Draft.

- Head: [`6844c8179b822309c174c25bd03bcdc0847bf6b1`](https://gitlab.com/thorchain/devops/serai/-/commit/6844c8179b822309c174c25bd03bcdc0847bf6b1)
- Changed signer files: `xmr-frost-signer/README.md`, `src/scanner/mod.rs`,
  `src/scanner/tracker.rs`, `src/server.rs`, and `src/types.rs`.

The MR requires an imported outbound wire memo to exactly match authenticated Monero transaction
Extra, rejects malformed/ambiguous/oversized/non-UTF-8 Extra data, preserves the earliest valid
vault scan birthday, returns `effective_birthday`, and atomically clears stale scanner checkpoints
when an earlier bounded birthday is accepted. It also reports the Sidecar daemon network so
Thornode can reject a mismatched runtime.

These contracts are intentionally paired with thornode !4983's wire-memo and effective-birthday
handling. The MR pipeline was successful at the checked head. This Sidecar must not be deployed
against an unmodified release-3.20 Thornode.

### 7.3 Procedure before merge and deployment

1. Complete !4983 review/approval and organization CI with the production private TSS dependency.
2. Freeze compatible final heads for **both** repositories after any further changes.
3. Provide Least Authority with a diff from the audited heads to the final heads and review every
   resulting change.
4. Verify the cross-repository schema, wire-memo, effective-birthday, authentication and
   activation contracts.
5. Recount the combined final trees, publish a matching immutable Sidecar image, and keep XMR
   keygen halted until the complete deployment is verified across active and ready nodes.

---

## 8. Reproduction methodology

- File inventories were read from the commit trees with `git ls-tree -r --name-only <commit>`;
  counts do not depend on the working tree.
- Go/production LOC is reproduced per file with:
  `git show <commit>:<file> | grep -c -v -E '^[[:space:]]*(//|$)'`.
- LOC excludes blank lines and full-line `//` comments. It does not strip block comments and is
  therefore a sizing metric rather than language-aware logical LOC.
- Rust sidecar production/test split uses the final top-level `#[cfg(test)]` marker in every
  `src/*.rs` file: lines before it are production; lines from it onward are tests. `build.rs` has
  no inline test module and is production. `src/sim.rs` is shown separately because it is
  mocknet-only.
- Optional Rust crates are classified structurally: files under `tests/` or `src/tests/` and
  dedicated test modules are test LOC. A `#[cfg(test)]` attribute on an import or module
  declaration is not treated as a production/test split marker.
- MR diffstats are descriptive only. Production and test totals are counted directly from the two
  exact commit trees pinned in this document.
