# TRON Failover Proxy

Standalone proxy for Bifrost's TRON REST and finalized JSON-RPC endpoints. It
sits between Bifrost and two upstreams, a local TRON daemon and an external
provider, and moves between them on health checks so Bifrost does not need a
restart when the local daemon falls behind or stops.

Broadcasts and transaction builders are supported, and cross-namespace ingress
is restricted to Bifrost. Request admission queues healthy bursts instead of
rejecting Bifrost's block prefetch window: the default limit is 32 active
requests with 32 more waiting.

## Bulk Receipt Compatibility

`gettransactioninfobyblocknum` support for the dedicated-proxy layout. It is not
needed when TRON goes through the two-layer router in `../bifrost-rpc-router`,
whose generic forwarding already passes the endpoint and arrays; the safety
contracts below describe this proxy only.

`POST /wallet/gettransactioninfobyblocknum` accepts exactly `{"num":<height>}`,
with a nonnegative int64 JSON integer. Duplicate keys, case aliases, extra
fields, fractions, strings and null heights are rejected. Receipt arrays must
contain unique canonical lowercase 64-hex IDs and explicit matching
`blockNumber` integers; malformed JSON, duplicate keys and error envelopes are
rejected. Other receipt fields pass through unchanged, including failed
transaction execution results.

`block_floor` still controls historical block reads. Optional `receipt_floor`
(default zero, nonnegative) independently describes retained receipt history;
bulk requests below either floor go directly to a freshly healthy external
provider. Zero means no additional configured boundary, not proven coverage.
No floor is inferred from an empty response.

Valid empty and partial arrays pass through unchanged. They do **not** certify
archival completeness or transaction absence: Bifrost's batch resolver fetches
each missing ID individually. The proxy does not fabricate receipts, synthesize
empty success on errors, cache absence, or fetch an extra block for each batch.
Unavailable/invalid local batches get one request-scoped external attempt;
unhealthy external or invalid external results return 503 so Bifrost can fall
back to individual calls. Optional bulk failures alone never evict an admitted
healthy local daemon; independent health checks still enforce normal failover.
Individual-lookup, broadcast journal/retry and automatic failback policies are
unchanged, as are the 64 KiB request, 8 MiB response and 32-active/32-queued bounds.

## Safety Contract

- Explicit allowlist covers the REST methods Bifrost uses,
  `eth_call`, and `eth_blockNumber`. No JSON-RPC batches, notifications, arbitrary
  proxy destinations, redirects, or other broadcast methods.
- A single persistent routing mode covers REST and JSON-RPC. Except for the
  request-scoped optional bulk endpoint described above, local read transport,
  HTTP, malformed-data, missing-history, and health failures latch external mode.
  Blocks below the configured Lite floor bypass local directly. An empty or
  mismatched external block/individual-transaction lookup returns
  **503, never 404 or empty 200**.
  This preserves Bifrost's ambiguous signer-cache lookup behavior.
- Startup/restart defaults external. Local admission is manual, through a
  mode-0600 Unix socket, after fresh health checks. Recovery never switches back
  automatically. Already in-flight reads can finish on the earlier provider;
  routing is not an atomic snapshot across multiple Bifrost requests.
- Health checks pin the genesis of both REST and JSON-RPC endpoints, require
  REST heads no more than 30 seconds old, compare local/external head heights
  within five blocks, validate finalized RPC lag at 0-64 blocks, and compare a
  common finalized-height REST block ID when both providers validate. An
  already-admitted route remains local if its independent checks pass while
  external validation is unavailable. Confirmed disagreement or local failure
  still latches external. New local admission requires both healthy providers
  and common-block agreement; startup and failback policy are unchanged.
  During external loss, local service lacks fresh independent corroboration;
  external history/retries must fail closed until external validation recovers.
  These are provider consistency checks, not independent consensus proofs or
  cryptographic validation of block contents.
- `broadcast_enabled: false` disables both broadcasts and unsigned transaction
  builders. Set it to `true` for Bifrost broadcasts; signed
  payloads still pass the journal and retry checks below.

## Broadcast Journal

