# XMR Signer State Machine Baseline

## Purpose

This document is the baseline for the signer-side refactor.

The execution checklist for finishing the refactor lives in
`docs/bifrost/xmr-signer-state-machine-finish-checklist.md`.

The flows and edge cases below are captured from the current Monero code paths in this repository so the refactor can start from an explicit baseline instead of oral history.

It describes:

- the current normal flow,
- the current rescan and replay flow,
- the edge cases the code already handles,
- the specific state seams the refactor should move into the signer.

The intended refactor target is the Rust signer sidecar, not another large control-flow layer in Bifrost. The signer should become the authoritative state engine for:

- vault lifecycle,
- tracked output lifecycle,
- outgoing lifecycle.

Bifrost should stay as the orchestrator that feeds events into that state engine and turns settled signer state into THORChain observations, broadcasts, and operational reporting.

## Main code paths

These are the primary Monero files this baseline was derived from.

| Area | Current primary files |
| --- | --- |
| Bifrost Monero client | `bifrost/pkg/chainclients/monero/client.go` |
| Bifrost scanner adapter | `bifrost/pkg/chainclients/monero/scanner.go` |
| Bifrost sidecar HTTP client | `bifrost/pkg/chainclients/monero/frost_signer.go` |
| Bifrost local work queue | `bifrost/pkg/chainclients/monero/xmr_work_state.go` |
| Sidecar routes | `bifrost/pkg/chainclients/monero/xmr-frost-signer/src/api/routes.rs` |
| Sidecar persistent state | `bifrost/pkg/chainclients/monero/xmr-frost-signer/src/state/store.rs` |
| Sidecar signing coordinator | `bifrost/pkg/chainclients/monero/xmr-frost-signer/src/frost/sign.rs` |
| Sidecar key-image coordinator | `bifrost/pkg/chainclients/monero/xmr-frost-signer/src/frost/key_image.rs` |
| Sidecar scanner state | `bifrost/pkg/chainclients/monero/xmr-frost-signer/src/tx/scanner.rs` |

## Current state stores

The signer-side state now owns the lifecycle model directly. This baseline records the post-refactor contract and the remaining Bifrost orchestration boundary around it.

| Entity | Where it lives | Current shape |
| --- | --- | --- |
| Vault | `VaultRecord` in `store.rs` | `vault_pub_key`, `group_key`, `address`, `birthday`, `rescan` |
| Output | `TrackedOutput` in `store.rs` | `tx_hash`, `output_index`, `amount`, `height`, `key_image`, `spent_by_key_image_height`, `state`, reservation metadata, subaddress info, wallet payload |
| Outgoing | `OutgoingTransferRecord` in `store.rs` | tx attribution plus `tx_keys`, typed `status`, `registered_inventory`, `has_owned_change`, `local_change_materialized`, `key_images_ready`, `planned_height`, `observed_height`, `reported_effective_height` |
| Registered key-image inventory | `RegisteredKeyImageRef` in `store.rs` | THORChain-provided tx/output to key-image mapping |
| Scan history | `ScanBlockMeta` in `store.rs` | per-block added outputs, spent key images, resolved spent output refs |
| Bifrost work state | `xmrWorkItem` in `xmr_work_state.go` | replay / finalize work queue state in LevelDB |

The main operational boundary is now explicit: signer lifecycle modules own vault, output, and outgoing truth, while Bifrost drives scanner timing, ceremony origination, and observation submission. Tx-key replay is inventory-driven through `register_vault(tx_keys, key_images)` and `syncTrackedVaultRegistrations()`, not a separate per-tx replay loop.

## Current transition matrix

This is the current behavior contract the refactor must preserve while moving transitions behind explicit signer lifecycle modules.

### Vault transitions

| Transition | Trigger today |
| --- | --- |
| unregistered -> registered | `register_vault` via `ensure_registered_vault_state()` |
| registered -> syncing_to_tip | `register_vault` when `birthday > 0`, or birthday backfill in `Store::register_vault()` |
| syncing_to_tip -> at_tip | `scanner_sync` when rescan catches up or is already at tip |
| at_tip -> syncing_to_tip | re-registration with rewind/backfilled birthday, or explicit `set_vault_rescan()` paths during catchup |
| any registered state -> reset_pending_reregistration | `scanner_reset` before vault-scoped state is purged and re-registration is required |
| registered mapping -> registered mapping (overwrite) | `register_vault` when the same `vault_pub_key` is re-registered with updated `group_key` or address |

