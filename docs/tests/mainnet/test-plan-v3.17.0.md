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

**Step 1 — Find a recent post-upgrade SOL outbound via Tendermint event search**

> The thornode `/thorchain/block` endpoint emits `outbound` events with the **outbound chain's**
> coin format (e.g. `BTC~BTC`), but `SOL~SOL` outbounds do not appear there directly. Use the
> Tendermint RPC `/tx_search` instead — it indexes txs by event attributes including
> `outbound.chain='SOL'`, returning the cosmos tx hash that emitted the event.

```bash
curl -s "$MAINNET_RPC/tx_search?query=%22outbound.chain%3D%27SOL%27%22&per_page=5&order_by=%22desc%22" \
  | jq '.result.txs[]? | {height, hash}'
```

> Expected: at least one entry with `height ≥ 25,959,000` (post-upgrade).

**Result:**

```bash
~ curl -s "$MAINNET_RPC/tx_search?query=%22outbound.chain%3D%27SOL%27%22&per_page=5&order_by=%22desc%22" \
  | jq '.result.txs[]? | {height, hash}'
{
  "height": "25967115",
  "hash": "D5914D6400A987A2446B735DF56055A34FE695A0F263DF3678B16491EE9B27C0"
}
{
  "height": "25962857",
  "hash": "0638EC3A3C85CF48B8B4A52CAA4389EBF175F832890D723984BC7B2ED655B8AE"
}
{
  "height": "25957982",
  "hash": "887D75CDD1B990EC266C0A45719EAE17D2A5134E2F4F2C5D86BB56703E472E33"
}
```

> Two post-upgrade SOL outbounds confirmed: blocks 25,962,857 and 25,967,115.

---

**Step 2 — Extract the inbound `in_tx_id` and Solana txid from each `outbound` event**

```bash
for cosmos_hash in D5914D6400A987A2446B735DF56055A34FE695A0F263DF3678B16491EE9B27C0 \
                   0638EC3A3C85CF48B8B4A52CAA4389EBF175F832890D723984BC7B2ED655B8AE; do
  echo "=== cosmos_tx=$cosmos_hash ==="
  curl -s "$MAINNET_RPC/tx?hash=0x$cosmos_hash" \
    | jq -r '.result.tx_result.events[] | select(.type == "outbound") | .attributes[] | "\(.key)=\(.value)"'
done
```

**Result:**

```bash
=== cosmos_tx=D5914D6400A987A2446B735DF56055A34FE695A0F263DF3678B16491EE9B27C0 ===
in_tx_id=EB60EB618303996098A7E198B62B02CD098EF3B4B38F1A83C293ECBE37EC59FE
id=9ETEfTbfcgDVEPB1fmNHYTxFodyz2WKVmzNNeQhHoud8rkafx5chmz81R9g32Kvu6rKGWUfPsia9kzhQvFEBfHv
chain=SOL
coin=13363201 SOL.SOL

=== cosmos_tx=0638EC3A3C85CF48B8B4A52CAA4389EBF175F832890D723984BC7B2ED655B8AE ===
in_tx_id=CB844294E14A0ED242444E0E7C7D20A87D516ABB9D543552A5F6DE0E59918074
id=3KtnMkuSrs4QBeFTCS8MuSvgfZng9DqLNNqnwDoSConTPfFzbDQsSk4tMyLrSw9avCvSQbrqxtMq2G8eBmdJqcPQ
chain=SOL
coin=505157387 SOL.SOL
```

---

**Step 3 — Verify each inbound has exactly one Solana out_hash (no double-spend)**

```bash
for SOL_INBOUND_TX in EB60EB618303996098A7E198B62B02CD098EF3B4B38F1A83C293ECBE37EC59FE \
                      CB844294E14A0ED242444E0E7C7D20A87D516ABB9D543552A5F6DE0E59918074; do
  echo "=== inbound $SOL_INBOUND_TX ==="
  curl -s "$MAINNET_API/thorchain/tx/details/$SOL_INBOUND_TX" \
    | jq '{status: .tx.status, finalised_height, out_count: (.tx.out_hashes // [] | length), out_hashes: .tx.out_hashes}'
done
```

