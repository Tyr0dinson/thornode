# `x/thorchain` Change Analysis

> Date: 2026-03-27
> Branch: `xmr-integration`
> Diff base: `origin/develop` merge-base `b55e655552db7c01633ceaf06e17320fdabc069f`
> Scope: current branch/worktree changes under `x/thorchain/...`

## Executive Summary

The current `x/thorchain` diff is substantial but coherent. It contains 26 changed files and is dominated by four real product/workflow themes:

1. XMR unknown-sender handling for add-liquidity and refund paths.
2. EdDSA vault-address correctness for XMR, SOL, and TAO flows.
3. TAO-specific reserve, migration, and solvency behavior.
4. Query/API fixes for XMR readiness and quote generation.

Most runtime changes are justified. The highest-signal fixes are:

- explicit handling of XMR "sender unknown" observations instead of treating the marker address like a real sender;
- consistent use of `vault.GetAddress(...)` for EdDSA chains rather than `vault.PubKey.GetAddress(...)`;
- TAO existential-deposit handling during migration and outbound selection;
- staged-pool solvency handling during Ragnarök;
- the `xmr_active_vault_ready` query contract.

The low-signal or incidental changes are limited to:

- logging cleanup in `handler_deposit.go` and `handler_observed_tx_helpers.go`;
- a trivial logger string cleanup in `keeper/keeper_dummy.go`;
- test helper exposure in `alias.go`.

## Scope Summary

- Files changed: `26`
- Net diff: `905` insertions, `46` deletions
- Runtime/support files: `16`
- Test files: `10`

Largest files by added lines:

- `x/thorchain/handler_add_liquidity_test.go`
- `x/thorchain/querier_test.go`
- `x/thorchain/manager_slasher_current_test.go`
- `x/thorchain/manager_network_current_test.go`
- `x/thorchain/handler_ragnarok_test.go`

That distribution is healthy. The branch adds a large amount of targeted coverage rather than relying only on behavioral changes.

## Main Findings

### 1. XMR unknown-sender handling is the most important `x/thorchain` change set

Monero inbound observations cannot reliably recover the real sender address. Before these changes, THORChain code paths could still treat the deterministic `xmrunknown...` marker as if it were a usable asset-side address. That caused three classes of problems:

- staged add-liquidity reconciliation could see an artificial address mismatch;
- refund logic could attempt to send funds to an undeliverable marker address;
- failed swap recovery could waste outbound attempts before falling back to treasury recovery.

The fixes in this branch properly move the runtime behavior away from "trust the observed sender string" and toward "respect the fact that the sender may be unavailable."

Verdict: required.

### 2. EdDSA vault-address correctness is the second major theme

Several older code paths still derived chain addresses from `vault.PubKey`, which is only correct for secp256k1 chains. For EdDSA chains such as XMR, SOL, and TAO, the correct address source is the vault's Ed25519 key via `vault.GetAddress(chain)`.

The branch fixes that in multiple places:

- recovery swaps;
- Ragnarök handling;
- over-solvency swaps;
- vault-address queries.

Without these changes, the branch can pass some flows by accident but still encode the wrong address for EdDSA chains in edge cases.

Verdict: required.

### 3. TAO support required real `x/thorchain` changes, not just simulation tweaks

The TAO-related changes are not cosmetic. They address actual chain semantics:

- TAO requires an existential deposit to keep the account alive;
- late-stage migration/Ragnarök can temporarily make staged pools appear insolvent if pending outbounds are not accounted for correctly;
- genesis/setup must treat TAO as a supported Asgard chain if the branch expects TAO-aware vault behavior from the start.

Verdict: required.

### 4. Query-layer fixes are a mix of product contract work and correctness fixes

The `xmr_active_vault_ready` addition is a real product contract change and is justified because the simulation actor should not be reverse-engineering XMR readiness from raw vault state.

The quote-path changes are smaller but still valid:

- XMR's `DefaultCoinbase()` is fractional (`0.6`), so inbound confirmation math needs integer-safe rounding when converted into THORChain units;
- memo shortening must not run when the caller is not actually requesting a sendable memo.

Verdict: justified, with `xmr_active_vault_ready` being the most important piece.

## Runtime File Analysis

