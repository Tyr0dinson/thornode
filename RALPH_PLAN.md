# Ralph Implementation Plan — Opt-In Chain Security

**Spec:** `docs/architecture/opt-in-chain-security.md`
**Branch:** `odin/opt-in-chain-security`
**Target MR base:** `develop`

This file is both the **implementation plan** and the **progress tracker**
Ralph will consume. Each iteration Ralph reads this file, picks the next
unchecked task, implements it, self-verifies (build + test + lint for the
files it touched), commits, checks the box here, and exits the turn. The
Ralph stop-hook re-feeds the same prompt so the next iteration starts fresh
with persistent file state.

---

## How Ralph should operate (the prompt)

The prompt is checked in at `.claude/ralph/prompt.md` and stays stable across
iterations. The two bash runners feed this prompt to the `claude` CLI:

- `.claude/ralph/ralph-once.sh` — single iteration, human-in-the-loop.
- `.claude/ralph/afk-ralph.sh` — autonomous loop with iteration cap and
  completion-promise detection.

Each iteration, the prompt directs Ralph to:

1. Read `docs/architecture/opt-in-chain-security.md` (spec) and `RALPH_PLAN.md` (this file).
2. Find the **first unchecked task** in `§ Task checklist`.
3. Implement **only that task**. No skipping ahead, no bundling.
4. Verify locally (scoped to touched files):
   - `goimports -w` on all touched Go files.
   - `make proto-format` if a `.proto` file was touched.
   - `make generate` if any `.proto` or `openapi/*.yaml` was touched.
   - `make build` must succeed.
   - `go test -tags=mocknet ./<pkg-of-touched-files>/...` must pass.
5. Commit with a descriptive message prefixed by the task tag (e.g.
   `[opt-in-chain-security T14] add handler_add_node_chain_support`).
6. Tick the checkbox for the task in this file and append the short commit
   SHA in a follow-up single-line commit.
7. If every task is checked AND the final gate (T55) passes, output the
   completion promise `<promise>OPT_IN_CHAIN_SECURITY_COMPLETE</promise>`.
   Otherwise, exit normally so the loop feeds the prompt again.

Rules Ralph must follow:

- **One task per iteration.** Do not chain tasks even if one looks trivial.
- **No task reordering.** If a task is blocked, add a `BLOCKED: <reason>`
  note on the line below the task in this file and skip to the next unblocked
  task. Never emit the completion promise with unresolved `BLOCKED:` notes —
  a human must review them first.
- **Never `--no-verify`.** Never skip hooks.
- **Never force-push.** Never rebase published commits. Never amend commits.
- **Preserve green tree.** Each committed task must leave `make build` green
  and touched-package tests passing.
- **Do not edit generated files** directly (`*.pb.go`, `*.pulsar.go`,
  `openapi/gen/**`, `test/regression/mnt/**`). Edit source and regenerate.
- **Follow CLAUDE.md** in the worktree (proto-format, make generate,
  goimports, `glab` for GitLab, regression-test env-var ordering).

---

## Invocation (bash runners)

Both runners live in `.claude/ralph/` and stream output to
`.claude/ralph/logs/`. They invoke the `claude` CLI in print mode with
`--permission-mode acceptEdits` so Ralph can write files, run builds, and
run tests without blocking on permission prompts.

### Single iteration (human-in-the-loop)

Run one task, review the resulting commit, decide whether to go again.
Recommended for the first few iterations so you can validate Ralph's
behavior before walking away.

```bash
./.claude/ralph/ralph-once.sh
```

### Autonomous loop (AFK)

Loops up to `MAX_ITER` iterations (default **80**, checklist has 55 tasks +
~45% retry headroom). Stops early when
`<promise>OPT_IN_CHAIN_SECURITY_COMPLETE</promise>` appears in the output.

```bash
# default cap
./.claude/ralph/afk-ralph.sh

# custom cap
./.claude/ralph/afk-ralph.sh 100

# env-var form
MAX_ITER=50 ./.claude/ralph/afk-ralph.sh
```

