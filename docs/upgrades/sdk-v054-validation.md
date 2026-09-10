# SDK v0.54.4 validation record

Validation date: September 10, 2026. Host: Go1.26.5 darwin/arm64; Docker29.7.2 linux/aarch64 available. Base commit: `757199a809e069932aeaff1628a6483d5fdde416`. Final dependency pins and activation gates are in [the upgrade instructions](v4.0.0.md).

**Dependency resolution, successful application checks, and mainnet readiness are distinct.** No recent mainnet snapshot, live Ledger, or isolated multi-validator chain was supplied. No deployment, publication or activation height was performed.

## Baseline

A clean `git archive HEAD` under a temporary directory preserved the original source while the working tree changed. Baseline app tests passed. Untagged `go test ./...` failed in pre-existing tests; the more relevant untagged module-only command failed the same 31 named cases before and after the upgrade, while both mocknet module runs passed. Examples include liquidity/slashing/saver/affiliate tests, five DexAggregator cases and KeeperOutboundFeesSuite.TestGetSurplusForTargetMultiplier. Assertions were not changed.

The baseline complete module graph already failed on EVM rc2's nonexistent `evmd/v0.0.0`. The simulation module's baseline Ledger fork already failed against the SDK three-argument SignSECP256K1 interface. Simulation tests also require absent local chain/wallet services; see below.

## Reproducible application checks

Run from the repository root unless shown otherwise. Commands that inspect the final graph use readonly module mode so they cannot silently select a different SDK.

```sh
go list -mod=readonly -m all
go mod verify
(cd test/simulation && go list -mod=readonly -m all && go mod verify)
go list -mod=readonly -deps ./app/... ./cmd/thornode ./cmd/bifrost
make build
TAG=stagenet make build
TAG=mainnet make build
TAG=chainnet make build
make test
go test -mod=readonly ./app/... ./log ./x/thorchain/keeper/abci ./cmd/thornode
go test -mod=readonly -tags mocknet ./app
go test -mod=readonly -tags mocknet ./app -run '^TestFullAppSimulation$' -Enabled -NumBlocks=3 -BlockSize=5 -Commit=true -Seed=42
go test -mod=readonly -tags mocknet ./x/thorchain/... ./x/denom/... ./x/scheduler/...
go test -mod=readonly -tags ledger ./app ./cmd/thornode -run 'Test(SDKSigningRoundTrip|SDKLegacyMultisigSigning|SDKDirectEIP712Translation|Eip712Signing)$'
go test -mod=readonly -count=1 -tags mocknet -timeout 10m ./bifrost/p2p/... ./bifrost/tss/go-tss/keysign
go test -mod=readonly -tags mocknet -run '^$' ./bifrost/tss/go-tss/...
(cd test/simulation && go build -mod=readonly -tags mocknet -o /tmp/thornode-sdk54-simtest ./cmd)
(cd test/simulation && go test -mod=readonly -tags mocknet ./...)
```

| Check                                                                                | Result                                                                                                                                              |
| ------------------------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| Both complete module graphs, production dependency listing and checksum verification | PASS with SDK0.54.4/EVM0.6.3                                                                                                                        |
| `go mod tidy`, both modules                                                          | PASS with the unchanged EVM v0.6.3 signing/codec packages retained locally                                                                          |
| Root app/params/log/proposal/CLI tests and full mocknet app suite                    | PASS                                                                                                                                                |
| Two local apps, replay and disk-backed restart                                       | PASS, heights7–10; equal app hashes, validator updates, commit IDs and final exported state                                                         |
| Mocknet THORChain/denom/scheduler module suites                                      | PASS, baseline and upgraded                                                                                                                         |
| `make build`, stagenet/mainnet/chainnet builds, `make test`                          | PASS; clean baseline `make test` also PASS                                                                                                          |
| Standalone simulation binary, mocknet                                                | PASS                                                                                                                                                |
| Enabled randomized app simulation, seed42/3blocks                                    | FAIL identically before/after: unresolved ContinuousVestingAccount type URL during genesis generation                                               |
| p2p full suite + TSS keysign                                                         | PASS (p2p136.5s, keysign5.7s)                                                                                                                       |
| All TSS packages, mocknet compile                                                    | PASS                                                                                                                                                |
| Root Ledger-tag software signing checks                                              | PASS; no hardware Ledger test                                                                                                                       |
| Simulation `-tags ledger`                                                            | FAIL in both baseline and upgraded: preserved ledger-thorchain-go v1.0.3 has two-argument SignSECP256K1, SDK requires three                         |
| Simulation unit tests                                                                | FAIL in same four packages as baseline: cmd, pkg/dag, pkg/solana, pkg/types; local chain IDs, Solana readiness and UTXO wallet services unavailable |

