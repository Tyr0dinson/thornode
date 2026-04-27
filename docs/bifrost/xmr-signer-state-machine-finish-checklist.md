# XMR Signer State Machine Finish Checklist

## Purpose

This is the completion record for the signer-first Monero refactor.

The baseline behavior contract remains in `docs/bifrost/xmr-signer-state-machine-baseline.md`.
This checklist tracks the parity gate and the deletion pass that are now complete.

Current status:

- signer lifecycle modules own vaults, outputs, and outgoings,
- tx-key replay is inventory-driven through `register_vault(tx_keys)` and `syncTrackedVaultRegistrations()`,
- the old per-tx replay loop and fallback reconstruction paths are removed from the Monero client sync path,
- `cargo test` for `xmr-frost-signer` passes,
- `go test ./bifrost/pkg/chainclients/monero/...` passes.

## Phase 1: Reach full parity with signer-owned state

- [x] Persist the remaining outbound lifecycle facts in the signer:
  - `local_change_materialized`
  - `key_images_ready`
  - stable pending reason for unresolved outbounds
- [x] Expose those facts through the signer read path used by Bifrost without breaking the current HTTP contract.
- [x] Move finalized outbound readiness decisions in `client.go` onto signer-owned outgoing state so Bifrost does not rediscover them by revalidation.
- [x] Remove the need for `resolveTrackedXMROutboundChange()` to call `validate_outbound` when signer outgoing state already has enough information.
- [x] Replace Bifrost tx-key replay inference with signer-owned replay truth so `txKeyCatchupNeedsReplay()` no longer depends on legacy transfer reconstruction during healthy operation.
- [x] Replace Bifrost solvency pending-outbound interpretation with signer-owned pending state so `xmrSolvencyHasPendingOutboundWork()` stops interpreting raw lifecycle state locally.
- [x] Finish vault lifecycle cutover:
  - register
  - inventory apply
  - rescan start/continue
  - rescan completion at tip
  - scan height advance
  - reset
- [x] Either wire `ResetPendingReregistration` and `start_or_continue_rescan()` into live flow or delete them.
- [x] Finish output lifecycle cutover so all production output mutation paths go through `src/state/lifecycle/output.rs`.
- [x] Confirm there are no production direct outgoing, output, or vault state writes outside `src/state/lifecycle/` except low-level persistence primitives in `store.rs`.

## Phase 1 test and parity gate

- [x] Add signer tests for outbound lifecycle milestones beyond `has_owned_change`, especially:
  - mined before local change materializes
  - local change materializes after tx-key validation
  - key-images-ready after tracked outputs are fully bound
  - stale outbound pruning with the richer audit state
- [x] Add signer tests for vault lifecycle edge cases:
  - birthday rewind
  - rescan-at-tip completion
  - reset followed by re-registration
  - inventory replay after restart
- [x] Add signer tests for output lifecycle edge cases:
  - reservation conflict and release
  - reservation finalize with bound tx hash
  - apply-key-images reconcile replay
  - metadata-only spend unwind
- [x] Keep route regression coverage green for:
  - `register_vault`
  - `scanner_sync`
  - `scanner_reset`
  - `validate_outbound`
  - `apply_key_images`
  - `sign_prepare`
  - `sign_combine`
- [x] Extend Go parity coverage for:
  - outbound with owned change
  - outbound without owned change
  - tx-keys arriving late
  - tracked change outputs materializing late
  - final outbound key-image readiness
  - MIGRATE handling
  - rescan and replay ordering
  - non-signer validation behavior
- [x] Parity gate:
  - `cargo test` in `bifrost/pkg/chainclients/monero/xmr-frost-signer` passes
  - `go test ./bifrost/pkg/chainclients/monero/...` passes
  - no production branch still needs legacy transfer reconstruction to answer a signer-truth question
  - no production branch still mutates vault, output, or outgoing state outside lifecycle modules

## Phase 2: Remove old code paths after parity

- [x] Remove legacy tx-key replay fallback logic from Bifrost once signer outgoing state is sufficient.
- [x] Remove redundant outbound revalidation fallbacks used only to rediscover signer-owned facts.
- [x] Remove dead status branches and duplicated readiness checks in `client.go`.
- [x] Remove unused lifecycle states, helpers, and compatibility scaffolding left behind after cutover.
- [x] Remove test fixtures that only exist to preserve deleted transitional paths.
- [x] Trim dead fields and dead methods surfaced by compiler warnings once the old paths are gone.

## Phase 2 cleanup gate

- [x] `cargo test` in `bifrost/pkg/chainclients/monero/xmr-frost-signer` passes after deletion.
- [x] `go test ./bifrost/pkg/chainclients/monero/...` passes after deletion.
- [x] The signer remains the source of truth for:
  - vault sync state
  - tracked output lifecycle
  - outgoing lifecycle
- [x] Bifrost remains limited to:
  - ceremony origination
  - peer readiness
  - observation submission
  - operational scheduling and retries

## Definition of done

- [x] Normal flow, rescan flow, and the currently supported edge cases still match the baseline contract.
- [x] Signer snapshots are sufficient for Bifrost to decide outbound readiness without reconstructing signer truth from timing and retries.
- [x] The remaining Monero client code paths are simpler because old fallback branches have been deleted, not because the same logic was moved around.
- [x] The baseline doc is updated to describe the final model rather than the transitional one.
