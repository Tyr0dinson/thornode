# Thorpy

`thorpy` allows for data wrangling of blocks, events, transactions, and Cosmos messages with Python. It relies on a shared object generated from the Golang libraries to handle RPC calls, websocket subscriptions, and protobuf serialization.

Things to note:

1. `scan` functions are for processing finite batch jobs with a start and end block
1. `subscribe` functions are for real time processing via websocket events
1. If listener functions return a value it will JSON-encoded and dumped to console for one-liner convenience

See further API documentation here: [docs/thorpy.md](./docs/thorpy.md).

## Install

```bash
TAG=mainnet make thorpy
```

## Examples

### Subscribe (Real Time)

```bash
# block headers
python3 -c 'import thorpy; thorpy.subscribe_blocks(lambda b: b["block"]["header"])'

# transaction count per block
python3 -c 'import thorpy; thorpy.subscribe_blocks(lambda b: (b["block"]["header"]["height"], len(b["block"]["data"]["txs"] or [])))'

# confirmed outbounds
python3 -c 'import thorpy; thorpy.subscribe_block_events(lambda h, e: f"{h}: {e}", types={"outbound"})'

# all tx events
python3 -c 'import thorpy; thorpy.subscribe_tx_events(lambda h,txh,tx,res,e: e)'

# deposit, withdraw, donate events
python3 -c 'import thorpy; thorpy.subscribe_tx_events(lambda h,txh,tx,res,e: e, types={"add", "withdraw", "add_liquidity"})'

# block height and transaction hash for mimir changes
python3 -c 'import thorpy; thorpy.subscribe_tx_events(lambda h,txh,tx,res,e: (h,txh), types={"add", "withdraw", "add_liquidity"})'

# MsgTssKeysignFail messages
python3 -c 'import thorpy; thorpy.subscribe_msgs(lambda h,txh,m: (m), types={"MsgTssKeysignFail"})'
```

### Scan (Historical)

```bash
# slash events
python3 -c 'import thorpy; thorpy.scan_tx_events(4000000, 4100000, lambda h,txr,e: e, types={"slash"})'

# bond cost events
python3 -c 'import thorpy; thorpy.scan_tx_events(4000000, 4100000, lambda h,txr,e: e if e["bond_type"] == "\u0003", types={"bond"})'
```

#### Count Outbound Observations by Chain

```python
import collections, json, thorpy

counts = collections.defaultdict(lambda: 0)

def listen(height, txhash, msg):
    global counts
    for tx in msg["txs"]:
        counts[tx["tx"]["chain"]] += 1

thorpy.scan_msgs(4000000, 4100000, listen, types={"MsgObservedTxOut"})

print(json.dumps(counts, indent=2))
```
