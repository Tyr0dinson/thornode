# THORNode Simulation Testing Framework

While we have existing test frameworks (unit, regression, smoke), none are able to fully explore the boundaries of the system and the dynamic interactions between all components. Unit tests are narrowly focused with limited usefulness on the majority of logic which relies on complex interactions between components. Regression tests are one layer removed to perform discrete and deterministic tests against the Thornode state machine - they have proven useful, but remain insufficient to cover the full path from external L1s, through Bifrost, and into Thornode. Smoke tests were intended to cover this end to end testing, but they fail to capture the dynamic nature of the underlying chain states, making them brittle and difficult to extend. Smoke tests have additional barriers that have prevented ongoing extension to their coverage - namely the static and inter-dependent test definitions, as well as the system being built in Python which requires protobuf changes, mock additions, etcetera to add any new chains.

The goal of the simulation test framework is to provide a revised approach to the smoke test problem. Instead of attempting to replicate the state machine and compare it to a mocknet state, we define independent test sequences similar to regression tests. The simulation tests are not an extension or replacement of regression tests, but rather they are complementary - the delineation being simulation test operations are dynamic instead of discrete numerical checks (ex: get a swap quote for half the account BTC balance -> ETH, send the deposit, ensure the received amount is within some expected range based on the quote). The full test suite will be defined as an execution DAG of tests - operations in each test are executed sequentially, but there are a configurable number of tests run concurrently and the global ordering is intentionally random and interleaved.

The initial implementation will leverage a static actor DAG that runs to completion - like current smoke tests. Future work can extend these static tests with fuzzing to generate a random DAG of arbitrary length to further probe boundaries of the system over a longer duration.

## Running Simulation Tests

### Hermetic unit suite

The simulation code is a separate Go module, so a root `go test ./...` does not
run its unit tests. From the repository root, run:

```bash
scripts/test-simulation-unit.sh /tmp/simulation-unit.jsonl
```

The script runs every package in `test/simulation` with the `mocknet` build tag,
Go 1.26.11, limited build parallelism, and the shared test-quality build lock.
The JSON Lines report contains per-test results for CI or local inspection. It
rejects unexpected skips and clears integration RPC settings, so it never
connects to a live chain. `TestDogeStalledBroadcastIsNotDuplicated` and
`TestZecIsolatedFunding` require separately provisioned isolated daemons and
are the only expected skips. The `test-utxo-daemons` CI job runs them against
fresh mocknet daemons (see `make test-utxo-daemons`). To check test order isolation, run from
`test/simulation` with the same build lock:

```bash
flock -w 1200 /tmp/thornode-test-quality-build.lock env GOMAXPROCS=4 GOTOOLCHAIN=go1.26.11 go test -p 2 -tags mocknet -count=5 -shuffle=on ./pkg/types ./pkg/dag ./pkg/solana
flock -w 1200 /tmp/thornode-test-quality-build.lock env GOMAXPROCS=4 GOTOOLCHAIN=go1.26.11 go test -p 2 -tags mocknet -count=5 -run '^Test$' -v ./pkg/types -check.f NewUser
```

These commands use only local fixtures. The second command exercises the
gocheck account case by itself, without depending on other suite tests.

Simulation tests can be run with the following command from the repo root:

```bash
make test-simulation
```

This will internally build the simulation framework image, start mocknet, and run the tests from a Docker container with host mode networking to reach the local mocknet.

The `swaps` stage always schedules two required swaps to ZEC: one with a normal
transparent destination and one with a TEX destination. Mocknet uses `tm` and
`textest` in place of mainnet's `t1` and `tex`. Both swaps must complete, and their
outbound actions and observations must use the normal transparent address.

### Inactive-vault refunds

The cluster profile runs `inactive-vault-refunds` after churn, when eligible
retired vaults exist. The scenario uses the same inactive-vault discovery as
Bifrost and fails if a selected chain has no eligible destination. The single-node
profile does not create retired vaults, so it excludes this scenario. To run it
explicitly, include `cluster-churn` or use a cluster that has already churned.
XMR registers a reference memo with an explicit refund address before depositing.

### XMR certificate recovery after churn

The optional `xmr-historical-refund` stage uses the five-node cluster after
`cluster-churn`. It selects a four-member inactive XMR vault with one departed
member, then temporarily jails one of its three active members using synthetic
keysign-failure votes. All five Bifrosts and XMR signers keep scanning. This leaves
an exact three-member signing party containing the departed member. The actor
checks the historical signing policy at the refund's actual outbound height,
finalized outbound bookkeeping, and absence of an inbound vault credit for the
refund transaction. A run with an all-active party fails the evidence check.