Per-iteration logs land in `.claude/ralph/logs/afk-<ts>-iter-<n>.log`; a
combined run log lives at `.claude/ralph/logs/afk-<ts>.log`. On
cap-exhaustion the script exits non-zero so it's easy to detect from a
wrapper (e.g. `tmux` / `screen` / `nohup` session).

### Prerequisites

- `claude` CLI on `PATH`, authenticated.
- `gorename`, `goimports`, `glab`, Docker + Compose v2 (for regression
  suites), all already required by the repo.

### Stopping early

- Ctrl-C the loop (`afk-ralph.sh` has no trap; your signal kills the
  current `claude` invocation and the loop exits).
- Or create an empty file `.claude/ralph/STOP` and add a check for it at
  the top of the for-loop — extend the script if you want this behavior.

---

## Global conventions for Ralph

- **Package location for new types:**
  `x/thorchain/types/msg_node_chain_support.go`.
- **Handler location:**
  `x/thorchain/handler_add_node_chain_support.go`,
  `x/thorchain/handler_remove_node_chain_support.go`.
- **Helper location:** `x/thorchain/helpers.go` unless the helper is
  strictly scoped to a manager file.
- **Mimir keys (string):**
  `MinNodeChainSupport`, `ChainLiquidityRunePerValidator`,
  `MinimumNodesForBFT`, `ChainSubBFTRatioNum`, `ChainSubBFTRatioDenom`.
- **Event types (string):** snake_case exactly as in the spec (`chain_support_added`, etc.).
- **Proto tag numbers:** pick the next available tag for each message; do
  NOT reuse retired tags.
- **Bifrost callback signature:** `SetShouldObserveCallback(func() bool)`.

---

## Task checklist

Each unchecked line is a unit of work Ralph picks up. When Ralph completes a
task, it:

1. Changes `- [ ]` to `- [x]`.
2. Appends ` — <commit-sha>` to the end of the task line.
3. Includes this edit in the same commit as the task.

### Phase 1 — Proto / type foundation

- [x] **T1** Add `repeated string supported_chains` to `NodeAccount` proto; `make generate`; ensure callers compile (empty-default behavior — no functional change yet). — 507267f45f
- [x] **T2** Add `repeated string target_chains` to `Keygen` proto; `make generate`; default empty, no functional change. — 2ff349e0c7
- [x] **T3** Add `ChainIncome` message and `repeated ChainIncome chain_income` to `Network` proto; `make generate`; default empty. — d194835794
- [x] **T4** Add `MsgAddNodeChainSupport` proto + type wrapper (`x/thorchain/types/msg_node_chain_support.go`), but no handler yet. Register nothing in router. Build green. — f88b8e23a5
- [x] **T5** Add `MsgRemoveNodeChainSupport` proto + type wrapper. Build green. — 7d3287ae52

### Phase 2 — Mimir + events

- [x] **T6** Register mimirs: `MinNodeChainSupport=1`, `ChainLiquidityRunePerValidator=10_000_00000000` (100k RUNE), `MinimumNodesForBFT=4`, `ChainSubBFTRatioNum=2`, `ChainSubBFTRatioDenom=3`. Add `IsValidMimirKey` entries if applicable. Unit test: mimir lookups return defaults when unset. — 09b55b589c
- [x] **T7** Add event types to `x/thorchain/events.go` (or appropriate file): `chain_support_added`, `chain_support_removed`, `chain_liquidity_cap_breach`, `chain_ragnarok_initiated`. Unit tests for each event constructor. — 8ce6eeb696

### Phase 3 — Core helpers + keeper