### Output transitions

| Transition | Trigger today |
| --- | --- |
| absent -> unspent | `scanner_sync` / `commit_scan_batch()` via `merge_scanned_outputs()` |
| unspent -> reserved | `sign_prepare` input reservation and sim-helper transfer reservation |
| reserved -> unspent | abandoned or expired signing session via `release_reservations_for_session()` / `reconcile_reservations()` |
| reserved -> spent | successful broadcast finalize via `finalize_reserved_for_msg()` or observed reservation reconciliation |
| unspent -> spent | `apply_key_images(spend=true)`, historical spend replay, or scanner-side key-image spend marking |
| any output -> key_image bound (metadata only) | `key_image_combine`, `apply_key_images(reconcile=false)`, `register_vault` inventory replay, or `scanner_sync` inventory reconcile |
| spent -> unspent | reconcile path unwinds legacy `RECONCILED_KEY_IMAGE_SPEND_HEIGHT` metadata-only spends |

### Outgoing transitions

| Transition | Trigger today |
| --- | --- |
| absent -> signed_pending_observation | `sign_combine` in `frost/sign.rs` |
| absent -> attested_pending_observation | shared tx-key inventory replay helper via `register_vault(tx_keys)` or `validate_outbound` when daemon lookup succeeds but tx is not yet mempool or mined |
| absent -> observed_mempool | shared tx-key inventory replay helper via `register_vault(tx_keys)` or `validate_outbound` when daemon lookup shows pool visibility |
| absent -> observed_mined | shared tx-key inventory replay helper via `register_vault(tx_keys)` or `validate_outbound` when daemon lookup shows confirmed visibility |
| signed_pending_observation -> observed_mempool | `broadcast`, daemon reconciliation in `frost/sign.rs`, or mempool observation lookup |
| attested_pending_observation -> observed_mempool | `broadcast` or daemon reconciliation after prior validation |
| signed_pending_observation -> observed_mined | scanner/block promotion or daemon reconciliation after mining |
| attested_pending_observation -> observed_mined | scanner/block promotion or daemon reconciliation after mining |
| observed_mempool -> observed_mined | scanner/block promotion or daemon reconciliation after mining |
| observed_mined -> reported | `scanner_sync` when `reported_effective_height` is advanced |
| any tx-key-backed mined record -> pruned | `prune_outbounds` during inventory reconciliation or reset paths |
| any existing record -> audit metadata updated | shared tx-key inventory replay helper via `register_vault(tx_keys)` or `validate_outbound` |

## Current normal flow

### 1. Keygen and vault bootstrap

1. Bifrost advertises `CustomKeygen() == true` and runs `Client.Keygen()`.
2. `Client.Keygen()` asks go-tss to run FROST keygen with the THORChain signer set.
3. The sidecar executes DKG rounds through `/dkg/round1`, `/round2`, `/round3`, then explicit `/dkg/finalize`.
4. The sidecar persists the vault mapping and keyshare.
5. Bifrost verifies the derived vault address and caches the vault address mapping locally.

Current behavior:

- keygen is fail-closed on membership mismatch, address mismatch, or finalize failure,
- the sidecar does not persist key material before explicit finalize,
- vault registration is keyed by `vault_pub_key`, but the durable lookup key for keyshares is the group key.

### 2. Scanner and observation

1. Bifrost `Scanner.FetchTxs()` fetches the canonical block from the sidecar.
2. Bifrost asks the sidecar to run `scanner_sync`.
3. The sidecar validates the canonical header hash and parent hash against monerod.
4. The sidecar scans registered vaults, persists outputs and block metadata, loads registered key-image inventory, replays historical spends for newly-bound key images, and returns transfer entries.
5. Bifrost converts those transfer entries into `TxInItem`s and feeds them into observer processing.
6. Bifrost applies Monero-specific observation filtering, confirmation logic, and attestation rules before the observation reaches THORChain.

Current behavior:

- inbound mempool attribution is intentionally unsupported,
- outbound mempool attribution is supported only when the sender can be tied to a known vault,
- network fee reporting uses fee-rate times representative outbound weight, not coinbase averaging unless fee-rate lookup fails,
- per-block solvency checks are driven from the scanner path so drift is surfaced promptly.

