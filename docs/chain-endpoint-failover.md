# Chain Endpoint Failover

THORNode bifrost can use several data endpoints per chain. An endpoint is a full node
RPC/API, either a self-hosted daemon or an external provider exposing the same native
protocol (another node you host, Ankr, QuickNode, ...). Bifrost monitors every endpoint,
routes traffic to the best healthy one, fails over automatically when it degrades, and
fails back when you ask it to.

## How failover works

Every chain keeps one **active** endpoint (the highest-priority healthy one at startup).
All requests go to the active endpoint. Reads that fail with a transport error, an HTTP
3xx / 5xx, a 401 / 403, an HTML error page, a JSON-RPC "internal error", or an empty or
invalid JSON body are retried on the next healthy endpoint immediately and charged to the
endpoint that failed. On the HTTP-routed chains a 2xx body is checked only when it is JSON
(or untyped) and under 64 KiB; larger bodies and other content types reach the caller
unchecked. When no endpoint is left to try, the error returned to the caller carries the
last node's JSON-RPC code and message. A rate-limited read is retried on the next endpoint too, but the rate-limited
endpoint is only put in cooldown (see below), never charged. On the HTTP-routed chains
(EVM, SOL, XRP, TRON, and the Cosmos CometBFT RPC) a JSON-RPC "method not found" is the
caller's business and is never charged: bifrost calls optional methods
(`eth_getBlockReceipts`) and falls back itself when a node lacks them. On XRP, a
provider answering `lgrNotFound` or `txnNotFound`, or a server answering that it cannot
serve right now (`tooBusy`, `noNetwork`, `notSynced`, `noCurrent`, `noClosed`,
`amendmentBlocked`, `unlBlocked`, `notReady`), is asked last: the other endpoints are
tried first and nothing is charged. Writes (broadcasts) are sent once, to the active endpoint, with the
caller's whole timeout, and never duplicated elsewhere: a write moves to the next
endpoint only when connecting to the active one failed before anything was sent (gRPC
writes never move). The XRP transport retry wrapper never replays `submit` either.

A read attempt is cut at the attempt timeout (half of `http_request_timeout`, at most
10s) only while another candidate is left to try; the last or only candidate keeps the
caller's whole timeout, so a single-endpoint configuration keeps the request timeout it
had before failover existed. The attempt timeout covers the wait for the response
headers and the check of the start of the body; once a response is handed to the
caller, its body is read within the caller's own deadline, so a slow large response
uses that deadline and is not moved to another endpoint. When the caller's timeout runs out waiting for an endpoint,
that endpoint is charged; when the caller cancels, it is not.

UTXO chains differ in two ways. The primary daemon is retried with the client's own
backoff (`max_rpc_retries`, up to about a minute with the default 9) before a fallback
is tried, as it was before failover existed. And "method not found" (-32601) and
"internal error" (-32603) both count as endpoint errors there: a UTXO fallback must serve
every RPC bifrost calls (`txindex=1` included). An endpoint that answered a read with an
application error ("no such transaction") is authoritative over one that failed.

