# Session Index

## Current Work In Progress

- Merge the recent TAO MR into `xmr-integration`; the request was made but interrupted before any local fetch or integration work started.
- Re-run `make test-simulation` and `make test-simulation-cluster` under Go `1.24.6` after the latest XMR sender-state, spendable-balance, keygen-result, and readiness-signal patches.
- Treat the full `x/thorchain` and full `xmr-frost-signer` suite failures as separate branch-wide cleanup unless they surface as direct blockers for the simulation reruns.

## Recent Sessions

| Session | Focus | Summary | Path |
|---|---|---|---|
| 2026-03-25 #1 | XMR Product Hardening and Readiness Signal | Replaced sentinel-driven XMR runtime logic with explicit sender-state modeling, aligned Monero balance checks to spendable funds, and added chain-level XMR readiness signaling. | `sessions/2026-03-25/session-1.md` |
| 2026-03-24 #1 | XMR Simulation Stabilization | Retried transient wallet-load startup failures and pushed the official single-node simulation deep into the DAG under Go `1.24.6`. | `sessions/2026-03-24/session-1.md` |