For a **fresh, dedicated cluster** (this command resets mocknet data):

```bash
JAIL_TIME_KEYSIGN=600 make test-simulation-cluster \
  TEST_SIMULATION_REQUIRE_XMR=1 \
  TEST_SIMULATION_CLUSTER_STAGES=seed,bootstrap,cluster-churn,xmr-post-churn,xmr-historical-refund,solvency
```

The longer jail is a mocknet-only environment override shared by THORNode and
Bifrost. The actor requires at least 120 blocks remaining before sending the
deposit, and verifies that the jail still selected the required party at the
payout height. It never stops a signer process or changes production selection.
Churning is halted for this isolated recovery scenario; use a fresh cluster for
subsequent churn scenarios. Other chains' jail fallback tests should run separately.

Save the actor's `signing_members` and `member_statuses` log fields. For the same
refund hash, preserve one non-signing Bifrost's logs showing, in order:

1. `skip incoming monero output carrying an outbound memo` (`txid`);
2. `applied spent output refs import` (`tx_hash`, with no missing refs);
3. `durably queued authenticated xmr payout source replay` (`txid`);
4. its outbound observation submission, followed by THORChain finalization.

A completed refund alone does not prove the late replay path: if proof arrived
before scanning, the filter/replay ordering was not exercised. Report that
separately. S29's focused tests force this ordering, disable the filter as a
negative control, cover Active/Retiring/Inactive source vaults, restart before
certificate delivery, high-value batches, pruned journals, and queue saturation.
The cluster supplements those tests; it does not replace their crash injection.

### TRON solvency slashing (#3098)

The default `make test-simulation-cluster` stages include
`tron-solvency-fragmentation`. To run just its setup and validator test, use:

```bash
make test-simulation-cluster \
  TEST_SIMULATION_CLUSTER_STAGES=seed,bootstrap,churn,tron-solvency-fragmentation
```

This rebuilds and **resets the local mocknet cluster**. Keep `churn` in this
focused stage selection so the validators are activated before the test.
Bootstrap creates pools but leaves the other validators on standby with churning
halted. The existing churn stage activates the cluster before the TRON actor
selects the active simulation clients. At least four validators must be active,
and each active validator must have exactly one matching client. Extra standby
clients are excluded from client locks and slash-point snapshots. Missing activation is a
setup error, not a five-minute wait or a successful slashing test. On an already
activated cluster the TRON stage can run alone; do not churn it again just to
satisfy a stage-name requirement.

The actor injects differing TRX/USDT solvency payloads at the same stale height,
checks that ordinary observations leave slash points unchanged, and verifies the
duplicate-message penalty. Slash points are compared only from the block before
to the block of each injected transaction. Bifrost observations in those blocks
can still change them, so a mismatch is measured again with fresh voters, up to
five attempts. It tests handler accounting, not historical TRON RPC
balances or Bifrost gossip/quorum dispatch. Other cluster readiness/keygen errors
can still prevent this test from running.

When both `cluster-churn` (or its `bond-yield` alias) and `tron-solvency-fragmentation` are selected, the TRON
assertions run inside the Bond Yield sequence, immediately after initial
activation and before target selection and voluntary leave. Bond Yield then
verifies the churn-out payout and completed migration, restores its Mimir
settings, and lets later stages proceed without waiting for the validator to
churn back in. This avoids an extra restoration churn solely for TRON's
requirement for at least four matching active validators. Selecting `churn` as well does not add a second
activation. Stage selection order does not override these dependencies.

### Faster reruns

`make test-simulation` rebuilds the mocknet images and resets chain state on every invocation, so the first run is expensive. When iterating, reuse the running mocknet instead of rebuilding it:

```bash
make reset-mocknet           # reset chain state without rebuilding images
make test-simulation-no-reset # rebuild only the framework image, then run
```

For background or non-interactive runs, set `CI=1` so Docker is not started with an interactive TTY. This also makes it easy to follow the output in another terminal:

```bash
CI=1 make test-simulation-no-reset > sim.log 2>&1 &
tail -f sim.log
```

### XMR keygen timeout recovery

Run the dedicated regression on a disposable local mocknet cluster:

```bash
CI=1 COMPOSE_PROJECT_NAME=xmr-keygen-recovery make test-simulation-xmr-keygen-recovery
```