### 3. Signing, keysign, and broadcast

1. THORChain schedules an outbound and Bifrost calls `Client.SignTx()`.
2. `validateXMRSignTx()` validates chain, amount, memo context, vault identity, and destination details.
3. `ensureXMRVaultBalance()` verifies spendable balance against amount plus max gas.
4. Bifrost resolves vault membership and local capability.
5. Bifrost blocks keysign if the local node is not allowed to originate work or if the XMR key-image backlog is still pending.
6. go-tss runs FROST signing through sidecar `sign_prepare`, `sign_partial`, and `sign_combine`.
7. `sign_prepare` builds the unsigned tx, selects inputs, and reserves those outputs.
8. `sign_combine` finalizes the tx, binds the reservation to the final tx hash, and persists an `OutgoingTransferRecord` with `signed_pending_observation`.
9. Bifrost broadcasts through sidecar `/broadcast`.
10. The sidecar finalizes reserved outputs for the `msg_id` and updates outgoing status to mempool or mined if the daemon already knows the tx.
11. Bifrost writes signer cache and can auto-observe the signed tx.

Current behavior:

- sign preparation is blocked if scan height lags daemon height beyond the configured threshold,
- input reservations are CAS-protected and retried with excluded conflicting inputs,
- expired or abandoned non-complete sessions release reservations,
- completed sessions do not release reservations until broadcast or reservation reconciliation,
- outgoing attribution is persisted before observation consensus completes.

### 4. Outgoing tx-key validation and audit

There are two entrypoints that create or update outgoing audit state:

- signer-originated sign flow in `frost/sign.rs`,
- tx-key inventory replay through `register_vault(tx_keys)` or the compatibility `validate_outbound` endpoint, both of which now use the same signer helper.

Current behavior:

- successful tx-key validation marks `registered_inventory=true` when the replayed outbound is part of registered inventory,
- successful validation can create an outgoing record even on a non-signer node,
- the sidecar currently stores outbound state as one record per `vault_pub_key + tx_hash`,
- that record is used for mempool/mined reporting, replay verification, finalized outbound readiness, and vault-state reporting.

### 5. Key-image generation and finalized observation

1. For finalized XMR observations, Bifrost calls `PrepareObservedTxInItem()`.
2. For outbounds, Bifrost first recovers or validates tx keys and checks whether the tx has tracked vault-owned change.
3. Bifrost resolves the canonical XMR vault pubkey and signing members.
4. Bifrost refuses to originate key-image work if the local role is not allowed or if earlier backlog items are still ahead in the queue.
5. Bifrost queries tracked outputs from the sidecar.
6. If key images are already cached locally, Bifrost reuses them.
7. Otherwise go-tss runs FROST key-image prepare/partial/combine through the sidecar.
8. The sidecar persists the combined key image onto the tracked output.
9. Bifrost validates the proof and attaches `XmrKeyImages` to the finalized observation.

Current behavior:

- finalized outbound observation waits for tx-key propagation first,
- finalized inbound change observations can be intentionally skipped so the key images ride on the finalized outbound instead,
- partial key-image population for a tx causes Bifrost to wait for a coherent proof set instead of emitting mixed state,
- backlog ordering is tx-scoped and favors the earliest pending tx,
- key-image proofs are cached in Bifrost and the bound key image itself is persisted in the signer.

### 6. Gas, fees, confirmations, solvency, and consolidation

Current behavior:

- sign-time balance checks use unlocked balance, not total balance,
- solvency reporting uses total balance, not unlocked balance, because newly-arrived locked outputs still exist on chain,
- confirmation count for observations is value-based and capped by mimir; XMR outbound-bearing observations are treated as finalized at one confirmation,
- fee-rate reporting is derived from the daemon fee rate per weight and a representative vault outbound weight,
- consolidation is a real outbound-sign/broadcast path with its own gating on node status, mempool state, settings, and retry accounting.

### 7. MIGRATE handling

Current behavior:

- MIGRATE outbounds can be observed against the receiving vault rather than the signing vault,
- metadata lookup falls back to any matching XMR observation when the exact observed vault does not match,
- older MIGRATE paths that only carry EDDSA pubkeys are recovered by resolving the FROST pubkey from the Asgard set,
- `shouldSweepXMRMigration()` drives sweep behavior for final migration-style spends.

