# Two-Layer Bifrost RPC Router

One small Go executable runs as two independently configured processes. The
examples are templates, not a description of any running node.

## Layering Rule

P1 and P2 are **manual switches**. Neither probes provider health, fails over
or back on its own, or retries a read. A route changes only through a
revision-checked admin command.

```text
Bifrost -> P1 gateway -> native:  the chain's normal provider, direct
                      -> adapter: that chain's P2
                           P2 adapter (this binary) -> selected provider
```

- P1 ("gateway") is the stable front door. Every chain on P1 has two backends:
  `native` and `adapter`.
- P2 ("adapter") is the provider adapter, with one backend per provider.

## Switching Contract

- P1 has stable HTTP URLs. Each chain has independent admission and a selected
  backend. A backend contains all that chain's endpoint roles; they cannot be
  switched independently by this API.
- Selection happens **per admitted HTTP request**, including on an existing
  keep-alive connection. Waiting requests use the route current at admission.
  In-flight requests keep their old provider and deadline. Responses are streamed,
  with no block cache, extra RPC calls, or per-request chain verification.
- This is not a snapshot across several requests or both layers. Separate reads
  around a switch can come from different providers. Validate agreement before
  switching; Bifrost's canonical-chain checks remain intact. Do not switch both
  layers simultaneously. Configure P2 first, then move the chain at P1.
- Switching does not restart either proxy or Bifrost. Reload can add/update
  provider targets/credentials without restarting. Listener addresses, endpoint
  paths, chain membership, defaults and limits are immutable during reload.
- Compare-and-swap revisions prevent stale commands from overwriting newer
  choices. A synced mode-0600 state file is atomically replaced before publishing
  the route. An exclusive OS lock prevents two processes using one state file.
  Missing/corrupt state fails startup; initialization is explicit.
  A persistence failure rejects that one change (HTTP 503, route and revision
  unchanged, retry with the same revision), logs `event=routing_state_fault`
  and increments `rpc_router_state_faults_total`; dispatch and `/readyz`
  continue. A rename may have occurred before a failing fsync, so a restart
  before a successful retry can start on either route.
- Each state file binds the choices to a digest of the entire loaded catalog,
  including credentials. Keep configuration/Secret revisions and state backups
  together. A failed reload leaves the running catalog intact. A restart with a
  changed catalog adopts it (`event=state_catalog_drift`): saved routes that
  still name a backend are kept, others return to the chain default, and the
  state is rewritten at a new revision. Never delete state to force a restart.
- Neither layer rebuilds transactions or interprets RPC errors as success.
  Incoming idempotency headers are stripped, outgoing bodies cannot be
  implicitly replayed, and outbound HTTP/2 is disabled. Bodyless GET forwarding
  also disables implicit transport replay. No request, read or write, is retried or sent to a second backend.
- An ambiguous broadcast returns an error without replay or provider
  switching. The routers have no broadcast journal; Bifrost handles broadcasts.
- Never switch a chain to a backend lacking the scanner's history.

P1 remains a shared failure point; healthy configuration changes are seamless
at request boundaries, but outages/process upgrades are not zero downtime.

## Configuration

See `examples/gateway.json` and `examples/adapter.json`. The same binary runs
both files; use separate sockets, state volumes and service identities. Bind
loopback for a host trial; use private ClusterIP listeners in
Kubernetes. Do not expose either data port publicly.

Every backend member points to an absolute, restricted target file:

```json
{"url":"https://rpc.example.invalid/credential-path","headers":{}}
```

Basic auth, where required, is an `Authorization: Basic ...` header in that
file, not URL userinfo. Supported headers are `Authorization` and `X-Api-Key`.
HTTPS verifies certificates; no environment proxy or redirects.
URL query credentials/userinfo/fragments are rejected. Never pass credentials
on a command line or commit target files.

Internal HTTP targets require an explicit opt-in, for example P1 to P2:

```json
{"url":"http://127.0.0.1:8081/base","allow_http":true}
```

With `suffix:true`, the
frontend prefix is replaced by that target path and the remaining path/query
is retained. Exact JSON-RPC endpoints should not
enable suffixes. Caller authorization, cookies, forwarding headers and trailers
are never forwarded. Payload bytes and IDs remain unchanged and all traffic is
opaque to both routers.

Only HTTP GET/POST request-response RPC is supported. WebSockets, subscriptions,
gRPC streaming, CONNECT and protocol upgrades are not supported. This is trusted
private RPC transport, not a public method-filtering firewall.

Limits are per chain, shared across its endpoint roles. Typical configuration
is 32 active + 32 queued. Queue time counts against the total
request deadline. Each layer bounds its own queue, so saturation can incur both
waits. Outer deadlines/caller cancellation abort inner work. Requests are capped
at the configured byte limit, responses at 64 MiB with bounded streaming memory,
headers at 16 KiB. Oversize/truncated responses abort rather than inventing a
successful RPC result. Size the timeouts for the real Bifrost client before use.