Several UTXO decisions ask only the primary daemon and fail closed when it cannot
answer: block-meta pruning (kept while our own broadcast may still be in the primary's
mempool, listed via `getrawmempool` on daemons without `getmempoolentry`, such as Zebra;
while the health checks keep the primary out of rotation the meta is kept without
asking), the mempool-ancestor check before spending an unconfirmed UTXO (the UTXO is
skipped that round), the relay fee that sets the outbound fee floor on LTC, BCH, DOGE and
ZEC (the last value is kept; a fallback provider's is never taken, since every signer
must compute the same fee), ZEC input selection (checked against the primary's own
height, as the inputs come from the primary), retiring an expired ZEC payload (only once
the primary has reached the expiry height, read before the transaction is looked up),
and reorg re-confirmation. A long primary
outage therefore retains block meta and spends only confirmed UTXOs. While the health
checks hold the primary out of rotation and a fallback serves the scan, the per-block
reads only the primary can answer do not wait for it: the relay fee keeps its last
value, the solvency report (read from the primary's wallet) is skipped, and a reorg
with customer transactions fails at once and is retried; without a fallback the
primary is asked as before.

A UTXO reorg is walked back by block hash from the scanned block: each earlier block is
fetched by the hash its child names, so an endpoint that is behind or still on the old
fork cannot answer for another chain, and a reorg without customer transactions is
resolved through any endpoint that has the new blocks. The walk stops at the first stored
block on the new chain, or where no stored block is left in the window; a height with no
stored block (a gap a skip-ahead left) is walked through by hash. A customer
transaction becomes an errata only on the primary's positive "not found", or when the
primary holds it only in a block that is no longer on its chain (txindex keeps the
transactions of disconnected blocks; the daemon looks in its mempool first, so a block
hash with no confirmations means the transaction is in no active block and not in the
mempool), and only once
the primary's block at the scanned block's height minus one is the scanned block's
parent (the primary is caught up and on the new chain); one contained in the scanned
block, or in a new-chain block the walk has fetched, counts as on chain without asking.
While the primary cannot give that answer, the scan of the block that revealed the reorg
fails and is retried, so that chain's scanner waits.

On every failover chain with reorg handling (UTXO, ETH, and the EVM chains whose
`max_reorg_rescan_blocks` is above 0, so not AVAX) a reorg is resolved all or nothing:
the errata, the repaired block records and the observations of the rescanned blocks are
reported and written only once the whole scan of the block that revealed the reorg has
succeeded. A transaction lookup that cannot be answered fails the scan, which is
retried, so it never turns into an errata (on ETH and the EVM chains a receipt that
cannot be read keeps the observation, as before). A receipt read for a scanned or
rescanned block must carry that block's hash, or the scan fails and is retried; this is
checked on ETH and on BSC, BASE and POL, whose block hash bifrost computes exactly as
their nodes do, but not on AVAX, whose C-Chain header bifrost cannot hash and which has
instant finality. A failed attempt may already have
removed the signed-transaction and signer-cache entries of outbounds seen in the
rescanned blocks, which is what the retry does anyway; UTXO undoes the
observed-transaction marks the failed attempt set. With several endpoints, ETH and the
EVM chains take a reorg's headers, transaction re-confirmations, rescanned blocks and
their receipts from one witness, the active endpoint, and only once its block at the reorg tip
is the parent of the block being scanned, its block at the scanned height is the scanned
block itself (not a competing block on the same parent), and its tip is at least the
chain height the scan was driven by, which proves it is caught up and on the same fork;
otherwise the scan fails and is retried. An active endpoint that cannot answer these
reads at all (it answers its probes, so nothing else would move it) is put in the
rate-limit cooldown and the next endpoint, held to the same checks, takes over. No other endpoint is asked, so one still
on the old fork cannot suppress an errata. A witness that answers "not found" for a
stored transaction the scanned block lists has not imported that block yet (another
endpoint served it), so the scan is retried instead of reporting an errata for it. A witness whose lookups fail rather than
answer "not found" (for example a node still building its transaction index) holds the
scan until it answers. If a scan stays stuck for longer than `max_resume_block_lag`
while THORChain's last observed height for the chain moves more than that window ahead
of it, the block scanner's existing skip-ahead moves past the stalled height (never for
an unavailable-block error), and this node may not report that reorg or observe the
rescanned blocks (a later reorg re-checks the stored block records still in the
window); the errata then depends on the other nodes' votes.

A background health check runs every `health_check_interval` against every endpoint:

- **Liveness.** A probe that errors or exceeds `health_check_timeout` is a strike.
- **Lag.** When at least two usable endpoints (not disabled, with their network
  verified when that is required, not quarantined for a minority block hash, and not
  found with one in this cycle's integrity check) agree on a tip (within
  `max_block_lag`), an endpoint further behind than `max_block_lag` gets a strike each
  cycle. With two or
  more endpoints, THORChain's last observed height for the chain is a second reference:
  an endpoint more than `max_block_lag` below it gets a strike. That height is set only
  once 2/3 of nodes agree and never goes back, so no endpoint or single node can move
  it, and it can only strike an endpoint behind what the network has already observed,
  never one at the tip. This is what lets two endpoints, which form no tip when they
  disagree, evict a stalled endpoint that still answers. When THORChain's height is
  unavailable, does not answer within `health_check_timeout`, or no usable endpoint
  outside quarantine has reached it (within `max_block_lag`), it strikes nothing; a height that stops moving (a
  halted or quiet chain) is still used, but cannot strike an endpoint at or above it.
  With a single endpoint, the block scanner's own height is the baseline instead.
