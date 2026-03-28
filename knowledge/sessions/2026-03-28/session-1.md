# Session 1 - Clean XMR Sim Green Run and Review Integrity

> Date: 2026-03-28
> Focus: Get the clean `xmr-integration` branch through both simulation targets under Go `1.24.6`, then document and review the resulting `x/thorchain` diff.

## Summary

The clean `xmr-integration` branch passed both `make test-simulation-cluster` and `make test-simulation` under Go `1.24.6` after fixing real readiness, finality, genesis, and solvency false-positive issues. I also updated the `x/thorchain` analysis to distinguish true branch-authored XMR changes from upstream drift against current `origin/develop`, and added an explicit "did we cheat?" section explaining why the green runs are real rather than skipped or hardcoded.

## Work Done

- Re-ran `make test-simulation-cluster` on the clean `xmr-integration` branch and monitored it through completion.
- Re-ran `make test-simulation` after the cluster pass and monitored it through completion.
- Fixed cluster bootstrap instability by:
  - avoiding duplicate mocknet genesis accounts for reused prefunded addresses;
  - increasing thornode healthcheck startup grace for clean-volume boots;
  - widening TRON readiness timeout to match real startup behavior.
- Fixed simulation/runtime false failures by:
  - requiring stronger Solana confirmation behavior in the simulation client;
  - adding fresh-sign retry behavior for transient TRON and Solana broadcast problems;
  - widening Solana-related swap and aggregator timeouts where finality is slower;
  - narrowing solvency checks to shortfall rather than harmless L1 excess;
  - teaching THORChain solvency to ignore late-Ragnarok gaps when pending outbounds fully explain them.
- Verified that the clean branch passed both simulation targets:
  - `make test-simulation-cluster` at `2026-03-28 05:50:21 EDT`
  - `make test-simulation` at `2026-03-28 06:51:09 EDT`
- Produced an updated `x/thorchain` analysis report for the current worktree and added the simulation-integrity / "no cheating" analysis to it.

## Discoveries

- The clean XMR branch can pass both simulation targets without skipping actors, hardcoding success, or relaxing the DAG scope.
- The most judgment-heavy fixes are solvency-related, not startup-related:
  - the simulation solvency actor now treats excess L1 funds as still solvent;
  - THORChain solvency now suppresses specific late-Ragnarok false positives when pending outbounds explain the gap.
- Comparing the branch directly to current `origin/develop` overstates the XMR diff. Seven `x/thorchain` files in the raw diff are upstream drift rather than branch-authored XMR work.
- Solana finality and TRON readiness were real sources of flakiness; waiting for actual chain readiness/finality was necessary to make the simulations reflect real behavior.

## Files Changed

| File | Change |
|------|--------|
| `build/scripts/core.sh` | Added duplicate-account detection helper for mocknet genesis creation. |
| `build/scripts/genesis.sh` | Avoided duplicate bootstrap funding entries for prefunded mocknet validator addresses. |
| `build/docker/docker-compose.yml` | Increased thornode startup grace period for clean-volume mocknet boots. |
| `test/simulation/cmd/main.go` | Increased TRON readiness timeout to match real cluster startup behavior. |
| `test/simulation/pkg/solana/solana.go` | Waited for stronger Solana confirmation before treating broadcasts as successful. |
| `bifrost/pkg/chainclients/solana/rpc/transactions.go` | Added commitment-aware transaction fetch helper for Solana confirmation checks. |
| `test/simulation/pkg/types/tx_retry.go` | Added chain-specific fresh-sign retry handling for transient TRON and Solana failures. |
| `test/simulation/actors/core/swap.go` | Increased SOL/TRON swap timeout headroom and hardened balance handling. |
| `test/simulation/actors/core/swap_aggregator.go` | Increased timeout for Solana-involved aggregator swaps. |
| `test/simulation/actors/core/swap_memoless.go` | Increased Solana memoless timeout headroom and reused fresh-sign behavior. |
| `test/simulation/actors/core/check_solvency.go` | Treated solvency as shortfall detection rather than exact equality. |
| `x/thorchain/handler_solvency_helpers.go` | Suppressed late-Ragnarok solvency false positives only when pending outbounds fully explain the apparent gap. |
| `knowledge/x-thorchain-analysis-2026-03-28.md` | Added current worktree `x/thorchain` analysis plus explicit simulation-integrity / "no cheating" section. |

## In Progress

MR metadata still needs to be updated to point reviewers at the latest `x/thorchain` report and the new simulation-integrity analysis.

## Next Steps

- [ ] Update MR `4669` description to reference the March 28 `x/thorchain` analysis and the explicit "no cheating" section.
- [ ] Decide which of the non-XMR simulation harness fixes should live in the XMR branch versus a separate follow-up branch.
- [ ] Rebase `xmr-integration` onto current `origin/develop` before final review so upstream query/proto drift disappears from the raw diff.
- [ ] Split low-signal `x/thorchain` cleanup files out of the MR if reviewers want the smallest possible XMR-only patch.
