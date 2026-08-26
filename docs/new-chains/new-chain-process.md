# New Chain Integration Process

Integrating a new L1 chain into THORChain is a structured process involving proposal, development, testing, and mainnet rollout. This guide outlines the required phases, technical standards, and contributor responsibilities.

## Overview

Each new chain poses risk and operational cost to the network. Chains must be economically meaningful, decentralised, and technically sound to be considered for integration.

The process consists of:

1. [Proposal & Node Vote](#phase-i-proposal-and-approval)
2. [Development & Stagenet Testing](#phase-ii-development-and-stagenet-testing)
3. [Mainnet Rollout](#phase-iii-mainnet-release)

## Phase I: Proposal and Approval

### Chain Requirements

Before proposing, ensure the chain meets the minimum standards defined in [Evaluating New Chains](./evaluating-new-chains.md), including:

- Sufficient decentralization and ossification
- Meaningful trading volume and user base
- Healthy developer and community support

### Proposal Submission

- Share a proposal in `#propose-a-chain` on Discord
- A new channel will be created under “Community Chains” for discussion
- Follow the [Chain Proposal Template](#chain-proposal-template)

### Node Mimir Vote

- Prompt node operators to vote using:

  ```toml
  Halt<ProposedChain>Chain = 1
  ```

- If 50%+ consensus is reached, the chain may proceed to development

## Phase II: Development and Stagenet Testing

### Build the Chain Client

Develop a `ChainClient` implementation under:

```text
/bifrost/pkg/chainclients/<yourchain>
```

Reference existing implementations:

- [UTXO](../chain-clients/utxo.md)
- [EVM](../chain-clients/evm.md)
- [BFT](../chain-clients/bft.md)

Open PRs to:

- [`thornode`](https://gitlab.com/thorchain/thornode)
- [`node-launcher`](https://gitlab.com/thorchain/node-launcher)

Follow the [Chain Client Implementation Guide](./implementation-guide.md) for detailed requirements.

### Stagenet Testing Requirements

Test the following on stagenet:

- Swapping to/from the asset
- Add/withdraw liquidity
- Synth mint/burn
- THORName registration
- Vault funding and churning
- Inbound address responses
- Handling insolvency and unauthorized txs
- `HaltSigning<Chain>` behavior

Minimum usage targets:

- 100 inbound txs
- 100 outbound txs
- 100 RUNE in total adds
- 100 RUNE in total withdrawals

### Chain Client Audit

- An expert familiar with the chain (but not the author) must independently review the implementation
- Audit must be published in the PR under:

```text
/bifrost/pkg/chainclients/<yourchain>
```

## Phase III: Mainnet Release

Steps performed by the dev team:

1. **Admin Mimir**: Halt the chain and disable trading
2. **Daemon Install**: Node operators sync the new chain daemon (`make install`)
3. **Enable Scanning**: Final `node-launcher` PR merged and deployed
4. **Admin Mimir**: Unhalt to enable scanning
5. **Admin Mimir**: `Enable<Chain>Chain = 1` to stamp the chain onto the live vaults
6. **Admin Mimir**: Enable trading once scanning reaches chain tip

Step 5 replaces waiting for a churn. Historically `vault.Chains` was written only at
keygen, so a new chain went live no earlier than the next churn. `Enable<Chain>Chain`
adds it to the vaults already active, once the network has proven it can observe the
chain — a network fee must have reached consensus, and a supermajority of today's
active nodes (and of every Asgard's own membership, so TSS keysign can be assembled)
must be among the nodes that attested it. EVM chains additionally need their router
contract installed first. The chain must be unhalted, so step 4 comes before step 5.

If the gates are not met nothing is stamped and the reason is logged each block
(`chain activation not ready`). Leave the mimir set until the chain is live; unset it
afterwards, since it is only re-read while a chain is still awaiting activation.

### Operational notes

**EVM routers must be installed before step 5.** The gate rejects an EVM chain until a
`ChainContract` exists for it, and `RouterUpgradeController` can only swap an existing
router, never create the first one. A net-new EVM chain therefore ships its initial
`ChainContract` in the enabling release, written from `processMigration`
(`manager_scheduled_migration_<network>.go`) or from genesis. Do this before setting
`Enable<Chain>Chain`, or the chain sits at `evm chain has no router contract` forever.

**Deposits sent before activation are not credited.** An inbound observed on a chain the
receiving vault does not yet carry is no-oped: quorum is recorded, nothing is credited
and nothing is refunded, and activating the chain later does not revisit it. Each one
emits an `EventSecurity` reading `inbound for unsupported chain no-oped: vault=<pubkey>`
with the external tx attached, so the affected deposits can be enumerated from the event
stream and credited case by case in a migration handler. There is no automatic refund:
signing an outbound would require the network to transact on a chain it has not yet
proved it can observe. Keep the chain halted until step 4 so this stays rare.

**Refreshing the readiness proof.** The attester set is drawn from the network-fee voters
that reached consensus. Bifrost posts `MsgNetworkFee` only when the fee rate *changes*,
and its last-posted value is per-process, so on a stable-fee chain (XRP, Cosmos, TRON)
the set is effectively frozen at the validators active when the fee last reached
consensus. Nodes churning in afterwards never join it — a standby node's `MsgNetworkFee`
is rejected — so the supermajority requirement decays across churns. If activation is
being retried long after the first fee consensus and the log reads `readiness proof lacks
a current supermajority`, the recovery depends on how the chain's scanner posts:

- **XRP, Cosmos** post only on a period-aligned chain height (`height %
  GasUpdatePeriodBlocks == 0`). Have at least two thirds of active nodes restart bifrost
  within the same fee-update window: the restart clears the in-memory last fee, and they
  all post again at the same aligned height and rate, forming one fresh voter that can
  reach consensus. Staggered restarts land on different aligned heights, spreading the
  posts across several voter keys, and will not converge.
- **TRON** posts at whatever height it is scanning when the fee changes, with no
  alignment, so restarts do not put the nodes on a common voter key and there is no
  operator-driven refresh. The attester set there only advances when the fee genuinely
  changes network-wide.

A voter key is `CHAIN-height-rate-size`, which is why alignment is what decides whether
independent nodes land on the same one.

## Technical Requirements

### Thornode PR

- Add mocknet service in `build/docker/docker-compose.yml`
- ≥70% unit test coverage
- Respect `<chain>_DISABLED` env var in `bifrost.sh`
- Host a live walkthrough with dev team
- Answer key safety questions:

  - Can value be spoofed?
  - Is asset whitelisting enforced?
  - Are decimals handled correctly?
  - Is gas reporting deterministic?
  - Is solvency reporting implemented?

### Node Launcher PRs

Three sequential PRs are required:

#### 1. Image PR

- Add Dockerfile under `ci/images/<chain>/Dockerfile`
- Pin all source versions to commit hashes

#### 2. Service PR

- Use a previous chain as a template
- Slightly over-provision resources (+20%)
- Add chain to:

  - `get_node_service` in `core.sh`
  - `deploy_fullnode` in `core.sh` with default `enabled=false`
  - `bifrost/values.yaml` with `<chain>_DISABLED`

#### 3. Enable PR

- Update `bifrost/values.yaml` to enable the chain

## Chain Proposal Template

```yaml
Chain Name:
Chain Type: EVM / UTXO / Cosmos / Other
Hardware Requirements: Memory and Storage
Year Started:
Market Cap:
CoinMarketCap Rank:
24hr Volume:
Current DEX Integrations:
Other relevant dApps:
Number of previous hard forks:
```
