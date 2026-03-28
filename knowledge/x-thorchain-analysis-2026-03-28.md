# `x/thorchain` Change Analysis

> Date: 2026-03-28
> Branch: `xmr-integration`
> Primary diff base for branch-authored changes: merge-base with `origin/develop` `b55e655552db7c01633ceaf06e17320fdabc069f`
> Raw comparison target: current `origin/develop`
> Scope: current branch plus current worktree changes under `x/thorchain/...`

## Executive Summary

There are two different answers depending on how the diff is measured:

1. Branch-authored plus current worktree changes: `24` files under `x/thorchain`.
2. Raw diff to current `origin/develop`: `31` files under `x/thorchain`.

The extra `7` files in the raw diff are not part of the XMR branch itself. They are upstream drift caused by this branch being older than current `origin/develop`. They should not be justified as XMR work.

The `24` branch/worktree files are coherent and mostly justified. They fall into five buckets:

1. XMR unknown-sender handling
2. EdDSA vault-address correctness
3. XMR readiness and quote/query correctness
4. Late-Ragnarok solvency false-positive reduction
5. Targeted regression tests

The only low-signal files in the branch-authored set are:

- `x/thorchain/alias.go`
- `x/thorchain/handler_deposit.go`
- `x/thorchain/handler_observed_tx_helpers.go`
- `x/thorchain/keeper/keeper_dummy.go`

Everything else has a concrete product or simulation-correctness reason.

## Diff Hygiene

### Branch-Authored / Current Worktree Scope

These are the files that belong to the XMR branch itself, including the current unstaged solvency helper work:

- `x/thorchain/alias.go`
- `x/thorchain/handler.go`
- `x/thorchain/handler_add_liquidity.go`
- `x/thorchain/handler_add_liquidity_test.go`
- `x/thorchain/handler_deposit.go`
- `x/thorchain/handler_observed_tx_helpers.go`
- `x/thorchain/handler_observed_txin_test.go`
- `x/thorchain/handler_ragnarok.go`
- `x/thorchain/handler_ragnarok_test.go`
- `x/thorchain/handler_solvency_helpers.go`
- `x/thorchain/handler_solvency_test.go`
- `x/thorchain/handler_test.go`
- `x/thorchain/helpers.go`
- `x/thorchain/keeper/keeper_dummy.go`
- `x/thorchain/manager_network_current.go`
- `x/thorchain/manager_network_current_test.go`
- `x/thorchain/manager_slasher_current.go`
- `x/thorchain/manager_slasher_current_test.go`
- `x/thorchain/querier.go`
- `x/thorchain/querier_quotes.go`
- `x/thorchain/querier_test.go`
- `x/thorchain/types/query_network.pb.go`
- `x/thorchain/types/type_vault.go`
- `x/thorchain/types/type_vault_test.go`

### Upstream Drift Present In Raw `origin/develop` Diff

These files appear only when comparing the branch directly to today's `origin/develop`. They are not branch-authored XMR work:

- `x/thorchain/grpc_query.go`
- `x/thorchain/manager_pool_current.go`
- `x/thorchain/module.go`
- `x/thorchain/types/jsonpb_marshalers.go`
- `x/thorchain/types/query.pb.go`
- `x/thorchain/types/query.pb.gw.go`
- `x/thorchain/types/query_supply.pb.go`

Interpretation:

- these files should be treated as "branch needs rebase" noise;
- they should not be used to evaluate whether the XMR branch itself is well-scoped;
- if the branch is rebased, most or all of this raw diff should disappear.

## Main Findings

### 1. XMR unknown-sender handling is the most important runtime change

Monero observations cannot reliably recover the real external sender address. Before this branch, multiple THORChain paths could still treat the deterministic `xmrunknown...` marker as if it were a real deliverable address.

That is wrong in three places:

- add-liquidity staging and reconciliation;
- refund construction;
- failed-swap recovery / reverse-swap handling.

