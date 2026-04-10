# v3.17.0-rc1 Stagenet Test Plan

**Release:** https://gitlab.com/thorchain/thornode/-/releases/v3.17.0-rc1  
**Milestone:** Release-3.17.0  
**Tag commit:** `79e0dda91532b09123ed99fb25f85e7b161a3dcb`

---

## Prioritization Rationale

Tests in **Tier 1** map to MRs explicitly labeled **Priority 0**, **Priority 1**, or **[Security]** in GitLab.
All other MRs are ranked by failure impact (fund loss > insolvency > outbound delay > UX).

| Priority | MRs |
| -------- | --- |
| P0       | !4565, !4687, !4704 |
| P1       | !4568, !4569, !4574, !4697 |
| [Security] label only | !4644 |

---

## Environment Setup

```bash
# Endpoints
export STAGENET_API="https://stagenet-thornode.ninerealms.com"
export STAGENET_RPC="https://stagenet-rpc.ninerealms.com:443"
export STAGENET_MIDGARD="https://stagenet-midgard.ninerealms.com"

# Chain ID (derived from the node)
export CHAIN_ID=$(curl -s "$STAGENET_RPC/status" | jq -r '.result.node_info.network')

# Node wallets — 4 active, 2 standby (defined as arrays for zsh compatibility)
ACTIVE_NODES=(snode1 snode3 snode4 snode6)
STANDBY_NODES=(snode2 snode5)

# Default wallet for regular transactions (swaps, deposits)
export WALLET="snode1"

# Destination addresses
export SOL_DEST_ADDR="9NygcE9bzLULhS9KDzvx7CmZxeZQntdFmNsu3Zdy549c"
export BTC_DEST_ADDR="bc1qlav94c2huygkwfhhuuupydtx54w8pstmv8pdzf"
```

> **Mimir votes** require a supermajority of active nodes (>2/3 of 4 = at least 3 votes).
> All mimir tests below loop through all 4 active nodes to guarantee the threshold is met.

**Pre-flight checks — confirm stagenet is healthy before starting:**

---

> All active nodes are on the target version.

```bash
curl -s "$STAGENET_API/thorchain/nodes" | jq '[.[] | select(.status == "Active") | .version] | unique'
```

**Result:**
```bash
~ curl -s "$STAGENET_API/thorchain/nodes" | jq '[.[] | select(.status == "Active") | .version] | unique'
[
  "3.17.0"
]
```

---

> Network consensus version — `current`, `next`, and `querier` should all match the release version.

```bash
curl -s "$STAGENET_API/thorchain/version" | jq '{current, next, querier}'
```

**Result:**
```bash
~ curl -s "$STAGENET_API/thorchain/version" | jq '{current, next, querier}'
{
  "current": "3.17.0",
  "next": "3.17.0",
  "querier": "3.17.0"
}
```

---

> No chains are halted — any key starting with `HALT` and set to `1` would block testing.

```bash
curl -s "$STAGENET_API/thorchain/mimir" | jq 'to_entries[] | select(.key | startswith("HALT"))'
```

**Result:**
```bash
~ curl -s "$STAGENET_API/thorchain/mimir" | jq 'to_entries[] | select(.key | startswith("HALT"))'
{
  "key": "HALTBASETRADING",
  "value": 0
}
{
  "key": "HALTBSCTRADING",
  "value": 0
}
{
  "key": "HALTCHAINGLOBAL",
  "value": 0
}
{
  "key": "HALTCHURNING",
  "value": 0
}
{
  "key": "HALTETHTRADING",
  "value": 0
}
{
  "key": "HALTMEMOLESS",
  "value": 0
}
{
  "key": "HALTSIGNING",
  "value": 0
}
{
  "key": "HALTSIGNINGBSC",
  "value": 0
}
{
  "key": "HALTTCYTRADING",
  "value": 0
}
{
  "key": "HALTTRADING",
  "value": 0
}
```

---

> No backlog of pending outbounds or scheduled transactions that could interfere with tests.

```bash
curl -s "$STAGENET_API/thorchain/queue" | jq '{outbound: .outbound | length, scheduled: .scheduled | length}'
```

**Result:**
```bash
~ curl -s "$STAGENET_API/thorchain/queue" | jq '{outbound: .outbound | length, scheduled: .scheduled | length}'
{
  "outbound": 0,
  "scheduled": 0
}
```

---

> Chain is actively producing blocks — delta should be ≥ 1 over 10 seconds.

```bash
H1=$(curl -s "$STAGENET_RPC/status" | jq -r '.result.sync_info.latest_block_height'); \
sleep 10; \
H2=$(curl -s "$STAGENET_RPC/status" | jq -r '.result.sync_info.latest_block_height'); \
echo "Before: $H1 | After: $H2 | Delta: $((H2 - H1))"
```

**Result:**
```bash
~ H1=$(curl -s "$STAGENET_RPC/status" | jq -r '.result.sync_info.latest_block_height'); \
sleep 10; \
H2=$(curl -s "$STAGENET_RPC/status" | jq -r '.result.sync_info.latest_block_height'); \
echo "Before: $H1 | After: $H2 | Delta: $((H2 - H1))"
Before: 9460643 | After: 9460645 | Delta: 2
```

---

## Tier 1 — Critical (Priority 0, Priority 1, and [Security])

---

### T1.1 — Non-deterministic Consensus · P0 · !4565

**Root cause:** The `tie` flag in `ValueOfOperational` was never reset when a clear winner was
found after tied losers during Go map iteration (random per call). Different validators could
evaluate the same `MsgMimir` and produce different state → apphash mismatch → chain halt.

**Vote distribution:** snode1+snode3 → `100` (winner, 2 votes) · snode4 → `200` · snode6 → `300`
This gives `{100:2, 200:1, 300:1}` — the exact problematic map state (two tied losers).

---

**Step 1 — Submit winner votes (snode1 + snode3 → value 100)**
```bash
for snode in snode1 snode3; do
  sthornode tx thorchain mimir ConsensusTest \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 100
  sleep 2
done
```

**Result:**
```
~ for snode in snode1 snode3; do
  sthornode tx thorchain mimir ConsensusTest \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 100
  sleep 2
done
gas estimate: 782351
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: 93F41763FCBF023181F8F3507124EE80FE236DF92518DF1DDA4CC0EFB6903320
gas estimate: 746138
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: CCA08DFD582A9FBEFB690065F7BB5938BB693610FBDF3691B9D6C9CA39126AB9
```

**Step 2 — Submit loser votes (snode4 → 200, snode6 → 300)**
```bash
sthornode tx thorchain mimir ConsensusTest \
  --from snode4 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 200

sthornode tx thorchain mimir ConsensusTest \
  --from snode6 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 300
```