This resets that Compose project's cluster. It completes seed/bootstrap before
adding 500 ms network delay on the Bifrost P2P port, and gives only pig a 100 ms
mocknet round-3 deadline. The delay must still allow the regular Asgard keygen to
finish. Override these with `XMR_KEYGEN_RECOVERY_NETEM_DELAY_MS` and
`XMR_KEYGEN_RECOVERY_ROUND3_TIMEOUT_MS` if necessary; increasing the deadline can
prevent the fault and must cause the regression to fail.

The `xmr-keygen-recovery` stage requires pig's existing structured logs through
`XMR_KEYGEN_RECOVERY_LOG`, mounted read-only by the target. It requires the
override, actual round-3 timeout, and quorum-result adoption with a locally
finalized keyshare for the **same ceremony**. The adopted address must match a
new active four-member XMR vault with exactly the active node membership, and
all four nodes must have submitted successful keygen results for that vault.
These conditions must remain true for 30 seconds. Any observed
`failed to vote keygen in time` jail fails immediately, even before activation.
The actor has a 20-minute deadline; running the stage without its log source
fails before triggering churn.

This replaces the obsolete jail-positive reproduction. It covers keygen timeout
recovery, not attributed keysign failure or keysign jailing. No production
consensus or penalty policy is changed.

The target leaves its cluster available for inspection. Remove only that
project's resources when finished:

```bash
COMPOSE_PROJECT_NAME=xmr-keygen-recovery make stop-mocknet-cluster
```

### Mempool swap lifecycle stage

The end-to-end `mempool-swaps` stage runs as part of the normal multi-validator
cluster suite. From the repository root, run:

```bash
make test-simulation-cluster
```

To run only the required setup and this stage, use the existing cluster-stage
selection variable:

```bash
TEST_SIMULATION_CLUSTER_STAGES=seed,bootstrap,mempool-swaps \
  make test-simulation-cluster
```

The cluster target builds the simulation and mocknet images and resets the
cluster to fresh chain state. A fresh cluster matters: the scenarios expect no
existing mempool TOR reservation and configure lifecycle Mimirs relative to
known defaults. The stage waits for real L1 blocks, Bifrost observations,
THORChain blocks, and outbound signing, so expect it to take tens of minutes.

Despite the name, these tests do not act on an unmined Polygon transaction. They
use `POL.POL` inbounds because mocknet Bifrost observes a mined transaction while
still waiting for Polygon's finality threshold. This creates a repeatable
confirmed-but-not-finalized window in which the stage can verify speculative
settlement, staged outbounds, capacity gating, finality, and timeout rollback
behavior across the real L1 -> Bifrost -> Thornode path.

The list is fixed and fail-fast: 7 base + 6 native-target + 1 memoless scenario,
in the order below. There is no per-scenario selector; a failed scenario fails
the stage before any later entry runs. “Base”, “Native”, and “Memoless” refer to
`MempoolSwapSimulationActor`, `MempoolSwapNativeSimulationActor`, and
`MempoolSwapMemolessActor`, respectively.

