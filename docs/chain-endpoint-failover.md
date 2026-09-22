# Chain Endpoint Failover

THORNode bifrost can use several data endpoints per chain. An endpoint is a full node
RPC/API, either a self-hosted daemon or an external provider exposing the same native
protocol (another node you host, Ankr, QuickNode, ...). Bifrost monitors every endpoint,
routes traffic to the best healthy one, fails over automatically when it degrades, and
fails back when you ask it to.

## How failover works

Every chain keeps one **active** endpoint (the highest-priority healthy one at startup).
All requests go to the active endpoint. Reads that fail with a transport error, an HTTP
3xx / 5xx, a 401 / 403, a JSON-RPC "method not found" or "internal error", or a rate
limit are retried on the next healthy endpoint immediately. Writes (broadcasts) are sent
once, to the active endpoint, and never duplicated elsewhere.

A background health check runs every `health_check_interval` against every endpoint:

- **Liveness.** A probe that errors or exceeds `health_check_timeout` is a strike.
- **Lag.** When at least two endpoints agree on a tip (within `max_block_lag`), an
  endpoint further behind than `max_block_lag` gets a strike each cycle. With a single
  endpoint, the block scanner's own height is the baseline instead.
- **Identity.** Every endpoint must report the same chain identity (EVM `eth_chainId`,
  UTXO genesis block hash, Cosmos `node_info.network`, Solana genesis hash, XRP
  `network_id`). The reference is `expected_identity` when set, otherwise the priority-1
  endpoint's, otherwise one that at least two endpoints agree on. A single reachable
  endpoint never becomes the reference: it would otherwise disable your own node when it
  came back. Until a reference exists, endpoints serve but none can be disabled for a
  mismatch. Once one exists, an endpoint on another network is disabled permanently, and
  the priority-1 endpoint disagreeing with `expected_identity` stops the chain client
  from starting. With more than one endpoint, an endpoint that reports no identity at
  all is never used, even when every other endpoint is down: observing an unknown
  network is worse than observing nothing.
- **Integrity.** When `integrity_check_depth` is greater than 0 **and at least two
  endpoints agree on a tip**, the block hash at `tip - depth` is compared across the
  healthy endpoints; a minority hash is a strike, and an endpoint evicted for one must
  match the majority again before it may serve. With fewer than two agreeing endpoints
  there is no tip and the check does not run.

`error_threshold` consecutive probe strikes, or `error_threshold` consecutive inline
request errors no more than `error_window` apart, mark an endpoint **unhealthy**. If it
was active, traffic moves to the next healthy endpoint by priority. Unhealthy endpoints
are re-probed every `recovery_check_interval` and return to healthy after
`min_recovery_checks` consecutive passing probes. Traffic moves back to a recovered
higher-priority endpoint only when `prefer_higher_priority` is true.

A rate limit (HTTP 429, gRPC `ResourceExhausted`, JSON-RPC -32005 / -32029) never marks
an endpoint unhealthy and never counts as a strike, including when a health probe is
rate limited: the endpoint enters a cooldown of `rate_limit_backoff`, is not probed while
cooling down, and traffic moves to the next eligible endpoint immediately.

`rate_limit_backoff` is also slept, once, on the UTXO chains only, and only when the
rate-limited endpoint was the primary and no other endpoint served the read. That
applies whatever the endpoint count. On every other chain the request fails fast after
the cooldown is recorded; a lone rate-limited endpoint there does not pause, so set a
provider's own client-side limits accordingly.

If every endpoint is unhealthy, bifrost logs an error, sets
`bifrost_endpoint_all_down{chain}` to 1, and keeps trying all of them.

### Per-chain notes

| Chain                           | Reads and writes                                                                                                | Broadcast / verification                                                                                                      |
| ------------------------------- | --------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| UTXO (BTC, LTC, BCH, DOGE, ZEC) | reads fail over across endpoints                                                                                | wallet RPC and `sendrawtransaction` always use the priority-1 daemon (it holds the wallet); signer verification reads it only |
| EVM (ETH, AVAX, BSC, BASE, POL) | all JSON-RPC routed to the active endpoint; the signing chain id comes from the verified identity               | `eth_sendRawTransaction` sent once; signer verification asks every endpoint                                                   |
| Cosmos (GAIA, NOBLE)            | gRPC and CometBFT RPC routed to the same active endpoint; each endpoint needs `cosmos_grpc_host` and `rpc_host` | `BroadcastTx` sent once; mempool verification asks every endpoint                                                             |
| Solana                          | routed; `max_block_lag` is in slots (default 50); integrity check disabled (slots can be skipped)               | `sendTransaction` sent once                                                                                                   |
| XRP                             | routed                                                                                                          | `submit` sent once                                                                                                            |
| TRON                            | `api_host` and `rpc_host` routed together; each endpoint needs both; both are probed                            | `/wallet/broadcasttransaction` sent once                                                                                      |
| XMR                             | not supported; the Monero client talks to its signer sidecar only                                               |                                                                                                                               |

`authorization_bearer` and `username` / `password` are honored on every chain (gRPC sends
them as `authorization` metadata). Credentials embedded in a URL
(`https://user:pass@host/`) are moved into the request header. Endpoint URLs and
credentials never appear in logs, metrics or status output.

Two URLs from the same provider count as two votes for the lag quorum and the integrity
majority; prefer endpoints from different operators.

