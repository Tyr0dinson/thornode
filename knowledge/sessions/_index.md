# Session Index

## Current Work In Progress

- `make test-simulation` is still running under Go `1.24.6`; monitor `/tmp/test-simulation-official-2.log` until it either finishes successfully or surfaces a deterministic failure.
- `make test-simulation-cluster` is queued to start after the single-node run succeeds; inspect `/tmp/test-simulation-cluster.log` for the cluster-only `xmr-post-churn` path.
- Docker has adequate headroom after the disk expansion, but `docker system df` still shows about `19GB` of reclaimable build cache that can be pruned after the simulation runs if space becomes tight again.

## Recent Sessions

| Session | Focus | Summary | Path |
|---|---|---|---|
| 2026-03-24 #1 | XMR Simulation Stabilization | Retried transient wallet-load startup failures and pushed the official single-node simulation deep into the DAG under Go `1.24.6`. | `sessions/2026-03-24/session-1.md` |