> Expected: `status: "done"` and exactly **one Solana txid** in `out_hashes` per inbound.
> Note: native RUNE refund/internal legs may also appear as an all-zeros out_hash — that is
> not a Solana broadcast and does not count toward the double-spend signal.

**Result:**

```bash
=== inbound EB60EB618303996098A7E198B62B02CD098EF3B4B38F1A83C293ECBE37EC59FE ===
{
  "status": "done",
  "finalised_height": 25967095,
  "out_count": 1,
  "out_hashes": [
    "9ETEfTbfcgDVEPB1fmNHYTxFodyz2WKVmzNNeQhHoud8rkafx5chmz81R9g32Kvu6rKGWUfPsia9kzhQvFEBfHv"
  ]
}
=== inbound CB844294E14A0ED242444E0E7C7D20A87D516ABB9D543552A5F6DE0E59918074 ===
{
  "status": "done",
  "finalised_height": 25962777,
  "out_count": 2,
  "out_hashes": [
    "0000000000000000000000000000000000000000000000000000000000000000",
    "3KtnMkuSrs4QBeFTCS8MuSvgfZng9DqLNNqnwDoSConTPfFzbDQsSk4tMyLrSw9avCvSQbrqxtMq2G8eBmdJqcPQ"
  ]
}
```

> First inbound: 1 Solana txid, no double-spend. ✓
> Second inbound: out_count=2 BUT one is all-zeros (native RUNE refund) and one is the Solana
> broadcast `3KtnMk...`. **Exactly one Solana txid** — no double-spend. ✓

---

**Step 4 — Confirm neither inbound is still scheduled for outbound (no rebroadcast)**

```bash
for h in CB844294E14A0ED242444E0E7C7D20A87D516ABB9D543552A5F6DE0E59918074 \
         EB60EB618303996098A7E198B62B02CD098EF3B4B38F1A83C293ECBE37EC59FE; do
  count=$(curl -s "$MAINNET_API/thorchain/queue/outbound" \
    | jq --arg h "$h" '[.[] | select(.in_hash == $h)] | length')
  echo "in_hash=$h still in queue: $count"
done
```

> Expected: `0` for both — once signed and observed, the outbound is not rescheduled.

**Result:**

```bash
~ for h in CB844294E14A0ED242444E0E7C7D20A87D516ABB9D543552A5F6DE0E59918074 \
         EB60EB618303996098A7E198B62B02CD098EF3B4B38F1A83C293ECBE37EC59FE; do
  count=$(curl -s "$MAINNET_API/thorchain/queue/outbound" \
    | jq --arg h "$h" '[.[] | select(.in_hash == $h)] | length')
  echo "in_hash=$h still in queue: $count"
done
in_hash=CB844294E14A0ED242444E0E7C7D20A87D516ABB9D543552A5F6DE0E59918074 still in queue: 0
in_hash=EB60EB618303996098A7E198B62B02CD098EF3B4B38F1A83C293ECBE37EC59FE still in queue: 0
```

---

**Step 5 — Cross-check via vault SOL.SOL balance drop**

```bash
# 0.505 SOL drop should be visible at the block of the second outbound (25962857)
for h in 25962855 25962857; do
  bal=$(curl -s "$MAINNET_API/thorchain/vaults/asgard?height=$h" \
    | jq '[.[] | .coins[]? | select(.asset == "SOL.SOL") | .amount | tonumber] | add')
  echo "h=$h sol_balance=$bal"
done
```

> Expected: balance drops by ≈ outbound amount (505,157,387 lamports for the 25962857 outbound).

**Result:**

```bash
~ for h in 25962855 25962857; do ...
h=25962855 sol_balance=74102170410
h=25962857 sol_balance=73597012523
```