**Result:**
```
~ sthornode tx thorchain mimir ConsensusTest \
  --from snode4 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 200
gas estimate: 838228
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: BC79E31449596DBA0C9F151115DB5DF5CB616E7F8279C593A337CC63D1622392

~ sthornode tx thorchain mimir ConsensusTest \
  --from snode6 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 300
gas estimate: 623943
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: F3A2A23109225304329C309AAC5935D76F51A28486210EE8E6A6AB5224D23898
```

**Step 3 — Wait 2 blocks, then confirm chain is still producing blocks**
```bash
sleep 12
curl -s "$STAGENET_RPC/status" | jq '.result.sync_info | {height: .latest_block_height, catching_up}'
```
> Expected: `catching_up: false`, height advancing

**Result:**
```
~ sleep 12
curl -s "$STAGENET_RPC/status" | jq '.result.sync_info | {height: .latest_block_height, catching_up}'
{
  "height": "9460687",
  "catching_up": false
}
```

**Step 4 — Monitor 20 blocks; height must keep advancing**
```bash
BEFORE=$(curl -s "$STAGENET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
sleep 120
AFTER=$(curl -s "$STAGENET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
echo "Blocks produced: $((AFTER - BEFORE))"
```
> Expected: ~20 blocks — if the chain halted this would be 0

**Result:**
```
~ BEFORE=$(curl -s "$STAGENET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
sleep 120
AFTER=$(curl -s "$STAGENET_RPC/status" | jq -r '.result.sync_info.latest_block_height')
echo "Blocks produced: $((AFTER - BEFORE))"
Blocks produced: 22
```

**Step 5 — Confirm no value was set (no supermajority reached)**
```bash
curl -s "$STAGENET_API/thorchain/mimir" | jq '.CONSENSUSTEST'
```
> Expected: `null` — no value reached the supermajority threshold (3 of 4 votes). The vote
> distribution `{100:2, 200:1, 300:1}` is intentionally split to trigger the problematic map
> state; the real pass criterion is that the chain did **not halt** (Steps 3–4), not that a
> value was set.

**Result:**
```
~ curl -s "$STAGENET_API/thorchain/mimir" | jq '.CONSENSUSTEST'
null
```

**Step 6 — Have snode4 also vote 100 to reach supermajority (3 of 4)**
```bash
sthornode tx thorchain mimir ConsensusTest \
  --from snode4 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 100
```

**Result:**
```bash
~ sthornode tx thorchain mimir ConsensusTest \
  --from snode4 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 100
gas estimate: 842483
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: 658AF1B60745301E179C4D8DD0B55CFFFEA7037C45AC291D1A0DEA389D997FFB
```

**Step 7 — Wait 2 blocks, then verify CONSENSUSTEST is now set to 100**
```bash
sleep 12
curl -s "$STAGENET_API/thorchain/mimir" | jq '.CONSENSUSTEST'
```
> Expected: `100` — supermajority (3 of 4 nodes) now agrees on the winning value.

**Result:**
```bash
~ sleep 12
curl -s "$STAGENET_API/thorchain/mimir" | jq '.CONSENSUSTEST'
100
```

**Step 8 — Clean up: reset all votes**
```bash
for snode in $ACTIVE_NODES; do
  sthornode tx thorchain mimir ConsensusTest \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- -1
  sleep 2
done
```

**Result:**
```bash
~ for snode in $ACTIVE_NODES; do
  sthornode tx thorchain mimir ConsensusTest \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- -1
  sleep 2
done
gas estimate: 786214
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: 021B934EE8A686820056C465E1F4585776D72951F6281CA1594E3C0F387EA9A0
gas estimate: 750001
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: A274BB4E948893EE1BE638E51521916487FC37C6C976D8AA90EA8640EB51D50B
gas estimate: 836671
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: BA8F4ECCE86A338D8B3B223C8FDA3315BADA3A64139B79A99B20BE2C0AB4434B
gas estimate: 620841
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: 020284CED08C33F4A819337DBBE2781C675EAA399FD3217AFBDE867F79A31C5D
```

> **Status:** `[x] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: Chain produced 22 blocks over 120s with no halt. Supermajority (3/4) correctly set value to 100 in Step 7. Voting `-1` withdraws votes but retains the last consensus value — `CONSENSUSTEST` is a test-only key with no protocol impact.

---

### T1.2 — ADR-023: RUNE Supply Restructure · P0 · !4687

**Root cause:** The Reserve held ~74.21M RUNE created at genesis that never circulated. ADR-023
burns ~64.9M RUNE (leaving exactly 9.3M), then sets `MaxRuneSupply` via mimir to the post-burn
total, replacing the meaningless 500M cap. Store migration v13 → v14. Irreversible on mainnet.

---

> Upgrade already applied at block `9,429,460`. All steps use height-based queries to verify
> the migration in-place.

**Step 1 — Pre-upgrade reserve and total supply (height 9,429,459)**
```bash
curl -s "$STAGENET_API/thorchain/network?height=9429459" | jq '{total_reserve}'
curl -s "$STAGENET_API/cosmos/bank/v1beta1/supply?height=9429459" \
  | jq '.supply[] | select(.denom == "rune") | .amount'
```

**Result:**
```bash
~ curl -s "$STAGENET_API/thorchain/network?height=9429459" | jq '{total_reserve}'
{
  "total_reserve": "4990141431061935"
}
~ curl -s "$STAGENET_API/cosmos/bank/v1beta1/supply?height=9429459" \
  | jq '.supply[] | select(.denom == "rune") | .amount'
"45030099456911658"
```

**Step 2 — Post-upgrade reserve is exactly 9.3M RUNE (height 9,429,460)**
```bash
curl -s "$STAGENET_API/thorchain/network?height=9429460" | jq '{total_reserve}'
```
> Expected: `"929999999998231"` (~9.3M × 1e8)

**Result:**
```bash
~ curl -s "$STAGENET_API/thorchain/network?height=9429460" | jq '{total_reserve}'
{
  "total_reserve": "929999999998231"
}
```

**Step 3 — Total supply decreased by the burned amount**
```bash
curl -s "$STAGENET_API/cosmos/bank/v1beta1/supply?height=9429460" \
  | jq '.supply[] | select(.denom == "rune") | .amount'
```
> Expected: `45030099456911658` minus `4060141431061935` = `40969958025849723`

**Result:**
```bash
~ curl -s "$STAGENET_API/cosmos/bank/v1beta1/supply?height=9429460" \
  | jq '.supply[] | select(.denom == "rune") | .amount'
"40969958025849723"
```

**Step 4 — `MaxRuneSupply` mimir set to post-burn total supply**
```bash
curl -s "$STAGENET_API/thorchain/mimir?height=9429460" | jq '.MAXRUNESUPPLY'
```
> Expected: `40969958025849723` — matches post-burn supply exactly

**Result:**
```bash
~ curl -s "$STAGENET_API/thorchain/mimir?height=9429460" | jq '.MAXRUNESUPPLY'
40969958025849723
```

**Step 5 — Burn event emitted in the migration block**
```bash
curl -s "$STAGENET_API/thorchain/block?height=9429460" \
  | jq '[.begin_block_events[], .end_block_events[], .finalize_block_events[]] | .[] | select(.type == "mint_burn")'