- **Identity.** Every endpoint must report the same chain identity (EVM `eth_chainId`,
  UTXO genesis block hash, Cosmos `node_info.network`, Solana genesis hash, XRP
  `network_id`). The reference is `expected_identity` when set, otherwise the priority-1
  endpoint's, otherwise one held by a strict majority (and at least two) of the
  endpoints that reported an identity. A single reachable endpoint never becomes the
  reference: it would otherwise disable your own node when it came back. Until a
  reference exists, endpoints serve but none can be disabled for a mismatch. The EVM
  chains need a reference to start, as they sign with the verified chain id: with two
  or more endpoints, the priority-1 endpoint unreachable at startup, and no
  `expected_identity`, the chain client starts only when at least two endpoints reported
  an identity and a strict majority of them agree; otherwise it does not start (it is
  retried every minute). Pin `expected_identity` to let it start on a single reachable
  backup. Once one
  exists, an endpoint on another network is disabled permanently, and the priority-1
  endpoint disagreeing with `expected_identity` stops the chain client from starting.
  Identity is required whenever there is more than one endpoint **or** `expected_identity`
  is set: an endpoint that reports no identity is then never used, even when every other
  endpoint is down, because observing an unknown network is worse than observing
  nothing. XRP servers report `network_id` only when configured with one, so make sure
  yours does before pinning XRP or adding a second XRP endpoint. BTC and BCH share the
  same genesis block hash, so identity cannot tell them apart; only the integrity check
  (with enough endpoints) protects against mixing them. An endpoint with two hosts
  (Cosmos `rpc_host` + `cosmos_grpc_host`, TRON `rpc_host` + `api_host`) must have both
  on the same network: a proven disagreement disables the endpoint, even when it is the
  only one, since bifrost cannot name the network it would be observing. TRON compares
  the block 20 below the lower of the two hosts' tips, so a pair on different networks
  is proven different whenever both hosts answer; a single legacy TRON endpoint keeps
  serving while that check cannot run (a host unreachable), as before endpoint lists
  existed.
- **Integrity.** When `integrity_check_depth` is greater than 0 **and at least two
  endpoints agree on a tip**, the block hash `depth` below the healthy endpoints' level
  (the lower median of their heights, so endpoints that report heights ahead, or are far
  behind while they recover, do not pick it) is compared across the healthy endpoints
  that answered. When one hash holds a strict majority of them, every
  other hash is a strike; an endpoint whose eviction run contained such a strike (even
  when a lag or probe strike completed the eviction) is quarantined, shown as
  `integrity_evicted` in `/status/provider`, and must serve the majority hash again
  before it may serve traffic, even as an all-down fallback. Without a strict majority
  (a tie, or a plurality such as H, H, X, Y) nothing is evicted or cleared and the
  disagreement is logged. With fewer than two agreeing endpoints there is no tip and the
  check does not run. A quarantined endpoint is cleared once it serves the same hash as
  a healthy endpoint at a common height (its own probed height counts toward that tip),
  so one healthy endpoint is enough; with none it stays out until one comes back or
  bifrost is restarted.

`error_threshold` consecutive probe strikes, or `error_threshold` consecutive inline
request errors no more than `error_window` apart, mark an endpoint **unhealthy**. If it
was active, traffic moves to the next healthy endpoint by priority. Unhealthy endpoints
are re-probed every `recovery_check_interval` and return to healthy after
`min_recovery_checks` consecutive passing probes. While every endpoint is down, a
successful request to an unhealthy endpoint (not one quarantined for a minority block
hash) counts as one of those checks too, so traffic can bring it back before the probes
do. Traffic moves back to a recovered
higher-priority endpoint only when `prefer_higher_priority` is true.

A rate limit (HTTP 429, gRPC `ResourceExhausted` or a gateway's HTTP 429, JSON-RPC -32005 / -32029; on Solana
-32005 means the node is behind and is charged as an endpoint error instead, as is -32016) never marks
an endpoint unhealthy and never counts as a strike, including when a health probe is
rate limited: the endpoint enters a cooldown of `rate_limit_backoff`, a healthy endpoint
is not probed while cooling down (recovery probes of an unhealthy one still run), and
traffic moves to the next eligible endpoint immediately. A cooldown is not a minimum
interval between requests: when every endpoint is cooling down or unhealthy, reads go to
the usable ones anyway rather than fail. One exception at startup: an
endpoint rate limited during bootstrap cannot report its identity, so when identity is
required it starts unhealthy and unverified until a later probe succeeds.

