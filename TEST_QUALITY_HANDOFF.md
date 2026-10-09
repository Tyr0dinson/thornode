# THORNode test-quality work handoff

Snapshot: 2026-10-09 21:53 UTC. This file is the entry point for a future session continuing the THORNode test-quality project. Recheck GitLab and Orca before acting because the status below is a snapshot.

## Current outcome and source of truth

- Draft GitLab MR [!5181, Expand THORNode safety and security test coverage](https://gitlab.com/thorchain/thornode/-/merge_requests/5181) targets `develop` from `test-quality-integration`. Its source HEAD at this snapshot is `64bdecbc9b4e93fb7014f7948ca99fe4ab3a55ad`; GitLab reports it can be merged, but it should remain **draft** until human review.
- [MR pipeline 2932029891](https://gitlab.com/thorchain/thornode/-/pipelines/2932029891) **passed all 20 jobs** on 2026-10-09 at 20:35 UTC. This includes lint, root and simulation unit tests, mainnet Monero, Bifrost race, real cluster simulation, regression, consensus determinism, fast fuzz and fast security mutations. All listed jobs were non-allow-failure except the pre-existing secret-detection job.
- The integrated Orca workspace is `/home/cbarraford/orca/workspaces/thornode/thornode/test-quality-integration`. All ten topic workspace commits were merged here. This is the branch behind the MR and the place to make any follow-up source changes.
- The ten topic branches remain Orca workspaces marked `in-review`. They are now pushed and each has a draft MR with `huginn-bot` assigned as reviewer. Their own pipelines are still being worked through; the green integration pipeline does not certify each topic MR. The user explicitly wants every topic MR to have passing CI.
- This handoff lives in the original `unit-test-expansion` workspace, separate from the integrated MR. Its documentation commit should not trigger a new pipeline for !5181 or disturb the verified green HEAD.

## Topic merge requests opened 2026-10-09

| Topic                    | Draft MR                                                             | Target                                     |
| ------------------------ | -------------------------------------------------------------------- | ------------------------------------------ |
| eBifrost race            | [!5182](https://gitlab.com/thorchain/thornode/-/merge_requests/5182) | `develop`                                  |
| Simulation unit          | [!5183](https://gitlab.com/thorchain/thornode/-/merge_requests/5183) | `develop`                                  |
| Simulation invariants    | [!5184](https://gitlab.com/thorchain/thornode/-/merge_requests/5184) | `develop`                                  |
| Test-quality CI          | [!5185](https://gitlab.com/thorchain/thornode/-/merge_requests/5185) | `test-quality-sim-unit` (stacked on !5183) |
| Consensus determinism    | [!5186](https://gitlab.com/thorchain/thornode/-/merge_requests/5186) | `develop`                                  |
| Accounting               | [!5187](https://gitlab.com/thorchain/thornode/-/merge_requests/5187) | `develop`                                  |
| Quorum security          | [!5188](https://gitlab.com/thorchain/thornode/-/merge_requests/5188) | `develop`                                  |
| Signer/observer recovery | [!5189](https://gitlab.com/thorchain/thornode/-/merge_requests/5189) | `develop`                                  |
| Native security fuzz     | [!5190](https://gitlab.com/thorchain/thornode/-/merge_requests/5190) | `develop`                                  |
| Curated mutation checks  | [!5191](https://gitlab.com/thorchain/thornode/-/merge_requests/5191) | `develop`                                  |

All ten are draft, and all have `huginn-bot` as reviewer. The integrated draft !5181 overlaps them all. Coordinate the merge strategy before merging any of these; do not merge both an umbrella change and its component change without checking what has already landed. Some standalone branches gained focused CI wiring and lint fixes after the integrated MR's green pipeline. Use the latest pipeline for **each** MR, not the initial failed or auto-canceled pipeline. At this snapshot, !5183's simulation unit job, !5186's determinism job, and !5191's fast mutation job have individually passed, but the full pipelines are still in progress. !5182 and !5188 had existing race-suite timing failures under load and their failed/canceled jobs were retried. !5189 fixed an older signer test's obsolete broadcast expectation. !5185 removed a duplicate simulation job in the stacked merge result. Keep working until all ten latest pipelines pass.

## Workspaces and agent assignments

All paths below share the prefix `/home/cbarraford/orca/workspaces/thornode/thornode/`. Each topic was assigned to a direct Orca Codex terminal, finished with local commits, and marked `in-review`. The terminal handles may become stale, but preserve the assignment map for context. The most reliable live view is `orca-ide worktree list --json` (worktrees are under `.result.worktrees`).

| Workspace / branch         | Assigned work and current result                                                                                                                                                                                                                                                         | Orca agent terminal                                    |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------ |
| `test-quality-race`        | eBifrost quorum cache/event ownership race: snapshot protobuf payloads before confirmation, test mutation and concurrent access. Focused and full mocknet race tests passed; merged starting at `dd6cb80fb9`.                                                                            | `term_e3f47408-b22f-4e44-9a14-3bca4793001b`            |
| `test-quality-sim-unit`    | Run the separate `test/simulation` Go module as a hermetic unit suite. Agent reported 14 packages, 461 tests, and five shuffled isolation runs green; two daemon integration cases remain outside the unit subset. CI job `test-simulation-unit` passed.                                 | `term_b6f8a38d-82e1-4b11-adc7-172acff6e570`            |
| `test-quality-invariants`  | Make simulation invariant watcher failures fail closed, with watcher and command tests. Cluster simulation CI passed.                                                                                                                                                                    | `term_d963652e-f9b8-440d-9141-499f82c72e77`            |
| `test-quality-ci`          | Inventory test selection across modules and tags; add required test/skip checks, JUnit and coverage artifacts, mainnet-only Monero coverage, simulation and Bifrost race gates. CI script checks now pass. Some commands it added are supplied by sibling branches in the integrated MR. | `term_fdc7d7aa-ee6e-4e3d-915a-aaa467c053ac`            |
| `test-quality-determinism` | Deterministic consensus replay, state comparison, divergence detection, export/restart and 18→19 upgrade tests. CI `consensus-determinism` passed. See `docs/test-consensus-determinism.md` on the integrated branch.                                                                    | `term_cb2128a3-3bde-43e3-9ab2-1f3ef58d0afa`            |
| `test-quality-accounting`  | Seeded swap, liquidity, refund and vault accounting property campaigns with independent obligation checks. Four-seed fast suite runs in root `unit-tests`; 32-seed campaign is scheduled-only. Fast and campaign tests passed locally; root CI passed.                                   | `term_149c879b-a1bf-40cc-a2f3-1d3cb704edfb`            |
| `test-quality-quorum`      | Adversarial quorum payload binding, voter and authorization tests. Focused/mocknet suites passed; direction-guard mutation failed as intended. Root CI passed.                                                                                                                           | `term_a5f76047-6cdb-499a-86e5-5cd5fc790b87`            |
| `test-quality-recovery`    | Signer and observer crash/retry/reorg recovery, durable-write-before-broadcast and retiring-vault retry checks, plus a durability fix. Agent reported local race runner green; integrated race and regression CI passed.                                                                 | `term_90556b9a-cb88-41ba-88d7-e88a0855993b`            |
| `test-quality-fuzz`        | Five native Go fuzz targets, security corpora and decoder guards. Agent ran each target 1,000 iterations; `security-fuzz-fast` CI passed. Deep time-based fuzzing is scheduled-only. Agent noted that the full Solana suite needs loopback sockets locally.                              | `term_0dd58a57-c250-4caf-9cab-043cd1667c53`            |
| `test-quality-mutation`    | Curated Go-overlay security mutations and assertions. Five of five fast and seven of seven deep mutants killed locally while the control survived; fast GitLab job passed. Deep campaign is scheduled-only.                                                                              | `term_21ef480a-2c91-47e0-9bbe-99beda97eba2`            |
| `test-quality-integration` | Integration of all ten topics, CI-only fixes, draft !5181, pipeline monitoring. Current HEAD `64bdecbc9b`; green MR pipeline above. Original integration Orca agent terminal: `term_7c283352-0ef0-432d-b965-c94efdf18cec`.                                                               | Integration agent finished; continue in this worktree. |

The machine-readable original topic handoff list is `/tmp/thornode-test-quality-handoff-index.json` if the same host still has it. It contains the workspace paths and terminal handles. `/tmp/thornode-test-quality-integration-receipt.json` is an **old creation receipt**, not a current status record.

## CI coverage and how it is wired

The integrated branch changes `.gitlab-ci.yml` and Make/script targets. Merge-request pipelines now run `test-simulation-unit`, `test-mainnet-monero`, `unit-tests-race-bifrost`, `consensus-determinism`, `security-fuzz-fast`, and `security-mutations-fast` when its path rules match, in addition to existing `unit-tests`, `test-regression`, and `test-simulation`. Root `unit-tests` includes the fast accounting property suite and produces test inventory, skip and changed-coverage reports. The race job includes `x/thorchain/ebifrost`. The simulation job runs a native cluster with an explicit Monero requirement and records its random seed. Regression and cluster simulation both passed in the green pipeline.

Scheduled pipelines add `accounting-properties-deep` (32 seeds), `security-fuzz-deep` (20 seconds per target), and `security-mutations-deep` (seven mutants). These deeper scheduled jobs were validated locally where noted above, but **were not run in MR pipeline 2932029891**. Do not claim they passed GitLab until a scheduled pipeline actually runs. Fast mutation results are in the CI artifact `test-quality-reports/mutations-fast.json`; fuzz and determinism logs, JUnit, coverage, and source inventory are also published as job artifacts.

The CI definition was accepted by `glab ci lint .gitlab-ci.yml -R thorchain/thornode --include-jobs`; the strongest evidence is the all-green MR pipeline. Relevant scripts are `scripts/test-quality-run.py`, `scripts/test-quality-inventory.py`, `scripts/test-quality-coverage.py`, `scripts/test-security-mutations.py`, `scripts/test-security-fuzz.sh`, `scripts/test-consensus-determinism.sh`, and `scripts/test-accounting-property.sh`. The integration MR description summarizes the user-facing test change.

## Integration fixes and failure history

These failures occurred on earlier pipelines and are resolved in the green HEAD:

1. Pipeline `2931973741`: `test-mocknet-scripts` assumed a one-line simulation command. Commit `3893b0a886` made the test understand the block-style CI script and avoid live Docker during Make dry-run.
2. Pipeline `2931981116`: lint found formatting, weak-random warnings in deterministic tests, unchecked fixture assertions, and shadowed variables. Commit `4532064789` fixed the findings. The deterministic seeded generator remains intentional test data generation.
3. Pipeline `2931993808`: repository `gofumpt` required four more test files to be formatted. Commit `b14f293227` did so. The repository `scripts/lint.sh`, Trunk check, and full `scripts/golangci-ci.sh` passed locally with Go 1.26.9.
4. Pipeline `2932009480`: security mutations executed but failed when writing `test-quality-reports/mutations-fast.json` into a missing directory. Commit `64bdecbc9b` creates the directory. The exact fast command then passed locally (five killed, control survived) and the subsequent GitLab job passed.

Do not retry those old failed pipelines; use the current MR branch and pipeline. A source push creates a new MR pipeline and GitLab auto-cancels running sibling jobs after a failure.

## How to continue

1. Read this file, then inspect the current MR and latest pipeline. `glab api projects/13422983/merge_requests/5181 | jq '{title,state,draft,sha,head_pipeline}'` and `glab api 'projects/13422983/merge_requests/5181/pipelines?per_page=5'` work with the current GitLab authentication. Project ID is `13422983` (`thorchain/thornode`). For job detail, use `glab api 'projects/13422983/pipelines/PIPELINE_ID/jobs?per_page=100'` and `glab api projects/13422983/jobs/JOB_ID/trace`.
2. Check `git status` and `git log` in `test-quality-integration`; work on that branch for fixes. Use `orca-ide worktree list --json` to inspect the original workspaces. The Orca CLI is `orca-ide` on this Linux host; its local skill is `/home/cbarraford/.agents/skills/orca-cli/SKILL.md`.
3. Keep all MRs draft through review. First finish the ten topic pipelines above, fix any failures in the relevant topic workspace, then coordinate a merge path with reviewers. The integrated green MR pipeline confirms its own merge-request gates, not the standalone MRs or future scheduled runs.
4. If code changes, run the relevant focused tests, `scripts/trunk check --ci --filter=-golangci-lint`, and the appropriate Go lint/test target before pushing. The repository requires Go **1.26.9**; locally, `GOTOOLCHAIN=go1.26.9 ./scripts/golangci-ci.sh` worked. Every tracked Go file is checked by `gofumpt` in `scripts/lint.sh`.
5. Keep this documentation accurate as each MR HEAD or CI state changes. The component workspaces are already represented in !5181, but each standalone MR now needs its own passing pipeline. Do not merge an MR or mark it ready without a user request/review decision.

The full local cluster simulation was intentionally not run in the shared development workspace: its Make prerequisite resets live Docker volumes. The isolated GitLab `test-simulation` job did run and passed. All 20 jobs in the current MR pipeline passed, so there is no known CI blocker at this snapshot.