- [x] **T8** Implement `getNodeSupportedChains(ctx, mgr, na) []common.Chain` in `x/thorchain/helpers.go` with the empty-set-means-all fallback (union of active + retiring vault chains; `common.AllChains` for standby). Full unit test coverage. — bb360354a8
- [x] **T9** Implement `GetAsgardVaultsByChain` and `GetActiveAsgardVaultsByChain` in `x/thorchain/keeper/v1/keeper_vault.go` (or current keeper version). Unit tests for mixed-vault-set scenarios. — bb19373c5f
- [x] **T10** Implement `GetChainIncome`, `AddChainIncome`, `ZeroChainIncome` on the Network keeper. Unit tests including overflow-safety and missing-chain-returns-zero. — bd9f552746
- [x] **T11** Implement `getChainConsensusParticipants(ctx, mgr, chain) []cosmos.AccAddress` returning `union(activeVaultMembers, retiringVaultMembers)` for the chain. Unit tests. — b2cee5693c
- [x] **T12** Implement `getChainAwareNonSigners(ctx, mgr, signers, chain) []cosmos.AccAddress`. Unit tests with chain-specific membership matrices. — 3a539230a1
- [x] **T13** Implement `validateChainLiquidityCap(ctx, mgr, chain, inboundRuneValue) error` per spec §8.7. Unit tests: below-2/3 triggers cap, at-cap rejects, at-or-above-2/3 skips cap, RUNE-valuation sums pool+secured+trade. — 869ca9900d

### Phase 4 — Handlers

- [x] **T14** Implement `handler_add_node_chain_support.go`: validate signer is a NodeAccount, chain in `common.SupportedChains()`, idempotent append, emit `chain_support_added`. Register in router + ante. Unit tests for each error path + happy path. Activation is churn-deferred — do not touch vaults here. — e27f5d10d7
- [x] **T15** Implement `handler_remove_node_chain_support.go`: validate, enforce `MinNodeChainSupport`, remove chain, emit `chain_support_removed`. Register in router + ante. Unit tests. — b0f2505792

### Phase 5 — Validator manager / churn

- [x] **T16** In `manager_validator_current.go` churn path: partition eligible nodes by effective `SupportedChains` set; emit one Keygen per partition (size ≥ `MinimumNodesForBFT`, sharded by `AsgardSize`) with `TargetChains`. Unit tests covering: single partition (legacy), two partitions, partition < MinimumNodesForBFT is excluded. — e60be92ba5
- [x] **T17** Auto-ragnarok trigger at churn for any chain without a viable partition. Emit `chain_ragnarok_initiated`. Unit tests covering: chain lost to opt-outs, chain lost to node removals. — 2accd594f6
- [x] **T18** Block `MsgLeave` / unbond while node is in any active or retiring vault for a ragnaroking chain. Unit tests. — ff0481e216

### Phase 6 — TSS

- [x] **T19** TSS voter in `handler_tss.go` consumes `Keygen.TargetChains`; vault created from successful keygen carries `TargetChains` as its `Chains`. Unit tests. — 72ebb16fe8

### Phase 7 — Chain-aware consensus

- [x] **T20** `handler_observed_tx*` uses `getChainConsensusParticipants(chain)` for 2/3 threshold. Unit tests against a vault-membership fixture. — efaa96f472
- [x] **T21** `handler_network_fee*` uses chain-aware participants. Unit tests. — 15513c2046
- [x] **T22** `handler_solvency*` uses chain-aware participants. Unit tests. — 2c645523a0
- [x] **T23** `handler_errata_tx*` uses chain-aware participants. Unit tests. — 92d758932c
- [x] **T24** Instant-observation fast-path scoped so the sufficient signer must be a member of the chain's vault. Unit tests. — 7f2b3fbbac

### Phase 8 — Slashing

- [x] **T25** In `manager_slasher_current.go`, for chain-scoped observation misses: call `getChainAwareNonSigners`, forfeit their share of the chain's pending income bucket to the global reserve reward bucket (no bond slash). Update existing tests; add new ones for chain-aware behavior. — 397e01499e

### Phase 9 — Rewards