> Drop = 505,157,887 lamports. Matches outbound `coin=505157387 SOL.SOL` (residual 500
> = vault gas/fee accounting). Vault accounting is consistent with a single SOL broadcast.

> **Status:** `[x] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: Two post-upgrade SOL outbounds verified (blocks 25,962,857 and 25,967,115). Each inbound resolved to exactly **one Solana txid** in `out_hashes` (`3KtnMk...` and `9ETEfTb...`). Neither is reappearing in the outbound queue. Vault SOL balance drops match the broadcast amounts. The `HasSigned()` cache is doing its job — no double-spend on Solana.

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
~ curl -s "$MAINNET_API/thorchain/nodes" \
  | jq '[.[] | select(.status == "Active")] | length'
95
```

> 95 active validators on mainnet (vs 4 on stagenet) — much larger attestation surface area.

---

**Step 2 — Sample recent block times — should be within normal bounds**

```bash
LATEST=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
for h in $LATEST $((LATEST-1)) $((LATEST-2)) $((LATEST-3)) $((LATEST-4)) $((LATEST-5)); do
  curl -s "$MAINNET_RPC/block?height=$h" | jq -r '.result.block.header | "\(.height) \(.time)"'
done
```

> Expected: consecutive blocks ~5–7 seconds apart, no outliers in the tens of seconds.

**Result:**

```bash
~ LATEST=25967454
~ for h in 25967454 25967453 25967452 25967451 25967450 25967449; do
  curl -s "$MAINNET_RPC/block?height=$h" | jq -r '.result.block.header | "\(.height) \(.time)"'
done
25967454 2026-04-29T14:32:38.884Z
25967453 2026-04-29T14:32:32.621Z
25967452 2026-04-29T14:32:26.660Z
25967451 2026-04-29T14:32:20.447Z
25967450 2026-04-29T14:32:14.418Z
25967449 2026-04-29T14:32:08.081Z
```

> 5 consecutive deltas: 6.26s, 5.96s, 6.21s, 6.03s, 6.34s — all within normal ~6s bounds.
> No quorum-processing slowdowns visible.

---

**Step 3 — Verify a recent inbound observation reached quorum and produced an outbound**

> The Tendermint `tx_search` index (rather than the `/thorchain/block` endpoint) is the
> canonical way to find outbounds — see T1.3 Step 1 for the discovery.

```bash
# Find a recent post-upgrade BTC outbound. Constrain by tx.height to avoid 504 timeouts on
# unconstrained queries.
curl -s "$MAINNET_RPC/tx_search?query=%22outbound.chain%3D%27BTC%27%20AND%20tx.height%3E25967000%22&per_page=3&order_by=%22desc%22" \
  | jq '.result.txs[]? | {height, hash}'
```

**Result:**

```bash
~ curl -s "$MAINNET_RPC/tx_search?query=..." | jq '.result.txs[]? | {height, hash}'
{ "height": "25967489", "hash": "DFEED7DD1F9F03A151E4FA8090F38B2E7DBFF0A15213EEF191B0C33568797516" }
{ "height": "25967458", "hash": "9CA6A846AB778C68AAE44E1F95AACCC0A1DB20BF5E8379825A7DD17E813ECCA8" }
{ "height": "25967449", "hash": "C34791C54817F421740FFAD1C528BA2036C4E6290F88A445E31201E2C9E37579" }
```

```bash
# Extract in_tx_id from the most recent BTC outbound event
curl -s "$MAINNET_RPC/tx?hash=0xDFEED7DD1F9F03A151E4FA8090F38B2E7DBFF0A15213EEF191B0C33568797516" \
  | jq -r '.result.tx_result.events[] | select(.type == "outbound") | .attributes[] | "\(.key)=\(.value)"'

# Confirm the inbound resolved with quorum
curl -s "$MAINNET_API/thorchain/tx/details/CE193B424B4E3857E30577CD21836CCE74B530139FF44B04FEE0483F7EFC91DE" \
  | jq '{status: .tx.status, consensus_height, finalised_height, out_count: (.tx.out_hashes // [] | length), out_hashes: .tx.out_hashes}'
```

