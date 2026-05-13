# Chain Provider Failover

THORNode bifrost supports multiple data providers per chain. When the primary provider
(local daemon) is unavailable, bifrost automatically fails over to backup providers
(e.g. Blockchair, Ankr, QuickNode) and recovers when the primary comes back.

## Configuration

Add a `providers` list to any chain in `bifrost.yaml`. If `providers` is omitted,
bifrost falls back to the existing `rpc_host` / `api_host` fields (backward-compatible).

```yaml
chains:
  BTC:
    providers:
      - name: local               # human-readable label for logs/metrics
        rpc_host: http://localhost:8332
        priority: 1               # lower number = preferred
        type: bitcoin-rpc
        username: user
        password: pass
      - name: blockchair
        rpc_host: https://api.blockchair.com
        priority: 2
        type: blockchair-rest
        authorization_bearer: YOUR_BLOCKCHAIR_API_KEY
      - name: ankr
        rpc_host: https://rpc.ankr.com/btc/YOUR_ANKR_KEY
        priority: 3
        type: bitcoin-rpc
    failover:
      error_threshold: 5          # errors within error_window before marking unhealthy
      error_window: 60s
      health_check_interval: 10s  # how often to probe unhealthy providers
      max_block_lag: 3            # blocks behind consensus tip before eviction
      min_recovery_checks: 3      # passing checks required to restore a provider
      recovery_check_interval: 30s
      prefer_higher_priority: false # set true to actively fail back to lower-priority-number providers
      rate_limit_backoff: 30s     # minimum sleep on HTTP 429 (rate limit); does NOT trigger failover
  ETH:
    providers:
      - name: local
        rpc_host: http://localhost:8545
        priority: 1
        type: evm-rpc
      - name: quicknode
        rpc_host: https://your-endpoint.quiknode.pro/YOUR_KEY/
        priority: 2
        type: evm-rpc
        authorization_bearer: YOUR_BEARER_TOKEN
```

## Provider Types

| Type | Chains | Notes |
|---|---|---|
| `bitcoin-rpc` | BTC, LTC, BCH, DOGE | Standard Bitcoin JSON-RPC |
| `zcash-rpc` | ZEC | Zcash JSON-RPC dialect |
| `evm-rpc` | ETH, AVAX, BSC, BASE, POL | Ethereum JSON-RPC |
| `blockchair-rest` | BTC, LTC, BCH, DOGE, ZEC | Read-only REST API |
| `blockcypher-rest` | BTC, LTC, DOGE | Read-only REST API (no BCH/ZEC) |
| `cosmos-grpc` | GAIA, NOBLE | Cosmos SDK gRPC |
| `solana-rpc` | SOL | Solana JSON-RPC |
| `tron-api` | TRON | Tron HTTP API + JSON-RPC |
| `xrp-rpc` | XRP | XRP Ledger JSON-RPC |

## Blockchair Limitation

When Blockchair is the active read provider, `GetBlockVerboseTxs` returns transaction
stubs with only `Txid` populated. Network fee estimation from full block data may return
zeros until the primary recovers. Blockchair is safe for block scanning and individual
transaction lookups.

## Monitoring

Provider health is visible at the `/status/provider` HTTP endpoint on the bifrost health
port. Prometheus metrics are exposed under the `bifrost_provider_*` namespace.
