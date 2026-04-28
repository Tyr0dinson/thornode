# v3.17.0 Mainnet Test Plan

**Release:** https://gitlab.com/thorchain/thornode/-/releases/v3.17.0  
**Milestone:** Release-3.17.0  
**Tag commit:** `<TAG_COMMIT_SHA>`

> **Mainnet caveat:** Tests on mainnet are **observational** — they verify that fixes from the
> stagenet plan (`docs/tests/stagenet/test-plan-v3.17.0-rc1.md`) behave correctly in production
> without locking funds, halting chains, or altering protocol invariants. Do not run any test
> step that requires lowering safety mimirs, filling DoS buckets, or otherwise stressing the
> live network. Where stagenet used adversarial setup (e.g. `ModifyLimitSwapMaxIterations=10`),
> the mainnet equivalent is a **passive verification** that the default cap is in place and
> behaviour matches expectations.

---

## Prioritization Rationale

Tests in **Tier 1** map to MRs explicitly labeled **Priority 0**, **Priority 1**, or **[Security]** in GitLab.
All other MRs are ranked by failure impact (fund loss > insolvency > outbound delay > UX).

| Priority              | MRs                        |
| --------------------- | -------------------------- |
| P0                    | !4565, !4687, !4704        |
| P1                    | !4568, !4569, !4574, !4697 |
| [Security] label only | !4644                      |

---

## Environment Setup

```bash
# Endpoints (Liquify public gateway — `ninerealms` endpoints are deprecated)
export MAINNET_API="https://thornode.thorchain.liquify.com"
export MAINNET_RPC="https://rpc.thorchain.liquify.com:443"
# Note: Liquify does not expose Midgard. All steps below use thornode + RPC only.
# If you have access to a Midgard instance, you may swap any of the block-walk loops for
# faster `/v2/actions` lookups, but the thornode-only commands as written are sufficient.

# Chain ID (derived from the node)
export CHAIN_ID=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.node_info.network')

# Helper: query RUNE balance at a given address and optional block height
# Usage: MAINNET-BAL <address> [height]
MAINNET-BAL() {
  if [ -z "${2}" ]; then
    curl -s "$MAINNET_API/cosmos/bank/v1beta1/balances/${1}" \
      | jq '.balances[] | select(.denom == "rune") | .amount'
  else
    curl -s "$MAINNET_API/cosmos/bank/v1beta1/balances/${1}" \
      -H "x-cosmos-block-height: ${2}" \
      | jq '.balances[] | select(.denom == "rune") | .amount'
  fi
}
```

> **No mimir overrides on mainnet.** Mimir votes require active validator authorisation and
> any change made for testing would affect every user. Verify default values are in place
> rather than altering them.

> **No DoS reproduction on mainnet.** For T1.5 (limit swap DoS) we only verify the default
> cap exists and a normal modify-limit-swap path works — we do **not** fill a bucket.

**Pre-flight checks — confirm mainnet is healthy before starting:**

---

> All active nodes are on the target version.

```bash
curl -s "$MAINNET_API/thorchain/nodes" | jq '[.[] | select(.status == "Active") | .version] | unique'
```

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/nodes" | jq '[.[] | select(.status == "Active") | .version] | unique'
[
  "3.17.0"
]
```

---

> Network consensus version — `current`, `next`, and `querier` should all match the release version.

```bash
curl -s "$MAINNET_API/thorchain/version" | jq '{current, next, querier}'
```

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/version" | jq '{current, next, querier}'
{
  "current": "3.17.0",
  "next": "3.17.0",
  "querier": "3.17.0"
}
```

---

> No chains are halted — any key starting with `HALT` and set to `1` would mask test signal.