| Order | Actor / scenario                                    | Class                           | Flow / trigger                                                                                                                        | Principal assertions                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ----: | --------------------------------------------------- | ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
|     1 | Base / `happy-market`                               | Happy path / market             | A regular `POL.POL -> BTC.BTC` market swap is observed before POL finality.                                                           | While unfinalized, it reserves TOR, mutates the pools, and creates only a staged outbound—never a vault credit, recipient credit, or real outbound. Finality releases exactly one signed outbound and clears its reservation, stage, and queue state.                                                                                                                                                                                    |
|     2 | Base / `affiliate-revshare-finality`                | v1 deferred effect / affiliate  | A regular speculative swap uses ordinary memo affiliate bps `0`, while `REVSHARE-<name>` config assigns the THORName a nonzero share. | The affiliate collector remains exactly at its pre-swap baseline while TOR and staged state are live, then grows only after inbound finality and completion. This isolates REVSHARE attribution from outbound affiliate payments and verifies v1's pending behavior without coupling the scenario to its KV representation.                                                                                                              |
|     3 | Base / `stream-settle-before-finality`              | Happy path / streaming          | A `POL.POL -> BTC.BTC` `0/1/3` stream completes all three parts inside the unfinalized window.                                        | Speculative settlement retains TOR and a staged-only outbound with no early vault or recipient credit. After finality, the swap has one action and out-tx, the recipient and vault are credited, and all lifecycle state is cleared.                                                                                                                                                                                                     |
|     4 | Base / `stream-finality-before-settlement`          | Finality boundary / streaming   | A slower `POL.POL -> BTC.BTC` `0/10/6` stream is only partially processed when the inbound finalizes.                                 | Before finality, a partial queue count exists with TOR reserved and no staged or real outbound or recipient credit. In v1, the same TOR reservation remains while the finalized stream is still partial; only terminal settlement clears it, stages the result, and releases one signed outbound.                                                                                                                                        |
|     5 | Base / `tor-cap-fallback`                           | Capacity fallback / network TOR | `MempoolSwapCapTOR=1` excludes an otherwise eligible `POL.POL -> AVAX.AVAX` swap.                                                     | The observed-unfinalized swap stays outside every speculative surface for at least two blocks, then finality processes it normally once with no residual TOR, stage, or queue state.                                                                                                                                                                                                                                                     |
|     6 | Base / `pool-cap-fallback`                          | Capacity fallback / pool TOR    | `MempoolSwapPoolCapBPS=1` excludes an otherwise eligible `POL.POL -> AVAX.AVAX` swap.                                                 | The same fail-closed pre-finality checks prove no reservation or state mutation; after finality it completes once through ordinary processing and leaves all lifecycle state clean.                                                                                                                                                                                                                                                      |
|     7 | Base / `timeout-rollback`                           | Recovery / timeout              | A speculative `POL.POL -> AVAX.AVAX` swap is staged with a short signing period and bounded recovery attempts.                        | The stage remains pending until the first v1 `LackSigning` cadence at or after its timeout threshold. Rollback emits exactly one expected security and lifecycle-revert event, restores asset custody, clears TOR and all queue/outbound state, and leaves the voter eligible for one later finalized execution. Rune depths are intentionally not required to equal their historical values because v1 reverses against the live pools. |
|     8 | Native / `native-rune-nonstream`                    | Native exclusion / market       | A plain `POL.POL -> THOR.RUNE` market swap is observed before finality.                                                               | At observation and again two THOR blocks later it has no TOR reservation, stage, swap queue, real outbound, or recipient credit. After finality there is exactly one matching THOR action and internal out-tx; the exact credit remains stable for three more blocks.                                                                                                                                                                    |
|     9 | Native / `native-rune-stream`                       | Native lifecycle / streaming    | A `POL.POL -> THOR.RUNE` `0/1/3` stream enters the advanced lifecycle before finality.                                                | TOR is reserved and a valid THOR staged item appears, but no bank credit or real outbound does. Finality clears reservation, stage, and queues; one action credits exactly the staged amount, no streaming out-tx record remains, and the balance stays stable.                                                                                                                                                                          |
|    10 | Native / `native-synth-nonstream`                   | Native exclusion / market       | A plain `POL.POL -> BTC/BTC` synth market swap is observed before finality.                                                           | The same multi-block exclusion checks prove no speculative state or early synth credit. Finality produces one exact synth credit represented by one matching THOR action and internal out-tx, with stable post-settlement state.                                                                                                                                                                                                         |
|    11 | Native / `native-synth-stream`                      | Native lifecycle / streaming    | A `POL.POL -> BTC/BTC` synth `0/1/3` stream is speculatively staged.                                                                  | The staged THOR escrow and TOR reservation exist before finality without credit or a real outbound. The final synth credit equals the staged action amount exactly, lifecycle state clears, no streaming out-tx record appears, and no duplicate credit follows.                                                                                                                                                                         |
|    12 | Native / `native-secured-nonstream`                 | Native exclusion / market       | A plain `POL.POL -> BTC-BTC` secured-asset market swap is observed before finality.                                                   | It remains entirely excluded over the required pre-finality blocks. After finality, exactly one matching internal THOR action/out-tx credits the secured asset, queues remain absent, and the balance is stable.                                                                                                                                                                                                                         |
|    13 | Native / `native-secured-stream`                    | Native lifecycle / streaming    | A `POL.POL -> BTC-BTC` secured-asset `0/1/3` stream is staged, using underlying `BTC.BTC` as escrow.                                  | The staged item’s hash, THOR chain, destination, heights, and escrow asset are validated while recipient credit and real outbounds remain absent. Finality yields one exact secured-asset action, no streaming out-tx record, clean lifecycle state, and no duplicate credit.                                                                                                                                                            |
|    14 | Memoless / `MempoolSwap-MemolessReferenceExclusion` | Memo exclusion / reference      | A registered `POL.POL -> BTC.BTC` memo is submitted as the literal `r:<ref>` `ReferenceRead` memo.                                    | Across three distinct pre-finality THOR heights, TOR, swap queue, staged and pending outbounds, actions, out-txs, recipient balance, and reference use all remain unchanged. After finality it settles exactly once within the quote range, consumes the reference once, signs one outbound, and leaves no pending or scheduled lifecycle state.                                                                                         |

