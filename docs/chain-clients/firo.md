# FIRO transparent integration

FIRO uses the shared Bifrost UTXO client with isolated FIRO helpers. It is disabled
in production by default and enabled in standard mocknet simulations. Enabling a
daemon or running the tests does not activate a production FIRO pool.

The implementation was checked against THORNode baseline
`2aae7aeaff2942462f1ce743d08246248974d8c4` and FIRO source
`82464654e394e8c548cadf234bc4c2737d49d2c9`. The native compatibility tests use the
official FIRO Core **v0.14.18.2**, commit
`420fdb81d33a822fe830a918aadfd200801461ed`. The later source commit only updates
OpenSSL in the depends build.

## Scope

| Property           | Behavior                                                                                          |
| ------------------ | ------------------------------------------------------------------------------------------------- |
| Asset              | `FIRO.FIRO`, 8 decimals, shortcode `fi`                                                           |
| Vault              | Compressed secp256k1 public key, exchange address                                                 |
| Networks           | Mainnet/stagenet/chainnet: `EXX`; mocknet: `EXR`; testnet: `EXT`                                  |
| Destinations       | Exact P2PKH, P2SH and exchange scripts                                                            |
| Custody            | Transparent funds only; no Spark, Sigma or Lelantus custody                                       |
| Deposits           | Ordinary transactions with a supported transparent first-input sender                             |
| Finality           | Active-chain block with native `chainlock=true`, or the opt-in InstantSend policy below           |
| Customer mempool   | Opt-in final observations for eligible InstantSend deposits, bounded by an allowance              |
| Vault mempool      | Early outbound bookkeeping after validator quorum; opt-in InstantSend vault-to-vault observations |
| Signing inputs     | Confirmed, ChainLocked exchange outputs; no unconfirmed change                                    |
| Transaction        | Version 1, type 0, no witness, final sequence, locktime 0                                         |
| Signatures         | Legacy `SIGHASH_ALL`, retaining `OP_EXCHANGEADDR` in scriptCode                                   |
| Memo               | 80-byte OP_RETURN; longer memos use P2PKH continuation outputs                                    |
| Continuation value | 546 atoms per output, integration policy                                                          |
| Deposit dust       | 10,000 atoms, integration policy                                                                  |
| Fee rate           | Transparent transaction fees / bytes; minimum 2, empty-block fallback 25 atoms/byte               |
| Relay floor        | Fixed 1,000 atoms/kB when building; live daemon policy can veto broadcast                         |

FIRO Core rejects Spark transactions paying exchange outputs. A sender must first
move Spark funds to a transparent address, then create an ordinary deposit. Bare
public-key, multisig and privacy sender scripts are not attributed as deposit
senders. Special transaction types are skipped before Bitcoin JSON input decoding.

FIRO testnet/regtest P2PKH uses the same Base58 prefix as TRON (`0x41`). The address
alone cannot identify the chain. Callers must pass the asset/chain and use
`IsCurrentNetwork(FIROChain)`; exchange vault prefixes distinguish these networks.

## Code boundaries

- `utxo/firoutil/`: address codec, network types, exact script templates,
  signature hashes, local/TSS signature encoding and validation of the exact
  exchange-spend format. No global Bitcoin network-prefix registration; the
  shared Bitcoin VM is unchanged.
- `common`: deployment network selection and shared address integration. Address
  encoding and validation delegate to `firoutil`, following the `dashutil`
  pattern. The helper package is independent of the parent `common` and UTXO
  client packages.
- `utxo/rpc/firo.go`: native FIRO JSON adapters and watch-only wallet readiness.
- `utxo/firo_finality.go`: native header lookup, finalized scan ceiling, freshness
  and active-chain checks. It does not call Dash's `getbestchainlock`.
- `utxo/firo_scanner.go`: transparent observations, mempool filtering, fee reports
  and finalized history checks.
- `utxo/firo.go`: deterministic input selection, signing, balance accounting and
  broadcast checks.
- `utxo/instant_send*.go`: the shared Dash/FIRO lifecycle, queue, allowance,
  startup recovery and settlement. Its private backend is selected once during
  client initialization; it contains no chain-specific RPC branches.