Focused checks added in `app/sdk_upgrade_test.go` exercise actual auth5→6 and Wasm1/2/3→4 migrations, preserved account/code identity, migration-only Wasm registration, writable branch lifecycle, pin/unpin charges and native library version. The signing tests cover direct, direct-aux with separate fee payer, textual metadata, custom Amino, multisig wire round-trip, and Ethereum signing. Fixed baseline EIP712 fixtures test old-signature acceptance, not merely self-sign/verify round trips. A proposal boundary test checks exact protobuf byte capacity; existing Bifrost suites exercise injection cache types and byte limits.

## Contract regression commands and findings

Build/run into isolated directories; never point REGTEST_HOME_BASE at a live node home. Use the same commands against a clean archive for baseline comparison. The runner uses local ports, so run old/new sequentially and wait for child nodes to exit. Keep AUTO_UPDATE and EXPORT unset when checking existing goldens.

```sh
mkdir -p /tmp/thornode-sdk54-bin /tmp/thornode-sdk54-homes
GOBIN=/tmp/thornode-sdk54-bin make _build-test-regression REGTEST_BIN_DIR=/tmp/thornode-sdk54-bin
cp -R test/regression/mnt /tmp/thornode-sdk54-mnt
mkdir -p /tmp/thornode-sdk54-mnt/coverage
cd test/regression
PATH=/tmp/thornode-sdk54-bin:$PATH \
REGTEST_BIN_DIR=/tmp/thornode-sdk54-bin \
REGTEST_HOME_BASE=/tmp/thornode-sdk54-homes \
REGTEST_MNT_DIR=/tmp/thornode-sdk54-mnt \
RUN='suites/wasm/' PARALLELISM=1 /tmp/thornode-sdk54-bin/regtest
```

Baseline: 11/12 strict suites passed; `wasm/bindings.yaml` failed its expected MsgSend error-log assertion at line160. That assertion also failed in an earlier upgraded run but passed in the final run, so it is intermittent.

The initial SDK0.54.4/EVM0.6.3 contract-only run completed all operations in 12/12 suites but failed the old state goldens because of expected SDK metadata changes. The subsequent full Docker regression run (`CI=true EXPORT=1 PARALLELISM=6 TIME_FACTOR=2 FAIL_FAST=true make test-regression`) passed **181/181 suites with zero retries**, preserving all operation assertions and export invariants.

The refreshed 181 state exports were compared structurally against the previous goldens. Differences were limited to new account numbers, contract `ibc2_port_id`, an empty consensus authority object, and removal of null vault `deficit` fields by the existing normalizer. Twelve normalized block exports changed only transaction signatures, transaction hashes, block data hashes, and matching signature/transaction-ID events. Transaction bodies and economic state were unchanged. A strict CI run with EXPORT and AUTO_UPDATE unset must verify the refreshed goldens.

Parallel execution exposed a CometBFT v0.39 startup failure when workers shared the profiling port. The runner now assigns each worker its own profiling port on both initial start and debug restart. It also checks HTTP errors before dereferencing responses, retaining retries and failure reporting.

