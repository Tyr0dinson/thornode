# Chain Endpoint Failover

THORNode bifrost supports multiple data endpoints per chain. An endpoint is a full node
RPC/API — either a self-hosted daemon or an external provider exposing the same interface
(e.g. another node hosted by the operator, Ankr, QuickNode). When the preferred endpoint
is unavailable, bifrost automatically fails over to the next one and recovers when the
preferred endpoint comes back.

All endpoints for a chain speak that chain's native protocol. There is no support for
partial third-party APIs (e.g. Blockchair, BlockCypher) that implement only a subset of
methods — a fallback must be able to serve the same requests as the primary.

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

Example — a primary local daemon with an external fallback for BTC:

```bash
BIFROST_CHAINS_BTC_ENDPOINTS_1_URL=http://localhost:8332
BIFROST_CHAINS_BTC_ENDPOINTS_1_USERNAME=user
BIFROST_CHAINS_BTC_ENDPOINTS_1_PASSWORD=pass
BIFROST_CHAINS_BTC_ENDPOINTS_2_URL=https://rpc.ankr.com/btc/YOUR_ANKR_KEY
```

## Monitoring

Endpoint health is visible at the `/status/provider` HTTP endpoint on the bifrost health
port. Prometheus metrics are exposed under the `bifrost_provider_*` namespace.