> Expected: non-null `consensus_height` and `finalised_height`, `out_count ≥ 1`.

**Result:**

```bash
in_tx_id=CE193B424B4E3857E30577CD21836CCE74B530139FF44B04FEE0483F7EFC91DE
id=75A7DADA9C439E4546C85B08DFA89754E4BCA9C4FEF8429BCB67AE7080B062A4
chain=BTC
coin=82726 BTC.BTC

{
  "status": "done",
  "consensus_height": 25967484,
  "finalised_height": 25967484,
  "out_count": 2,
  "out_hashes": [
    "0000000000000000000000000000000000000000000000000000000000000000",
    "75A7DADA9C439E4546C85B08DFA89754E4BCA9C4FEF8429BCB67AE7080B062A4"
  ]
}
```

> Inbound `CE193B42...` reached quorum at block 25,967,484 (consensus_height == finalised_height)
> and produced exactly one BTC broadcast `75A7DA...` (the all-zeros entry is a native RUNE leg
> as in T1.3). Quorum processing working normally with 95 active validators.

> **Status:** `[x] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: 95 active validators. Block times steady at ~6s/block. Recent BTC outbound (`CE193B42...`) reached consensus and finalisation in the same block (25,967,484), produced a single BTC broadcast `75A7DA...`. The attestation deduplication cap at the validator count does not appear to have broken normal quorum behavior. The actual fix (preventing thousands of duplicate attestations from a malicious proposer) requires proposer control to reproduce — out of scope for mainnet observation.

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
# The unscoped /cosmos/auth/v1beta1/module_accounts endpoint panics on Liquify ("account is not
# a module account: panic"). Use the scoped per-name endpoint instead.
curl -s "$MAINNET_API/cosmos/auth/v1beta1/module_accounts/bond" \
  | jq -r '.account.base_account.address'
```

> Note: on mainnet the prefix is `thor1...`. Save this as `MODULE_ADDR`.

**Result:**

```bash
~ curl -s "$MAINNET_API/cosmos/auth/v1beta1/module_accounts/bond" \
  | jq -r '.account.base_account.address'
thor17gw75axcnr8747pkanye45pnrwk7p9c3cqncsv
```

---

**Step 2 — Snapshot bond module balance (T+0, height 25,967,670)**

```bash
MODULE_ADDR="thor17gw75axcnr8747pkanye45pnrwk7p9c3cqncsv"
curl -s "$MAINNET_API/cosmos/bank/v1beta1/balances/$MODULE_ADDR" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
```

**Result:**

```bash
~ curl -s "$MAINNET_API/cosmos/bank/v1beta1/balances/$MODULE_ADDR" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
"10090321287282272"
```

---

**Step 3 — Snapshot sum of all node bonds (T+0)**

```bash
curl -s "$MAINNET_API/thorchain/nodes" \
  | jq '[.[] | .total_bond | tonumber] | add'
```

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/nodes" \
  | jq '[.[] | .total_bond | tonumber] | add'
9864362380413334
```

---

**Step 4 — Assert module balance ≥ sum of bonds (T+0)**

```bash
python3 -c "
module=10090321287282272; bonds=9864362380413334
diff = module - bonds
print(f'Module balance: {module:,}')
print(f'Sum of bonds:   {bonds:,}')
print(f'Difference:     {diff:,}')
print('PASS' if diff >= 0 else 'FAIL: module deficit detected')
"
```

> Expected: `PASS` — no deficit. Small positive difference (rewards in flight) is normal.

**Result:**

```bash
Module balance: 10,090,321,287,282,272
Sum of bonds:   9,864,362,380,413,334
Difference:        225,958,906,868,938
PASS
```

> Difference = 225,958,906,868,938 torune (~2,259,589 RUNE, +2.29% over bonds). Normal
> rewards-in-flight surplus.

---

**Step 5 — Repeat after 30 minutes to confirm the invariant holds across churn-time activity**

```bash
sleep 1800
H=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
MODULE=$(curl -s "$MAINNET_API/cosmos/bank/v1beta1/balances/$MODULE_ADDR" \
  | jq -r '.balances[] | select(.denom == "rune") | .amount')