The rollback scenario deliberately controls the fault at the Thornode boundary
by temporarily shortening `SigningTransactionPeriod`. It covers v1 timeout
recovery, including strict allow-listing of its expected security event, but
does not create a real Polygon reorg or exercise validator errata. Scanner-level
reorg and errata coverage remain separate test concerns until the corresponding
lifecycle handling is implemented.

## Design

The simulation test framework will accept a test suite defined as a DAG of actors. Each actor defines operations that are executed sequentially, but may be randomly delayed and interleaved with operations from other concurrently running actors. Unlike regression tests where operations are structs, in the dynamic world of simulation tests every operation is a function which receives config and a mutable state, and returns an error, a bool whether to continue to the next operation in the test, and a bool to mark the test as finished.

Separate from the test actors, we allow definition of watchers. These watchers run asynchrously to check things like invariants and trigger test failure independent of any actor execution.

### Structure

```none
actors/   # one file per actor definition
cmd/      # the executable entry point for the simulation framework
pkg/      # internal packages
suites/   # suites of actors
watchers/ # one file per watcher
```

### Actors

```golang
// Actor is a node in the actor tree, which is processed as a DAG. Each actor has a set
// of operations to execute and a set of children. The actor will execute each operation
// in order until the operation returns Continue = false. If the operation returns
// Finish = true, the actor will stop executing and return the error. Once an actor has
// finished, the children of the actor will be executed. Actors may be appended to other
// actors, which results in the final descendants of the actor being parents of the
// appended actor - blocking the execution of the appended actor until all parents have
// completed, allowing for "fan in" of the execution DAG.
type Actor struct {
	// Name is the name of the actor.
	Name string

	// Timeout is the maximum amount of time the actor is allowed to execute.
	Timeout time.Duration

	// Interval is the amount of time to wait between operations.
	Interval time.Duration

	// Ops is the set of operations to execute.
	Ops []Op `json:"-"`

	// Children is the set of children to execute after the operations have completed.
	Children []*Actor
}

// OpResult is the result of an operation.
type OpResult struct {
	// Continue indicates that actor should continue to the next operation.
	Continue bool

	// Finish indicates that the actor should stop executing and return the error.
	Finish bool

	// Error is the error returned by the operation.
	Error error
}

// Op is an operation that can be executed by an actor.
type Op func(config *OpConfig) OpResult
```

### Watchers

```golang
// Watcher wraps a function that will be executed on some interval in the background for
// the duration of the simulation. The function is passed the same OpConfig as the
// operations in the actor tree so it is able to access the clients and accounts used by
// the actors. If it returns an error or panics, the simulation will be aborted.
type Watcher struct {
	// Name is the name of the watcher.
	Name string

	// Interval is the interval at which the watcher will be executed.
	Interval time.Duration

	// Fn is the function to execute.
	Fn func(config *OpConfig) error
}
```

## Mocknet Master Accounts

The following accounts derive from the `master` mnemonic:

```bash
$ go run -tags mocknet tools/pubkey2address/pubkey2address.go -p tthorpub1addwnpepqwutw9cpacdkgnduh7e6cgd8ar7v5rgqkemxffuxdauzw3nlfq7sxtymlzs

BSC Address: 0xee4eaa642b992412f628ff4cec1c96cf2fd0ea4d
ETH Address: 0xee4eaa642b992412f628ff4cec1c96cf2fd0ea4d
BTC Address: bcrt1qf4l5dlqhaujgkxxqmug4stfvmvt58vx2h44c39
LTC Address: rltc1qf4l5dlqhaujgkxxqmug4stfvmvt58vx2fc03xm
BCH Address: qpxh73huzlhjfzcccr03zkpd9nd3wsasegmrreet72
DOGE Address: mnaioCtEGdw6bd6rWJ13Mbre1kN5rPa2Mo
THOR Address: tthor1f4l5dlqhaujgkxxqmug4stfvmvt58vx2tspx4g
GAIA Address: cosmos1f4l5dlqhaujgkxxqmug4stfvmvt58vx2fqfdej
AVAX Address: 0xee4eaa642b992412f628ff4cec1c96cf2fd0ea4d
BASE Address: 0xee4eaa642b992412f628ff4cec1c96cf2fd0ea4d
TRON Address: TXhG4Vhrxyu8htL8tBdCmndF66D1jyKgJW
```