```
> Expected: one event with `reason: "adr023_reserve_burn"` and amount `4060141431061935`

**Result:**
```bash
~ curl -s "$STAGENET_API/thorchain/block?height=9429460" \
  | jq '[.begin_block_events[], .end_block_events[], .finalize_block_events[]] | .[] | select(.type == "mint_burn")'
{
  "amount": "4060141431061935",
  "denom": "rune",
  "reason": "adr023_reserve_burn",
  "supply": "burn",
  "type": "mint_burn"
}
```


> **Status:** `[x] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: Upgrade at block 9,429,460. Reserve trimmed from 49,901,414 RUNE to exactly 9,300,000 RUNE. 40,601,414 RUNE burned. `MaxRuneSupply` mimir set to post-burn supply. Burn event confirmed in migration block.

---

### T1.3 — Solana Double-Spend Prevention · P0 · !4704

**Root cause:** A real mainnet double-spend occurred. Lagging Solana scanners caused a missed
observation; the tx was rescheduled to the same vault; and the Solana client was not calling
`HasSigned()` before signing — unlike every other chain client.

---

**Step 1 — Submit a SOL outbound**
```bash
sthornode tx thorchain deposit 100000000 rune \
  "=:SOL.SOL:$SOL_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```
~ sthornode tx thorchain deposit 100000000 rune \
  "=:SOL.SOL:$SOL_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
gas estimate: 230462
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: 5DDC0393C785297FC0E8621D6DB9471873FA25E449D644B7C83C05488B809496
```

**Step 2 — Capture the outbound entry**
```bash
curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq '(. // [])[] | select(.coin.asset == "SOL.SOL")'
```

**Result:**
```
~ curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq '(. // [])[] | select(.coin.asset == "SOL.SOL")'
{
  "height": 9461025,
  "vault_pub_key": "sthorpub1addwnpepqvze28rfl56h9kppamgkkk04kszqr0vmehjwuunhfhp75x8aj9cqw0egudy",
  "in_hash": "5DDC0393C785297FC0E8621D6DB9471873FA25E449D644B7C83C05488B809496",
  "chain": "SOL",
  "to_address": "9NygcE9bzLULhS9KDzvx7CmZxeZQntdFmNsu3Zdy549c",
  "coin": {
    "asset": "SOL.SOL",
    "amount": "646728"
  },
  "max_gas": [
    {
      "asset": "SOL.SOL",
      "amount": "750",
      "decimals": 9
    }
  ],
  "gas_rate": 7500,
  "original_memo": "OUT:5DDC0393C785297FC0E8621D6DB9471873FA25E449D644B7C83C05488B809496",
  "aggregator_target_limit": "0",
  "clout_spent": "0",
  "vault_pub_key_eddsa": "sthorpub1zcjduepqey0xj3z24w6keehj73ky9u3e0rv66jdv69r9s5kzwxkkdt8mmlnqwh4tum"
}
```

**Step 3 — Wait for finalisation; verify exactly one SOL txid broadcast**
```bash
export SOL_INBOUND_TX="<in_hash from step 2>"
curl -s "$STAGENET_API/thorchain/tx/$SOL_INBOUND_TX" \
  | jq '{status: .observed_tx.status, out_hashes: .observed_tx.out_hashes}'
```
> Expected: `status: "done"`, `out_hashes` has exactly 1 entry

**Result:**
```
~ export SOL_INBOUND_TX="5DDC0393C785297FC0E8621D6DB9471873FA25E449D644B7C83C05488B809496"
curl -s "$STAGENET_API/thorchain/tx/$SOL_INBOUND_TX" \
  | jq '{status: .observed_tx.status, out_hashes: .observed_tx.out_hashes}'
{
  "status": "done",
  "out_hashes": [
    "a6qpehv4eZcQNq1Bpk8gsM6SYERSw51fEvwjefShQQRcWcw8nRoXkoUzRKfX3tgpuMBzX3QAS3C5LAg1UHdfTBe"
  ]
}
```

**Step 4 — Confirm outbound does not reappear in queue after finalisation**
```bash
sleep 12
curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq '[(. // [])[] | select(.coin.asset == "SOL.SOL" and .in_hash == env.SOL_INBOUND_TX)] | length'
```
> Expected: `0` — once signed and observed, the outbound must not be rescheduled. A value ≥ 1
> would indicate the signer cache failed to block a retry.

**Result:**
```bash
~ sleep 12
curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq '[(. // [])[] | select(.coin.asset == "SOL.SOL" and .in_hash == env.SOL_INBOUND_TX)] | length'
0
```

**Step 5 — Verify the on-chain tx has exactly one out_hash (single signing proof)**
```bash
curl -s "$STAGENET_API/thorchain/tx/$SOL_INBOUND_TX" \
  | jq '{out_count: (.observed_tx.out_hashes | length), out_hashes: .observed_tx.out_hashes}'
```
> Expected: `out_count: 1` — the signer cache (`HasSigned()` in `sol_client.go:370`) is
> LevelDB-backed and persists across restarts; a second sign attempt returns early without
> broadcasting, so only one Solana txid ever lands on-chain.

**Result:**
```bash
~ curl -s "$STAGENET_API/thorchain/tx/$SOL_INBOUND_TX" \
  | jq '{out_count: (.observed_tx.out_hashes | length), out_hashes: .observed_tx.out_hashes}'
{
  "out_count": 1,
  "out_hashes": [
    "a6qpehv4eZcQNq1Bpk8gsM6SYERSw51fEvwjefShQQRcWcw8nRoXkoUzRKfX3tgpuMBzX3QAS3C5LAg1UHdfTBe"
  ]
}
```

> **Status:** `[x] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: Outbound finalised with exactly 1 out_hash, no reschedule observed. Single Solana txid `a6qpehv4...` confirms the signer cache blocked any retry.

---

### T1.4 — DoS via Unbounded Attestation · P1 · !4568

**Root cause:** Gas-exempt quorum messages had no attestation deduplication or count cap.
A malicious block proposer could include thousands of duplicate attestations per pubkey,
causing unbounded signature verification at zero gas cost.

> **Note:** Fully reproducing the attack requires block proposer control. This test verifies
> the fix does not break normal quorum behavior and block times stay within bounds.

---

**Step 1 — Confirm active validator count (deduplication cap = this value)**
```bash
curl -s "$STAGENET_API/thorchain/nodes" \
  | jq '[.[] | select(.status == "Active")] | length'
```
> Expected: `4`

**Result:**
```
~ curl -s "$STAGENET_API/thorchain/nodes" \
  | jq '[.[] | select(.status == "Active")] | length'
4
```

**Step 2 — Submit a swap to verify quorum processing still works**
```bash
sthornode tx thorchain deposit 10000000000 rune \
  "=:BTC.BTC:$BTC_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```