| File | What Changed | Why It Changed | Verdict |
|------|--------------|----------------|---------|
| `x/thorchain/handler.go` | `getMsgAddLiquidityFromMemo` now clears the asset-side address for XMR when the observed sender is unknown. | Prevents staged LP construction from treating the XMR unknown-sender marker as a real user address. | Required |
| `x/thorchain/handler_add_liquidity.go` | Validation and handling now tolerate XMR unknown-sender observations and preserve the real staged LP address when known from the paired side. | This is the core runtime fix for XMR add-liquidity reconciliation. Without it, staged LP flows can mismatch or reject valid XMR deposits. | Required |
| `x/thorchain/helpers.go` | `refundTx` now skips refunds to XMR unknown-sender markers unless an explicit refund address is present, and `unrefundableCoinCleanup` now donates stranded external coins into the pool/accounting path. | Prevents THORChain from generating impossible XMR refund outbounds and prevents accounting drift when such a refund must be skipped. | Required |
| `x/thorchain/manager_slasher_current.go` | Recovery swap address derivation now uses `vault.GetAddress(chain)`, and reverse swaps now fail fast when the destination is still an XMR unknown-sender marker. | Fixes EdDSA address derivation and avoids wasting failed outbound attempts on an undeliverable XMR marker address. | Required |
| `x/thorchain/handler_ragnarok.go` | Ragnarök now derives `fromAddress` using the correct signing algorithm, including `VaultPubKeyEddsa` for EdDSA chains. | Necessary for XMR Ragnarök observations and any EdDSA-chain migration path. | Required |
| `x/thorchain/manager_network_current.go` | Adds XMR and TAO to genesis support chains, uses `vault.GetAddress(chain)` for over-solvency swaps, and preserves TAO reserve balances during migration/burn logic. | This combines one XMR EdDSA-address fix with two TAO migration/support fixes. All three are behaviorally meaningful. | Required |
| `x/thorchain/manager_txout_current.go` | TAO now receives dust-threshold handling similar to XRP during outbound discovery. | TAO requires an existential deposit; THORChain must not try to spend through the reserve floor. | Required |
| `x/thorchain/handler_solvency_helpers.go` | Solvency checking now tracks pending outbounds explicitly and treats staged-pool gaps as explainable during Ragnarök when pending outbounds cover the shortage. | Fixes the late-Ragnarök false insolvency halt that surfaced on the TAO path. | Required |
| `x/thorchain/querier.go` | Fixes EdDSA vault-address enumeration in `getVaultChainAddresses` and adds `xmr_active_vault_ready` to the network response. Current worktree further tightens readiness to require both usable XMR address and non-zero XMR balance. | Both changes are real correctness fixes. The address-enumeration condition was backward for EdDSA chains, and the readiness field gives XMR-specific churn readiness a first-class contract. | Required |
| `x/thorchain/types/query_network.pb.go` | Generated protobuf artifact for `xmr_active_vault_ready`. | Required generated output to match the query contract. | Required if proto stays |
| `x/thorchain/querier_quotes.go` | Adds integer-safe conversion for fractional `DefaultCoinbase()` chains and guards memo shortening behind `sendMemo`. | The first fix matters for XMR quote confirmations; the second is a correctness fix for quote generation when a memo is not being returned. | Justified, though orthogonal to the main XMR runtime fixes |
| `x/thorchain/types/type_vault.go` | `Vaults.HasAddress` now uses `item.GetAddress(chain)` rather than `item.PubKey.GetAddress(chain)`. | Necessary for any EdDSA chain address match, including XMR, SOL, and TAO. | Required |
| `x/thorchain/handler_deposit.go` | Logging cleanup plus `hashHex` temporary for native deposit txid logging. | Improves clarity only; no meaningful behavior change. | Incidental cleanup |
| `x/thorchain/handler_observed_tx_helpers.go` | Logging cleanup for inbound processing errors. | No behavior change. | Incidental cleanup |
| `x/thorchain/keeper/keeper_dummy.go` | Logger module string now uses string concatenation instead of `fmt.Sprintf`. | Cosmetic only. | Incidental cleanup |
| `x/thorchain/alias.go` | Re-exports `GetRandomEd25519PubKey`. | Test support only. | Support-only |

## Test File Analysis

| File | What Changed | Why It Matters | Verdict |
|------|--------------|----------------|---------|
| `x/thorchain/handler_add_liquidity_test.go` | Adds comprehensive XMR add-liquidity tests covering RUNE-first, asset-first, validation, and staged reconciliation. Also adds `GetPools` to the test keeper. | This is the best coverage in the diff. It proves the unknown-sender model works across the actual LP lifecycle rather than only one narrow function. | Required coverage |
| `x/thorchain/handler_observed_txin_test.go` | Adds XMR refund test showing refunds are skipped for unknown senders unless an explicit refund address is present. | Directly covers the refund safety change. | Required coverage |
| `x/thorchain/handler_ragnarok_test.go` | Adds an XMR Ragnarök happy-path test using EdDSA vault addresses. | Proves the new EdDSA address derivation path works in a real handler flow. | Required coverage |
| `x/thorchain/handler_solvency_test.go` | Adds staged-pool TAO solvency tests covering both the explained-gap and unexplained-gap cases during Ragnarök. | Strong evidence that the late-Ragnarök solvency fix is targeted rather than over-broad. | Required coverage |
| `x/thorchain/handler_test.go` | Adds memo-to-liquidity conversion test for XMR unknown-sender observations. | Covers the earliest translation step in the XMR LP flow. | Required coverage |
| `x/thorchain/manager_network_current_test.go` | Adds over-solvency swap test proving XMR uses the vault's EdDSA address. | Good regression test for `vault.GetAddress(chain)` adoption. | Required coverage |
| `x/thorchain/manager_slasher_current_test.go` | Adds recovery-swap test proving EdDSA vault addresses are used when queueing recovery swaps. | Confirms the XMR recovery path does not regress back to secp256k1 assumptions. | Required coverage |
| `x/thorchain/querier_test.go` | Adds `xmr_active_vault_ready` assertions and XMR quote coverage ensuring quote swap memo behavior is sane for XMR. Current worktree also adds tighter readiness tests for "funds + address required". | Important coverage for both the query contract and the XMR quote fix. | Required coverage |
| `x/thorchain/types/type_vault_test.go` | Adds `Vaults.HasAddress` coverage for XMR/EdDSA. | Validates the shared vault address fix. | Required coverage |
| `x/thorchain/types/type_vault_v2_test.go` | Extends EdDSA address tests to TAO. | Confirms `Vault.GetAddress` works for TAO's address format as well. | Required coverage |

