# BASE RPC setup for a validator's Bifrost

Tooling one THORChain validator runs between Bifrost and its BASE RPC provider.
Nothing here changes THORNode or Bifrost; it only changes how fast and how
reliably Bifrost's BASE scanner gets its answers. Shared so other operators can
reuse or adapt it.

Every endpoint, credential, address and hostname has been removed. The example
configs point at placeholder files; supply your own.

## Why

BASE produces a block every two seconds, and Bifrost's scanner makes many small
requests one after another for each of them. With a remote provider the scanner
can fall behind the tip, and a node that observes late collects slash points
for observations and solvency reports that reached consensus without it.
Asking the provider for things slightly before Bifrost does recovers most of
that time without touching Bifrost itself.

## Layout

```text
Bifrost ──► gateway (P1) ──► adapter (P2) ──► BASE provider
                        └──► BASE provider directly
```

- **P1, the gateway.** One stable BASE endpoint for Bifrost. It only picks
  which backend is used and can be switched at runtime, so changing provider
  needs no Bifrost restart. It never retries, fails over or caches.
- **P2, the adapter.** Holds everything provider-specific: admission limits,
  the head cache and the fetch-ahead options below.

Both are the same binary, `bifrost-rpc-router`, with different configs.

## What the adapter does for BASE

All of it is opt-in and off by default. Each option returns the provider's own
answer to the exact request Bifrost made; none of them invents, merges or
retries a result.

| Option | What it does |
|---|---|
| `head_cache_ms` | Serves chain-head queries from a short cache instead of one round trip per poll |
| `read_ahead_blocks` | Fetches the next blocks and their receipts below the safe head before the scanner asks |
| `read_ahead_headers` | Does the same for header-by-height queries |
| `call_ahead` | Remembers the run of height-pinned `eth_call` / `eth_getBalance` requests a solvency report makes, and sends the rest in parallel when the first one of the next run arrives |

`bifrost-rpc-router/README.md` has the rules and limits of each, and
`bifrost-rpc-router/examples/` has a gateway and an adapter config for BASE.

## Using it

1. Build the image in `bifrost-rpc-router/` (`make test`, then the image
   target) and import it into your cluster's container runtime.
2. Write your own gateway and adapter configs from the examples, and your own
   target files holding the provider URL and headers. Keep target files in
   Secrets; they never appear in logs or metrics.
3. Deploy the gateway and adapter with no Bifrost traffic, check BASE through
   both layers, then point Bifrost's BASE RPC setting at the gateway in one
   watched restart. Keep the original endpoint for rollback.
4. Turn the options on one at a time and compare scanner lag and slash points
   before and after.

## Caveats

- This is one operator's setup, not an audited product. Read the code before
  running it next to a validator.
- A misbehaving proxy affects every BASE observation. Each option can be turned
  off by a reload, with no Bifrost restart.
- The tests use synthetic providers. They do not prove behaviour against any
  particular real provider.