- `utxo/dash_instant_send.go` and `utxo/firo_instant_send.go`: native candidate
  parsing, reference-height proof, final claim checks and inclusion checks.
  Dash keeps its cycle-hash proof and RPC formats; FIRO uses its own evidence.

The shared client has FIRO dispatch branches. Chain registration, gas metadata,
address derivation, quote continuation addresses, refund network checks, signer
observation policy and configuration are the other shared integration points.
Observer interfaces, TSS messages, consensus protobufs
and existing chain script engines are unchanged.

Custom refund addresses must match the current FIRO network in both ordinary
refunds and reverse swaps after a failed outbound; otherwise the original sender
is used. When mempool scanning is enabled, the signer does not add a sign-time
observation alongside the scanner's mempool and finalized observations, including
when retrying a saved signed transaction. With mempool scanning disabled, the
existing automatic-observation and memoless-outbound settings still apply.

FIRO uses the shared observation window. `ObservationDelayFlexibilityFIRO`, when
set to a nonnegative value, overrides the global window for inbound, outbound,
network-fee, solvency and errata observations. The value is measured in THORChain
blocks. Existing queued tally deadlines keep their original window, so configure
it before enabling the chain. TSS votes do not use this override.

Inputs sort by block height, transaction ID and output index. ChainLock evidence
is checked **after** selection: missing local evidence aborts the attempt instead
of replacing an input. Without MaxGas, selection also budgets the actual input
count and memo continuation cost. Checkpoint input amounts and scripts are checked
against Core before signing.

An RPC failure reading a relevant previous output retries the block before
observation IDs are claimed. Invalid memo bytes are observed with an empty memo,
allowing the normal refund path. A conflict with persisted finalized history
stops progress. `max_resume_block_lag: 0s` prevents recovery from skipping blocks.

Solvency includes external InstantSend-locked deposits with confirmed,
ChainLocked direct parents, even when local early observation is disabled or its
allowance is exhausted. Unlocked external deposits remain excluded. Proven
vault-origin change also counts, including pending change. Missing proof causes
an error instead of publishing a reduced balance. Signing still selects only
confirmed, ChainLocked inputs.

## Optional InstantSend

FIRO InstantSend is **disabled by default**, independently of enabling FIRO.
For the initial ChainLock-only customer launch, keep `FiroInstantSendEnabled=0`
and `FiroInstantSendMaxAmount` zero or unset. The explicit enable value overrides
an accidentally enabled environment setting. Normal vault outbound observations
remain available. Enabling customer InstantSend later is a separate rollout.
It uses the official Core RPCs; no Dash compatibility patch is required.

An eligible transaction must be an ordinary transparent deposit into an Asgard
vault, have native `instantlock=true`, and have every direct input confirmed in
the active ChainLocked chain. Unconfirmed parent chains, special/privacy
transactions, unsupported scripts and unavailable evidence wait for ordinary
block observation. The scanner must be healthy and all applicable halt reads
must succeed.

The observation's reference height is the **highest direct-parent block height**,
and must be positive. It is not the deposit's eventual mined height. Clients can
derive the same height independently, without a local-tip fallback or a Dash
cycle hash. Batches contain one reference height each. The lock and parent proofs
are checked again immediately before claiming allowance and observation markers.

| Control                                            | Default and meaning                                                                                               |
| -------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| `FiroInstantSendEnabled`                           | Unset uses `BIFROST_CHAINS_FIRO_INSTANT_SEND_ENABLED`, which defaults to false; 0 disables, positive enables      |
| `FiroInstantSendMinAmount`                         | Unset uses 1,000,000 atoms (0.01 FIRO); 0 removes this minimum                                                    |
| `FiroInstantSendMaxAmount`                         | Unset or 0 admits no customer deposits; positive values cap the total outstanding early-observed amount, in atoms |
| `BIFROST_CHAINS_FIRO_INSTANT_SEND_MAX_MEMPOOL_AGE` | `2m`; older transactions wait for blocks                                                                          |