The current branch fixes this by making `x/thorchain` treat "sender unknown" as a real condition rather than as a usable address string.

Verdict: required.

### 2. EdDSA vault-address correctness is the second major theme

Some older code paths still derived addresses from `vault.PubKey`, which is fine for secp256k1 chains but wrong for XMR. For XMR-facing logic the correct source is the chain-aware address derived from the vault itself, via `vault.GetAddress(chain)`.

This change matters in:

- Ragnarök outbounds;
- recovery swaps;
- over-solvency swap generation;
- vault-address lookup helpers.

Verdict: required.

### 3. Query and quote changes are real product-contract work

`xmr_active_vault_ready` is not cosmetic. It gives the rest of the system a first-class readiness signal for XMR instead of forcing simulation and clients to infer readiness from raw vault state.

The quote-path changes are also justified:

- XMR confirmation math now handles a fractional `DefaultCoinbase()` safely;
- memo shortening only occurs when a sendable memo is actually requested.

Verdict: justified.

### 4. The new solvency changes are not XMR-only, but they are justified

The latest local changes in `handler_solvency_helpers.go` and `handler_solvency_test.go` are not introducing a blanket waiver. They narrow a specific false positive:

- if a vault looks short only because Ragnarök outbounds are still pending;
- and the pending outbound amount fully explains the gap;
- and the asset is already in Ragnarök or effectively unpriceable in the relevant pool state;

then solvency should not fire.

That is a real late-Ragnarök accounting issue, not a test bypass.

Verdict: justified, but broader than XMR.

## File-By-File Analysis

| File | What Changed | Why It Changed | Verdict |
|------|--------------|----------------|---------|
| `x/thorchain/handler.go` | `getMsgAddLiquidityFromMemo` clears the asset-side address when the observed XMR sender is unknown. | Prevents memo translation from carrying the XMR unknown-sender marker into downstream LP handling as if it were a real address. | Required |
| `x/thorchain/handler_add_liquidity.go` | Validation and staged-liquidity reconciliation now tolerate XMR unknown-sender observations and preserve the known address from the paired side. | This is the main XMR LP correctness fix. Without it, valid XMR liquidity adds can mismatch or reject during staged reconciliation. | Required |
| `x/thorchain/helpers.go` | Refund logic skips impossible refunds to the XMR unknown-sender marker unless an explicit refund address is supplied; unrefundable external coins are cleaned up into pool accounting. | Prevents impossible XMR refunds and keeps accounting aligned when THORChain cannot construct a deliverable outbound. | Required |
| `x/thorchain/manager_slasher_current.go` | Recovery swap handling now derives addresses through `vault.GetAddress(chain)` and skips reverse-swap attempts when the destination is still the unknown XMR sender marker. | Fixes EdDSA address derivation and avoids wasting retries on undeliverable XMR reverse swaps. | Required |
| `x/thorchain/handler_ragnarok.go` | Ragnarök derives the outbound source address from the correct signing algorithm, including EdDSA vault pubkeys. | XMR Ragnarök outbounds must use the EdDSA-derived vault address, not secp256k1 assumptions. | Required |
| `x/thorchain/manager_network_current.go` | Adds XMR-aware supported-chain behavior and uses `vault.GetAddress(chain)` for over-solvency swap creation. | XMR needs to be included in chain-aware network-manager behavior, and over-solvency swaps must use the real EdDSA vault address. | Required |
| `x/thorchain/querier.go` | Adds `xmr_active_vault_ready` and fixes vault chain-address enumeration for EdDSA/XMR. | This creates a proper XMR readiness contract and prevents query consumers from seeing the wrong vault address on EdDSA chains. | Required |
| `x/thorchain/querier_quotes.go` | Uses integer-safe confirmation math for fractional `DefaultCoinbase()` chains and only shortens memos when `sendMemo` is requested. | The confirmation math is directly relevant to XMR; the memo guard is a small general correctness fix in the same path. | Justified |
| `x/thorchain/types/type_vault.go` | `Vaults.HasAddress` uses `item.GetAddress(chain)` instead of `item.PubKey.GetAddress(chain)`. | Shared EdDSA correctness fix needed by XMR vault-address matching. | Required |
| `x/thorchain/handler_solvency_helpers.go` | Solvency logic now tracks pending outbounds by asset and suppresses insolvency only when late-Ragnarök pending outbounds fully explain a missing-pool or unpriceable shortfall. | This removes a specific false positive seen late in simulation. It does not ignore real shortfalls; it recognizes that pending Ragnarök exits can explain them. | Justified |
| `x/thorchain/handler_deposit.go` | Logging cleanup around txid collision handling. | No meaningful runtime behavior change tied to XMR. | Incidental cleanup |
| `x/thorchain/handler_observed_tx_helpers.go` | Logging cleanup. | No meaningful runtime behavior change tied to XMR. | Incidental cleanup |
| `x/thorchain/keeper/keeper_dummy.go` | Cosmetic logger string cleanup. | No behavior change. | Incidental cleanup |
| `x/thorchain/alias.go` | Re-exports `GetRandomEd25519PubKey`. | Test support only. | Support-only |
| `x/thorchain/types/query_network.pb.go` | Generated protobuf output for `xmr_active_vault_ready`. | Required generated artifact if the network response exposes that field. | Required generated code |

