# `x/thorchain` Change Analysis

> Date: 2026-03-27
> Branch: `xmr-integration`
> Diff base: `origin/develop` merge-base `b55e655552db7c01633ceaf06e17320fdabc069f`
> Scope: current branch changes under `x/thorchain/...` after removing the unrelated TAO MR

## Executive Summary

The current `x/thorchain` diff is coherent and XMR-focused. It contains `22` changed files with `779` insertions and `34` deletions. The changes cluster into four themes:

1. XMR unknown-sender handling for add-liquidity, refunds, and failed-swap recovery.
2. EdDSA vault-address correctness for XMR-facing flows.
3. Query/API correctness for XMR readiness and quote generation.
4. Targeted tests that lock in the new XMR behavior.

Most behavior-changing files are justified. The highest-signal fixes are:

- stop treating the deterministic `xmrunknown...` marker as a usable customer address;
- derive vault addresses through `vault.GetAddress(chain)` for EdDSA chains instead of `vault.PubKey.GetAddress(chain)`;
- expose a first-class `xmr_active_vault_ready` query contract; and
- make quote confirmation math safe for XMR's fractional `DefaultCoinbase()`.

The low-signal changes are small and localized:

- logging cleanup in `handler_deposit.go` and `handler_observed_tx_helpers.go`;
- a trivial logger string cleanup in `keeper/keeper_dummy.go`;
- test-helper exposure in `alias.go`.

## Scope Summary

- Files changed: `22`
- Net diff: `779` insertions, `34` deletions
- Runtime/support files: `14`
- Test files: `8`

Largest additions are concentrated in tests:

- `x/thorchain/handler_add_liquidity_test.go`
- `x/thorchain/querier_test.go`
- `x/thorchain/manager_slasher_current_test.go`
- `x/thorchain/manager_network_current_test.go`
- `x/thorchain/handler_ragnarok_test.go`

That is the right shape for this branch: the diff is backed by focused regression coverage, not just runtime edits.

## Main Findings

### 1. XMR unknown-sender handling is the core runtime change

Monero inbound observations cannot reliably recover the user's sender address. Before this branch, several THORChain paths could still treat the deterministic `xmrunknown...` marker as if it were a deliverable external address. That creates three real problems:

- staged add-liquidity reconciliation can report artificial address mismatches;
- refund logic can attempt impossible outbounds;
- failed-swap recovery can waste retries on an undeliverable destination before falling back to treasury recovery.

This branch fixes that by carrying sender state explicitly and by treating "sender unknown" as a first-class condition inside `x/thorchain`.

Verdict: required.

### 2. EdDSA vault-address correctness is the second major theme

Several older runtime paths still derived external addresses from `vault.PubKey`, which is correct for secp256k1 chains but wrong for XMR. The correct source for XMR is the Ed25519 vault identity through `vault.GetAddress(chain)`.

The branch fixes that in:

- failed-swap recovery;
- Ragnarök address derivation;
- over-solvency swap generation; and
- shared vault address lookup helpers.

Verdict: required.

### 3. Query-layer changes are real product-contract work

`xmr_active_vault_ready` is not a cosmetic field. It gives consumers a chain-level readiness contract instead of forcing them to infer XMR churn readiness from raw vault state. The tightened semantics in the current branch are sensible: an active vault must have both a usable XMR address and non-zero XMR funds recorded in THORChain state.

The quote-path changes are smaller but valid:

- XMR confirmation math now handles a fractional `DefaultCoinbase()` without truncation to zero;
- memo shortening only happens when a sendable memo is actually requested.

Verdict: justified.

## Runtime File Analysis