## Current rescan and replay flow

### Fresh registration and rescan entry

- sidecar `register_vault` sets `rescan=true` automatically when `birthday > 0`,
- a fresh vault therefore enters replay mode immediately without a second API call,
- Bifrost `syncXMRState()` treats pending rescans as a hard gate before metadata catchup continues.

### Rescan loop

1. Bifrost `syncXMRState()` calls `syncTrackedVaultRegistrations()`.
2. Bifrost ensures every tracked XMR vault is registered in the signer, including key-image and tx-key inventory.
3. If any signer-side vault state still has `rescan=true`, Bifrost runs `drivePendingScannerRescans()`.
4. `drivePendingScannerRescans()` repeatedly calls `scanner_sync` at tip until rescans stop progressing or all vaults are caught up.
5. `scanner_sync` performs per-vault rescan batches from stored scan height or birthday toward chain tip.
6. Once rescan reaches tip, the sidecar flips `rescan=false` and normal scan resumes on the same or next request.

### Batch commit semantics

`commit_scan_batch()` has four phases today:

1. Persist scanned outputs, `ScanBlockMeta`, and scan height.
2. Load registered key-image inventory onto tracked outputs with reconcile enabled.
3. Replay historical spends from persisted `ScanBlockMeta` now that key images are bound.
4. Prune old `ScanBlockMeta` entries outside the reorg window.

This distinction matters: loading a key image is metadata binding, not proof that the output was spent.

### Replay after rescan

After vault registration and rescan settle:

1. `syncTrackedVaultRegistrations()` is the single tx-key inventory replay writer. It re-registers full watched-vault inventory, including tx keys and registered key images, when signer state is behind or fingerprints diverge.
2. `catchupKeyImagesForVaults()` can still heal key-image inventory drift without waiting for a full rescan loop.
3. `driveLocalKeyImageBacklogForVaults()` originates local key-image ceremonies for tracked outputs that still lack key images.

### Hard reset path

`scanner_reset` is a true vault-state purge:

- clears outputs,
- clears scan metadata and scan height,
- clears registered key-image inventory,
- clears outgoing transfers,
- deletes the vault registration entry,
- removes in-memory scanner state,
- does not touch `keys/<pubkey>.frost`.

After reset, the caller must re-register the vault with full inventory.

## Edge cases currently handled

### Vault identity and registration

| Edge case | Current behavior |
| --- | --- |
| `vault_pub_key`, `group_key`, and derived address disagree | registration is rejected |
| same vault registered again with same mapping | preserved as idempotent |
| same vault registered again with updated mapping | mapping is overwritten |
| birthday backfilled later | sidecar updates birthday and may rewind scan height |
| same group key re-registered during active rescan | scanner preserves in-memory outputs instead of rebuilding and losing rescanned outputs |
| missing FROST pubkey on older outbound paths | Bifrost resolves it from the Asgard set |

### Scanner, rescan, and reorg

| Edge case | Current behavior |
| --- | --- |
| canonical block header mismatch | `scanner_sync` returns conflict and refuses scan |
| rescanned outputs arrive without `ScanBlockMeta` | sidecar still reports them correctly from persisted state |
| historical spends are known only after key-image inventory replay | batch reconcile replays old block metadata and marks real spent heights |
| legacy metadata-only spend markers exist | reconcile unwinds them using `RECONCILED_KEY_IMAGE_SPEND_HEIGHT` handling |
| reorg window expires | old block metadata is pruned |
| inactive vault still receives observations | scanner can attribute to inactive vaults |
| inbound mempool attribution requested | intentionally unsupported |
| duplicate mempool outbound appears repeatedly | scanner suppresses duplicates until confirmation clears the seen set |

### Signing and reservation lifecycle

| Edge case | Current behavior |
| --- | --- |
| scanner height is stale relative to daemon height | `sign_prepare` fails closed |
| too many concurrent signing sessions | sidecar enforces capacity |
| same `msg_id` reused with same payload | idempotent replay |
| same `msg_id` reused with different payload | conflict or stale-record clearing path |
| sidecar restart loses in-memory session but keeps idempotency record | route detects stale session and clears cached idempotency |
| reserved inputs conflict with another in-flight tx | prepare retries with conflicting inputs excluded |
| expired or abandoned session | reservations are released |
| complete-but-unbroadcast session | reservations are retained until broadcast or reconciliation |
| broadcast tx hash does not match reservation candidate | finalize reports mismatch instead of silently spending outputs |