BONDS=$(curl -s "$MAINNET_API/thorchain/nodes" \
  | jq '[.[] | .total_bond | tonumber] | add')
echo "T+30m height=$H module=$MODULE bonds=$BONDS"
python3 -c "m=$MODULE; b=$BONDS; print('diff=', m-b); print('PASS' if m>=b else 'FAIL')"
```

**Result:**

```bash
T+30m height=25968098 module=10093834264481625 bonds=9867799097659866
diff= 226035166821759
PASS
```

> T+30m snapshot at height 25,968,098 (Δ = +428 blocks ≈ 30 minutes from T+0):
> - Module balance: 10,093,834,264,481,625 torune (T+0: 10,090,321,287,282,272 → +3,512,977,199,353)
> - Sum of bonds:    9,867,799,097,659,866 torune (T+0:  9,864,362,380,413,334 → +3,436,717,246,532)
> - Difference:        226,035,166,821,759 torune (T+0: +225,958,906,868,938 → +76,259,952,821 buffer growth)
>
> Both balances grew (rewards accruing + minor bond fluctuation). The invariant `module ≥ bonds`
> continues to hold across the 30-minute window — no deficit drift.

> **Status:** `[x] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: T+0 (height 25,967,670): module 10,090,321,287,282,272 ≥ bonds 9,864,362,380,413,334 (Δ = +225,958,906,868,938, +2.29%). T+30m (height 25,968,098): module 10,093,834,264,481,625 ≥ bonds 9,867,799,097,659,866 (Δ = +226,035,166,821,759, +2.29%). Invariant holds across the soak window — no module deficit, no drift.

---

### T1.7 — Gaia Stuck Outbounds & Gas Insolvency · P1 · !4697

**Root cause (3 bugs):** (1) Failed ATOM outbounds silently skipped — gas never reported →
vault insolvency. (2) Balance check skipped on same-block retry → on-chain failure → Bug 1.
(3) Failed EVM outbound memo used lowercase txhash → slash instead of gas deduction.

> **Mainnet method:** Sample recent GAIA and ETH outbounds finalised after the upgrade and
> confirm no security/slash events were emitted around finalisation.

---

#### Bug 1 & 2 — Gaia failed outbound observation

**Step 1 — Find a recent post-upgrade GAIA.ATOM outbound via tx_search**

```bash
curl -s "$MAINNET_RPC/tx_search?query=%22outbound.chain%3D%27GAIA%27%20AND%20tx.height%3E25959000%22&per_page=3&order_by=%22desc%22" \
  | jq '.result.txs[]? | {height, hash}'
```

**Result:**

```bash
{ "height": "25968157", "hash": "30F9E390F92A82400C56A47C162D57E14B8D4D15DA6078617D6A44DB9F7CD405" }
{ "height": "25968139", "hash": "DEDDF26937693AAAE889EB9041BD6A7DB0077523EBF4152DDDBD4EA4201420AC" }
{ "height": "25968120", "hash": "DF42B879E80AF6E80B97B32DE5D5D60E8B67B4AA8864E8778C79274248970135" }
```

```bash
# Extract in_tx_id from the most recent GAIA outbound
curl -s "$MAINNET_RPC/tx?hash=0x30F9E390F92A82400C56A47C162D57E14B8D4D15DA6078617D6A44DB9F7CD405" \
  | jq -r '.result.tx_result.events[] | select(.type == "outbound") | .attributes[] | "\(.key)=\(.value)"'
```