gRPC `Unimplemented` counts as an endpoint error: a Cosmos node that lacks a module
bifrost needs cannot serve it, and is evicted like an unreachable one. So do
`Unauthenticated` and `PermissionDenied`, which is how grpc-go reports an HTTP 401 / 403
from a gateway (a revoked or exhausted API key). An HTTP 429 from a gateway arrives as
`Unavailable` with the status in the message and is treated as a rate limit.

`rate_limit_backoff` is also slept, once, on the UTXO chains only, and only when the
rate-limited endpoint was the primary and no other endpoint served the read. That
applies whatever the endpoint count. On every other chain the request fails fast after
the cooldown is recorded; a lone rate-limited endpoint there does not pause, so set a
provider's own client-side limits accordingly.

If every endpoint is unhealthy, bifrost logs an error, sets
`bifrost_endpoint_all_down{chain}` to 1, and keeps trying all of them.

### Per-chain notes

| Chain                           | Reads and writes                                                                                                                                             | Broadcast / verification                                                                                                      |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------- |
| UTXO (BTC, LTC, BCH, DOGE, ZEC) | reads fail over across endpoints                                                                                                                             | wallet RPC and `sendrawtransaction` always use the priority-1 daemon (it holds the wallet); signer verification reads it only |
| EVM (ETH, AVAX, BSC, BASE, POL) | all JSON-RPC routed to the active endpoint; the signing chain id comes from the verified identity                                                            | `eth_sendRawTransaction` sent once; signer verification asks every endpoint                                                   |
| Cosmos (GAIA, NOBLE)            | gRPC and CometBFT RPC routed to the same active endpoint; each endpoint needs `cosmos_grpc_host` and `rpc_host`                                              | `BroadcastTx` sent once; mempool verification asks every endpoint                                                             |
| Solana                          | routed; `max_block_lag` is in slots (default 50); integrity check disabled (slots can be skipped)                                                            | `sendTransaction` sent once                                                                                                   |
| XRP                             | routed                                                                                                                                                       | `submit` sent once                                                                                                            |
| TRON                            | `api_host` and `rpc_host` routed together; each endpoint needs both; its height is the lower of the two tips                                                 | `/wallet/broadcasttransaction` sent once                                                                                      |
| XMR                             | not supported (no endpoint list); the Monero client talks to its signer sidecar and, when `rpc_host` is set, reads heights and blocks from that daemon first |                                                                                                                               |

On Solana, a vault with more than one page (1,000) of new signatures has all its pages
listed by one endpoint: a node that does not have a page's last signature answers the
next page with an empty list, which would end the scan early and skip the older
signatures for good. A failed listing is retried from its first page on the same
endpoint; after three failed listings of a vault in a row the next endpoint lists it, so
one that keeps failing a page does not hold the vault back. A vault's listing resumes from its cursor signature with that
signature's own slot as `minContextSlot`: Agave ignores a cursor it does not know and
lists the whole history it holds, so a node that has not yet finalized the cursor's slot
(an endpoint behind the previous one) must refuse with -32016 instead, which on Solana is
an endpoint error, so the read moves to the next endpoint. A cursor stored before its
slot was kept has none until the vault's next new signature.

`authorization_bearer` and `username` / `password` set on an `endpoints:` entry are
honored on every chain (gRPC sends them as `authorization` metadata). A legacy
configuration without `endpoints:` sends the chain-level credentials only where its
chain's client sent them before endpoint lists existed, because every chain inherits the
default `thorchain` / `password` pair from the config template: `username` / `password`
on the UTXO chains, and on AVAX, BSC, BASE and POL when both are set;
`authorization_bearer` on AVAX, BSC, BASE and POL only. Credentials embedded in an HTTP
URL (`https://user:pass@host/`) are sent, as a header, only when the endpoint has no
`username`, `password` or `authorization_bearer` of its own, and are stripped from the
URL either way. Endpoint URLs and credentials never appear in logs, metrics or status
output.

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
fields keeps its previous behavior when both hosts agree with each other but not with
`cosmos_chain_id`: the mismatch is logged as an error and bifrost starts. Set `endpoints:`
or `expected_identity` to make it fatal. The two hosts disagreeing with each other is
never advisory: that endpoint is disabled.