- [x] **T26** In `manager_network_current.go` (and fee handling sites), bucket per-chain income as specified in spec §4.3 and §8.6: pool swap fees, outbound fees, network fees → chain bucket; emission + slash-forfeiture → global. Unit tests. — 3abb774aaf
- [x] **T27** Per-chain reward payout at the reward distribution interval: pro-rata by effective bond (capped at `MaxEffectiveBond` per chain) across chain's active vault members. Unit tests covering: single-chain node, multi-chain node, bond-over-cap truncation. — 11954f07f9
- [x] **T28** Confirm dev fund / marketing fund / reserve skims draw from global system income (union across all buckets plus emission). Adjust if current code implicitly assumed a single bucket. Unit tests. — cab7a9d42f

### Phase 10 — Liquidity cap enforcement + swap gating

- [x] **T29** Wire `validateChainLiquidityCap` into `handler_deposit` add-liquidity path. Unit tests. — f9278800ca
- [x] **T30** Wire into secured-asset deposit handler. Unit tests. — 9e191c513f
- [x] **T31** Wire into trade-account deposit handler. Unit tests. — 33fb71b413
- [x] **T32** Wire into `handler_swap` for swap-to-external-chain path (increases RUNE-valued TVL on the output chain). Unit tests. — ce4c058c4f
- [x] **T33** Reject swap and add-liquidity into chains with no active Asgard (`GetActiveAsgardVaultsByChain` empty). Unit tests. — 2a9bc6f67f

### Phase 11 — Genesis

- [x] **T34** `x/thorchain/genesis.go`: export `SupportedChains` on each `NodeAccount`; import honors field if present, defaults nil. Round-trip unit test. — d6f1b60444

### Phase 12 — API

- [x] **T35** `openapi/openapi.yaml`: add `supported_chains` and `supports_all_chains` to node_account response. `make generate`. Update querier serialization so empty set returns the resolved list plus `supports_all_chains: true`. Test via query unit test. — 95b0fa54f4
- [x] **T36** `openapi/openapi.yaml`: add `supporting_validators`, `active_validator_fraction`, `liquidity_cap_applied`, `liquidity_cap_rune` to `inbound_addresses`. Implement the handler. Remove/adjust any stale "all active validators" claim. Unit tests. — db85694d9e
- [x] **T37** Node querier test coverage for the new field end-to-end through the API gateway. — 6c98071b5e

### Phase 13 — Metrics

- [x] **T38** Register Prometheus gauges `thornode_chain_supporting_validators{chain}` and `thornode_chain_liquidity_cap_headroom_ratio{chain}`. Update values each block / at relevant state change. Unit tests confirm gauge updates. — 79ed83d810

### Phase 14 — Bifrost

- [x] **T39** `bifrost/thorclient/pubkey_manager.go`: implement `GetSupportedChains() []common.Chain` with active+retiring vault union and fallback to `common.AllChains` when no membership. Unit tests. — 6807845b23
- [x] **T40** `bifrost/blockscanner/blockscanner.go`: add `SetShouldObserveCallback(func() bool)`; pause polling when callback returns false; preserve cursor across pause/resume. Unit tests. — 9e8e87517b
- [x] **T41** Wire callback in `bifrost/pkg/chainclients/ethereum/`. Unit tests. — d569756f94
- [x] **T42** Wire callback in `bifrost/pkg/chainclients/evm/` (generic EVM). Unit tests. — ae9bd842b8
- [ ] **T43** Wire callback in `bifrost/pkg/chainclients/gaia/`. Unit tests.
- [ ] **T44** Wire callback in `bifrost/pkg/chainclients/solana/`. Unit tests.
- [ ] **T45** Wire callback in `bifrost/pkg/chainclients/tron/`. Unit tests.
- [ ] **T46** Wire callback in `bifrost/pkg/chainclients/utxo/` (btc/ltc/bch/doge variants). Unit tests.
- [ ] **T47** Wire callback in `bifrost/pkg/chainclients/xrp/`. Unit tests.
- [ ] **T48** `bifrost/observer/observer.go`: per-block-time tick that recomputes supported set, diffs against previous, logs `chains_added`/`chains_removed`, invokes callbacks. Unit tests with mocked PubKeyManager.
- [ ] **T49** `bifrost/cmd/bifrost/main.go` + health endpoint: report `observed_chains`, `configured_chains`, `last_observed_change_height`. Integration test via health HTTP handler.