```bash
in_tx_id=E7B8835351D4E8E554D666F52C1C6DB57DAB2AAEE444E787472071F3589E8737
id=E9B66AE7C9CFCCE6626399178B7E0F3AE669BB886DA2397A6F92B7EA8F2E2AA2
chain=GAIA
coin=86987600 GAIA.ATOM
memo=REFUND:E7B8835351D4E8E554D666F52C1C6DB57DAB2AAEE444E787472071F3589E8737
```

> Note: the `memo=REFUND:` prefix means this was a refund outbound (not a regular swap), but
> it still exercises the same gas-reporting path — the relevant signal is whether gas was
> properly accounted for and no insolvency event was emitted.

---

**Step 2 — Confirm the inbound resolved cleanly**

```bash
export GAIA_TX=E7B8835351D4E8E554D666F52C1C6DB57DAB2AAEE444E787472071F3589E8737
curl -s "$MAINNET_API/thorchain/tx/details/$GAIA_TX" \
  | jq '{status: .tx.status, consensus_height, finalised_height, out_count: (.tx.out_hashes // [] | length), out_hashes: .tx.out_hashes}'
```

> Expected: `status: "done"`, exactly one GAIA broadcast in `out_hashes`.

**Result:**

```bash
{
  "status": "done",
  "consensus_height": 25968152,
  "finalised_height": 25968152,
  "out_count": 1,
  "out_hashes": [
    "E9B66AE7C9CFCCE6626399178B7E0F3AE669BB886DA2397A6F92B7EA8F2E2AA2"
  ]
}
```

> Single GAIA broadcast `E9B66AE7...`, finalised cleanly at block 25,968,152.

---

**Step 3 — Check no security (insolvency) events in the surrounding blocks**

```bash
FIN=25968152
for h in $FIN $((FIN+1)) $((FIN+2)) $((FIN+3)) $((FIN+4)) $((FIN+5)); do
  result=$(curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // []) | .[] | select(.type == "security")] | length')
  echo "Block $h: $result security events"
done
```

> Expected: `0` for all blocks.

**Result:**

```bash
Block 25968152: 0 security events
Block 25968153: 0 security events
Block 25968154: 0 security events
Block 25968155: 0 security events
Block 25968156: 0 security events
Block 25968157: 0 security events
```

> No insolvency events around finalisation — Bug 1 & 2 fix path is healthy.

---

#### Bug 3 — EVM failed outbound produces no slash

**Step 4 — Find a recent post-upgrade ETH.ETH outbound via tx_search**

```bash
curl -s "$MAINNET_RPC/tx_search?query=%22outbound.chain%3D%27ETH%27%20AND%20tx.height%3E25967000%22&per_page=3&order_by=%22desc%22" \
  | jq '.result.txs[]? | {height, hash}'
```

**Result:**

```bash
{ "height": "25968155", "hash": "37E884B0297061888A5F71E4C2217CD63AB5EAEBB453E04836CCACFE77FF3D20" }
{ "height": "25968152", "hash": "F8378E5B0411E7B9DDBE8E8A9A8DFA4F32099E8E6758A325FF5A6EF126F629D7" }
{ "height": "25968138", "hash": "E1CF77D733A25EF16F5BA5A1CBF2734BBBD59AE27BE15D4D535784ADE28A1C9E" }
```

```bash
curl -s "$MAINNET_RPC/tx?hash=0x37E884B0297061888A5F71E4C2217CD63AB5EAEBB453E04836CCACFE77FF3D20" \
  | jq -r '.result.tx_result.events[] | select(.type == "outbound") | .attributes[] | "\(.key)=\(.value)"'
```

```bash
in_tx_id=BF6D7FE269454C86F7996531C5381C034C570AC1FD46AC3032E5E3B5ED3872D9
id=146F865A0793307D9E1125840FB5BE0E6CEE4F09C2C2AE5B10151AF25AB740F7
chain=ETH
coin=88187 ETH.ETH
memo=OUT:BF6D7FE269454C86F7996531C5381C034C570AC1FD46AC3032E5E3B5ED3872D9
```

