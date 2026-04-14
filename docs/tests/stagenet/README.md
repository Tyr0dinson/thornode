# Stagenet Test Plans

This directory contains manual test plans executed against the THORChain stagenet for each release candidate. Each plan is run before a release is promoted to mainnet.

---

## File Naming

```
test-plan-v<VERSION>-rc<N>.md
```

Examples: `test-plan-v3.17.0-rc1.md`, `test-plan-v3.18.0-rc1.md`

---

## Test Plan Structure

Each test plan is a self-contained Markdown document with the following sections:

### Header

- **Release** — GitLab release URL and tag commit SHA.
- **Milestone** — The GitLab milestone associated with the release.

### Prioritization Rationale

A table mapping MR numbers to their priority tier (P0, P1, [Security]). This drives the test ordering — highest-risk changes are validated first so a blocking failure is caught early.

### Environment Setup

Shell variables and helpers that every test step assumes are set. Copy-paste this block into your terminal before starting. Key variables:

| Variable | Purpose |
| --- | --- |
| `STAGENET_API` | REST API endpoint |
| `STAGENET_RPC` | Tendermint RPC endpoint |
| `STAGENET_MIDGARD` | Midgard API endpoint |
| `CHAIN_ID` | Derived from the node at test time |
| `ACTIVE_NODES` | Array of active signer node keys |
| `STANDBY_NODES` | Array of standby node keys |
| `WALLET` | Default key used for regular transactions |

Destination addresses for external chains (`BTC_DEST_ADDR`, `SOL_DEST_ADDR`, etc.) are also defined here and should point to wallets you control on the respective testnet/stagenet.

### Pre-flight Checks

Sanity checks that must pass before any test is run:

1. All active nodes are on the target version.
2. Consensus version (`current`, `next`, `querier`) matches the release.
3. No chains are halted (`HALT*` mimir keys).
4. No backlog of pending outbounds or scheduled transactions.
5. Chain is actively producing blocks (delta ≥ 1 over 10 seconds).

If any pre-flight check fails, stop and investigate before proceeding.

### Test Tiers

Tests are grouped by risk and priority:

| Tier | Description | Must pass for Go/No-Go? |
| --- | --- | --- |
| **Tier 1 — Critical** | P0, P1, and [Security] MRs. Fund-safety and consensus bugs. | Yes |
| **Tier 2 — Bifrost** | Bifrost reliability: signing races, broadcast correctness. | Recommended |
| **Tier 3 — Features** | New protocol features with observable on-chain behaviour. | Recommended |
| **Tier 4 — Infra** | Infrastructure fixes: gas caps, address support, supply endpoints. | Best-effort |

For release candidate testing, **completing all Tier 1 tests is the minimum bar** for a Go decision.

### Individual Tests

Each test follows this layout:

```
### T<tier>.<n> — <Title> · <Priority> · <MR>

**Root cause:** One-paragraph explanation of the bug or feature.

---

**Step N — <What this step does>**
```bash
<command>
```
> Expected: <what a passing result looks like>

**Result:**
```bash
<actual output pasted here after running>
```

> **Status:** `[x] Pass`   `[ ] Fail`   Notes: <brief summary>
```

The **Expected** annotation is written before the test is run and defines the pass criterion. The **Result** block is filled in during execution. The **Status** line is updated to `[x] Pass` or `[x] Fail` with a one-line note summarising what was observed.

### Execution Checklist

A summary table at the end of the document with one row per test. Update the `Status` column as tests complete. This is the at-a-glance view for sign-off.

### Go/No-Go Criteria

A table of gates that must be satisfied before the release is promoted. Each gate maps to one or more test groups.

---

## Running Tests

### Prerequisites

- `sthornode` binary built from the release tag (`make install`).
- Node key files for `snode1`–`snode6` accessible to the keyring.
- Network access to the stagenet endpoints.

### Workflow

1. Set up the environment variables from the **Environment Setup** section.
2. Run the **Pre-flight Checks**. All must pass.
3. Work through the tests in order, top to bottom.
4. For each step: run the command, paste the output into the **Result** block, and confirm it matches **Expected**.
5. Mark each test `[x] Pass` or `[x] Fail` and add a brief note.
6. Update the checklist table.
7. After all Tier 1 tests are done, evaluate against the **Go/No-Go Criteria**.

---

## Conventions and Gotchas

These were learned the hard way during v3.17.0 testing and apply to all future plans.

### Sequential transactions — use `sleep 12`, not `--unordered`

`--unordered` transactions are not enabled on stagenet. Sending multiple deposits in a loop without a sleep causes sequence mismatch errors on all but the first. Use `sleep 12` (2 full blocks) between sequential deposits from the same account.