### Head Cache (adapter layer only)

`"head_cache_ms": N` on a chain (0-5000, default 0 = off) answers chain-head
queries from one provider call for up to N milliseconds:

```text
eth_blockNumber        []
eth_getBlockByNumber   ["latest"|"safe"|"finalized", false]
eth_getHeaderByNumber  ["latest"|"safe"|"finalized"]
```

Bifrost's EVM scanners issue about five of these for every block scanned (more
than half of all its requests). The caller sees a head at most N ms
older than a fresh call would give. Every other request, every batch, and any
head query the provider fails is forwarded untouched. Entries are per backend,
so a switch or reload never serves another provider's head. Concurrent identical
queries share one call. `rpc_router_head_cache_hits_total` counts requests
answered without a provider call; `rpc_router_requests_total` includes them.

A head entry that is still being asked for is replaced in the background after
half its life, so a polling scanner never waits a provider round trip for a
head. A header-by-height query whose height is exactly a head this cache handed
out recently is answered with that head reply; replies replaced by a refresh stay
answerable this way until they are twice `head_cache_ms` old, because the
scanner keeps using a head for about a second after fetching it.

Set it on P2 (the adapter), where chain-specific behaviour lives. Leave it unset
on P1, which stays a switch. The value can be changed by `reload`.

### Read-Ahead (adapter layer only)

`"read_ahead_blocks": N` (0-8, default 0, needs `head_cache_ms`) fetches the
calls a catching-up scanner is certain to make next. When the scanner asks for
block H (`eth_getBlockByNumber [H, true]`), the router immediately asks the
provider for H's receipts (`eth_getBlockReceipts [H]`) and for blocks H+1..H+N
and their receipts, and hands each answer over when the scanner asks.

- Only heights at or below the provider's `safe` head, learned from the
  scanner's own `safe` query. Above it everything is forwarded untouched.
- Each answer is the provider's own result for that exact call, handed out
  once, dropped after 5 seconds if unused. Null and failed early calls are never
  handed out.
- The provider sees the same calls it would have seen, slightly earlier.

`"read_ahead_headers": true` (default false, needs `read_ahead_blocks`) applies
the same rule to header-by-height queries (`eth_getBlockByNumber [H, false]`,
`eth_getHeaderByNumber [H]`): a query for H starts H+1..H+N. Bifrost's BASE
scanner can send two such queries per block (that height and a fixed distance
behind). They
remain real provider calls for the exact height, at or below the safe head, made
up to a few hundred milliseconds early. Leave it off if those queries must be
made at the moment the scanner asks.

Metrics: `rpc_router_read_ahead_fetches_total` (early provider calls) and
`rpc_router_read_ahead_hits_total` (early answers the scanner then asked for).
Both settings can be changed by `reload`.

## Call Fetch-Ahead

`call_ahead: true` (needs `head_cache_ms`; adapter layer only) is for the
bursts of height-pinned state queries a scanner makes one after another, the
EVM solvency report being the case it was built for: on the order of a hundred
`eth_call` requests plus one `eth_getBalance` per vault, taking tens of seconds
against a remote provider, during which the scanner holds back that block's
transactions.

- The router remembers the `eth_call` and `eth_getBalance` queries of the last
  run that were pinned to a canonical block height.
- When a query from that run arrives for a new height it sends the rest of the
  remembered queries for that height, twelve at a time, and answers the
  scanner's own requests from those replies for 30 s.
- Each early call resends the scanner's call object or address byte for byte
  with only the height replaced, so every answer is the provider's own result
  for that exact call.
- Queries at `latest`, `pending` or by hash are never learned or answered. An
  unrelated query at another height is forwarded and starts nothing, and a run
  only replaces the remembered one when it is at least half as long.
- A failed, null or error reply is never handed out; the scanner's request is
  forwarded.
- Reload-tunable. Early calls and hits count in `rpc_router_read_ahead_fetches_total`
  and `rpc_router_read_ahead_hits_total`.

## Private Administration

Use mode-0700 private subdirectories owned by the non-root service UID. Explicit
first-time initialization creates a missing state parent; normal startup never
reinitializes missing state. The admin listener creates its missing private
parent but rejects an existing non-private directory. Keep state on a persistent
local filesystem with working fsync/flock, not an ephemeral/shared NFS volume.
On Kubernetes, provision once with a separate Job, never an always-running init
container. Use `fsGroupChangePolicy: OnRootMismatch` so later mounts do not widen
the nested private directory permissions. Retain the PVC on restarts/upgrades.