### Outgoing audit and tx-key inventory

| Edge case | Current behavior |
| --- | --- |
| same `vault + tx_hash` saved twice with different immutable metadata | rejected as collision |
| validated outbound arrives on a non-signer node | tx-key audit record is still persisted |
| tx keys differ from existing saved record | validation returns conflict |
| status changes from pending to mempool to mined | sidecar reconciles outgoing status from daemon observation |
| tx-key inventory replay sees mismatched signer-side outgoings | stale outgoings can be pruned and replayed |
| Bifrost lacks tx keys on finalized outbound | local signer state is queried to recover them before giving up |

### Key-image flow

| Edge case | Current behavior |
| --- | --- |
| tracked output is missing from signer state | key-image prepare fails |
| tracked output exists but wallet payload is missing | key-image prepare/validate fails |
| tracked output is not owned by loaded threshold keys | key-image prepare fails |
| key image point or scalar proofs are non-canonical | validation fails |
| finalized outbound has tx keys but no local tracked outputs yet | Bifrost waits and retries |
| finalized outbound has tracked outputs but only some lack key images | Bifrost waits for coherent proof state |
| earlier key-image backlog tx is still pending | later finalized observation waits behind backlog head |
| local node is not allowed to originate key-image work | Bifrost defers instead of half-running the flow |

### Observation and memo handling

| Edge case | Current behavior |
| --- | --- |
| delayed XMR change inbound would create duplicate observation noise | Bifrost skips it and relies on finalized outbound key images |
| finalized outbound has no tracked vault-owned change | no key-image ceremony is required |
| MIGRATE observation is recorded against the receiving vault | metadata lookup falls back to any matching XMR observation |

### Gas, fees, confirmations, consolidation, solvency

| Edge case | Current behavior |
| --- | --- |
| total balance exists but unlocked balance is insufficient | sign path returns locked-output retry error |
| amount plus max gas overflows uint64 | sign path fails closed |
| fee-rate endpoint fails | scanner falls back to block-derived fee estimate |
| block-derived fee estimate would overstate normal vault outbounds | scanner prefers fee-rate times representative weight |
| outbound-bearing observation would otherwise wait too long for confirmations | treated as one-confirmation finalization |
| XMR system still has pending finalized propagation or backlog | solvency reporting is skipped until idle |
| consolidation would run into pending mempool or cooldown conditions | consolidation path skips instead of forcing a spend |

## Current modular model

The signer is already the authoritative source for:

- vault registry,
- tracked outputs,
- reservations,
- outgoing attribution,
- scan metadata,
- key-image bindings.

That authority is now enforced through three explicit signer lifecycle modules.

### 1. Vault state machine

Current state shape:

- `unregistered`
- `registered`
- `inventory_applied`
- `rescanning`
- `caught_up`
- `reset_pending_reregistration`

Responsibilities:

- vault mapping,
- birthday and scan height,
- rescan flag,
- inventory fingerprint,
- replay-required detection via inventory fingerprinting.

### 2. Output state machine

Current state shape:

- `discovered`
- `key_image_bound`
- `reserved`
- `spent_on_chain`

Responsibilities:

- wallet output payload,
- reservation lifecycle,
- spend height,
- key-image binding,
- subaddress classification.

### 3. Outgoing state machine

Current state shape:

- `signed_pending_observation`
- `observed_mempool`
- `observed_mined`
- `tx_key_validated`
- `owned_change_detected`
- `local_change_materialized`
- `key_images_ready`
- `reported`

The signer now persists those milestones directly. Bifrost still consumes them when deciding whether finalized outbound change has materialized, whether key images are ready, and whether replay is still required.

## Compatibility boundary

This model keeps protocol behavior and wire shapes stable:

- the existing HTTP API is preserved,
- persisted store fields stay schema-compatible for this refactor branch,
- `validate_outbound` remains exposed as a compatibility endpoint, but inventory replay is driven by `register_vault(tx_keys)` and `syncTrackedVaultRegistrations()`,
- ceremony origination, peer readiness, and THORChain observation submission remain in Bifrost.