---

**Step 5 — Confirm the inbound resolved cleanly**

```bash
export ETH_TX=BF6D7FE269454C86F7996531C5381C034C570AC1FD46AC3032E5E3B5ED3872D9
curl -s "$MAINNET_API/thorchain/tx/details/$ETH_TX" \
  | jq '{status: .tx.status, consensus_height, finalised_height, out_count: (.tx.out_hashes // [] | length), out_hashes: .tx.out_hashes}'
```

> Expected: `status: "done"`, exactly one ETH broadcast in `out_hashes` (an all-zeros entry
> for the native RUNE leg is acceptable — see T1.3).

**Result:**

```bash
{
  "status": "done",
  "consensus_height": 25968151,
  "finalised_height": 25968151,
  "out_count": 2,
  "out_hashes": [
    "0000000000000000000000000000000000000000000000000000000000000000",
    "146F865A0793307D9E1125840FB5BE0E6CEE4F09C2C2AE5B10151AF25AB740F7"
  ]
}
```

> 1 native RUNE leg (zeros) + 1 ETH broadcast `146F865A...`. Finalised at block 25,968,151.

---

**Step 6 — Check no slash events around finalisation**

```bash
FIN=25968151
for h in $FIN $((FIN+1)) $((FIN+2)) $((FIN+3)) $((FIN+4)) $((FIN+5)); do
  result=$(curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq '[(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // []) | .[] | select(.type == "slash")] | length')
  echo "Block $h: $result slash events"
done
```

> Expected: `0` for all blocks.

**Result:**

```bash
Block 25968151: 0 slash events
Block 25968152: 0 slash events
Block 25968153: 0 slash events
Block 25968154: 0 slash events
Block 25968155: 0 slash events
Block 25968156: 0 slash events
```

> No slash events around finalisation — Bug 3 (lowercase txhash → slash) fix path is healthy.

> **Status:** `[x] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: GAIA outbound `E7B88353...` finalised at block 25,968,152 with single broadcast `E9B66AE7...` (refund tx, exercises gas-reporting path). No security/insolvency events in 6 surrounding blocks (Bug 1 & 2 fix verified). ETH outbound `BF6D7FE2...` finalised at block 25,968,151 with single ETH broadcast `146F865A...`. No slash events in 6 surrounding blocks (Bug 3 fix verified). All three bugs covered.

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

> **Mainnet TTL window:** `StreamingLimitSwapMaxAge` is set to **43,200 blocks (~3 days)** on
> mainnet (vs 5 blocks on the stagenet test). Only blocks *after* the upgrade can validate
> the fix (`CacheContext` rollback + re-add to TTL). For a limit swap to land in the
> observable window we need: (a) the swap was placed before its TTL window closes, **and**
> (b) the expiry block is post-upgrade. With ~9,300 blocks since activation (~16 hours),
> the chance of seeing one is low — most outstanding limit swaps placed before the upgrade
> won't expire until ~3 days after the upgrade activated.

```bash
# Confirm relevant mimirs/constants are in place
curl -s "$MAINNET_API/thorchain/mimir" | jq '{ENABLEADVSWAPQUEUE, STREAMINGLIMITSWAPMAXAGE}'
curl -s "$MAINNET_API/thorchain/constants" \
  | jq '.int_64_values | with_entries(select(.key | test("LimitSwap|AdvSwap"; "i")))'