### Phase 15 — Regression tests

- [ ] **T50** `test/regression/suites/opt-in-chain-security/happy-path.yaml`: opt-in → churn → chain-only Asgard → BTC swap → reward credited to BTC supporters only.
- [ ] **T51** `test/regression/suites/opt-in-chain-security/liquidity-cap.yaml`: drop BTC support below 2/3, verify add-liquidity/secured-deposit/trade-deposit/swap-in rejected once at cap; existing untouched; withdrawals still succeed.
- [ ] **T52** `test/regression/suites/opt-in-chain-security/chain-ragnarok.yaml`: reduce BTC support below `MinimumNodesForBFT`; auto-ragnarok at next churn; `MsgLeave` rejected for in-vault node; after drain, unbond allowed.

Each regression suite must run via `make test-regression RUN=opt-in-chain-security/<name> CI=1 EXPORT=1 DEBUG=1` and pass.

### Phase 16 — Coverage

- [ ] **T53** Generate coverage report (`make test-coverage`); confirm every new/changed file in this feature is ≥80% line coverage. If below, add tests until threshold met. Do not degrade coverage on untouched files.

### Phase 17 — Integration + MR

- [ ] **T54** Full green-light pass: `make lint`, `make build`, `make test`, `make test-regression CI=1 EXPORT=1 DEBUG=1`. Fix anything the above tasks missed. This is the hard gate for the completion promise.
- [ ] **T55** Open/update the MR targeting `develop`: push branch, `glab mr create --target-branch develop --title "..." --description "..."` (no `--body`; use `--description`). Title + description reflect the final delivered scope; inline MR comments are the only way to respond to review. After push + MR exists, the feature is complete.

---

## Completion criteria

All of:

- Every task above is ticked.
- `make lint && make build && make test` green on this branch.
- All three regression suites in `test/regression/suites/opt-in-chain-security/` pass under `make test-regression CI=1 EXPORT=1 DEBUG=1`.
- Coverage report shows ≥80% on every new/changed source file.
- MR exists against `develop`.

When all are true, Ralph outputs `<promise>OPT_IN_CHAIN_SECURITY_COMPLETE</promise>`.

---

## Safeguards

- **Iteration cap:** 80. Prevents runaway on stuck tasks.
- **No `--no-verify`.** Pre-commit hooks must pass — they enforce formatting and lint.
- **No force-push.** Rebases of published commits are forbidden.
- **Commit-per-task** gives a clean `git log` that can be reverted granularly if a bad commit slips in.
- **BLOCKED escape hatch:** if a task needs a design call the spec doesn't cover, Ralph annotates `BLOCKED: <reason>` under the task and emits `<promise>OPT_IN_CHAIN_SECURITY_BLOCKED</promise>`. Because that is NOT the configured completion promise, the loop continues — which forces Ralph to keep attempting, but the annotation is visible in the git log so a human reviewer can intervene on the next check-in.
- **Per-iteration checkpoint:** each commit is a durable checkpoint. Even if Ralph is interrupted mid-run, the state is recoverable.

---

## When the human should intervene

- If iteration cap hits without completion: review the last task; either extend iterations or fix the blocker manually.
- If Ralph writes `BLOCKED:` under any task: resolve the design question, clear the annotation, and restart the loop.
- If `make test-regression` fails repeatedly on the same suite: inspect mnt fixtures (do NOT edit generated files); add or adjust test yaml inputs.
- If proto regen produces diffs outside the touched files: regenerate from a clean tree and investigate.