Customer admission also needs an active THORChain node. All early InstantSend
observations need a persistent scanner database, including exempt vault transfers.
Without it, ordinary outbound and ChainLock scanning continue. Startup snapshots
reserve eligible locked deposits already in the mempool, then wait for block
scanning to advance. Existing reservations survive
restart, eviction, missing lock flags, disabled admission and lowered limits.
Only verified inclusion in a ChainLocked active block releases them. Scanner
startup and gaps trigger another recovery pass. Settlement precedes deduplication.

Admission saves the allowance reservation and a pending observation marker in one
synchronous write. Observer's per-item callbacks complete the marker only after
it stores the observation. Rejection, a missing callback or a failed acknowledgment
write leaves the ChainLock block fallback available, including after restart or
after InstantSend/mempool scanning is disabled. Rejection does not release the
allowance: another validator may already have observed the payment. Completed
markers suppress later block observations; pending markers allow recovery at the
actual mined height. There is no separate delivery retry worker.

Existing observation markers remain completed, so this upgrade cannot recover
handoffs already lost under those markers. If a block scan races an acknowledgment,
it can report another height variant. Consensus prevents another payment, but a
vault transfer already seen through both mempool paths can retain an additional
`ObserveSlashPoints` charge. Ordinary block claims still precede Observer delivery;
this change does not make all block delivery restart-safe.

Vault-to-vault transactions bypass the amount limits, but still require finality
proof and successful halt checks. Vault outbounds can complete THORChain
bookkeeping once validator observations reach quorum, before InstantSend or
ChainLock finality. A successful broadcast
RPC alone does not complete this bookkeeping. The later ChainLock scan may report
the transaction again. This feature does not enable spending unconfirmed
funds and does not change Observer, TSS, consensus messages or the reservation format.

Metrics use `firo_instantsend_`: `observed_total`, `deferred_total` (by reason),
`reserved_atoms`, `available_atoms`, `pending_transactions`, and `warming_up`.
Existing Dash metric names are preserved.

## Daemon and wallet

Run an unpruned, fully indexed Core node with its wallet enabled:

```ini
server=1
txindex=1
prune=0
disablewallet=0
minrelaytxfee=0.00001
```

Set RPC credentials and restrict RPC access to Bifrost. Mainnet RPC defaults to
8888; regtest defaults to 28888. Every validator needs its own reliable node view
and watch-only wallet. Do not import vault private keys into Core.

Bifrost imports and rescans each exchange address before using its wallet balance.
It writes the wallet account label `thornode-firo-rescanned-v1` only after a
successful rescan. A failed callback is retried when signing or reading balance.
The dedicated import call allows up to four hours and does not automatically
submit overlapping retries. Other RPC calls retain the normal scanner timeout.
Do not set this label manually or restore an incomplete wallet containing it.
Keep the daemon wallet persistent across restarts.
Complete vault registration before accepting InstantSend deposits: Core's wallet
rescan covers blocks and does not discover unmined payments received before the
address was watched. After replacing a lost wallet, let pending payments reach
ChainLocked blocks and finish the rescan before enabling early observation.

## Enable on a prepared network

After all validators have the same implementation and daemon setup, configure:

```sh
export BIFROST_CHAINS_FIRO_DISABLED=false
export BIFROST_CHAINS_FIRO_RPC_HOST=http://firo:8888
export BIFROST_CHAINS_FIRO_USERNAME=your_rpc_user
export BIFROST_CHAINS_FIRO_PASSWORD=your_rpc_password
```

For mocknet, point RPC at a real FIRO regtest quorum. A single regtest daemon cannot
produce ChainLocks, so it cannot make the production client healthy. Exchange
outputs activate at regtest height 1000. Mine to an ordinary wallet address first;
coinbase cannot pay directly to an exchange address.

### Local mocknet quorum

The main `build/docker/docker-compose.yml` includes FIRO in the standard `mocknet`
and `mocknet-cluster` profiles. The `firo` profile also starts the quorum on its own.
It follows the Dash setup: all six services use the same wrapper around the pinned
official Core image, with one mining/controller node (`firo1`) and five masternodes
(`firo2` through `firo6`).
All six share `firo1`'s network namespace, using loopback P2P ports 18444–18449
and RPC ports 28888–28893. This is required by the pinned Core release: it rejects
private `externalip` addresses when choosing its local address and falls back to
loopback in regtest. Registering a Docker bridge address would leave masternodes
in `ERROR` with `Local address does not match the address from ProTx`.