### Recommended `expected_identity` values (mainnet)

| Chain | Value                                                              |
| ----- | ------------------------------------------------------------------ |
| ETH   | `0x1`                                                              |
| AVAX  | `0xa86a`                                                           |
| BSC   | `0x38`                                                             |
| BASE  | `0x2105`                                                           |
| POL   | `0x89`                                                             |
| BTC   | `000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f` |
| LTC   | `12a765e31ffd4059bada1e25190f6e98c99d9714d334efa41a195a7e7e04bfe2` |
| BCH   | `000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f` |
| DOGE  | `1a91e3dace36e2be3bf030a65679fe821aa1d6ef92e7c9902eb318182c355691` |
| GAIA  | `cosmoshub-4` (defaults to `cosmos_chain_id`)                      |
| SOL   | `5eykt4UsFv8P8NJdTREpY1vzqKqZKvdpKuc147dw2N9d`                     |
| XRP   | `0`                                                                |
| TRON  | `0x2b6653dc`                                                       |

Mocknet and stagenet use different values, so nothing is pinned by default.

On GAIA and NOBLE, `cosmos_chain_id` is used as the expected identity when an
`endpoints:` list is configured, and a mismatch then stops the chain client from
starting. A legacy configuration with only the flat `rpc_host` / `cosmos_grpc_host`
fields keeps its previous behavior: a mismatch is logged as an error and bifrost
starts. Set `endpoints:` or `expected_identity` to make it fatal.

A dead endpoint left in the list does not block recovery of a stuck outbound: an
endpoint continuously unhealthy for longer than `signer_cache_expiry` is skipped when
verifying whether a broadcast is still alive. That window counts any time spent out of
the healthy state, lag included, not only unreachability.

An endpoint's identity is verified once, when it is first reached. A provider URL
repointed at another network after bifrost started is caught by the block-hash
integrity check rather than by identity verification, so leave `integrity_check_depth`
enabled on chains that support it.

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
      health_check_interval: 10s # how often every endpoint is probed
      max_block_lag: 3 # blocks behind consensus tip before eviction
      min_recovery_checks: 3 # passing checks required to restore an endpoint
      recovery_check_interval: 30s
      prefer_higher_priority: false # set true to actively fail back to lower-priority-number endpoints
      rate_limit_backoff: 30s # cooldown after a rate limit (429, -32005): the endpoint is skipped this long and traffic moves on at once. UTXO chains also sleep it once when the primary was rate limited and nothing else served the read
      health_check_timeout: 5s # a probe slower than this is a strike; must be < health_check_interval
      integrity_check_depth: 20 # compare block hashes at tip - depth across endpoints; 0 disables
    expected_identity: "000000000019d6689c085ae165831e934ff763ae46a2a6c172b3f1b60a8ce26f" # optional network pin
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

Set only one auth method per endpoint: if both are set, the bearer token wins.

## Environment variables

Because environment variables cannot be bound to elements of a list, endpoints are
configured via explicit indexed keys of the form:

```text
BIFROST_CHAINS_<CHAIN>_ENDPOINTS_<N>_<FIELD>
```

where `<CHAIN>` is the chain symbol (e.g. `BTC`, `ETH`, `GAIA`) and `<N>` is the 1-based
endpoint index. Every endpoint needs a distinct `NAME`. An endpoint with no explicit
`PRIORITY` defaults to its index, so `ENDPOINTS_1` is preferred over `ENDPOINTS_2`. Supported fields (case-insensitive):

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
BIFROST_CHAINS_BTC_ENDPOINTS_1_NAME=local
BIFROST_CHAINS_BTC_ENDPOINTS_1_URL=http://localhost:8332
BIFROST_CHAINS_BTC_ENDPOINTS_1_USERNAME=user
BIFROST_CHAINS_BTC_ENDPOINTS_1_PASSWORD=pass
BIFROST_CHAINS_BTC_ENDPOINTS_2_NAME=ankr
BIFROST_CHAINS_BTC_ENDPOINTS_2_URL=https://rpc.ankr.com/btc/YOUR_ANKR_KEY
```

## Monitoring

`GET /status/provider` (and `GET /status/provider/{CHAIN}`) on the bifrost health port
lists every endpoint per chain with its classified host, `state` (healthy, unhealthy,
recovering, disabled), `active`, last probed `height`, `probe_latency_ms`, `in_cooldown`,
`identity_verified`, `inline_errors`, `probe_strikes` and `last_error_kind`.

Prometheus metrics (labels `chain`, `endpoint`):

| Metric                                            | Meaning                                                            |
| ------------------------------------------------- | ------------------------------------------------------------------ |
| `bifrost_endpoint_state`                          | 0 healthy, 1 unhealthy, 2 recovering, 3 disabled                   |
| `bifrost_endpoint_active`                         | 1 for the active endpoint                                          |
| `bifrost_endpoint_height`                         | latest probed height                                               |
| `bifrost_endpoint_probe_latency_seconds`          | latest probe round trip                                            |
| `bifrost_endpoint_errors_total{kind}`             | errors by kind: transport, http, probe, lag, integrity, rate_limit |
| `bifrost_endpoint_failovers_total{from,to,event}` | active endpoint changes; event is failover or failback             |
| `bifrost_endpoint_recoveries_total`               | returns to healthy                                                 |
| `bifrost_endpoint_all_down`                       | 1 when no endpoint of the chain is healthy. Alert on this.         |