With broadcasts explicitly enabled, only one-contract TRX transfers and TRC20
contract calls in Bifrost's current format are supported. The parser rejects
unknown/duplicate/case-aliased fields and verifies JSON against canonical TRON
protobuf, the SHA256 transaction ID, address encodings, and signature length.
The daemon, not the proxy, verifies the ECDSA signature and execution validity.

The bbolt journal uses synchronous commits, an exclusive process lock, a network
binding, mode-0600 files, and an independent retained PVC. Original request bytes
are durable before dispatch; each attempt is committed before sending. Signed
payloads are never rebuilt, re-signed, or modified. Same-ID/different-signature
requests are rejected rather than replacing a pending journal entry.

Only a positive acknowledgement with the matching transaction ID, or a lookup
whose serialized raw transaction hashes to that ID, produces success. An
unverified `DUP_TRANSACTION_ERROR` becomes 503: Bifrost would otherwise treat
it as success. Acceptance does not mean confirmation or finality.

After an ambiguous attempt, the proxy reconciles by ID before resending. A retry
uses the original journaled bytes even when the caller changes JSON whitespace.
An ambiguous local broadcast's external retry is request-scoped:
it does not change the persistent route or evict a healthy admitted local daemon.
An unhealthy external provider is not contacted; an unconfirmed transaction still
returns 503. Genuine health/chain failures and operator rollback take precedence,
and completing a retry never re-admits local. Broadcast diagnostics retain
classified attempt/lookup/outcome information without raw responses or payloads.
There are at most two writes per request (local then external), and four total
attempts per transaction. Expired transactions and exhausted entries are only
reconciled, never resent. There is no unattended/background broadcaster.
Journal failures close admission; failure to commit an acknowledgement also
returns 503, allowing reconciliation after recovery. Disk loss, broken fsync
semantics, and host power-loss recovery are outside the process-crash test.

The journal does not deduplicate different transaction IDs representing the same
intent. It does not replace Bifrost's signing checkpoint or signer cache. It
retains all entries, with a 50,000-entry ceiling and no automatic pruning. Monitor
PVC capacity and plan audited archival before that ceiling; never delete an
ambiguous entry merely to resume service. Record-limit and disk failures stop
new dispatches instead of falling back to an unjournaled path.

## Request Admission

At most 32 requests actively read bodies or contact upstreams; up to 32 more
may wait, without parsed bodies or upstream response buffers. Waiting ends after
five seconds or caller/shutdown cancellation. A full queue or elapsed wait returns
503 with `Retry-After: 2`, before journal or upstream work. Permits are released on
every exit. This bounds work inside the handler, not TCP connections accepted by
Go's HTTP server; private ingress and existing header/read timeouts still apply.

The five-second wait fits within the server's ten-second read deadline and
Bifrost's default 30-second HTTP timeout. This absorbs the normal 32-block burst,
not unlimited overload or indefinitely slow providers. Health, readiness, metrics and the
private admin socket bypass the request queue. Queue rejection does not trigger
upstream failover or broadcast retries.

`prefetch_test.go` reproduces a 16-of-32 rejection and exercises 32
parallel block/serial-receipt streams plus head/RPC traffic through a real HTTP
listener in local, external and historical modes. A barrier test verifies all 32
requests reach upstream concurrently while a 33rd head request waits.
`admission_test.go` covers
queue overflow, timeouts, caller deadlines, cancellation/permit races, control
endpoint availability and no journal/body side effects for rejected waiters.

## Build And Test

```sh
make test
make binary
make image CA_BUNDLE=/path/to/verified/ca-certificates.crt
go test -run='^$' -fuzz=FuzzSigned -fuzztime=30s -parallel=4
```

The module and Makefile pin Go 1.26.10. `go.sum` pins dependency content. The
scratch image contains only the static Linux/amd64 binary and a CA bundle; the
final image digest binds both. Refresh and reverify the image when updating CAs
or dependencies. Fixtures are synthetic transactions built in the tests.
Mock broadcast tests use synthetic signatures and
controlled HTTP servers; no real keys or chain submissions are involved.
