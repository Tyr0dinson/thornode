# Thorpy

`thorpy` allows for data wrangling of blocks, events, transactions, and Cosmos messages with Python. It relies on a shared object generated from the Golang libraries to handle RPC calls, websocket subscriptions, and protobuf serialization.

Things to note:

1. `scan` functions are for processing finite batch jobs with a start and end block
1. `subscribe` functions are for real time processing via websocket events
1. If listener functions return a value it will be JSON-encoded and dumped to console for one-liner convenience
1. The `thorpy` CLI is an alias to evaluate the provided arguments within the package scope

See further API documentation here: [docs/thorpy.md](./docs/thorpy.md).

## Install

```bash
TAG=mainnet make thorpy
```

## Examples

### Subscribe (Real Time)

```bash
# block headers
thorpy 'subscribe_blocks(lambda b: b["block"]["header"])'

# or extract with jq to the same effect
thorpy 'subscribe_blocks(lambda b: b)' | jq -r .block.header

# transaction count per block
thorpy 'subscribe_blocks(lambda b: (b["block"]["header"]["height"], len(b["block"]["data"]["txs"] or [])))'

# confirmed outbounds
thorpy 'subscribe_block_events(lambda h, e: f"{h}: {e}", types={"outbound"})'

# all tx events
thorpy 'subscribe_tx_events(lambda h,txh,tx,res,e: e)'

# deposit, withdraw, donate events
thorpy 'subscribe_tx_events(lambda h,txh,tx,res,e: e, types={"add", "withdraw", "add_liquidity"})'

# block height and transaction hash for mimir changes
thorpy 'subscribe_tx_events(lambda h,txh,tx,res,e: (h,txh), types={"set_mimir", "set_node_mimir"})'

# MsgTssKeysignFail messages
thorpy 'subscribe_msgs(lambda h,txh,m: (m), types={"MsgTssKeysignFail"})'
```

### Scan (Historical)

```bash
# empty blocks
thorpy 'scan_blocks(4000000, 4010000, lambda b: b["block"]["header"]["height"] if b["block"]["data"]["txs"] is None else None)'

# all swap events
thorpy 'scan_block_events(4000000, 4000100, lambda h, e: e, types={"swap"})'

# gas used
thorpy 'scan_txs(4010000, 4100000, lambda h,txh,tx,txr: (txh,txr["gas_used"]), failed=False)'

# failed transactions
thorpy 'scan_txs(4010000, 4100000, lambda h,txh,tx,txr: (txh,txr), successful=False)'

# slash and leave events
thorpy 'scan_tx_events(4020000, 4030000, lambda h,txr,e: (h,e), types={"slash", "validator_request_leave"})'

# bond cost events
thorpy 'scan_tx_events(4000000, 4010000, lambda h,txr,e: e if e["bond_type"] == "\u0003" else None, types={"bond"})'
```

```python
# count outbound observations by chain
import collections, json, thorpy

counts = collections.defaultdict(lambda: 0)

def listen(height, txhash, msg):
    global counts
    for tx in msg["txs"]:
        counts[tx["tx"]["chain"]] += 1

thorpy.scan_msgs(4000000, 4010000, listen, types={"MsgObservedTxOut"})

print(json.dumps(counts, indent=2))
```
