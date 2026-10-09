# Test quality CI

The root Go module and `test/simulation` are independent modules. Run the fast
checks with the Go version specified by their `go.mod` files. The root module
currently pins Go 1.26.9; the nested module requires 1.26.9. Limit concurrent
builds on a shared host with `flock /tmp/thornode-test-quality-build.lock` and
`GOMAXPROCS=4 GOFLAGS=-p=2`. Use a writable `GOCACHE` if the shared cache is
read only. Put the Go 1.26.9 toolchain's `bin` directory first on `PATH` and
set `GOTOOLCHAIN=local`; verify `go version` reports `go1.26.9` before testing.

Fast MR commands, in dependency order (run each `make` command under the shared
lock on a shared host):

```sh
python3 -m unittest discover -s scripts/tests -p 'test_quality_*_test.py' -v
GOTOOLCHAIN=go1.26.9 GOMAXPROCS=4 GOFLAGS=-p=2 flock -w 600 /tmp/thornode-test-quality-build.lock make test-quality-mainnet
GOTOOLCHAIN=go1.26.9 GOMAXPROCS=4 GOFLAGS=-p=2 flock -w 600 /tmp/thornode-test-quality-build.lock make test-quality-simulation
GOTOOLCHAIN=go1.26.9 GOMAXPROCS=4 GOFLAGS=-p=2 flock -w 600 /tmp/thornode-test-quality-build.lock make test-coverage-sum
GOTOOLCHAIN=go1.26.9 GOMAXPROCS=4 GOFLAGS=-p=2 flock -w 600 /tmp/thornode-test-quality-build.lock make test-race-bifrost
```

`test-quality-mainnet` requires the Monero fail-closed test to pass. The runner
also fails if no top-level tests execute. Each run saves selected packages,
JSON events, a JUnit report, skipped test counts, and coverage under
`test-quality-reports/<module>-<configuration>/`. Generated protobuf files are
excluded from the filtered profile, matching the root coverage target.
The simulation module job fails on any skip outside its two named daemon
integration exclusions and requires the core, Solana, and types gocheck suites
to report their expected minimum case counts.
The root unit job also saves a source inventory of modules, build constraints,
and test-file directories. Its stagenet, chainnet, mainnet, mainnet migration,
and mocknet subruns each publish their own JUnit and coverage artifacts.

`scripts/test-quality-coverage.py --profile coverage.txt --base-ref <base-sha>
--output test-quality-reports/root-mocknet/changed-coverage.json` reports
statement coverage for changed Go lines by package. Add
`--baseline-profile <base-coverage.txt>` to compare overall coverage in changed
packages against the latest successful target-branch coverage artifact. Without
a baseline, the report explicitly says that a regression comparison is
unavailable. CI uses the MR diff base when the commit is present in its checkout
and attempts to fetch the baseline with its job token. This report is
diagnostic; it has no arbitrary coverage threshold.

Deeper runs on isolated infrastructure:

```sh
GOTOOLCHAIN=go1.26.9 GOMAXPROCS=4 GOFLAGS=-p=2 make test-regression
GOTOOLCHAIN=go1.26.9 GOMAXPROCS=4 GOFLAGS=-p=2 make test-simulation-cluster-native TEST_SIMULATION_REQUIRE_XMR=1
```

Simulation records `RANDOM_SEED_HEX` as an artifact before
starting. Both integration jobs retry only runner or infrastructure failures;
an assertion failure is a failed job. The full simulation's container and
mocknet setup must be isolated from other concurrent test work.

The curated mutation runner is committed with this branch. Its fast mode runs
for relevant MRs with a 30 minute runner budget; its deep mode runs on
scheduled pipelines with a 55 minute budget. Both preserve JSON summaries and
fail for unexpected surviving mutations. Run them locally with
`python3 scripts/test-security-mutations.py --mode fast --summary /tmp/mutations-fast.json`
or `python3 scripts/test-security-mutations.py --mode deep --budget-seconds 3300 --summary /tmp/mutations-deep.json`; the runner owns the build
lock. `consensus-determinism` runs the committed THORChain replay runner after
unit tests on merge requests and schedules and saves its full output.
`security-fuzz-fast` gives each of the five native targets 1,000 iterations on
merge requests; `security-fuzz-deep` gives each target 20 seconds on schedules.
Both fail when any target fails, save the command log, and reuse the Go fuzz
cache across pipelines. The source seed corpus is committed under
`testdata/fuzz` where needed. These jobs use the committed runners directly.
The four-seed accounting property suite is part of the root unit job.
`accounting-properties-deep` runs its 32-seed campaign on scheduled pipelines
and saves the result log. Locally, run
`scripts/test-accounting-property.sh fast` or
`scripts/test-accounting-property.sh campaign`; the runner owns the build lock.

The simulation fixture changes in this integrated branch make the nested
module unit suite self-contained. Its two daemon integration tests remain
explicit exclusions in `scripts/test-simulation-unit.sh` and require their
own isolated service environment.