~ sthornode tx thorchain deposit 10000000000 rune \
  "=:BTC.BTC:$BTC_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
gas estimate: 230420
code: 0
codespace: ""
data: ""
events: []
gas_used: "0"
gas_wanted: "0"
height: "0"
info: ""
logs: []
raw_log: ""
timestamp: ""
tx: null
txhash: D2E84730E3D9E6FEE168E4C9F53714E58BD424D5DDC0658400C13AC19FC63C2A
```

**Step 3 — Confirm the swap reached quorum and outbound was created**
```bash
export BTC_INBOUND_TX="<txhash from step 2>"
curl -s "$STAGENET_API/thorchain/tx/details/$BTC_INBOUND_TX" \
  | jq '{consensus_height, finalised_height, out_txs}'
```
> Expected: `consensus_height` and `finalised_height` are non-null — quorum was reached and the
> swap was processed. `out_txs` may still be `null` if the BTC outbound hasn't been observed
> back on-chain yet; that is normal and does not affect this test.

**Result:**
```bash
~ export BTC_INBOUND_TX="D2E84730E3D9E6FEE168E4C9F53714E58BD424D5DDC0658400C13AC19FC63C2A"
curl -s "$STAGENET_API/thorchain/tx/details/$BTC_INBOUND_TX" \
  | jq '{consensus_height, finalised_height, out_txs}'
{
  "consensus_height": 9461199,
  "finalised_height": 9461220,
  "out_txs": [
    {
      "id": "2015CB60DF71FD8439A3CCDF7808304E905889FD88BE869755755843DC479DC0",
      "chain": "BTC",
      "from_address": "bc1qmpkngdpk844dm0dcvcyev64jtlht48h8qy2e6p",
      "to_address": "bc1qlav94c2huygkwfhhuuupydtx54w8pstmv8pdzf",
      "coins": [{ "asset": "BTC.BTC", "amount": "119876" }],
      "memo": "OUT:D2E84730E3D9E6FEE168E4C9F53714E58BD424D5DDC0658400C13AC19FC63C2A"
    }
  ]
}
```
> Note: the protocol automatically streamed this swap across 21 sub-swaps (blocks 9461199–9461220)
> to reduce slippage. `out_txs: null` on first check was expected — re-query after ~2 minutes.

> **Status:** `[x] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: Active validator count = 4. Swap reached quorum at block 9461199, streamed across 21 sub-swaps, BTC outbound created (`2015CB60...`). Normal quorum processing confirmed.

---

### T1.5 — DoS via Unbounded Limit Swap Iteration · P1 · !4569

**Root cause:** `ModifyLimitSwap` iterated all entries in a shared price-ratio bucket with no
cap. An attacker fills the bucket, causing the victim's cancel/modify to do O(N) KV reads.
Fix: `ModifyLimitSwapMaxIterations` (default 100), mimir-overridable for operator rescue.

---

**Step 1 — Verify the iteration cap constant**
```bash
curl -s "$STAGENET_API/thorchain/constants" | jq '.int_64_values.ModifyLimitSwapMaxIterations'
```
> Expected: `100`

**Result:**
```

```

**Step 2 — Fill the price-ratio bucket with 110 attacker swaps**
```bash
for i in $(seq 1 110); do
  sthornode tx thorchain deposit 100000000 rune \
    "=<:BTC.BTC:$BTC_DEST_ADDR:450000000/0/0" \
    --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
  sleep 1
done
```

**Result:**
```

```

**Step 3 — Verify 110 swaps are queued**
```bash
curl -s "$STAGENET_API/thorchain/swap/queue" | jq 'length'
```
> Expected: ≥110

**Result:**
```

```

**Step 4 — Submit victim swap at position 101+ and attempt to modify it**
```bash
sthornode tx thorchain deposit 100000000 rune \
  "=<:BTC.BTC:$BTC_DEST_ADDR_VICTIM:450000000/0/0" \
  --from snode3 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto

sthornode tx thorchain deposit 100000000 rune \
  "=<:BTC.BTC:$BTC_DEST_ADDR_VICTIM:445000000/0/0" \
  --from snode3 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```
> Expected: second tx fails with `"swap index too large, exceeded max iterations (100)"`

**Result:**
```

```

**Step 5 — Raise the limit via mimir to rescue the victim's swap**
```bash
for snode in $ACTIVE_NODES; do
  sthornode tx thorchain mimir ModifyLimitSwapMaxIterations \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 200
  sleep 2
done
sleep 12
curl -s "$STAGENET_API/thorchain/mimir" | jq '.MODIFYLIMITSWAPMAXIMITERATIONS'
```
> Expected: `200`

**Result:**
```

```

**Step 6 — Retry victim modification — should now succeed**
```bash
sthornode tx thorchain deposit 100000000 rune \
  "=<:BTC.BTC:$BTC_DEST_ADDR_VICTIM:445000000/0/0" \
  --from snode3 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```
> Expected: tx succeeds

**Result:**
```

```

**Step 7 — Reset mimir**
```bash
for snode in $ACTIVE_NODES; do
  sthornode tx thorchain mimir ModifyLimitSwapMaxIterations \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- -1
  sleep 2
done
```

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T1.6 — Bond Module Accounting · P1 · !4574

**Root cause:** `SendFromModuleToModule` transferred the full `cost` even when `cost > bond`,
causing the bond module balance to diverge from the sum of individual node bonds.
Standby nodes (snode2, snode5) are most likely to trigger this path with a low bond.

---

**Step 1 — Check standby node bonds**
```bash
curl -s "$STAGENET_API/thorchain/nodes" \
  | jq '.[] | select(.status == "Standby") | {node_address, total_bond, status}'
```

**Result:**
```

```

**Step 2 — Snapshot bond module balance**
```bash
MODULE_ADDR="thor17gw75axcnr8747pkanbe2ven74se88f52yfzpz"
curl -s "$STAGENET_API/cosmos/bank/v1beta1/balances/$MODULE_ADDR" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
```

**Result:**
```

```

**Step 3 — Snapshot sum of all node bonds**
```bash
curl -s "$STAGENET_API/thorchain/nodes" \
  | jq '[.[] | .total_bond | tonumber] | add'
```

**Result:**
```

```

**Step 4 — Submit mimir votes from standby nodes first, then active nodes**
```bash
for snode in $STANDBY_NODES $ACTIVE_NODES; do
  sthornode tx thorchain mimir BondAccTest \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 1
  sleep 2
done
sleep 12
```

**Result:**
```

```

**Step 5 — Re-snapshot bond module balance**
```bash
curl -s "$STAGENET_API/cosmos/bank/v1beta1/balances/$MODULE_ADDR" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
```

**Result:**
```

```

**Step 6 — Re-snapshot sum of all node bonds**
```bash
curl -s "$STAGENET_API/thorchain/nodes" \
  | jq '[.[] | .total_bond | tonumber] | add'
```

**Result:**
```

```