The existing normalized block export zeros gas_used, app hashes and timing fields. Matching it is **not** proof of matching raw gas or distributed app hashes. Separate old/new `gas-fee.yaml` runs with `UNFILTER_EXPORT=1` used fresh temporary artifacts and retained all fee/event assertions:

| Transaction          | Baseline gas | Upgraded gas | Difference |
| -------------------- | -----------: | -----------: | ---------: |
| Mimir, height2       |       101857 |       101857 |          0 |
| Mimir, height3       |        88497 |        88497 |          0 |
| StoreCode, height4   |      2248012 |      2248012 |          0 |
| Instantiate, height5 |       107955 |       106954 |      -1001 |
| Execute, height6     |        71453 |        71868 |       +415 |

The upgraded measurements above come from a freshly rebuilt final SDK0.54.4/EVM0.6.3 binary, superseding intermediate candidate measurements. The repository regression target intentionally sets `constants.Version=9.999.0`; app release metadata is4.0.0. All five transactions returned code0 and unchanged fee/event assertions passed. These are measured fixture results, not a guarantee that all existing contracts consume equivalent gas. Exact internal attribution of each delta and mainnet contract gas-margin review remain activation gates. The block gas meter is explicitly enabled; unchanged configured gas constants alone do not prove unchanged VM gas.

## Native and networking evidence

`LibwasmvmVersion()` returns3.0.7 in the application test. Linux ARM64 Debian shared-library dependency resolution passed for the packaged .so. Container COPY points to the matching v3.0.7 module's Linux libraries; this is not a claim of a complete release-image build.

An isolated new libp2p0.48 host using production transport/security/mux options connected to an old0.11 host built against the baseline and exchanged ping/pong over `/p2p/tss`, negotiating TCP, Noise and Yamux. Full new-node p2p tests pass. Cross-version DHT and complete threshold signing, sustained load, real multi-validator upgrade/restart/catch-up/state-sync remain outstanding; a transport handshake does not establish those properties.

## Independent reviews

- Dependency reviewer found the invalid baseline EVM graph, modern libp2p dependency collision, native packaging change and baseline simulation Ledger mismatch. A fixed pre-upgrade signature test rejected EVM0.7.x; the unchanged v0.6.3 signing/codec packages retained locally preserve behavior and resolve the tidy dependency-test closure.
- Migration reviewer verified registered modules, auth5→6 and Wasm4→4 source versions, validator-version ordering and context lifecycle. Executing old Wasm migrations exposed the missing legacy parameter key table, which was restored. Actual migration tests pass from Wasm1/2/3 and auth5.
- Correctness reviewer identified the EIP712 round-trip coverage gap and independently reproduced contract operations, expected state metadata changes and actual gas differences. No additional introduced production defect was found in the reviewed paths. Reviews do not substitute for mainnet rehearsal or distributed consensus evidence.

## Final execution summary

Final `make build` and `make test` passed with EVM v0.6.3 and release metadata4.0.0. The prescribed test target includes network-specific common/UTXO and migration checks and a stagenet build; separate mainnet and chainnet builds passed. The standalone mocknet simulation binary also builds. The enabled randomized app simulation fails identically in the clean baseline and upgraded tree during genesis generation because `/cosmos.vesting.v1beta1.ContinuousVestingAccount` is not registered. This is recorded as an inherited simulation failure, not a passing simulation.

The synthetic `TestSDKTwoAppReplayAndRestart` initializes two independent applications from the checked-in Wasm gas-fee export. Because normalized regression exports strip active-node versions, the test restores them to the binary version before executing blocks. Both apps agree through heights7–10, including closing/reopening one disk database and native VM after height8; final exported state and consensus parameters agree. This does not rehearse an old-binary upgrade halt or distributed consensus.

Strict CI verification of the refreshed regression goldens, physical Ledger, recent-mainnet-state migration rehearsal and distributed consensus/networking validation remain open. Successful local builds and tests do not establish mainnet upgrade readiness.