Broadcast verification (before a stuck outbound may be signed again) asks every usable
endpoint, however long it has been unhealthy, and an endpoint that does not answer makes
the result ambiguous, which keeps the outbound from being re-signed. Endpoint health says
nothing about whether a transaction an endpoint accepted can still settle, and a re-sign
does not necessarily reuse the original's nonce or sequence: Cosmos signs with its
locally cached sequence (which the broadcast advanced), and a Solana durable-nonce
outbound does not expire with its blockhash. A dead endpoint left in an EVM, Cosmos or
Solana list therefore blocks the automatic re-sign of a stuck outbound until it answers
again or is removed from the list. An endpoint whose identity has not been verified since
bifrost started (one unreachable at startup, which may hold a broadcast from before a
restart) counts the same way: it cannot be asked, so absence is not proven while it is
configured. A disabled endpoint (proven to be on another network) does not count. TRON is
the exception: its transactions expire 20 minutes after creation, so an endpoint
continuously unhealthy for longer than `signer_cache_expiry` is skipped there. Each
verification left ambiguous this way counts on
`bifrost_endpoint_verification_blocked_total{chain,endpoint}` against the endpoint that
blocked it, and a warning naming that endpoint is logged at most every five minutes.
Alert on a rising rate, and remove an endpoint that stays dead from the configuration
(and restart bifrost) so stuck outbounds can be signed again.

An endpoint's identity is verified once, when it is first reached. A provider URL
repointed at another network after bifrost started is not re-verified, except a Cosmos
gRPC host, whose network is compared with the verified identity on every probe (a
mismatch disables the endpoint). Otherwise the only later defence is the block-hash
integrity check, and that check has limits. It needs
a height quorum (two endpoints within `max_block_lag` of each other), so it never
runs with a single endpoint. It evicts an endpoint only when a strict majority of the
healthy endpoints that answered agree on another hash, so with two endpoints a
disagreement cannot be attributed to either: both stay in service and the tie is
logged as a warning. In practice that means three or more endpoints are needed before
a repointed provider is caught automatically; with fewer, watch the `endpoints disagree
on the block hash` log line and restart bifrost after fixing the configuration (identity
is re-verified at startup). Leave `integrity_check_depth` enabled on chains that support
it either way: an evicted endpoint is only let back in after it serves the majority hash
again.

## Configuration

Bifrost reads its configuration from the embedded defaults plus environment variables;
it does not load a `bifrost.yaml` file. The YAML below shows the configuration
structure; in a deployment, set the same values through the indexed environment
variables described further down. Add an `endpoints` list to any chain whose client
supports it (UTXO, EVM, GAIA, NOBLE, SOL, XRP, TRON; an `endpoints` list on any other
chain is rejected at startup). If `endpoints` is omitted, bifrost falls back to the
existing `rpc_host` / `api_host` fields (backward-compatible), which must still satisfy
the chain's host rules (GAIA and NOBLE need `cosmos_grpc_host` too, TRON needs `api_host`
too). Hosts use http or https; a `tcp://` host is read as `http://`, and any other
scheme (for example `ws://`) is refused at startup, since the endpoint router speaks
HTTP only.

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
      rate_limit_backoff: 30s # cooldown after a rate limit (429; -32005 except on Solana): the endpoint is skipped this long while another endpoint can serve, and traffic moves on at once. UTXO chains also sleep it once when the primary was rate limited and nothing else served the read
      health_check_timeout: 5s # a probe slower than this is a strike; clamped to half of health_check_interval when larger
      integrity_check_depth: 20 # compare block hashes depth below the healthy endpoints' level; 0 disables
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
endpoint index (at most 16: a higher number is refused at startup, and `0` is ignored
with a warning). Every endpoint needs a distinct `NAME`. An endpoint with no explicit
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
| `bifrost_endpoint_verification_blocked_total`     | verifications this endpoint left ambiguous (re-sign blocked)       |