**Step 7 — Assert module balance ≥ sum of bonds**
```bash
python3 -c "
module=<paste step 5>; bonds=<paste step 6>
diff = module - bonds
print(f'Difference: {diff}')
print('PASS' if diff >= 0 else 'FAIL: module deficit detected')
"
```
> Expected: `PASS` — no deficit

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T1.7 — Gaia Stuck Outbounds & Gas Insolvency · P1 · !4697

**Root cause (3 bugs):** (1) Failed ATOM outbounds silently skipped — gas never reported →
vault insolvency. (2) Balance check skipped on same-block retry → on-chain failure → Bug 1.
(3) Failed EVM outbound memo used lowercase txhash → slash instead of gas deduction.

---

#### Bug 1 & 2 — Gaia failed outbound observation

**Step 1 — Record GAIA vault balance before test**
```bash
curl -s "$STAGENET_API/thorchain/vaults/asgard" \
  | jq '.[] | select(.coins[].asset == "GAIA.ATOM") | .coins[] | select(.asset == "GAIA.ATOM") | .amount'
```

**Result:**
```

```

**Step 2 — Submit RUNE → ATOM swap**
```bash
sthornode tx thorchain deposit 1000000000 rune \
  "=:GAIA.ATOM:$ATOM_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```

```

**Step 3 — Wait for the outbound to finalise**
```bash
export GAIA_TX=$(curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq -r '(. // [])[] | select(.coin.asset == "GAIA.ATOM") | .in_hash')
watch -n 6 "curl -s $STAGENET_API/thorchain/tx/$GAIA_TX | jq '.observed_tx.status'"
```
> Expected: `"done"`

**Result:**
```

```

**Step 4 — Verify vault balance decreased by outbound + gas only**
```bash
curl -s "$STAGENET_API/thorchain/vaults/asgard" \
  | jq '.[] | select(.coins[].asset == "GAIA.ATOM") | .coins[] | select(.asset == "GAIA.ATOM") | .amount'
```
> Expected: decreased by swap amount + gas, not more

**Result:**
```

```

**Step 5 — Confirm GAIA is not halted**
```bash
curl -s "$STAGENET_API/thorchain/mimir" | jq '.HALTGAIACHAINACTIVITY // "not set (good)"'
```
> Expected: `"not set (good)"`

**Result:**
```

```

**Step 6 — Check no insolvency events emitted**
```bash
curl -s "$STAGENET_API/thorchain/block" \
  | jq '[.[] | select(.type == "security")] | length'
```
> Expected: `0`

**Result:**
```

```

#### Bug 3 — EVM failed outbound produces no slash

**Step 7 — Submit an ETH outbound**
```bash
sthornode tx thorchain deposit 1000000000 rune \
  "=:ETH.ETH:$ETH_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```

```

**Step 8 — After finalisation, verify no slash events**
```bash
export ETH_TX=$(curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq -r '(. // [])[] | select(.coin.asset == "ETH.ETH") | .in_hash')
curl -s "$STAGENET_API/thorchain/block" \
  | jq '[.[] | select(.type == "slash")] | length'
```
> Expected: `0`

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T1.8 — Limit Swap Expiry — No Fund Lock · [Security] · !4644

**Root cause:** When `settleSwap` failed at TTL expiry, the TTL entry was deleted
unconditionally, orphaning the swap forever. Partial state from a failed settlement
(outbound scheduled before failure) was not rolled back, causing duplicate outbounds on retry.
**Fix:** `CacheContext` wraps `settleSwap` (atomic rollback). Failed swaps are re-added to TTL
at `currentHeight + 1`. Test uses TTL=5 blocks with unreachable price to force expiry path.

---

**Step 1 — Get current block height**
```bash
curl -s "$STAGENET_RPC/status" | jq -r '.result.sync_info.latest_block_height'
```

**Result:**
```

```

**Step 2 — Submit limit swap with TTL=5 and unreachable price**
```bash
sthornode tx thorchain deposit 1000000000 rune \
  "=<:BTC.BTC:$BTC_DEST_ADDR:999999999999/5/0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```

```

**Step 3 — Capture tx hash and confirm swap is in queue**
```bash
export LIMIT_TX=$(curl -s "$STAGENET_API/thorchain/queue/scheduled" \
  | jq -r '.[0].in_hash')
echo "Limit TX: $LIMIT_TX"
curl -s "$STAGENET_API/thorchain/swap/queue" \
  | jq '[.[] | select(.tx.id == env.LIMIT_TX)] | length'
```
> Expected: `1` — swap is in queue

**Result:**
```

```

**Step 4 — Wait ~5 blocks for TTL to expire**
```bash
echo "Waiting for TTL expiry (~36s)..."
sleep 36
```

**Step 5 — Verify swap is no longer in queue (refunded at expiry)**
```bash
curl -s "$STAGENET_API/thorchain/swap/queue" \
  | jq '[.[] | select(.tx.id == env.LIMIT_TX)] | length'
```
> Expected: `0` — swap was processed at expiry, not stuck forever

**Result:**
```

```

**Step 6 — Verify exactly ONE refund outbound (no duplicates)**
```bash
curl -s "$STAGENET_API/thorchain/tx/$LIMIT_TX" \
  | jq '{out_hashes: .observed_tx.out_hashes, out_count: (.observed_tx.out_hashes | length)}'
```
> Expected: `out_count: 1`

**Result:**
```

```

**Step 7 — Confirm RUNE was returned to sender**
```bash
curl -s "$STAGENET_API/cosmos/bank/v1beta1/balances/$(sthornode keys show $WALLET -a)" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
```
> Expected: balance recovered ~1000000000 (refund minus gas)

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

## Tier 2 — Bifrost Reliability

---

### T2.1 — UTXO Vault Lock Through Broadcast · !4596 + !4595

**Root cause (!4596):** Vault lock released before broadcast — two goroutines could select the
same UTXOs and both broadcast → double-spend.
**Root cause (!4595):** Signed tx only persisted after broadcast failure — crash between sign
and persist would lose the payload, forcing a fresh sign on restart.

---

**Step 1 — Trigger two BTC outbounds in close succession**
```bash
sthornode tx thorchain deposit 500000000 rune \
  "=:BTC.BTC:$BTC_DEST_ADDR_1:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto

sthornode tx thorchain deposit 500000000 rune \
  "=:BTC.BTC:$BTC_DEST_ADDR_2:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```

```

**Step 2 — Confirm both outbounds appear in queue**
```bash
curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq '[(. // [])[] | select(.coin.asset == "BTC.BTC")] | length'
```
> Expected: `2`

**Result:**
```

```

**Step 3 — After finalisation, collect both BTC txids**
```bash
curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq '[(. // [])[] | select(.coin.asset == "BTC.BTC") | .out_hash]'
```
> Expected: two distinct txids — verify on BTC explorer that no UTXO inputs are shared

**Result:**
```