| File | What Changed | Why It Changed | Verdict |
|------|--------------|----------------|---------|
| `x/thorchain/handler.go` | `getMsgAddLiquidityFromMemo` now clears the asset-side address when an XMR observation says the sender is unknown. | Prevents memo-to-message translation from hard-coding the XMR unknown-sender marker as the LP's asset address. | Required |
| `x/thorchain/handler_add_liquidity.go` | Validation and reconciliation now tolerate XMR unknown-sender observations and preserve staged addresses when the real address is known from the paired side. | This is the main XMR LP correctness fix. Without it, staged liquidity can mismatch or reject otherwise valid XMR deposits. | Required |
| `x/thorchain/helpers.go` | `refundTx` now skips refunds to the XMR unknown-sender marker unless an explicit refund address is present, and `unrefundableCoinCleanup` donates stranded external coins into pool accounting. | Prevents impossible XMR refunds and keeps accounting aligned when an external refund cannot be delivered. | Required |
| `x/thorchain/manager_slasher_current.go` | Recovery swap tracking now uses `vault.GetAddress(chain)`, and reverse swaps fail fast if the destination is still the XMR unknown-sender marker. | Fixes EdDSA address derivation and avoids burning retries on undeliverable XMR reverse swaps. | Required |
| `x/thorchain/handler_ragnarok.go` | Ragnarök now derives `fromAddress` by signing algorithm, including `VaultPubKeyEddsa` for EdDSA chains. | Necessary for XMR outbounds observed during Ragnarök and similar EdDSA paths. | Required |
| `x/thorchain/manager_network_current.go` | Genesis support now includes XMR, and over-solvency swaps derive addresses through `vault.GetAddress(chain)`. | The XMR chain must be present in supported-chain setup, and over-solvency swaps must use the EdDSA vault address for XMR. | Required |
| `x/thorchain/querier.go` | Fixes EdDSA chain-address enumeration and adds `xmr_active_vault_ready`, with readiness tightened to "usable address + non-zero XMR funds". | Both are correctness fixes and together define a cleaner XMR readiness contract. | Required |
| `x/thorchain/types/query_network.pb.go` | Generated protobuf output for `xmr_active_vault_ready`. | Required generated artifact for the query contract. | Required if proto stays |
| `x/thorchain/querier_quotes.go` | Adds integer-safe conversion for fractional `DefaultCoinbase()` chains and guards memo shortening behind `sendMemo`. | The first fix is directly XMR-motivated; the second is a general quote-correctness improvement in the same path. | Justified |
| `x/thorchain/types/type_vault.go` | `Vaults.HasAddress` now uses `item.GetAddress(chain)` rather than `item.PubKey.GetAddress(chain)`. | Shared EdDSA correctness fix needed for XMR vault address matching. | Required |
| `x/thorchain/handler_deposit.go` | Logging cleanup plus a `hashHex` temporary for native deposit txid logging. | No meaningful behavior change. | Incidental cleanup |
| `x/thorchain/handler_observed_tx_helpers.go` | Logging cleanup for inbound processing errors. | No behavior change. | Incidental cleanup |
| `x/thorchain/keeper/keeper_dummy.go` | Logger module string now uses concatenation instead of `fmt.Sprintf`. | Cosmetic only. | Incidental cleanup |
| `x/thorchain/alias.go` | Re-exports `GetRandomEd25519PubKey`. | Test support only. | Support-only |

## Test File Analysis

| File | What Changed | Why It Matters | Verdict |
|------|--------------|----------------|---------|
| `x/thorchain/handler_add_liquidity_test.go` | Adds comprehensive XMR add-liquidity tests covering RUNE-first, asset-first, validation, and staged reconciliation. | Best coverage in the diff. It exercises the real LP lifecycle rather than a single helper. | Required coverage |
| `x/thorchain/handler_observed_txin_test.go` | Adds an XMR refund test showing refunds are skipped for unknown senders unless an explicit refund address is present. | Directly covers the refund safety change. | Required coverage |
| `x/thorchain/handler_ragnarok_test.go` | Adds an XMR Ragnarök path using the EdDSA vault address. | Proves the new EdDSA address derivation works in a real handler flow. | Required coverage |
| `x/thorchain/handler_test.go` | Adds memo-to-liquidity translation coverage for XMR unknown-sender observations. | Covers the earliest translation step in the XMR LP flow. | Required coverage |
| `x/thorchain/manager_network_current_test.go` | Adds an over-solvency swap test proving XMR uses the vault's EdDSA address. | Good regression test for `vault.GetAddress(chain)` in an XMR network-manager path. | Required coverage |
| `x/thorchain/manager_slasher_current_test.go` | Adds recovery-swap coverage proving EdDSA vault addresses are used when queueing recovery swaps. | Confirms the XMR recovery path no longer assumes secp256k1 vault addresses. | Required coverage |
| `x/thorchain/querier_test.go` | Adds `xmr_active_vault_ready` assertions and quote coverage around XMR confirmation behavior and memo handling. | Important coverage for the new query contract and quote fix. | Required coverage |
| `x/thorchain/types/type_vault_test.go` | Adds `Vaults.HasAddress` coverage for XMR/EdDSA. | Validates the shared vault address fix. | Required coverage |

