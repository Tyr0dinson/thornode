# Chain Endpoint Failover

THORNode bifrost supports configuring multiple data endpoints per chain. An endpoint is a
full node RPC/API, either a self-hosted daemon or an external provider exposing the same
interface (e.g. another node hosted by the operator, Ankr, QuickNode).

Failover support varies by chain:

- UTXO (BTC, LTC, BCH, DOGE, ZEC): each read request fails over to the next endpoint when
  the primary errors. Writes always use the primary, and an HTTP 429 backs off
  (`rate_limit_backoff`) rather than failing over.
- EVM (ETH, AVAX, BSC, BASE, POL): the first endpoint that answers an `eth_chainId` probe
  is selected when the client starts.
- Cosmos (GAIA, NOBLE): the first endpoint whose gRPC host is TCP-reachable is selected at
  startup, and its `rpc_host` is used for the block scanner.
- Solana, XRP, Tron: only the highest-priority endpoint is used. XMR uses only the legacy
  `rpc_host` and ignores the `endpoints` list. Automatic failover for these is tracked in
  #2828.

All endpoints for a chain speak that chain's native protocol. There is no support for
partial third-party APIs (e.g. Blockchair, BlockCypher) that implement only a subset of
methods. A fallback must be able to serve the same requests as the primary.

## Configuration

Add an `endpoints` list to any chain in `bifrost.yaml`. If `endpoints` is omitted, bifrost
falls back to the existing `rpc_host` / `api_host` fields (backward-compatible).

```yaml
chains:
  BTC:
    endpoints:
      - name: local # human-readable label for logs/metrics
        rpc_host: http://localhost:8332
        priority: 1 # lower number = preferred
        username: user
        password: pass
      - name: ankr
        rpc_host: https://rpc.ankr.com/btc/YOUR_ANKR_KEY
        priority: 2
    failover:
      error_threshold: 5 # errors within error_window before marking unhealthy
      error_window: 60s
      health_check_interval: 10s # how often to probe unhealthy endpoints
      max_block_lag: 3 # blocks behind consensus tip before eviction
      min_recovery_checks: 3 # passing checks required to restore an endpoint
      recovery_check_interval: 30s
      prefer_higher_priority: false # set true to actively fail back to lower-priority-number endpoints
      rate_limit_backoff: 30s # minimum sleep on HTTP 429 (rate limit); does NOT trigger failover
  ETH:
    endpoints:
      - name: local
        rpc_host: http://localhost:8545
        priority: 1
      - name: quicknode
        rpc_host: https://your-endpoint.quiknode.pro/YOUR_KEY/
        priority: 2
        authorization_bearer: YOUR_BEARER_TOKEN
```

Only `rate_limit_backoff` is consumed today (the minimum sleep applied to UTXO reads on an
HTTP 429). The other `failover:` keys are parsed and defaulted but not yet acted on; they
are reserved for the health-monitor failover and failback work tracked in #2828.

### Per-endpoint fields

| Field                   | Notes                                                              |
| ----------------------- | ------------------------------------------------------------------ |
| `name`                  | Human-readable label used in logs and metrics.                     |
| `rpc_host`              | The endpoint URL (JSON-RPC / HTTP API).                            |
| `api_host`              | Secondary HTTP API host, where the chain client needs one (TRON).  |
| `cosmos_grpc_host`      | Cosmos SDK gRPC host (GAIA, NOBLE).                                |
| `cosmos_grpc_tls`       | Whether the gRPC host uses TLS.                                    |
| `priority`              | Lower number is preferred. Endpoints are tried in priority order.  |
| `username` / `password` | Optional basic-auth credentials.                                   |
| `authorization_bearer`  | Optional bearer token (sent as an `Authorization: Bearer` header). |

`authorization_bearer` is honored by EVM, UTXO, and XMR endpoints; Cosmos (GAIA, NOBLE),
Solana, XRP, and Tron currently ignore it and use only `username` / `password`. Set only
one auth method per endpoint: if both are set, EVM and UTXO use the bearer token while XMR
uses basic auth.

## Environment variables

Because environment variables cannot be bound to elements of a list, endpoints are
configured via explicit indexed keys of the form:

```text
BIFROST_CHAINS_<CHAIN>_ENDPOINTS_<N>_<FIELD>
```

where `<CHAIN>` is the chain symbol (e.g. `BTC`, `ETH`, `GAIA`) and `<N>` is the 1-based
endpoint index. An endpoint with no explicit `PRIORITY` defaults to its index, so
`ENDPOINTS_1` is preferred over `ENDPOINTS_2`. Supported fields (case-insensitive):

| Env field              | Maps to                |
| ---------------------- | ---------------------- |
| `URL` or `RPC_HOST`    | `rpc_host`             |
| `API_HOST`             | `api_host`             |
| `COSMOS_GRPC_HOST`     | `cosmos_grpc_host`     |
| `COSMOS_GRPC_TLS`      | `cosmos_grpc_tls`      |
| `NAME`                 | `name`                 |
| `PRIORITY`             | `priority`             |
| `USERNAME`             | `username`             |
| `PASSWORD`             | `password`             |
| `AUTHORIZATION_BEARER` | `authorization_bearer` |

Example, a primary local daemon with an external fallback for BTC:

```bash
BIFROST_CHAINS_BTC_ENDPOINTS_1_URL=http://localhost:8332
BIFROST_CHAINS_BTC_ENDPOINTS_1_USERNAME=user
BIFROST_CHAINS_BTC_ENDPOINTS_1_PASSWORD=pass
BIFROST_CHAINS_BTC_ENDPOINTS_2_URL=https://rpc.ankr.com/btc/YOUR_ANKR_KEY
```

## Monitoring

The `/status/provider` HTTP endpoint on the bifrost health port lists the configured
endpoints per chain (name, priority, and classified host). Per-endpoint health checks and
Prometheus metrics are tracked in #2828.