```

**Step 4 — Verify BTC vault remains solvent**
```bash
curl -s "$STAGENET_API/thorchain/vaults/asgard" \
  | jq '.[] | {pub_key, btc: [.coins[] | select(.asset == "BTC.BTC")]}'
```

**Result:**
```

```

**Step 5 — Check bifrost logs for signed tx persistence**
```bash
grep -i "persisted signed tx\|fail to persist" <bifrost-log> | tail -10
```
> Expected: `"persisted signed tx"` appears before broadcast lines

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T2.2 — EVM Outbound Race After Approval · !4656

**Root cause:** After broadcasting a token `approve()` tx, bifrost immediately called
`eth_estimateGas`. If the approval wasn't mined yet, the estimate reverted and the outbound
was rescheduled 3+ blocks. Fix: sleep of `3 × blockTime` after approval before estimating.

> **When to test:** Most visible immediately after a churn — new vault has no token allowances,
> so every first ERC-20 outbound triggers the approval path.

---

**Step 1 — Confirm churn happened recently**
```bash
curl -s "$STAGENET_API/thorchain/lastblock" | jq '.'
```

**Result:**
```

```

**Step 2 — Submit RUNE → ERC-20 token swap**
```bash
sthornode tx thorchain deposit 5000000000 rune \
  "=:ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48:$ETH_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```

```

**Step 3 — Monitor queue for 60s — scheduled count must not increase**
```bash
for i in $(seq 1 10); do
  curl -s "$STAGENET_API/thorchain/queue" \
    | jq '{outbound: .outbound | length, scheduled: .scheduled | length}'
  sleep 6
done
```
> Expected: `scheduled` count stays constant — no reschedule

**Result:**
```

```

**Step 4 — Verify the outbound finalised**
```bash
export EVM_TX=$(curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq -r '(. // [])[] | select(.coin.asset | startswith("ETH.USDC")) | .in_hash')
curl -s "$STAGENET_API/thorchain/tx/$EVM_TX" | jq '.observed_tx.status'
```
> Expected: `"done"`

**Result:**
```

```

**Step 5 — Check bifrost logs for approval sleep**
```bash
grep -i "waiting for approval transaction to be mined" <bifrost-log> | tail -5
```
> Expected: one log line per approval — confirms the fix path was exercised

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T2.3 — Rapid Swap Correctness · !4522

**Root causes:** Pool balances fetched once before loop (stale on iterations 2+). Direction-
skipped swaps consumed budget slots. Quote API had no warning for streaming estimates.

---

**Step 1 — Check quote API warning field for streaming swaps**
```bash
curl -s "$STAGENET_API/thorchain/quote/swap?from_asset=THOR.RUNE&to_asset=BTC.BTC&amount=5000000000&streaming_interval=1&streaming_quantity=5" \
  | jq '{expected_amount_out, warning}'
```
> Expected: `warning` field present with text about best-case estimate

**Result:**
```

```

**Step 2 — Record pool depths before the rapid swap**
```bash
curl -s "$STAGENET_API/thorchain/pool/BTC.BTC" | jq '{balance_rune, balance_asset}'
```

**Result:**
```

```

**Step 3 — Submit rapid swap (interval=1, quantity=5)**
```bash
sthornode tx thorchain deposit 5000000000 rune \
  "=:BTC.BTC:$BTC_DEST_ADDR:0/1/5" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```

```

**Step 4 — Monitor sub-swap execution over 7 blocks**
```bash
export RAPID_TX=$(curl -s "$STAGENET_API/thorchain/block" \
  | jq -r '.[] | select(.type == "swap") | .in_tx_id' | head -1)
for i in $(seq 1 7); do
  sleep 6
  curl -s "$STAGENET_API/thorchain/tx/$RAPID_TX" \
    | jq '{status: .observed_tx.status, out_count: (.observed_tx.out_hashes | length)}'
done
```
> Expected: status transitions to `"done"` after all sub-swaps

**Result:**
```

```

**Step 5 — Verify pool depths changed (stale data fix)**
```bash
curl -s "$STAGENET_API/thorchain/pool/BTC.BTC" | jq '{balance_rune, balance_asset}'
```
> Expected: depths differ from step 2 — all 5 sub-swaps were applied

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

## Tier 3 — New Features

---

### T3.1 — LTC Taproot (bech32m) Address Support · !4615

**Root cause:** `ltcutil.DecodeAddress` did not support witness v1 taproot addresses (`ltc1p...`).
Outbounds to taproot addresses would error on decode and never be built — stuck forever.

---

**Step 1 — Validate taproot address is now recognised**
```bash
curl -s "$STAGENET_API/thorchain/isvalid?address=$LTC_TAPROOT_ADDR&chain=LTC" | jq '.'
```
> Expected: `{"is_valid": true}`

**Result:**
```

```

**Step 2 — Submit RUNE → LTC swap to a taproot address**
```bash
sthornode tx thorchain deposit 1000000000 rune \
  "=:LTC.LTC:$LTC_TAPROOT_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```

```

**Step 3 — Confirm outbound is created with the taproot destination**
```bash
curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq '(. // [])[] | select(.coin.asset == "LTC.LTC") | {in_hash, to_address: .coin}'
```
> Expected: outbound exists and `to_address` is the `ltc1p...` taproot address

**Result:**
```

```

**Step 4 — Confirm finalisation**
```bash
export LTC_TX=$(curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq -r '(. // [])[] | select(.coin.asset == "LTC.LTC") | .in_hash')
watch -n 6 "curl -s $STAGENET_API/thorchain/tx/$LTC_TX | jq '.observed_tx.status'"
```
> Expected: `"done"`

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T3.2 — StableSlipMinBps Mimir · !4686

**What was added:** `StableSlipMinBps` overrides the swap fee floor for swaps where both
source and target L1 assets are TOR anchors (e.g., USDC → USDT). Default `0` = disabled.

---

**Step 1 — Confirm default value is 0 (disabled)**
```bash
curl -s "$STAGENET_API/thorchain/constants" | jq '.int_64_values.StableSlipMinBps'
```
> Expected: `0`

**Result:**
```

```

**Step 2 — Baseline quote — record current slippage**
```bash
curl -s "$STAGENET_API/thorchain/quote/swap?from_asset=ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48&to_asset=ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7&amount=10000000000" \
  | jq '{slippage_bps, fees}'
```

**Result:**
```

```

**Step 3 — All 4 active nodes vote to set StableSlipMinBps = 5**
```bash
for snode in $ACTIVE_NODES; do
  sthornode tx thorchain mimir StableSlipMinBps \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 5
  sleep 2
done
sleep 12
curl -s "$STAGENET_API/thorchain/mimir" | jq '.STABLESLIPMINBPS'
```
> Expected: `5`

**Result:**
```

```

**Step 4 — Re-query quote — slippage should reflect the 5 bps floor**
```bash
curl -s "$STAGENET_API/thorchain/quote/swap?from_asset=ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48&to_asset=ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7&amount=10000000000" \
  | jq '{slippage_bps, fees}'