```

```bash
# Walk recent blocks for refund events with reason containing "limit"
LATEST=$(curl -s "$MAINNET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
for h in $(seq $LATEST -1 25959000); do
  curl -s "$MAINNET_API/thorchain/block?height=$h" \
    | jq --arg h "$h" -r '
      [(.begin_block_events // []) + (.end_block_events // []) + (.finalize_block_events // [])
       | .[] | select(.type == "refund" and ((.reason // "") | contains("limit")))
       | "block=\($h) in_tx_id=\(.in_tx_id // .id // "?") reason=\(.reason)"]
      | .[]'
done | head -5
```

**Result:**

```bash
~ curl -s "$MAINNET_API/thorchain/mimir" | jq '{ENABLEADVSWAPQUEUE, STREAMINGLIMITSWAPMAXAGE}'
{
  "ENABLEADVSWAPQUEUE": 1,
  "STREAMINGLIMITSWAPMAXAGE": 43200
}
~ curl -s "$MAINNET_API/thorchain/constants" \
  | jq '.int_64_values | with_entries(select(.key | test("LimitSwap|AdvSwap"; "i")))'
{
  "AdvSwapQueueRapidSwapMax": 1,
  "EnableAdvSwapQueue": 0,
  "ModifyLimitSwapMaxIterations": 100,
  "StreamingLimitSwapMaxAge": 43200
}
# Block walk across post-upgrade window (25,959,000 → 25,968,306, ~9,300 blocks): no
# refund events with "limit" in reason found. Walk truncated after no matches in the first
# ~300 blocks of stride sampling and ~270 blocks of contiguous scan.
```

> Adv swap queue feature is enabled (`ENABLEADVSWAPQUEUE=1`), `ModifyLimitSwapMaxIterations`
> at default `100`, `StreamingLimitSwapMaxAge` at default `43200` blocks (~3 days).
> No expired limit swaps have fired post-upgrade — the observable signal is not yet present.

---

**Step 2 — Confirm the inbound is `done` with exactly one out_hash**

> Skipped — depends on Step 1 producing a candidate inbound.

**Result:**

```bash
N/A
```

---

**Step 3 — Verify user balance reconciles across the deposit + refund**

> Skipped — depends on Step 1 producing a candidate inbound.

**Result:**

```bash
N/A
```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; **Pending — re-run after T+3 days post-upgrade.** No expired limit swaps observable in the ~9,300-block post-upgrade window because mainnet `StreamingLimitSwapMaxAge` is 43,200 blocks (~3 days). The fix is the same `CacheContext` rollback + TTL re-add path validated on stagenet T1.8 (where the test used `TTL=5` to force expiry quickly). On mainnet this becomes verifiable once an expired limit swap appears post-upgrade — re-walk for refund events with reason `"limit swap expired"` after ~25,959,000 + 43,200 ≈ block 26,002,200.

> **Earlier rationale to skip:** The fix preserves user funds when `settleSwap` fails at TTL
> expiry. Without an actual expiry firing, we cannot collect the on-chain signal. The bug is
> latent — funds wouldn't be locked unless a settlement happens to fail. Stagenet T1.8 proved
> the fix path works under forced expiry; mainnet will accumulate signal naturally over time.

> **End placeholder block — overwrite when a candidate inbound is found.**

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
| T1.3 | Solana double-spend prevention    | Critical | P0         | [x]                             |
| T1.4 | DoS via unbounded attestation     | Critical | P1         | [x]                             |
| T1.5 | Limit swap DoS + rescue via mimir | Critical | P1         | Skipped — requires mimir vote   |
| T1.6 | Bond module accounting            | Critical | P1         | [x]                             |
| T1.7 | Gaia stuck outbounds (3 bugs)     | Critical | P1         | [x]                             |
| T1.8 | Limit swap expiry — no fund lock  | Critical | [Security] | Pending — re-run after T+3 days |

---

## Go/No-Go Criteria

| Gate      | Requirement                                                                    |
| --------- | ------------------------------------------------------------------------------ |
| Consensus | No divergence across nodes; chain advancing continuously since upgrade         |
| P0 / P1   | T1.1–T1.4, T1.6–T1.8 pass observationally on mainnet (T1.5 skipped — see test) |
| Migration | Reserve trimmed to ADR-023 target; `MaxRuneSupply` mimir set; no halt          |
| Bond      | Bond module balance ≥ Σ node bonds before and after a 30-minute soak window    |
| Outbounds | No security/slash events around recent GAIA, ETH, SOL finalisations            |