## Test Coverage Analysis

| File | What Changed | Why It Matters | Verdict |
|------|--------------|----------------|---------|
| `x/thorchain/handler_add_liquidity_test.go` | Adds XMR add-liquidity coverage for RUNE-first, asset-first, validation, and staged reconciliation. | Best coverage in the branch. It exercises the real LP lifecycle rather than a single helper. | Required coverage |
| `x/thorchain/handler_observed_txin_test.go` | Adds refund behavior coverage for unknown XMR senders unless an explicit refund address exists. | Directly covers the refund safety change. | Required coverage |
| `x/thorchain/handler_ragnarok_test.go` | Adds an XMR Ragnarök path proving the EdDSA vault address is used. | Covers the handler flow that would otherwise silently use the wrong address type. | Required coverage |
| `x/thorchain/handler_solvency_test.go` | Adds late-Ragnarök solvency tests for missing-pool, unpriceable, and uncovered shortfall cases. | Important because the helper now suppresses only a narrow class of false positives; these tests prove that line is intentional. | Required coverage |
| `x/thorchain/handler_test.go` | Adds coverage for memo-to-liquidity translation with XMR unknown-sender observations. | Covers the first stage where the sender marker could leak into runtime handling. | Required coverage |
| `x/thorchain/manager_network_current_test.go` | Adds XMR over-solvency swap coverage and related network-manager behavior checks. | Confirms that XMR uses the EdDSA vault address in a real network-manager path. | Required coverage |
| `x/thorchain/manager_slasher_current_test.go` | Adds recovery-swap coverage proving EdDSA vault addresses are used when queueing recovery swaps. | Locks in the XMR recovery-path fix. | Required coverage |
| `x/thorchain/querier_test.go` | Adds `xmr_active_vault_ready` coverage and XMR-related quote/query assertions. | Important coverage for the new XMR readiness contract. | Required coverage |
| `x/thorchain/types/type_vault_test.go` | Adds XMR / EdDSA coverage for `Vaults.HasAddress`. | Validates the shared vault-address fix. | Required coverage |

## Necessity Matrix

### Keep: clearly required for XMR correctness

- `x/thorchain/handler.go`
- `x/thorchain/handler_add_liquidity.go`
- `x/thorchain/helpers.go`
- `x/thorchain/manager_slasher_current.go`
- `x/thorchain/handler_ragnarok.go`
- `x/thorchain/manager_network_current.go`
- `x/thorchain/querier.go`
- `x/thorchain/querier_quotes.go`
- `x/thorchain/types/type_vault.go`
- `x/thorchain/types/query_network.pb.go`

### Keep: justified non-XMR runtime correctness

