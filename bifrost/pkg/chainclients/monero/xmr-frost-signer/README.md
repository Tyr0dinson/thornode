# xmr-frost-signer

`xmr-frost-signer` is the THORNode Monero FROST sidecar.

The sidecar source now lives under `bifrost/pkg/chainclients/monero/xmr-frost-signer` so the
Monero Rust service remains colocated with the Go Monero chain client it serves.

## Provenance

This sidecar includes THORNode-specific service code plus Serai-derived Rust cryptography and
request dependencies.

- Upstream Serai source: <https://github.com/serai-dex/serai>
- Pinned upstream revision: `737dbcbaa78ab817cc1c435cb2b6c5d24d1c4391`
- Pinned Serai crates in `Cargo.toml`: `dkg-pedpop`, `dalek-ff-group`, `ciphersuite`, and the
  `simple-request` patch dependency

Keeping the upstream URL and exact git revision here makes the provenance auditable from both the
crate manifest and the documentation.

## Required Environment

- `XMR_FROST_CHAIN_NETWORK` is required and must be explicitly set to `mocknet` or `mainnet`.
- Vault mapping is committed by the DKG ceremony (round3). There is no standalone vault-registration API.

## Hard Cutover Storage Contract

This sidecar now runs in strict hard-cutover mode.

- Legacy runtime compatibility paths are removed.
- In-place data migration is unsupported.
- Existing sidecar runtime data from older schemas is unsupported.
- Operators must start from a clean sidecar data directory when upgrading to this cutover release.

## Daemon Endpoint Hardening

In `mainnet` mode, non-loopback Monero daemon endpoints are hardened:

- `https://` is required unless `XMR_FROST_DAEMON_DISABLE_TLS=true`.
- When TLS is enforced, non-loopback daemon URLs must include URL userinfo credentials
  (`user[:pass]@host`) so outbound RPC auth is explicit.
- `XMR_DAEMON_URL` accepts optional `/json_rpc` suffix and normalizes to a base endpoint.

The sidecar also enforces bounded daemon response parsing via
`XMR_FROST_DAEMON_MAX_RESPONSE_BYTES` (default `52428800` bytes).

Rate-limiter memory growth is bounded via
`XMR_FROST_RATE_LIMIT_MAX_TRACKED_IPS` (default `8192`, minimum `1`).

## Scan Freshness Guard

`sign_prepare` is blocked when vault scan state is stale relative to chain tip.

- `XMR_FROST_MAX_SIGN_SCAN_LAG_BLOCKS` controls maximum allowed lag (default `3`).
- Canonical block scan/reorg orchestration is driven by Bifrost through
  `POST /v1/monero/scanner/sync`.
- `GET /v1/monero/transfers` remains a read-only diagnostic endpoint.

## License

This sidecar is licensed under `AGPL-3.0-only` (see `LICENSE`).

## Mixed-License Note

THORNode contains components under different licenses. This sidecar is intentionally isolated as
its own AGPL-licensed service so distribution obligations are explicit for sidecar binaries and
source changes.