```sh
# Offline validation only; does not contact providers.
./bifrost-rpc-router -config /etc/rpc-gateway/config.json -command check
# Once on first provisioning. Refuses to overwrite bad existing state.
./bifrost-rpc-router -config /etc/rpc-gateway/config.json -command init
./bifrost-rpc-router -config /etc/rpc-gateway/config.json

./bifrost-rpc-router -socket /run/rpc-gateway/admin/control.sock -command status
# Use the current version returned by status, not a guessed version.
./bifrost-rpc-router -socket /run/rpc-gateway/admin/control.sock \
  -command switch -chain base -backend native -revision 1
```

Switch back using the same command with `-backend adapter` and the new revision.
An admin response timeout is ambiguous: **read status**, do not blindly retry.
For P2, edit its private catalog/target files atomically, run `-command check`,
and send `-command reload -revision <current>` to **P2's** socket. Then select
the desired P2 backend. Reload intentionally does not probe or certify a
provider; verify its chain identity, head, common block and history off-path
before selecting it. An active target edit followed by reload changes that
provider immediately, so prefer adding a new named backend first.

The HTTP data port has no administration API. Private endpoints:
`GET /routes` returns choices/revision (no URLs); `POST /switch` takes
`{"chain":"base","backend":"native"}`; `POST /reload` takes `{}`. Mutations
require `If-Match: "<revision>"` including the quotes. Admin socket mode is 0600.

## Monitoring And Deployment Gate

`/healthz` is process liveness. `/readyz` means the routing state is loaded,
**not** that providers or Bifrost are healthy. Do not use it to
certify a switch of Bifrost onto the router. `/metrics` exposes revision, per-chain selected backend,
active/waiting counts, admission rejections, backend request/error counters and
latency sums. Counter series reset on catalog reload. JSON-RPC application
errors inside HTTP 200 are opaque; monitor Bifrost scanner/observer errors too.
Logs contain route transitions and classified transport failures, never target
URLs, credentials, payloads or raw provider errors.

The error counter is **not a count of failed logical RPC operations**.
`rpc_router_errors_total` counts transport/timeout/response-limit/body-read
failures, rejected redirects and upstream 429/502/503/504 only. Every upstream
4xx/5xx reply is
counted in `rpc_router_upstream_status_total{class}`, and a caller that
cancels or closes before HTTP EOF in `rpc_router_client_canceled_total`.
Correlate them with scanner progress, client errors and provider checks; do
not use zero errors as the sole acceptance gate.

Rollout checklist:

1. Inventory Bifrost's current endpoints; do not trust example defaults.
2. Populate and validate private configs.
3. Build/pin the image, verify CA bundle and digest, stage two independent
   single-replica services with retained state volumes, non-root/read-only root,
   dropped capabilities, no ServiceAccount token and bounded CPU/memory. Use
   Recreate for an exclusive state volume. Allow ingress only from Bifrost/P1
   and monitoring as appropriate. No public/host ports.
4. **Review egress explicitly:** if Bifrost's egress is routed differently
   from other pods, the proxy pods must follow the same path. Check each provider's
   network path and compare timings from the new pods' network namespace.
5. Probe chain identity, advancing fresh head, a common finalized block, actual
   scanner-height blocks and complete receipts/results. Test both sides of history
   boundaries and the 32-block workload. Use reads only, no mainnet test broadcasts.
6. Configure P2 while isolated or with the affected P1 chains on validated native
   routes. Verify P1->P2 and P1->native. Stop/restart P2 only after draining affected
   chains. Confirm metrics/alerts.
7. Only then make the initial stable Bifrost endpoint
   changes in a watched restart, retaining exact original endpoints for rollback.
   Future provider switches need no restart. Watch scanner progress/errors, observation/signing health, resource use and quota afterward.

If P1 is unavailable, rollback still requires restoring Bifrost's original
endpoints and a restart. The router does not eliminate that risk.
No cluster deployment manifest or Bifrost Secret patch is applied by these tools.

## Tests And Build

```sh
make test
make vet
make vuln
make benchmark
make binary
docker build --platform linux/amd64 -t bifrost-rpc-router:local .
```

Go 1.26.10, standard library only; uses `net/http/httputil.ReverseProxy` rather
than a custom HTTP implementation. Sources:
[ReverseProxy](https://pkg.go.dev/net/http/httputil#ReverseProxy),
[Transport retry behavior](https://pkg.go.dev/net/http#Transport).

Tests use synthetic loopback providers and no real transactions/credentials.
Coverage includes live keep-alive switching, old-request drain, atomic endpoint
groups, reload/restart durability, stale-command rejection, state fault handling,
HTTP/TLS failures, no broadcast or read replay, bounded admission, a stalled
request body and 32 concurrent requests through both layers. Both example files are loaded and validated. These mocks do not prove live chain
identity/history, signing or Kubernetes routing. Loopback overhead is not a
prediction of real provider latency.