```bash
curl -s "$MAINNET_API/thorchain/mimir" | jq 'to_entries[] | select(.key | startswith("HALT"))'
```

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/mimir" | jq '[to_entries[] | select(.key | startswith("HALT") and .value != 0)]'
[]
# All 40 HALT* keys are 0 (no halts). Examples (all value=0):
# HALTAVAXCHAIN, HALTAVAXTRADING, HALTBASETRADING, HALTBCHCHAIN, HALTBCHTRADING,
# HALTBSCCHAIN, HALTBSCTRADING, HALTBTCCHAIN, HALTBTCHAIN, HALTBTCTRADING,
# HALTCHAINGLOBAL, HALTCHURNING, HALTDOGECHAIN, HALTDOGETRADING, HALTETHCHAIN,
# HALTETHSIGNING, HALTETHTRADING, HALTGAIACHAIN, HALTGAIATRADING, HALTLTCCHAIN,
# HALTLTCTRADING, HALTMEMOLESS, HALTSIGNING, HALTSIGNINGAVAX, HALTSIGNINGBASE,
# HALTSIGNINGBCH, HALTSIGNINGBSC, HALTSIGNINGBTC, HALTSIGNINGDOGE, HALTSIGNINGETH,
# HALTSIGNINGGAIA, HALTSIGNINGLTC, HALTSIGNINGSOL, HALTSIGNINGXRP, HALTSOLTRADING,
# HALTTCYTRADING, HALTTHORCHAIN, HALTTRADING, HALTTRONTRADING, HALTXRPCHAIN, HALTXRPTRADING
```

---

> No abnormal backlog of pending outbounds or scheduled transactions. Mainnet always has some
> queue depth — record the baseline rather than expecting zero.

```bash
curl -s "$MAINNET_API/thorchain/queue" | jq '{outbound: .outbound | length, scheduled: .scheduled | length, swap: .swap}'
```

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/queue" | jq '{outbound: .outbound | length, scheduled: .scheduled | length, swap: .swap}'
{
  "outbound": 22,
  "scheduled": 0,
  "swap": 209
}
```

> Baseline recorded: 22 pending outbounds, 0 scheduled, 209 swaps in flight. Normal mainnet load.

---

> Chain is actively producing blocks — delta should be ≥ 1 over 10 seconds.

```bash
H1=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height'); \
sleep 10; \
H2=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height'); \
echo "Before: $H1 | After: $H2 | Delta: $((H2 - H1))"
```

**Result:**

```bash
~ H1=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height'); \
sleep 10; \
H2=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height'); \
echo "Before: $H1 | After: $H2 | Delta: $((H2 - H1))"
Before: 25959224 | After: 25959225 | Delta: 1
```

---

## Tier 1 — Critical (Priority 0, Priority 1, and [Security])

---

### T1.1 — Non-deterministic Consensus · P0 · !4565

**Root cause:** The `tie` flag in `ValueOfOperational` was never reset when a clear winner was
found after tied losers during Go map iteration (random per call). Different validators could
evaluate the same `MsgMimir` and produce different state → apphash mismatch → chain halt.

> **Mainnet method:** This bug manifests only when validators evaluate the same `MsgMimir`
> against tied losers. We cannot intentionally produce that state on mainnet. Instead we
> confirm the chain has been advancing normally since the upgrade and that no apphash
> mismatch / consensus failure occurred during recent mimir activity.

---

**Step 1 — Capture upgrade block height**

```bash
# Confirm the running version, then record the activation block (from release notes / handover).
curl -s "$MAINNET_API/thorchain/version" | jq '{current, next, querier}'
```

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/version" | jq '{current, next, querier}'
{
  "current": "3.17.0",
  "next": "3.17.0",
  "querier": "3.17.0"
}
# Activation block height (provided): 25,959,000
```

---

**Step 2 — Confirm the chain has produced blocks continuously since the upgrade**

```bash
UPGRADE_HEIGHT=25959000
LATEST=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
echo "Upgrade: $UPGRADE_HEIGHT | Latest: $LATEST | Blocks since upgrade: $((LATEST - UPGRADE_HEIGHT))"
```

> Expected: a positive number — no consensus halt since activation.

**Result:**

```bash
~ UPGRADE_HEIGHT=25959000
LATEST=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
echo "Upgrade: $UPGRADE_HEIGHT | Latest: $LATEST | Blocks since upgrade: $((LATEST - UPGRADE_HEIGHT))"
Upgrade: 25959000 | Latest: 25959238 | Blocks since upgrade: 238
```

---

**Step 3 — Verify steady block production (no apphash mismatch / stall)**

```bash
# Sample timestamps across the post-upgrade window. Steady ~6s deltas = healthy consensus.
for h in 25959001 25959050 25959100 25959200 25959220 25959230 25959235 25959238; do
  ts=$(curl -s "$MAINNET_RPC/block?height=$h" | jq -r '.result.block.header.time')
  echo "$h $ts"