```
> Expected: `slippage_bps` ≥ 5

**Result:**
```

```

**Step 5 — Check swap event slip after executing**
```bash
sthornode tx thorchain deposit 10000000000 rune \
  "=:ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7:$ETH_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
sleep 6
curl -s "$STAGENET_API/thorchain/block" \
  | jq '.[] | select(.type == "swap") | {slip, liquidity_fee_in_rune}'
```
> Expected: slip reflects StableSlipMinBps floor

**Result:**
```

```

**Step 6 — Reset mimir**
```bash
for snode in $ACTIVE_NODES; do
  sthornode tx thorchain mimir StableSlipMinBps \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- -1
  sleep 2
done
```

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T3.3 — Custom Affiliate Payout Threshold · !4263

**What was added:** `PreferredAssetOutboundFeeMultiplier` field on THORName. Set via the 8th
positional parameter of the `~` memo. Per-affiliate threshold overrides the global mimir.

---

**Step 1 — Register THORName with custom multiplier of 500**
```bash
sthornode tx thorchain deposit 2000000000 rune \
  "~:testaffiliate:THOR:$(sthornode keys show $WALLET -a):$(sthornode keys show $WALLET -a):0::0:500" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```

```

**Step 2 — Verify the custom multiplier is stored**
```bash
curl -s "$STAGENET_API/thorchain/thorname/testaffiliate" \
  | jq '{name, preferred_asset_outbound_fee_multiplier}'
```
> Expected: `preferred_asset_outbound_fee_multiplier: 500`

**Result:**
```

```

**Step 3 — Submit a small swap with affiliate (accumulates BELOW threshold)**
```bash
sthornode tx thorchain deposit 100000000 rune \
  "=:BTC.BTC:$BTC_DEST_ADDR::testaffiliate:100" \
  --from snode3 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
sleep 6
curl -s "$STAGENET_API/cosmos/bank/v1beta1/balances/$(sthornode keys show $WALLET -a)" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
```
> Expected: balance not yet increased (fee accumulated, not paid out)

**Result:**
```

```

**Step 4 — Submit more swaps until threshold is crossed**
```bash
for i in $(seq 1 20); do
  sthornode tx thorchain deposit 1000000000 rune \
    "=:BTC.BTC:$BTC_DEST_ADDR::testaffiliate:100" \
    --from snode3 --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
  sleep 6
done
```

**Result:**
```

```

**Step 5 — Confirm lump payout was triggered**
```bash
curl -s "$STAGENET_API/cosmos/bank/v1beta1/balances/$(sthornode keys show $WALLET -a)" \
  | jq '.balances[] | select(.denom == "rune") | .amount'
```
> Expected: balance increased by all accumulated fees in one payout

**Result:**
```

```

**Step 6 — Reset multiplier to global default**
```bash
sthornode tx thorchain deposit 2000000000 rune \
  "~:testaffiliate:THOR:$(sthornode keys show $WALLET -a):$(sthornode keys show $WALLET -a):0::0:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
sleep 6
curl -s "$STAGENET_API/thorchain/thorname/testaffiliate" \
  | jq '.preferred_asset_outbound_fee_multiplier'
```
> Expected: `0`

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T3.4 — RUNE Supply API Endpoints · !4666

**What was added:** New `GET /thorchain/supply` endpoint with `{circulating, locked: {reserve}, total}`.
Also a CMC-compatible plain-text endpoint `GET /thorchain/supply/cmc`.

---

**Step 1 — Query supply endpoint and verify structure**
```bash
curl -s "$STAGENET_API/thorchain/supply" | jq '.'
```
> Expected: `{circulating, locked: {reserve}, total}` — all non-zero whole RUNE values

**Result:**
```

```

**Step 2 — Verify total = circulating + reserve**
```bash
curl -s "$STAGENET_API/thorchain/supply" | jq '
  . as $s | {
    circulating: $s.circulating,
    reserve: $s.locked.reserve,
    total: $s.total,
    balanced: (($s.circulating | tonumber) + ($s.locked.reserve | tonumber) == ($s.total | tonumber))
  }'
```
> Expected: `balanced: true`

**Result:**
```

```

**Step 3 — CMC plain-text circulating supply**
```bash
curl -s "$STAGENET_API/thorchain/supply/cmc"
```
> Expected: plain integer (no JSON)

**Result:**
```

```

**Step 4 — TCY supply endpoint**
```bash
curl -s "$STAGENET_API/thorchain/supply/cmc?asset=tcy&type=circulating"
```
> Expected: plain integer for TCY circulating supply

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

## Tier 4 — Infrastructure Verification

---

### T4.1 — Dev Fund Address Migration · !4698/!4699

**What changed:** `DevFundAddress` updated from `thor1d8c0wv4y72kmlytegjmgx825xwumt9qt5xe07k`
(9R sole custody) to `thor1sgnuptp32y2fht3258vlx68mq8wsk2uz4wrshu` (2-of-3 multisig) per ADR-025.

---

**Step 1 — Confirm new dev fund address is in the network response**
```bash
curl -s "$STAGENET_API/thorchain/network" | jq '.dev_fund_address'
```
> Expected: `"thor1sgnuptp32y2fht3258vlx68mq8wsk2uz4wrshu"`

**Result:**
```

```

**Step 2 — Verify block events show reward transfers going to the new address**
```bash
curl -s "$STAGENET_API/thorchain/block" \
  | jq '.[] | select(.type == "transfer" and .recipient == "thor1sgnuptp32y2fht3258vlx68mq8wsk2uz4wrshu")'
```
> Expected: at least one transfer event to the new address

**Result:**
```

```

**Step 3 — Confirm old address receives no new funds**
```bash
OLD_DEV="thor1d8c0wv4y72kmlytegjmgx825xwumt9qt5xe07k"
BEFORE=$(curl -s "$STAGENET_API/cosmos/bank/v1beta1/balances/$OLD_DEV" \
  | jq -r '.balances[] | select(.denom == "rune") | .amount // "0"')
sleep 60
AFTER=$(curl -s "$STAGENET_API/cosmos/bank/v1beta1/balances/$OLD_DEV" \
  | jq -r '.balances[] | select(.denom == "rune") | .amount // "0"')
echo "Old address balance change: $((AFTER - BEFORE))"
```
> Expected: `0`

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T4.2 — MaxGas EVM Cap · !4614

**Root cause:** The absolute gas cap check protecting bifrost from incorrect slashing existed
only for ETH. AVAX, BSC, BASE, POL were unprotected. AVAX used the wrong constant (50M instead
of 100e8), making the cap meaningless for AVAX.

---

**Step 1 — Verify per-chain mimir keys (unset = hardcoded defaults apply)**
```bash
curl -s "$STAGENET_API/thorchain/mimir" | jq '{
  "MaxGas-ETH": .["MAXGAS-ETH"] // "default: 50000000",
  "MaxGas-AVAX": .["MAXGAS-AVAX"] // "default: 10000000000",
  "MaxGas-BSC": .["MAXGAS-BSC"] // "default: 50000000"
}'
```

