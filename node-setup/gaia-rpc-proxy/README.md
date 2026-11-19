# GAIA Historical RPC Proxy

Standalone, read-only Cosmos Hub RPC service. It is not part of the TRON proxy
and shares no journal, routing mode, deployment, credentials or request pool
with it. The implementation uses only the Go standard library.

## Routing

- Primary RPC handles status, latest blocks, and retained historical heights.
  Successful latest-block responses are shared for at most one second from
  fetch start, with each caller's JSON-RPC ID preserved. Expired entries and
  errors are never served from cache.
- Missing-history errors trigger verification of both providers' chain IDs,
  fresh heads, and a matching common block. This is a provider comparison, not
  a consensus light client.
- The primary's advertised earliest height is a hint. A bounded correction
  from exact, height-matched pruning errors establishes a boundary where both
  block bodies and transaction results are available and agree on tx count.
  An omitted, empty, or exactly `"0"` earliest height means the boundary is
  unspecified; it is not proof of archive availability. Current and requested
  block heights must still be positive, and malformed advertisements fail.
- Once verified, explicit heights below the shared boundary go directly to
  the archive, including transaction-result reads. Provider checks expire after
  five minutes and are refreshed on demand. Unknown boundaries retain a primary
  read plus explicit missing-history fallback.
- At or above the boundary, requests go to the primary. No Bifrost restart,
  manual mode change, or endpoint replacement is needed as it catches up.
- Generic errors, timeouts, malformed responses and future-height errors
  never become archive fallback or empty successful blocks. Archive errors
  do not disable valid current reads from the primary.

Provider retention can differ between methods or backend connections. A status
advertisement is not proof that every height is available. Keep the boundary
dynamic and monitor actual historical reads, not just the readiness endpoint.
Historical transaction results do not trigger an extra block fetch. A bounded
128-height metadata cache checks transaction counts when that block was already
read; results-first requests rely on normal RPC identity/height/shape checks.
Admitted historical reads retain their provider-check snapshot for their bounded
request lifetime rather than failing when another request refreshes it or its
timer expires mid-read. This is ordinary trusted-RPC operation, not per-block
cryptographic verification.

For an exact, height-matched archive pruning error only, the proxy allows four
fresh-connection retries. The original deadline, admission slot and response
validation still apply. Persistent pruning fails closed. Retry, recovery and
exhaustion counters and sanitized diagnostics distinguish this from
normal-provider fallback.

Only `status`, `block`, and `block_results` are accepted. JSON-RPC POSTs use
`/gaia` (optional trailing slash). GET equivalents are `/gaia/status`,
`/gaia/block?height=...` and `/gaia/block_results?height=...`.
Writes, batches, WebSockets, arbitrary methods and redirects are rejected.
Requests are limited to 64 KiB, responses to 16 MiB, with eight active requests
and sixteen waiting slots. Queue waits are capped at five seconds and the total
request deadline includes admission and provider checks.

## Configuration

```json
{
  "listen": "0.0.0.0:8080",
  "primary_rpc_file": "/etc/gaia-proxy/credentials/primary",
  "archive_rpc": "https://archive.example:443",
  "timeout_seconds": 15
}
```

Both upstreams must use HTTPS. Keep the primary provider URL in a restricted
Secret/file, never in source, command-line arguments or logs. The caller's own
deadline still applies; the Bifrost block reader has a 10-second deadline.
Validate cold-start and expired-proof prefetch latency before use.

Deploy in its own namespace with private ClusterIP service, scoped ingress from
Bifrost and monitoring, DNS/HTTPS-only egress, non-root/read-only filesystem,
and independent CPU/memory limits. No persistent volume is required. The scratch image includes only the static
binary and a trusted CA bundle; verify and pin its Linux/amd64 manifest digest.

`/healthz` is process liveness. `/readyz` checks the primary's chain identity,
fresh head and sync status, not archive history availability. `/metrics` exposes
read/fallback/error/admission counters, head-cache hits/misses, provider-check
validity and the verified floor.
Also monitor Bifrost's GAIA scanner lag and historical-read failures.

Point only `BIFROST_CHAINS_GAIA_RPC_HOST` at the private service's `/gaia` URL.
Keep GAIA gRPC/TLS settings unchanged: signing and broadcasts stay on Bifrost's
normal gRPC provider, outside this proxy. The initial endpoint change requires
one controlled Bifrost restart. Preserve the original endpoint for rollback;
restore it and restart Bifrost before removing a proxy it still references.

## Verification

Run `make test` for race tests, vet and vulnerability scanning, then `make
binary` for the static Linux/amd64 build. Supply `build/ca-certificates.crt`
before building the image. Test live read-only history, the verified boundary,
and an eight-request prefetch burst before deployment. Never use broadcasts
as test traffic. Default tests use synthetic local HTTP servers only.