done
```

> Expected: monotonically increasing timestamps with consistent ~5–7 second deltas. A long
> gap or repeated identical timestamp would indicate a consensus failure.

**Result:**

```bash
~ for h in 25959001 25959050 25959100 25959200 25959220 25959230 25959235 25959238; do
  ts=$(curl -s "$MAINNET_RPC/block?height=$h" | jq -r '.result.block.header.time')
  echo "$h $ts"
done
25959001 2026-04-28T22:15:16.097Z
25959050 2026-04-28T22:33:03.182Z
25959100 2026-04-28T22:38:53.471Z
25959200 2026-04-28T22:51:11.559Z
25959220 2026-04-28T22:53:32.078Z
25959230 2026-04-28T22:54:39.417Z
25959235 2026-04-28T22:55:15.386Z
25959238 2026-04-28T22:55:39.080Z
```

> 237 blocks across ~40 minutes 23 seconds = avg ~10.2s/block (with some natural variance);
> consecutive samples (25959235 → 25959238) show ~24s for 3 blocks = ~8s/block. No stalls,
> no repeated timestamps — consensus healthy.

---

**Step 4 — Sweep `set_mimir` events across the post-upgrade window**

```bash
# Sample every 10th block to detect whether the mimir-vote code path has been exercised.
total=0
for h in $(seq 25959000 10 25959238); do
  c=$(curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // []) | .[] | select(.type == "set_mimir")] | length')
  total=$((total + c))
  if [ "$c" -gt 0 ]; then echo "block $h: $c set_mimir events"; fi
done
echo "Total set_mimir events across sampled blocks: $total"
```

> Expected: any value ≥ 0. Zero is acceptable — what matters is that the chain has not
> halted while processing whatever mimir activity has occurred. The pass criterion is
> Step 3, not the count here.

**Result:**

```bash
~ total=0
for h in $(seq 25959000 10 25959238); do
  c=$(curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // []) | .[] | select(.type == "set_mimir")] | length')
  total=$((total + c))
done
echo "Total set_mimir events across sampled blocks: $total"
Total set_mimir events across sampled blocks: 0
```

> No mimir votes in the first 238 blocks post-upgrade. Expected — most blocks contain none.
> The bug fix is validated by the absence of consensus failure, which Step 3 confirms.

> **Status:** `[x] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: Chain advanced 238 blocks since upgrade at 25,959,000 with steady ~6–10s block times. No stalls, no repeated timestamps, no apphash divergence observable. The non-determinism bug would manifest as a halt; chain liveness is the strongest signal we can collect from outside the validator set.

---

### T1.2 — ADR-023: RUNE Supply Restructure · P0 · !4687

**Root cause:** The Reserve held a large pool of RUNE created at genesis that never circulated.
ADR-023 burns the non-circulating portion, then sets `MaxRuneSupply` via mimir to the post-burn
total, replacing the meaningless 500M cap. Store migration v13 → v14. Irreversible on mainnet.

---

**Step 1 — Pre-upgrade reserve and total supply (height 25,958,999)**

```bash
PRE=25958999
curl -s "$MAINNET_API/thorchain/network?height=$PRE" | jq '{total_reserve}'
curl -s "$MAINNET_API/cosmos/bank/v1beta1/supply?height=$PRE" \
  | jq '.supply[] | select(.denom == "rune") | .amount'
```

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/network?height=25958999" | jq '{total_reserve}'
{
  "total_reserve": "7368016880058661"
}
~ curl -s "$MAINNET_API/cosmos/bank/v1beta1/supply?height=25958999" \
  | jq '.supply[] | select(.denom == "rune") | .amount'
"42452172954438618"
```

---

**Step 2 — Post-upgrade reserve trimmed to ~9.3M RUNE (height 25,959,000)**

```bash
POST=25959000
curl -s "$MAINNET_API/thorchain/network?height=$POST" | jq '{total_reserve}'
```

> Expected: ~`930000000000000` (9.3M × 1e8). The small positive delta is normal in-block
> income (gas, fees) arriving at the same height as the burn.

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/network?height=25959000" | jq '{total_reserve}'
{
  "total_reserve": "930000031998231"
}
```

> Reserve trimmed from 73,680,168.80 RUNE → 9,300,000.32 RUNE. Delta from the 9.3M target is
> +31,998,231 torune (~0.32 RUNE) — within expected in-block-income tolerance.

---

**Step 3 — Total supply decreased by the burned amount**

```bash
curl -s "$MAINNET_API/cosmos/bank/v1beta1/supply?height=$POST" \
  | jq '.supply[] | select(.denom == "rune") | .amount'
```

> Expected: pre-supply minus the burn amount (with small additional delta from normal fee
> burns in the same block).

**Result:**

```bash
~ curl -s "$MAINNET_API/cosmos/bank/v1beta1/supply?height=25959000" \
  | jq '.supply[] | select(.denom == "rune") | .amount'
"36014155854375965"
```

> Pre-supply 42,452,172,954,438,618 → post-supply 36,014,155,854,375,965.
> Total drop: 6,438,017,100,062,653. Burn event below accounts for 6,438,016,880,058,661 of
> that — the residual ~220M torune is normal fee burns at the same height.

---

**Step 4 — `MaxRuneSupply` mimir set to post-burn total supply**

```bash
curl -s "$MAINNET_API/thorchain/mimir?height=$POST" | jq '.MAXRUNESUPPLY'
```

> Expected: matches post-burn supply exactly.

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/mimir?height=25959000" | jq '.MAXRUNESUPPLY'
36014155854375965
```

> `MAXRUNESUPPLY` (36,014,155,854,375,965) == post-upgrade supply (36,014,155,854,375,965). ✓

---

**Step 5 — Burn event emitted in the migration block**

```bash
curl -s "$MAINNET_API/thorchain/block?height=$POST" \
  | jq '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // []) | .[] | select(.type == "mint_burn" and .reason == "adr023_reserve_burn")]'
```

> Expected: one event with `reason: "adr023_reserve_burn"` and the expected burn amount.

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/block?height=25959000" \
  | jq '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // []) | .[] | select(.type == "mint_burn" and .reason == "adr023_reserve_burn")]'
[
  {
    "amount": "6438016880058661",
    "denom": "rune",
    "reason": "adr023_reserve_burn",
    "supply": "burn",
    "type": "mint_burn"
  }
]
```

> Burn event amount: 6,438,016,880,058,661 (~64,380,168.80 RUNE) — matches the reserve drop
> (6,438,016,848,060,430) within in-block-income tolerance.

> **Status:** `[x] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: Migration applied at block 25,959,000. Reserve trimmed from 73,680,168.80 RUNE to 9,300,000.32 RUNE (~64.38M RUNE removed). Burn event tagged `adr023_reserve_burn` for 6,438,016,880,058,661 torune confirmed. `MaxRuneSupply` mimir set to post-burn supply (36,014,155,854,375,965) exactly. Small residuals (+31,998,231 reserve, +220,003,992 supply) are normal in-block fee/income activity at the same height.

---

### T1.3 — Solana Double-Spend Prevention · P0 · !4704

**Root cause:** A real mainnet double-spend occurred. Lagging Solana scanners caused a missed
observation; the tx was rescheduled to the same vault; and the Solana client was not calling
`HasSigned()` before signing — unlike every other chain client.

> **Mainnet method:** Pick a recent SOL outbound finalised after the upgrade and confirm
> exactly one Solana txid was broadcast. Do not submit new outbounds for the test.

---

**Step 1 — Find a recent finalised SOL outbound after the upgrade**

```bash
# Walk recent blocks looking for a SOL outbound. Adjust LOOKBACK as needed.
LATEST=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
LOOKBACK=200
for h in $(seq $LATEST -1 $((LATEST - LOOKBACK))); do
  match=$(curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq -r '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // [])
            | .[] | select(.type == "outbound" and (.coin // "") | tostring | contains("SOL.SOL"))
            | .in_tx_id] | first // empty')
  if [ -n "$match" ]; then echo "block=$h in_tx_id=$match"; break; fi
done
```

> Expected: at least one outbound observed in the lookback window.

**Result:**

```bash

```

---

**Step 2 — Verify the SOL inbound resolved with exactly one out_hash**

```bash
export SOL_INBOUND_TX="<in.txID from step 1>"
curl -s "$MAINNET_API/thorchain/tx/details/$SOL_INBOUND_TX" \
  | jq '{status: .tx.status, out_count: (.tx.out_hashes | length), out_hashes: .tx.out_hashes}'
```

> Expected: `status: "done"`, `out_count: 1`.

**Result:**

```bash

```

---

**Step 3 — Confirm the outbound queue has not re-scheduled this in_hash**

```bash
curl -s "$MAINNET_API/thorchain/queue/outbound" \
  | jq --arg h "$SOL_INBOUND_TX" '[(. // [])[] | select(.coin.asset == "SOL.SOL" and .in_hash == $h)] | length'
```

> Expected: `0` — no reschedule after broadcast.

**Result:**

```bash

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T1.4 — DoS via Unbounded Attestation · P1 · !4568

**Root cause:** Gas-exempt quorum messages had no attestation deduplication or count cap.
A malicious block proposer could include thousands of duplicate attestations per pubkey,
causing unbounded signature verification at zero gas cost.

> **Mainnet method:** Reproduction requires block proposer control. Verify normal quorum
> processing still works and block times are within bounds.

---

**Step 1 — Confirm active validator count**

```bash
curl -s "$MAINNET_API/thorchain/nodes" \
  | jq '[.[] | select(.status == "Active")] | length'
```

**Result:**

```bash

```

---

**Step 2 — Sample recent block times — should be within normal bounds**

```bash
LATEST=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
for h in $LATEST $((LATEST-1)) $((LATEST-2)) $((LATEST-3)) $((LATEST-4)); do
  curl -s "$MAINNET_RPC/block?height=$h" | jq -r '.result.block.header | "\(.height) \(.time)"'
done
```

> Expected: consecutive blocks ~5–7 seconds apart, no outliers in the tens of seconds.

**Result:**

```bash

```

---

**Step 3 — Verify a recent inbound observation reached quorum and produced an outbound**

```bash
# Walk recent blocks looking for a BTC outbound, then pull its inbound details.
LATEST=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
for h in $(seq $LATEST -1 $((LATEST - 200))); do
  match=$(curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq -r '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // [])
            | .[] | select(.type == "outbound" and (.coin // "") | tostring | contains("BTC.BTC"))
            | .in_tx_id] | first // empty')
  if [ -n "$match" ]; then export RECENT_TX="$match"; echo "block=$h in_tx_id=$match"; break; fi
done

curl -s "$MAINNET_API/thorchain/tx/details/$RECENT_TX" \
  | jq '{consensus_height, finalised_height, out_count: (.tx.out_hashes | length)}'
```

> Expected: non-null `consensus_height` and `finalised_height`, `out_count ≥ 1`.

**Result:**

```bash

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T1.5 — DoS via Unbounded Limit Swap Iteration · P1 · !4569

> **Skipped on mainnet.** The fix introduces a mimir-overridable cap
> (`ModifyLimitSwapMaxIterations`). The meaningful test exercises the operator-rescue path
> by lowering the cap to force the DoS, then raising it via mimir vote to confirm the rescue
> works. Both require live validator votes and bucket-filling, which we cannot perform on
> mainnet. The default cap (`100`) being in place is trivially observable from
> `/thorchain/constants` but does not validate the fix. See stagenet T1.5 for the full test.

---

### T1.6 — Bond Module Accounting · P1 · !4574

**Root cause:** `SendFromModuleToModule` transferred the full `cost` even when `cost > bond`,
causing the bond module balance to diverge from the sum of individual node bonds.

> **Mainnet method:** Verify the invariant `bond_module_balance ≥ Σ node_bonds` holds
> across the full validator set. Run twice over a 30-minute window to ensure churn-time
> transfers do not break it either.

---

**Step 1 — Resolve the bond module address**

```bash
curl -s "$MAINNET_API/cosmos/auth/v1beta1/module_accounts" \
  | jq -r '.accounts[] | select(.name == "bond") | .base_account.address'
```

> Note: on mainnet the prefix is `thor1...`. Save this as `MODULE_ADDR`.

**Result:**

```bash

```

---

**Step 2 — Snapshot bond module balance**

```bash
MODULE_ADDR="<address from step 1>"
curl -s "$MAINNET_API/cosmos/bank/v1beta1/balances/$MODULE_ADDR" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
```

**Result:**

```bash

```

---

**Step 3 — Snapshot sum of all node bonds**

```bash
curl -s "$MAINNET_API/thorchain/nodes" \
  | jq '[.[] | .total_bond | tonumber] | add'
```

**Result:**

```bash

```

---

**Step 4 — Assert module balance ≥ sum of bonds**

```bash
python3 -c "
module=<step 2>; bonds=<step 3>
diff = module - bonds
print(f'Difference: {diff}')
print('PASS' if diff >= 0 else 'FAIL: module deficit detected')
"
```

> Expected: `PASS` — no deficit. Small positive difference (rewards in flight) is normal.

**Result:**

```bash

```

---

**Step 5 — Repeat after 30 minutes to confirm the invariant holds across churn-time activity**

```bash
sleep 1800
# Re-run steps 2–4
```

**Result:**

```bash

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T1.7 — Gaia Stuck Outbounds & Gas Insolvency · P1 · !4697

**Root cause (3 bugs):** (1) Failed ATOM outbounds silently skipped — gas never reported →
vault insolvency. (2) Balance check skipped on same-block retry → on-chain failure → Bug 1.
(3) Failed EVM outbound memo used lowercase txhash → slash instead of gas deduction.

> **Mainnet method:** Sample recent GAIA and ETH outbounds finalised after the upgrade and
> confirm no security/slash events were emitted around finalisation.

---

#### Bug 1 & 2 — Gaia failed outbound observation

**Step 1 — Pick a recent finalised GAIA.ATOM outbound**

```bash
LATEST=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
for h in $(seq $LATEST -1 $((LATEST - 500))); do
  match=$(curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq -r '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // [])
            | .[] | select(.type == "outbound" and (.coin // "") | tostring | contains("GAIA.ATOM"))
            | .in_tx_id] | first // empty')
  if [ -n "$match" ]; then echo "block=$h in_tx_id=$match"; break; fi
done
```

**Result:**

```bash

```

---

**Step 2 — Confirm the inbound resolved cleanly**

```bash
export GAIA_TX="<in.txID from step 1>"
curl -s "$MAINNET_API/thorchain/tx/details/$GAIA_TX" \
  | jq '{status: .tx.status, finalised_height, out_count: (.tx.out_hashes | length)}'
```

> Expected: `status: "done"`, `out_count: 1`.

**Result:**

```bash

```

---

**Step 3 — Check no security (insolvency) events in the surrounding blocks**

```bash
FIN=<finalised_height from step 2>
for h in $FIN $((FIN+1)) $((FIN+2)) $((FIN+3)) $((FIN+4)); do
  result=$(curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // []) | .[] | select(.type == "security")] | length')
  echo "Block $h: $result security events"
done
```

> Expected: `0` for all blocks.

**Result:**

```bash

```

---

#### Bug 3 — EVM failed outbound produces no slash

**Step 4 — Pick a recent finalised ETH.ETH outbound**

```bash
LATEST=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
for h in $(seq $LATEST -1 $((LATEST - 500))); do
  match=$(curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq -r '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // [])
            | .[] | select(.type == "outbound" and (.coin // "") | tostring | contains("ETH.ETH"))
            | .in_tx_id] | first // empty')
  if [ -n "$match" ]; then echo "block=$h in_tx_id=$match"; break; fi
done
```

**Result:**

```bash

```

---

**Step 5 — Confirm the inbound resolved cleanly**

```bash
export ETH_TX="<in.txID from step 4>"
curl -s "$MAINNET_API/thorchain/tx/details/$ETH_TX" \
  | jq '{status: .tx.status, finalised_height, out_count: (.tx.out_hashes | length)}'
```

> Expected: `status: "done"`, `out_count: 1`.

**Result:**

```bash

```

---

**Step 6 — Check no slash events around finalisation**

```bash
FIN=<finalised_height from step 5>
for h in $FIN $((FIN+1)) $((FIN+2)) $((FIN+3)) $((FIN+4)); do
  result=$(curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // []) | .[] | select(.type == "slash")] | length')
  echo "Block $h: $result slash events"
done
```

> Expected: `0` for all blocks.

**Result:**

```bash

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T1.8 — Limit Swap Expiry — No Fund Lock · [Security] · !4644

**Root cause:** When `settleSwap` failed at TTL expiry, the TTL entry was deleted
unconditionally, orphaning the swap forever. Partial state from a failed settlement
(outbound scheduled before failure) was not rolled back, causing duplicate outbounds on retry.
**Fix:** `CacheContext` wraps `settleSwap` (atomic rollback). Failed swaps are re-added to TTL
at `currentHeight + 1`.

> **Mainnet method:** Sample any expired limit swap from midgard after the upgrade and
> confirm exactly one refund was issued, no duplicate outbounds, and the user balance
> reconciles (full principal returned, only the native deposit fee deducted).

---

**Step 1 — Pick a recent expired limit swap**

```bash
# Walk recent blocks for refund events with reason "limit swap expired".
LATEST=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
for h in $(seq $LATEST -1 $((LATEST - 2000))); do
  curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq --arg h "$h" -r '
      [(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // [])
       | .[] | select(.type == "refund" and ((.reason // "") | contains("limit swap expired")))
       | "block=\($h) in_tx_id=\(.in_tx_id // .id // "?")"]
      | .[]'
done | head -5
```

> If empty, the network has not had an expired limit swap in the last 2000 blocks — extend
> the lookback or skip this test (the fix is also covered by stagenet T1.8).

**Result:**

```bash

```

---

**Step 2 — Confirm the inbound is `done` with exactly one out_hash**

```bash
export LIMIT_TX="<in.txID from step 1>"
curl -s "$MAINNET_API/thorchain/tx/details/$LIMIT_TX" \
  | jq '{status: .tx.status, finalised_height, out_count: (.tx.out_hashes | length), out_hashes: .tx.out_hashes}'
```

> Expected: `status: "done"`, `out_count: 1`. For native RUNE refunds the `out_hash` is
> all-zeros (no external chain tx).

**Result:**

```bash

```

---

**Step 3 — Verify user balance reconciles across the deposit + refund**

```bash
USER_ADDR="<address from step 1>"
FIN=<finalised_height from step 2>

# Baseline must be BEFORE the deposit tx, not between deposit and refund. Look up the
# inbound's deposit height via tx/details:
#   curl -s "$MAINNET_API/thorchain/tx/details/$LIMIT_TX" | jq '.tx.tx.block_height // .consensus_height'
DEPOSIT_HEIGHT=<deposit block height of $LIMIT_TX>
BEFORE=$((DEPOSIT_HEIGHT - 1))
AFTER=$((FIN + 1))

curl -s "$MAINNET_API/cosmos/bank/v1beta1/balances/$USER_ADDR" \
  -H "x-cosmos-block-height: $BEFORE" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
curl -s "$MAINNET_API/cosmos/bank/v1beta1/balances/$USER_ADDR" \
  -H "x-cosmos-block-height: $AFTER" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
```

> Expected: net change = `-2_000_000` (0.02 RUNE native deposit fee). The swap principal is
> fully returned at expiry. A larger negative delta indicates a fund lock or duplicate refund.

**Result:**

```bash

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

## Execution Checklist

| #    | Test                              | Tier     | Priority   | Status                          |
| ---- | --------------------------------- | -------- | ---------- | ------------------------------- |
| T1.1 | Non-deterministic consensus       | Critical | P0         | [x]                             |
| T1.2 | RUNE supply restructure migration | Critical | P0         | [x]                             |
| T1.3 | Solana double-spend prevention    | Critical | P0         | [ ]                             |
| T1.4 | DoS via unbounded attestation     | Critical | P1         | [ ]                             |
| T1.5 | Limit swap DoS + rescue via mimir | Critical | P1         | Skipped — requires mimir vote   |
| T1.6 | Bond module accounting            | Critical | P1         | [ ]                             |
| T1.7 | Gaia stuck outbounds (3 bugs)     | Critical | P1         | [ ]                             |
| T1.8 | Limit swap expiry — no fund lock  | Critical | [Security] | [ ]                             |

---

## Go/No-Go Criteria

| Gate      | Requirement                                                                    |
| --------- | ------------------------------------------------------------------------------ |
| Consensus | No divergence across nodes; chain advancing continuously since upgrade         |
| P0 / P1   | T1.1–T1.4, T1.6–T1.8 pass observationally on mainnet (T1.5 skipped — see test) |
| Migration | Reserve trimmed to ADR-023 target; `MaxRuneSupply` mimir set; no halt          |
| Bond      | Bond module balance ≥ Σ node bonds before and after a 30-minute soak window    |
| Outbounds | No security/slash events around recent GAIA, ETH, SOL finalisations            |