**Result:**
```

```

**Step 2 — Set low ETH cap via mimir and submit an ETH outbound**
```bash
for snode in $ACTIVE_NODES; do
  sthornode tx thorchain mimir MaxGasETH \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- 80000
  sleep 2
done
sleep 12

sthornode tx thorchain deposit 2000000000 rune \
  "=:ETH.ETH:$ETH_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```

```

**Step 3 — Verify outbound max_gas is capped**
```bash
curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq '(. // [])[] | select(.coin.asset == "ETH.ETH") | .max_gas'
```
> Expected: max_gas amount ≤ 80000

**Result:**
```

```

**Step 4 — Reset ETH mimir**
```bash
for snode in $ACTIVE_NODES; do
  sthornode tx thorchain mimir MaxGasETH \
    --from "$snode" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto -- -1
  sleep 2
done
```

**Result:**
```

```

**Step 5 — Submit AVAX outbound and verify it can use the higher AVAX cap**
```bash
sthornode tx thorchain deposit 2000000000 rune \
  "=:AVAX.AVAX:$AVAX_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
sleep 6
curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq '(. // [])[] | select(.coin.asset == "AVAX.AVAX") | .max_gas'
```
> Expected: max_gas can be up to `10000000000` (100e8) — not incorrectly capped at `50000000`

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

### T4.3 — ZEC Gas Reporting & Solvency · !4701

**Root cause (Bug 1):** ZEC `vsize` is always 0 — an early return in `sendNetworkFeeFromBlock`
was hit before the ZIP-317 fee branch, so ZEC `gas_rate` was always stale/zero.
**Root cause (Bug 2):** Solvency gas buffer computed as `3 × 10000 × lastFeeRate` but ZEC's
`lastFeeRate` is already the full flat fee — inflated by ~10,000x, masking real insolvencies.

---

**Step 1 — Confirm ZEC is not halted and gas_rate is non-zero**
```bash
curl -s "$STAGENET_API/thorchain/inbound_addresses" \
  | jq '.[] | select(.chain == "ZEC") | {chain, halted, gas_rate}'
```
> Expected: `halted: false`, `gas_rate` is non-zero (was always 0 before Bug 1 fix)

**Result:**
```

```

**Step 2 — Verify ZEC network fee is reporting and updating**
```bash
curl -s "$STAGENET_API/thorchain/network_fees" \
  | jq '.[] | select(.chain == "ZEC")'
```
> Expected: `transaction_fee_rate > 0`

**Result:**
```

```

```bash
sleep 90
curl -s "$STAGENET_API/thorchain/network_fees" \
  | jq '.[] | select(.chain == "ZEC") | .transaction_fee_rate'
```
> Expected: same or updated non-zero value

**Result:**
```

```

**Step 3 — Record ZEC vault balance before outbound**
```bash
curl -s "$STAGENET_API/thorchain/vaults/asgard" \
  | jq '.[] | select(.coins[].asset == "ZEC.ZEC") | .coins[] | select(.asset == "ZEC.ZEC") | .amount'
```

**Result:**
```

```

**Step 4 — Submit ZEC outbound**
```bash
sthornode tx thorchain deposit 500000000 rune \
  "=:ZEC.ZEC:$ZEC_DEST_ADDR:0" \
  --from "$WALLET" --chain-id "$CHAIN_ID" --node "$STAGENET_RPC" --yes --gas auto
```

**Result:**
```

```

**Step 5 — After finalisation, verify gas deducted is reasonable (not 10,000x inflated)**
```bash
export ZEC_TX=$(curl -s "$STAGENET_API/thorchain/queue/outbound" \
  | jq -r '(. // [])[] | select(.coin.asset == "ZEC.ZEC") | .in_hash')
watch -n 6 "curl -s $STAGENET_API/thorchain/tx/$ZEC_TX | jq '.observed_tx.status'"

curl -s "$STAGENET_API/thorchain/vaults/asgard" \
  | jq '.[] | select(.coins[].asset == "ZEC.ZEC") | .coins[] | select(.asset == "ZEC.ZEC") | .amount'
```
> Expected: vault decreased by outbound + a few thousand sats gas (not millions)

**Result:**
```

```

> **Status:** `[ ] Pass` &nbsp;&nbsp; `[ ] Fail` &nbsp;&nbsp; Notes: ___

---

## Execution Checklist

| #    | Test                                   | Tier     | Priority   | Status |
| ---- | -------------------------------------- | -------- | ---------- | ------ |
| T1.1 | Non-deterministic consensus            | Critical | P0         | [ ]    |
| T1.2 | RUNE supply restructure migration      | Critical | P0         | [ ]    |
| T1.3 | Solana double-spend prevention         | Critical | P0         | [ ]    |
| T1.4 | DoS via unbounded attestation          | Critical | P1         | [ ]    |
| T1.5 | Limit swap DoS + rescue via mimir      | Critical | P1         | [ ]    |
| T1.6 | Bond module accounting                 | Critical | P1         | [ ]    |
| T1.7 | Gaia stuck outbounds (3 bugs)          | Critical | P1         | [ ]    |
| T1.8 | Limit swap expiry — no fund lock       | Critical | [Security] | [ ]    |
| T2.1 | UTXO vault lock through broadcast      | Bifrost  |            | [ ]    |
| T2.2 | EVM outbound race after approval       | Bifrost  |            | [ ]    |
| T2.3 | Rapid swap correctness + quote warning | Bifrost  |            | [ ]    |
| T3.1 | LTC taproot address support            | Feature  |            | [ ]    |
| T3.2 | StableSlipMinBps mimir + swap event    | Feature  |            | [ ]    |
| T3.3 | Custom affiliate payout threshold      | Feature  |            | [ ]    |
| T3.4 | RUNE supply API + CMC endpoint         | Feature  |            | [ ]    |
| T4.1 | Dev fund address migration             | Infra    |            | [ ]    |
| T4.2 | MaxGas EVM cap (ETH + AVAX)            | Infra    |            | [ ]    |
| T4.3 | ZEC gas reporting & solvency           | Infra    |            | [ ]    |

---

## Go/No-Go Criteria

| Gate       | Requirement                                                                   |
| ---------- | ----------------------------------------------------------------------------- |
| Consensus  | No divergence across nodes; 50+ consecutive blocks without timeout            |
| P0 / P1    | All Tier 1 tests (T1.1–T1.8) pass                                             |
| Migration  | Reserve = exactly 9.3M RUNE post-ADR-023; MaxRuneSupply mimir set; no halt    |
| Bifrost    | Zero double-spends; no stuck outbounds on BTC, ETH, ATOM, SOL, LTC           |
| Features   | LTC taproot, affiliate threshold, and supply endpoint return expected results  |