## Change Buckets and Necessity

### Clearly Required For XMR Correctness

- `handler.go`
- `handler_add_liquidity.go`
- `helpers.go`
- `manager_slasher_current.go`
- `handler_ragnarok.go`
- `querier.go`
- `querier_quotes.go` (confirmation math portion)
- `types/type_vault.go`

These are the changes that directly close XMR product/runtime gaps.

### Clearly Required For TAO Correctness

- `manager_network_current.go`
- `manager_txout_current.go`
- `handler_solvency_helpers.go`
- `types/type_vault.go` and `querier.go` where EdDSA/TAO address derivation matters indirectly

These are the changes that make TAO behave like a real supported chain rather than only a simulation toggle.

### Required Generated / Contract Support

- `types/query_network.pb.go`

This is generated code, but it is required as long as the query contract includes `xmr_active_vault_ready`.

### Support-Only or Incidental

- `alias.go`
- `handler_deposit.go`
- `handler_observed_tx_helpers.go`
- `keeper/keeper_dummy.go`

These are not carrying the branch from red to green on their own.

## Risks and Observations

### 1. `xmr_active_vault_ready` is now a product contract

This is a good change, but it means simulation logic and any external consumers may start treating the field as authoritative. The current worktree tightens it to "active vault + usable XMR address + non-zero XMR funds in THORChain vault state," which is a better definition than "some active XMR vault exists." If the field remains, its semantics should stay explicit and stable.

### 2. The external-coin cleanup path is broader than just XMR

The `helpers.go` change that donates unrefundable external coins back into pool accounting is justified by the XMR unknown-sender case, but it affects all external assets routed through `unrefundableCoinCleanup`. That is still reasonable, but it is broader in blast radius than the rest of the XMR patch set and deserves careful review.

### 3. The quote changes are valid but only partly XMR-driven

`querier_quotes.go` contains one directly XMR-motivated fix and one general quote-correctness fix (`sendMemo` guard). They are both reasonable, but the second one should be understood as a generic bug fix rather than an XMR-specific requirement.

### 4. Test coverage is strong where it matters

The branch does a good job of adding focused tests for:

- XMR add-liquidity reconciliation;
- XMR refund safety;
- EdDSA vault-address selection;
- TAO solvency edge cases;
- XMR readiness/query behavior.

That coverage reduces the risk that these fixes regress during later refactors.

## Recommended Commit Grouping

If this branch needs cleanup before merge, the `x/thorchain` changes naturally split into four reviewable groups:

1. XMR unknown-sender handling
   - `handler.go`
   - `handler_add_liquidity.go`
   - `helpers.go`
   - `manager_slasher_current.go`
   - related tests

2. EdDSA vault-address correctness
   - `handler_ragnarok.go`
   - `manager_network_current.go` (address-selection portion)
   - `types/type_vault.go`
   - `querier.go` (`getVaultChainAddresses`)
   - related tests

3. TAO migration and solvency
   - `manager_network_current.go` (TAO support/reserve portions)
   - `manager_txout_current.go`
   - `handler_solvency_helpers.go`
   - related tests

4. Query/API contract changes
   - `querier.go`
   - `types/query_network.pb.go`
   - `querier_quotes.go`
   - related tests

The incidental logging/test-helper changes can be split out if a tighter MR is desired.

## Bottom Line

The `x/thorchain` diff is not random churn. It is a mostly coherent set of product fixes required to make XMR and TAO behave correctly in THORChain runtime paths.

If I had to reduce the diff, I would keep all behavior-changing files except:

- `handler_deposit.go`
- `handler_observed_tx_helpers.go`
- `keeper/keeper_dummy.go`
- `alias.go`

Everything else looks justified by either:

- XMR sender ambiguity,
- EdDSA vault-address correctness,
- TAO reserve/ragnarok semantics,
- or the new XMR readiness/query contract.
