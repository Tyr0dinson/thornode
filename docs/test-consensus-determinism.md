# Consensus determinism replay

Run `scripts/test-consensus-determinism.sh` from any directory. The runner uses the
Go 1.26.9 version pinned by `go.mod`, a private writable build cache, a 15 minute
test timeout, limited build parallelism, and the shared build lock. It selects only
`TestConsensusDeterminism*` in `./app`; none of those tests depend on the SDK
simulation flag or skip when it is unset. CI can call this script as a separate
bounded test job. The root Makefile and `.gitlab-ci.yml` are owned by the CI task.

The SDK simulation manager is built from modules implementing
`module.AppModuleSimulation`. The registration test found `auth`, `bank`,
`denom`, `params`, `scheduler`, and `wasm`. THORChain's `AppModule` does not
implement that interface or register weighted operations, so
`TestAppStateDeterminism` does not exercise THORChain swaps, observations, or
outbounds. This suite uses a sanitized, fixed-time THORChain genesis fixture
containing funded BTC and ETH pools, two
observed inbound transactions, a swap queue item, a scheduled outbound item,
network fees, and a nonzero Reserve. Independent app instances import and commit
that state three times per seed. A fixed active validator supplies the required
genesis validator update. Each run finalizes a block before committing and
compares both the app hash and exported THORChain state. The third run reverses
pool order, which should have no effect on keeper state. A one-unit pool change
must produce a different committed app hash, proving the comparator detects
divergence.

The suite also exports the THORChain state, reopens the committed database, and
imports the export into a fresh app. It compares domain records at import and
checks Reserve and pool balances after another commit.

The migration test invokes the standard 3.20.0 upgrade handler with THORChain's
module version map at 18 and checks its transition to version 19. It checks that
the active validator version becomes 3.20.0, Reserve and BTC pool balances
remain unchanged, and two independent runs produce the same post-migration
hash. This synthetic boundary contains no legacy
affiliate collector ledger, so it does not cover its owner-to-name conversion.
The hashes across software versions or across export/import are not assumed to
match: only same-version, same-sequence committed hashes are compared.

The fixture imports persisted domain records through `InitGenesis` and runs
ordinary block begin/end handlers. It does not submit signed user transactions,
so it does not prove swap execution or outbound signing. Those paths need a
separate transaction-level replay with validator and vault fixtures.

Validated on this workspace with `scripts/test-consensus-determinism.sh`:
five tests passed, no skips (Go 1.26.9, `go test -p 2 ./app -run
'^TestConsensusDeterminism' -count=1 -v`).
