# Session 1 - XMR Product Hardening and Readiness Signal

> Date: 2026-03-25
> Focus: Harden the XMR integration after simulation success by fixing sender modeling, spendable balance reporting, keygen outputs, and chain-specific readiness signaling.

## Summary

Follow-up XMR work landed on top of the earlier simulation stabilization: THORChain now models Monero unknown senders explicitly, the Monero signer/client boundary now reports spendable balance rather than raw wallet balance, custom keygen returns the FROST-derived EdDSA vault pubkey as a first-class result, and the network query exposes `xmr_active_vault_ready` for churn-aware simulation logic. Targeted Go and Rust verification passed for the touched paths, while the broader `x/thorchain` and full `xmr-frost-signer` suites still show unrelated branch-wide failures that were not resolved in this session.

## Work Done

- Replaced direct `xmrunknown...` sentinel branching in THORChain runtime code with explicit observed-sender metadata helpers.
- Updated XMR add-liquidity, refund, and treasury-recovery paths to consume the explicit sender state instead of parsing fake addresses.
- Changed the Monero signer/client balance contract so `GetAccountByAddress` and sign-time validation use spendable balance, aligning THORChain accounting with what the signer can actually spend.
- Removed the mutable custom-keygen pool-pubkey override hook and made the FROST-derived EdDSA vault pubkey part of the `CustomKeygenResult`.
- Added `xmr_active_vault_ready` to the THORChain network response and switched the post-churn simulation actor to wait on that chain-level signal instead of re-deriving readiness from raw vault inspection.
- Patched targeted tests across `common`, `bifrost`, `x/thorchain`, `test/simulation`, and `xmr-frost-signer` to cover the new sender-state, spendable-balance, keygen-result, and readiness behavior.
- Confirmed the TAO MR request was not started: the turn was interrupted before any TAO fetch/merge work or simulation reruns for that work began.

## Discoveries

- The Monero unknown-sender sentinel is still useful as a transport encoding between scanner and chain, but product/runtime code should consume an explicit `sender known / refund required` abstraction rather than inspect the fake address directly.
- The older `insufficient xmr balance for signing` symptom was best addressed by making account and solvency reporting spendable-balance aware; sign-time checks were already close, but upstream balance reporting was still overstating availability.
- Treating the FROST-derived EdDSA vault pubkey as a first-class keygen result is cleaner than a post-hoc override and avoids fragile hidden state between the chain client and signer.
- The simulation actor should trust THORChain for chain-specific readiness. Once `xmr_active_vault_ready` exists in the network query, the simulation no longer needs to reverse-engineer XMR vault funding from the raw vault list.
- Full-suite noise remains significant on this branch. The targeted XMR paths are green, but broad `x/thorchain` and full `xmr-frost-signer` runs still include unrelated failures and are not reliable gates for just the XMR hardening work.

## Files Changed

| File | Change |
|------|--------|
| common/xmr_address.go | Added `ObservedSenderInfo` and `ResolveObservedSender` for explicit Monero sender-state handling. |
| common/tx.go | Added `Tx.ObservedSender()` so runtime consumers can query sender metadata consistently. |
| x/thorchain/handler.go | Switched Monero memo/add-liquidity sender handling to use explicit observed-sender state. |
| x/thorchain/handler_add_liquidity.go | Updated XMR add-liquidity validation/handling to tolerate unknown sender markers without product-level sentinel parsing. |
| x/thorchain/helpers.go | Updated refund path to use explicit sender metadata for XMR unknown-sender handling. |
| x/thorchain/manager_slasher_current.go | Updated failed-outbound treasury recovery logic to consume explicit sender-state resolution. |
| x/thorchain/querier.go | Added `getXMRActiveVaultReady` and populated `XmrActiveVaultReady` in the network query. |
| proto/thorchain/v1/types/query_network.proto | Added the `xmr_active_vault_ready` network response field. |
| bifrost/pkg/chainclients/shared/types/types.go | Introduced `CustomKeygenResult` to carry first-class chain-specific keygen outputs. |
| bifrost/signer/sign.go | Consumed `CustomKeygenResult` directly instead of relying on a mutable pool-pubkey override hook. |
| bifrost/pkg/chainclients/monero/client.go | Used spendable balance for sign/account checks and returned FROST-derived EdDSA pubkey in custom keygen results. |
| xmr-frost-signer/src/api/types.rs | Added `spendable_balance` to the balance API response contract. |
| xmr-frost-signer/src/api/routes.rs | Returned `spendable_balance` from `get_balance` and updated route tests accordingly. |
| test/simulation/actors/core/xmr_post_churn.go | Switched post-churn readiness gating to the new THORChain-level `xmr_active_vault_ready` signal. |
| test/simulation/actors/core/xmr_post_churn_test.go | Reworked simulation readiness tests around the new network-level contract. |

## In Progress

- The recent TAO MR on GitLab has not been fetched or merged yet; the request was interrupted before work began.
- `make test-simulation` and `make test-simulation-cluster` were not rerun after this XMR hardening patch set.
- Full `x/thorchain` and full `xmr-frost-signer` suite failures remain on the branch outside the targeted XMR verification scope.

## Next Steps

- [ ] Identify the recent TAO MR on the THORNode GitLab and merge or rebase it onto `xmr-integration`.
- [ ] Re-run `make test-simulation` under Go `1.24.6` with the XMR hardening patches plus the TAO changes.
- [ ] Re-run `make test-simulation-cluster` and validate the XMR post-churn path against the new `xmr_active_vault_ready` network signal.
- [ ] If simulations regress, inspect the spendable-balance path, custom keygen result plumbing, and XMR readiness/query contract first.
- [ ] Decide whether to isolate or separately fix the unrelated broad-suite failures in `x/thorchain` and `xmr-frost-signer`.