The FIRO-specific Python entrypoint under `build/docker/mocknet/firo/` registers
the masternodes, forms real `llmq_5_60` quorums, mines past exchange activation,
and funds the simulation master with 10,000 FIRO. It then keeps mining every
12 seconds. No production consensus code or ChainLock RPC responses are changed.

Run these commands on a Docker-capable host, from the repository. For existing
data from the previous image, complete the [ownership migration](#migrating-existing-firo-data)
before starting the new containers.

```sh
# Build and start a single THORChain mocknet, including the FIRO quorum.
make build-mocknet
make run-mocknet
make test-simulation-no-reset
```

To repair an existing mocknet started with separate FIRO network namespaces,
update the checkout and recreate all six FIRO services together. Their data
volumes are retained; the controller updates the existing registrations:

```sh
docker compose -f build/docker/docker-compose.yml --profile mocknet up -d --force-recreate firo1 firo2 firo3 firo4 firo5 firo6
make test-simulation-no-reset
```

For a validator cluster, use `make test-simulation-cluster`. These simulations
include FIRO pool bootstrap, swaps, memoless transfers, solvency and Ragnarok
through the normal shared suites. The single-node default also includes
consolidation; cluster runs cover vault migration and inactive-vault refunds. FIRO
InstantSend remains disabled: customer deposits wait for ChainLocks.

Bifrost's mocknet environment is inherited by every validator and points to
`http://firo1:28888`. Simulation uses `http://127.0.0.1:28888`. This is the only
published FIRO port and is bound to loopback. RPC credentials are `thorchain` /
`password`, for local mocknet only.
The simulation waits for advancing ChainLocks after exchange activation and for
confirmed funding before spending change to fund the next user.

Standard simulations require the FIRO Bifrost client and quorum. Production
defaults in `config/default.yaml` remain disabled.

The default master address is `EXRKTVjv4YXSCYrX6C6or4WS2Sg1wWoKX5LK`, derived from
the simulation's master mnemonic and THORChain HD path. `FIRO_BLOCK_TIME` controls
the wall-clock block interval; `FIRO_MASTER_ADDR` and `FIRO_MASTER_AMOUNT` override
initial funding. Changing funding settings requires a fresh FIRO mocknet.

Each daemon has a named data volume. The `firo-quorum` volume stores operator
keys, registrations, the regtest clock and the signed funding transaction.
Restarting retains these and does not fund the master again. At startup, the
controller updates registered service addresses when endpoints change. Recreate
all six services together when replacing `firo1`, since the other five share its
network namespace. Preserve or reset all
seven FIRO volumes together. `down` preserves volumes; `down -v` removes them,
including other services' volumes if they are part of the selected Compose setup.

Readiness requires confirmed funding, a recent miner heartbeat and an actual
ChainLock at the tip or its parent. The miner advances regtest time together on
all six nodes so ordinary transactions can satisfy Core's ten-minute mining
delay quickly. It first waits for transaction relay. A missing peer pauses
mining and makes readiness expire until the peer returns. This is a local test
clock, not a public-network setting.

During bootstrap, masternodes restart once after registration to refresh peer
authentication. Clock updates wait through these RPC outages for up to three
minutes per node, repeating the same saved timestamp. Mining starts only after
all selected nodes accept the update.

The image is amd64. Building/running it on ARM requires the host's amd64 emulation.

### Daemon image

The image under `build/docker/firo/` is shared by mocknet and the integration test
runner. `thornode-firo:0.14.18.2` is a small wrapper based on the official
`firoorg/firod:0.14.18.2` image pinned to digest
`sha256:a14a82a076252d72366b54c919b364ced5610e71ac8d23178eceb40bd824457e`.
The wrapper retains the official daemon and CLI binaries and adds Python, the
health check and writable data directories. It does not compile FIRO or download
separate binaries. The Compose and integration test builds support `DOCKER_MIRROR`
for the base image.

`build/docker/firo/firo.conf.template` provides a daemon configuration reference.

### Migrating existing FIRO data

The previous image used UID/GID `1000:1000`; the official image uses
`1001:1001`. Existing data needs a one-time ownership change. Fresh named volumes
already receive the correct ownership from the wrapper image.

For mocknet, run the following in Bash from the repository, using the same
Compose project settings as the existing stack. The commands read the actual
volume names from all six existing containers before stopping them. Only the
six FIRO data volumes and the shared quorum volume are changed. Wallets, keys,
registrations, funding records and blockchain data are preserved; do not use
`down -v` for this migration.

```bash
(
  set -euo pipefail
  firo_compose() {
    docker compose -f build/docker/docker-compose.yml --profile firo "$@"
  }
  firo_services=(firo1 firo2 firo3 firo4 firo5 firo6)
  firo_volumes=()
  for firo_service in "${firo_services[@]}"; do
    firo_container=$(firo_compose ps -aq "$firo_service")
    test -n "$firo_container" || { echo "Missing existing container: $firo_service" >&2; exit 1; }
    firo_volume=$(docker inspect --format '{{range .Mounts}}{{if and (eq .Type "volume") (eq .Destination "/data")}}{{.Name}}{{end}}{{end}}' "$firo_container")
    test -n "$firo_volume" || { echo "Missing data volume: $firo_service" >&2; exit 1; }
    firo_volumes+=("$firo_volume")
  done
  firo_container=$(firo_compose ps -aq firo1)
  firo_quorum=$(docker inspect --format '{{range .Mounts}}{{if and (eq .Type "volume") (eq .Destination "/quorum")}}{{.Name}}{{end}}{{end}}' "$firo_container")
  test -n "$firo_quorum" || { echo "Missing quorum volume" >&2; exit 1; }
  firo_volumes+=("$firo_quorum")
  printf 'FIRO volume: %s\n' "${firo_volumes[@]}"

  firo_compose build firo1
  firo_compose stop "${firo_services[@]}"
  for firo_volume in "${firo_volumes[@]}"; do
    docker run --rm --platform linux/amd64 --network none --user 0:0 \
      --mount "type=volume,source=$firo_volume,target=/data" \
      --entrypoint chown thornode-firo:0.14.18.2 -R 1001:1001 /data
  done
  firo_compose up -d --force-recreate "${firo_services[@]}"
)
```

If the old containers have already been removed, identify the seven existing
volume names with `docker volume ls` and inspect them before applying the same
ownership change. Do not create new volumes in place of the existing data.

## Tests

On a Docker-capable host, build the wrapper and compare the daemon and CLI checksums
against the pinned official image. Each binary's checksum must match across both
images, and the wrapper's `id` output must show UID/GID `1001:1001`:

```sh
docker compose -f build/docker/docker-compose.yml --profile firo build firo1
docker run --rm --platform linux/amd64 --entrypoint sha256sum \
  firoorg/firod:0.14.18.2@sha256:a14a82a076252d72366b54c919b364ced5610e71ac8d23178eceb40bd824457e \
  /bin/firod /bin/firo-cli
docker run --rm --platform linux/amd64 --entrypoint sha256sum \
  thornode-firo:0.14.18.2 /bin/firod /bin/firo-cli
docker run --rm --platform linux/amd64 --entrypoint id thornode-firo:0.14.18.2
```

Start the mocknet and verify that all six services become healthy, ChainLocks
advance and the master receives its initial funding. After restarting, the saved
members and funding transaction in `/quorum/state.json` must remain unchanged
and block production must resume. Repeat this check after migrating old volumes.

From the THORNode repository, with normal access to its private TSS dependency:

```sh
go test -mod=readonly -tags mocknet ./common \
  ./bifrost/pkg/chainclients/utxo/... ./bifrost/signer ./x/thorchain
```

Native Core spend validation creates a temporary peerless wallet, mines funding,
tests all three destination types with a 250-byte memo, and checks that Core
rejects signatures which omit the exchange opcode:

```sh
FIROD=/absolute/path/to/firod go test -mod=readonly -count=1 -v \
  ./bifrost/pkg/chainclients/utxo/firoutil
```

Run all three integration suites (ChainLocks, InstantSend and mocknet recovery)
on a Docker-capable host:

```sh
make test-firo-integration
```

The target builds the existing official FIRO image wrapper, then a separate test
image containing Go, Python and FIRO's upstream test framework. It downloads the
v0.14.18.2 sources and verifies commit
`420fdb81d33a822fe830a918aadfd200801461ed`; it does not compile FIRO. The Python
environment includes `pyasyncore==1.0.5` for the framework's `asyncore` import.
No host `FIRO_SOURCE`, `FIROD`, `FIROCLI` or Python environment is needed.

The build uses the same `DOCKER_MIRROR` and `GO_PRIVATE_GIT_CREDENTIAL` support as
the other Docker test targets (`CI_JOB_TOKEN` is also supported). Normal repository
prerequisites, Docker and access to the private Go dependencies are required.
The initial build needs network access; subsequent builds reuse Docker's cache.
The daemon image is amd64, so an ARM host needs Docker's amd64 emulation.

The container runs as `firod`, runs the suites sequentially and stops at the first
failure. Each suite owns a fresh six-node network. It publishes no ports and does
not mount existing mocknet data or the Docker socket. Failure output includes the
last 100 lines of each test log before the container and its temporary data are
removed. This remains separate from `make test` and is not invoked by CI.

The ChainLocks and InstantSend scripts reuse FIRO's upstream EvoZnode test
framework. For native debugging only, they can also be run individually using a
local v0.14.18.2 source checkout and release binaries. Place `firo-cli` beside
`firod`, or set `FIROCLI` to its executable path:

```sh
export FIRO_SOURCE=/absolute/path/to/firo-source
export FIROD=/absolute/path/to/firod
python3 test/integration/firo/chainlocks.py --srcdir="$(dirname "$FIROD")"
python3 test/integration/firo/instantsend.py --srcdir="$(dirname "$FIROD")"
```

Use Python 3.11, or install `pyasyncore` in an isolated Python environment for newer
Python versions. The upstream framework imports `asyncore`. This test checks
startup without locks, real quorum locks, isolation, recovery, a finalized
deposit, production local-key signing/broadcast and outbound confirmation.
The InstantSend test creates an actual unmined quorum-locked deposit and compares
two independent Bifrost clients connected to different Core nodes. It checks
solvency recognition, signing exclusion, persisted allowance recovery and
delivery recovery after mining: one client persists and acknowledges its early
observation, while the other loses the handoff and recovers it from the ChainLocked
block after restarting with early scanning disabled. The test aligns Core's mock clock with wall time for
the mempool age gate. These tests do not replace a multi-validator THORChain TSS
ceremony test.

The mocknet entrypoint also has a standalone native test, using only Python's
standard library and the release binaries:

```sh
FIROD=/absolute/path/to/firod python3 test/integration/firo/mocknet.py
```

It tests fresh bootstrap, funding, health checks, ordinary payments, continued
quorum rotation, recovery from mismatched private-address registrations, and
restarting with changed peer addresses while preserving
keys, registrations and the original funding transaction. Set `FIRO_TEST_DIR` to
retain daemon data and logs. Without it, the test uses and cleans a temporary directory.
It also forces a masternode to restart between the RPC readiness probe and clock
synchronization, checking that the controller survives and forms ChainLocks.

The simulation module includes FIRO signing and broadcast support by default:

```sh
cd test/simulation
go test -mod=readonly -tags mocknet ./pkg/utxo ./cmd ./actors/common
go run -tags mocknet ./cmd
```

The full simulation needs the THORChain mocknet services, including the FIRO
quorum provided by the standard Compose profiles. The Compose startup funds the
master automatically. Both native tests own temporary nodes and stop
them when they finish; they are not persistent simulation services.

Before public activation, run the existing simulation's swap, refund,
consolidation, churn/migration, solvency and Ragnarok stages with FIRO enabled and
multiple THORChain validators. Include restart and failed-keysign recovery, and
compare unsigned transaction bytes across signers. Keep FIRO halted until this
deployment validation is complete; then follow the normal chain activation and
pool rollout process.