## Change Buckets and Necessity

### Clearly Required For XMR Correctness

- `handler.go`
- `handler_add_liquidity.go`
- `helpers.go`
- `manager_slasher_current.go`
- `handler_ragnarok.go`
- `manager_network_current.go`
- `querier.go`
- `querier_quotes.go` (confirmation math portion)
- `types/type_vault.go`

These are the files that directly close XMR product or runtime gaps.

### Required Generated / Contract Support

- `types/query_network.pb.go`

This is generated code, but it is required as long as the query response includes `xmr_active_vault_ready`.

### Support-Only or Incidental

- `alias.go`
- `handler_deposit.go`
- `handler_observed_tx_helpers.go`
- `keeper/keeper_dummy.go`

These are real changes, but they are not what carries the branch.

## Risks and Observations

### 1. `xmr_active_vault_ready` is now a product contract

That is a good direction, but it means the field should stay explicit and stable. The current semantics are reasonable: active XMR vault, usable XMR address, and non-zero XMR funds recorded on the vault in THORChain state.

### 2. `unrefundableCoinCleanup` is broader than just XMR

The pool-accounting donation path is justified by the XMR unknown-sender case, but it applies to external assets generally. That broader blast radius is acceptable, but it deserves review with that in mind.

### 3. `querier_quotes.go` mixes one XMR fix with one generic cleanup

The fractional-coinbase confirmation fix is specifically relevant to XMR. The `sendMemo` guard is a generic quote-path correctness improvement that simply landed in the same file.

### 4. Coverage is strongest where the branch is riskiest

The branch adds focused tests for:

- XMR LP reconciliation;
- XMR refund safety;
- EdDSA vault-address selection;
- XMR readiness/query behavior.

That materially reduces regression risk.

## Recommended Review Grouping

If reviewers want to mentally split the `x/thorchain` diff, it breaks down cleanly into:

1. XMR unknown-sender handling
   - `handler.go`
   - `handler_add_liquidity.go`
   - `helpers.go`
   - `manager_slasher_current.go`
   - related tests

2. EdDSA vault-address correctness
   - `handler_ragnarok.go`
   - `manager_network_current.go`
   - `types/type_vault.go`
   - `querier.go` (`getVaultChainAddresses`)
   - related tests

3. Query/API contract work
   - `querier.go`
   - `types/query_network.pb.go`
   - `querier_quotes.go`
   - related tests

4. Incidental cleanup and test support
   - `alias.go`
   - `handler_deposit.go`
   - `handler_observed_tx_helpers.go`
   - `keeper/keeper_dummy.go`

## Bottom Line

The current `x/thorchain` diff is not random churn. It is a coherent XMR-focused set of runtime fixes plus targeted coverage.

If the branch needs trimming before merge, I would keep every behavior-changing file and treat only these as optional cleanup or support:

- `handler_deposit.go`
- `handler_observed_tx_helpers.go`
- `keeper/keeper_dummy.go`
- `alias.go`

Everything else is justified by:

- XMR sender ambiguity,
- EdDSA vault-address correctness,
- or the XMR readiness/query contract.