- `x/thorchain/handler_solvency_helpers.go`

### Keep: targeted regression coverage

- `x/thorchain/handler_add_liquidity_test.go`
- `x/thorchain/handler_observed_txin_test.go`
- `x/thorchain/handler_ragnarok_test.go`
- `x/thorchain/handler_solvency_test.go`
- `x/thorchain/handler_test.go`
- `x/thorchain/manager_network_current_test.go`
- `x/thorchain/manager_slasher_current_test.go`
- `x/thorchain/querier_test.go`
- `x/thorchain/types/type_vault_test.go`

### Optional to split out if the MR needs to be tighter

- `x/thorchain/alias.go`
- `x/thorchain/handler_deposit.go`
- `x/thorchain/handler_observed_tx_helpers.go`
- `x/thorchain/keeper/keeper_dummy.go`

## Review Notes

### What I would defend strongly

- explicit handling of XMR sender ambiguity;
- all `vault.GetAddress(chain)` conversions in XMR-facing paths;
- `xmr_active_vault_ready`;
- the quote confirmation math fix for XMR;
- the late-Ragnarök solvency false-positive reduction.

### What I would not spend political capital on

- helper/test exposure in `alias.go`;
- minor log cleanup in `handler_deposit.go`;
- minor log cleanup in `handler_observed_tx_helpers.go`;
- cosmetic cleanup in `keeper/keeper_dummy.go`.

### What should be called out in review to avoid confusion

- A raw diff against today's `origin/develop` includes non-XMR query/proto drift that is not authored by this branch.
- The clean way to evaluate the XMR work is against the merge-base, plus the two current solvency files that are still unstaged.

## Simulation Integrity

This report is about `x/thorchain`, but one review question is broader: did the green simulation runs come from real fixes, or did we relax the tests until they passed?

My answer is: we did not cheat the tests in the usual sense.

What we did **not** do:

- we did not add `t.Skip` or equivalent test skips to avoid failing paths;
- we did not remove DAG actors or reduce the scenario breadth;
- we did not hardcode success markers or bypass failure handling;
- we did not turn failing runtime checks into unconditional passes.

What actually happened instead:

- startup and chain-client readiness issues were fixed so the simulation could exercise the real paths;
- Solana confirmation handling was tightened to require stronger observation before success;
- TRON and Solana broadcast retry behavior was made resilient to transient RPC and transport faults;
- XMR, Ragnarök, and solvency flows were corrected so the simulation stopped flagging false failures that did not reflect a real protocol bug.

The two places that are worth scrutinizing, because they intentionally narrow false positives rather than just fixing startup mechanics, are:

1. `x/thorchain/handler_solvency_helpers.go`

   Late-Ragnarök solvency gaps are now ignored only when pending outbounds fully explain the apparent shortfall. That is not a blanket waiver. It is a targeted rule for the case where THORChain has already scheduled the asset outflow and the wallet delta is therefore expected.

2. `test/simulation/actors/core/check_solvency.go`

   The simulation solvency actor now treats L1 excess as still solvent instead of requiring exact equality. That is a deliberate narrowing of what the simulation flags. I believe it is correct, because solvency should detect shortfall rather than harmless overfunding or observation lag, but it is still a judgment call and should be reviewed as such.

So the honest framing is:

- no fake green;
- no skipped coverage;
- no forced-success harness changes;
- but yes, there are targeted false-positive reductions in solvency handling, and those should be reviewed explicitly rather than treated as invisible infrastructure fixes.

## Bottom Line

The current `x/thorchain` branch/worktree changes are mostly well-justified and XMR-focused.

If the goal is to trim the branch to the minimum credible set:

- keep every behavior-changing XMR file;
- keep the new solvency helper/test pair because they eliminate a real late-Ragnarök false positive;
- optionally split out the four low-signal cleanup/support files.

If the goal is to review against current `origin/develop`, do not mistake the extra query/proto/module drift for XMR work. That is branch age, not branch intent.