```bash
# Correct
for i in $(seq 1 5); do
  sthornode tx thorchain deposit ... --yes --gas auto
  sleep 12
done
```

### Swap queue endpoint

`/thorchain/swap/queue` returns "Not Implemented" on stagenet. Use `/thorchain/queue` instead:

```bash
# Wrong — returns error JSON; jq 'length' gives 3 (key count), not swap count
curl -s "$STAGENET_API/thorchain/swap/queue" | jq 'length'

# Correct
curl -s "$STAGENET_API/thorchain/queue" | jq '.swap'
```

### Block events — flatten before filtering

`/thorchain/block` returns a nested object, not a flat array. Flatten all three event arrays before filtering:

```bash
curl -s "$STAGENET_API/thorchain/block?height=$H" \
  | jq '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // []) | .[] | select(.type == "security")]'
```

### Security and slash events — query the right block

Security (insolvency) and slash events are emitted once in the block where the issue occurs. Always query at and around `finalised_height`, not the current block:

```bash
for height in $FIN $((FIN+1)) $((FIN+2)) $((FIN+3)) $((FIN+4)); do
  result=$(curl -s "$STAGENET_API/thorchain/block?height=$height" \
    | jq '[...events... | select(.type == "security")] | length')
  echo "Block $height: $result security events"
done
```

### Transaction details endpoint

To look up a finalised transaction, use `/thorchain/tx/details/<hash>` (not `/thorchain/tx/<hash>`):

```bash
curl -s "$STAGENET_API/thorchain/tx/details/$TX_HASH" \
  | jq '{status: .tx.status, finalised_height, out_hashes: .tx.out_hashes}'
```

### Historical balance queries

The Cosmos bank endpoint accepts an `x-cosmos-block-height` header for point-in-time balance lookups. Use this to capture balances before and after a transaction by using the block **before the deposit** as the baseline — not a block after the funds have already left:

```bash
# Snapshot BEFORE deposit (e.g. current height = 9526542)
curl -s "$STAGENET_API/cosmos/bank/v1beta1/balances/$ADDR" \
  -H "x-cosmos-block-height: 9526542" \
  | jq '.balances[] | select(.denom == "rune") | .amount'

# Snapshot AFTER refund (e.g. finalised_height + 1 = 9526552)
curl -s "$STAGENET_API/cosmos/bank/v1beta1/balances/$ADDR" \
  -H "x-cosmos-block-height: 9526552" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
```

Using a post-deposit block as the "before" baseline hides the deposit deduction and makes the refund appear larger than it is.

### All-zeros out_hash

An `out_hash` of `0000...0000` (64 zeros) means a native RUNE refund on THORChain — no external chain transaction is involved. This is expected for expired limit swaps or RUNE refunds.

### Mimir votes require supermajority

Mimir changes require >2/3 of active nodes (3 of 4 on the standard stagenet). Loop through all 4 active nodes to guarantee the threshold:

```bash
for snode in $ACTIVE_NODES; do
  sthornode tx thorchain mimir <KEY> \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- <VALUE>
  sleep 2
done
sleep 12  # wait for the new value to take effect
```

To reset a mimir key to its hardcoded default, vote `-1`.

### Standby nodes cannot vote mimir

Only active nodes are authorised to submit mimir transactions. Standby nodes (snode2, snode5) will return `"are not authorized: unauthorized"` — this is expected and not a test failure.

### Address prefix

Stagenet addresses use the `sthor1` prefix; mainnet uses `thor1`. Module addresses also differ between networks. Verify the bond module address via:

```bash
curl -s "$STAGENET_API/cosmos/auth/v1beta1/module_accounts" \
  | jq '.accounts[] | select(.name == "bonding") | .base_account.address'
```

### Native deposit fee

Every `sthornode tx thorchain deposit` deducts a small native transaction fee (~2,000,000 torune = 0.02 RUNE) via the ante handler, regardless of the swap outcome. When measuring net balance change across a swap-and-refund cycle, account for this fee — the net loss should be exactly the deposit fee, not zero.

---

## Writing a New Test Plan

When creating a test plan for a new release:

1. Copy `test-plan-v<PREV>.md` as a starting template.
2. Update the header (release URL, tag commit, milestone).
3. Replace all test sections with tests relevant to the new release MRs.
4. Keep the **Environment Setup**, **Pre-flight Checks**, and structural sections — they rarely change.
5. Use the prioritization table to decide which tests belong in Tier 1.
6. Leave all **Result** blocks empty and all statuses as `[ ] Pass   [ ] Fail   Notes: ___`.
7. Run the pre-flight checks first, then fill in results top to bottom during execution.

A good test step has:
- A single observable outcome (one thing to check).
- A concrete **Expected** annotation written before running.
- A command that can be copy-pasted without modification (all variables resolved via the env setup).
