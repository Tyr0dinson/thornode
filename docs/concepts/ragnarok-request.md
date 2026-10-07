# Request for Ragnarok (RFR)

A Request for Ragnarok (RFR) is the process for deciding whether THORChain should stop supporting a connected chain and return its funds to users. It runs in parallel to [Architecture Decision Records](../architecture/PROCESS.md): an ADR decides how the protocol is built, while an RFR decides whether a chain leaves it.

The process is chain-agnostic. `<CHAIN>` below is the chain identifier used in mimir keys, e.g. `GAIA`. The first RFR was for GAIA ([work item 3388](https://gitlab.com/thorchain/thornode/-/work_items/3388)).

```admonish info
An RFR does not change any protocol rules. It is a structured way to reach and record a decision before nodes vote on the [Ragnarok mimir](../mimir.md) that actually retires the chain.
```

## Process

### 0. Triggers

An RFR can be opened when a chain meets eligibility criteria agreed in advance, for example:

- **Fee share**: the chain's share of network fee revenue stays below an agreed level.
- **Incident and developer hours**: maintaining the chain client or handling its incidents costs more than an agreed budget.
- **Node cost**: running the chain's daemon is a disproportionate share of node operating cost.

The criteria are set before any specific chain is considered, so that opening an RFR is a mechanical check rather than a judgement about one chain.

### 1. Team input

Collect votes, opinions and requirements from the teams that build on or operate THORChain, including Maya, Liquify, Rujira and the core developers. Each participant discloses any conflict of interest, such as holdings in, or business dependence on, the chain under review.

### 2. Issue

Open a public issue for the RFR. It contains:

- the reasons for the request, with reference to the trigger criteria;
- the team input from step 1;
- the pros and cons of retiring the chain;
- an impact statement for liquidity providers, trade account and secured asset holders, and integrators.

The issue stays open for comment for at least 14 days before any vote.

### 3. Signal vote

Nodes signal with the mimir key `RFR_<CHAIN>`:

| Value   | Meaning               |
| ------- | --------------------- |
| `1`     | in favour of Ragnarok |
| `0`     | against Ragnarok      |
| no vote | abstain               |

The RFR states, before voting opens, the approval threshold, the quorum and the block height at which the vote closes. `RFR_<CHAIN>` is a signal only; it has no effect on the protocol.

An approval lapses if the chain has not been retired within about 180 days of the vote closing. A new RFR is then needed.

### 4. Exit window

Before execution, users get a window to withdraw to L1 while the chain's vaults can still sign:

- liquidity providers withdraw from the chain's pools;
- trade account and secured asset holders withdraw to L1.

The RFR states the conditions under which execution is aborted, for example a security issue or a material change in the trigger criteria, and the path for re-listing the chain later through the normal [new chain process](../new-chains/new-chain-process.md).

### 5. Execution

Node operators are coordinated to vote the Ragnarok mimir for the chain. Once the chain has been retired, a short post-mortem is added to the RFR issue covering what went as planned, what did not, and any changes to this process.
