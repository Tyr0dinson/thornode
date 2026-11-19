# Validator RPC setup

Tooling one THORChain validator runs between Bifrost and its chain RPC
providers. Nothing here changes THORNode or Bifrost; it only changes how fast
and how reliably Bifrost gets its answers. Shared so other operators can reuse
or adapt it.

Every endpoint, credential, address and hostname has been removed. The example
configs point at placeholder files and `*.invalid` / `*.example` hosts; supply
your own.

## Why

Bifrost's scanners make many small requests one after another. When a provider
is 100 ms or more away, a scanner can fall a few blocks behind the tip, and a
node that observes late collects slash points for observations, network fees
and solvency reports that reached consensus without it. Moving work closer to
Bifrost, and asking for things slightly before Bifrost does, recovers most of
that time without touching Bifrost itself.

## Layout

```text
Bifrost ──► gateway (P1) ──► adapter-<chain> (P2) ──► provider
                        └──► provider directly
                        └──► tron-rpc-proxy ──► local TRON daemon | provider
                        └──► gaia-rpc-proxy ──► local GAIA | archive provider
```

- **P1, the gateway.** One stable endpoint per chain for Bifrost. It only
  picks which backend a chain uses, and can be switched at runtime, so changing
  provider needs no Bifrost restart. It never retries, fails over or caches.
- **P2, one adapter per chain.** Holds everything provider- or chain-specific:
  admission limits, the head cache, and the fetch-ahead options below. One pod
  per chain, so a restart or a bad config affects one chain only.
- **TRON and GAIA proxies.** Chains with their own rules (a local daemon with
  an external fallback; historical queries a pruned node cannot answer).

P1 and P2 are the same binary, `bifrost-rpc-router`, with different configs.

## What the adapter layer does

All of it is opt-in per chain and off by default. Each option returns the
provider's own answer to the exact request Bifrost made; none of them invents,
merges or retries a result.

| Option | Chains | What it does |
|---|---|---|
| `head_cache_ms` | EVM | Serves the latest block number from a short cache instead of one round trip per poll |
| `read_ahead_blocks` | EVM | Fetches the next blocks and receipts below the safe head before the scanner asks |
| `tip_ahead` | EVM | Keeps the head current without waiting for the cache to expire, and fetches the new tip's data early |
| `utxo_ahead_ms` | BTC-style RPC | Fetches the next block and its per-block fee calls ahead |
| `call_ahead` | EVM | Remembers the run of height-pinned `eth_call` / `eth_getBalance` requests a solvency report makes, and sends the rest in parallel when the first one of the next run arrives |
| `hold_blocks` | EVM, fast finality | Shows the scanner a block that touches a vault only once that many later blocks exist, so it sends one final observation instead of an early one that never reaches consensus (needs `tip_ahead` and `call_ahead`) |

The adapter also answers batches of header queries element by element from the
same caches, and stops fetching receipts early while the scanner is not asking
for them.

`bifrost-rpc-router/README.md` has the rules and limits of each.

## Contents

| Path | What |
|---|---|
| `bifrost-rpc-router/` | The P1/P2 router: Go, standard library only, with tests, a reproducible image build and `deploy.yaml`, a Kubernetes template for the gateway and one adapter |
| `tron-rpc-proxy/` | TRON failover proxy with a broadcast journal; `deploy.yaml` is a template |
| `gaia-rpc-proxy/` | GAIA proxy that sends historical queries to an archive endpoint |
| `tools/p2-split/` | Scripts that move a setup with one consolidated adapter to one adapter Deployment per chain, and give the gateway a backend for each |

## Using it

1. Build the images from each directory (`make test`, then the image target)
   and import them into your cluster's container runtime.
2. Write your own gateway and adapter configs from
   `bifrost-rpc-router/examples/`, and your own target files holding provider
   URLs and headers. Keep target files in Secrets; they never appear in logs
   or metrics.
3. Deploy the gateway and adapters with no Bifrost traffic, check each chain
   through both layers, then point Bifrost's chain RPC settings at the gateway
   in one watched restart. Keep the original endpoints for rollback.
4. Turn the fetch-ahead options on one chain at a time and compare scanner lag
   and slash points before and after.

Make sure the proxy pods use the same network path to providers as Bifrost: a
proxy that reaches its provider by a slower route costs more than it saves.

## Caveats

- This is one operator's setup, not an audited product. Read the code before
  running it next to a validator.
- A misbehaving proxy affects every observation on that chain. Each option was
  introduced on one chain first, with a rollback that needs no Bifrost restart.
- The tests use synthetic providers. They do not prove behaviour against any
  particular real provider.
