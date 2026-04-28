# XMR TSS Signer API

The Monero sidecar exposes HTTP endpoints for DKG, signing rounds, broadcast, vault registration, scanner sync, and read APIs.

## Configuration

Bifrost (`bifrost.chains.xmr`):

- `signer_backend`: `tss`
- `signer_endpoint`: base URL for the signer service
- Auth credentials used by Bifrost when calling sidecar:
  - `authorization_bearer` (raw `Authorization` header token for XMR), or
  - `username` + `password` (HTTP Basic auth)

Sidecar (`xmr-frost-signer` environment):

- `XMR_FROST_CHAIN_NETWORK=mocknet|mainnet` (required; `mainnet` means real Monero mainnet)
- `XMR_DAEMON_URL` (required)
- `XMR_FROST_STORE_SECRET` (required; mainnet mode requires strong secret material)
- `XMR_FROST_AUTH_BEARER` or (`XMR_FROST_AUTH_USERNAME` + `XMR_FROST_AUTH_PASSWORD`) (required)
  - if `XMR_FROST_AUTH_BEARER` is unset, the sidecar falls back to `SIGNER_SEED_PHRASE`
  - the token is sent as the raw `Authorization` header value, not `Authorization: Bearer ...`
- `XMR_FROST_MAX_SIGN_SCAN_LAG_BLOCKS` (optional; default `3`)

`signer_endpoint` is required for the Monero FROST signer path.

Bifrost endpoint policy:

- non-loopback signer endpoints must use `https://`,
- non-loopback signer endpoints must include auth.

Sidecar policy:

- protected `/v1/monero/*` routes always require auth (no unauthenticated mode).

## Hard Cutover Contract

The XMR sidecar storage model is strict and breaking:

- Legacy runtime compatibility paths are removed.
- Runtime migration of persisted sidecar data is unsupported.
- Existing pre-cutover sidecar runtime data is unsupported.
- Operators must start from a clean sidecar data directory for this cutover.

## Signing Model

Signing is a 3-step FROST flow coordinated by Bifrost over libp2p:

1. `POST /v1/monero/sign/prepare`
2. `POST /v1/monero/sign/partial`
3. `POST /v1/monero/sign/combine`

The sidecar does not expose a unified multi-peer signing endpoint.

FROST DKG (keygen) is also coordinated by Bifrost over libp2p. Each node calls only
its local sidecar round endpoints while exchanging round payloads over p2p:

1. `POST /v1/monero/dkg/round1`
2. `POST /v1/monero/dkg/round2`
3. `POST /v1/monero/dkg/round3`
4. `POST /v1/monero/dkg/finalize`

## Vault Registration And Inventory

`POST /v1/monero/vaults/register` is the canonical path for loading vault metadata into the signer. Bifrost calls it after keygen and during state sync/rescan catch-up. It registers the vault pubkey, FROST group key, derived XMR address, creation height/birthday, and optional inventory.

Optional `key_images` and `tx_keys` are append/reconcile metadata. Omitting them preserves existing signer state; it does not clear previously registered inventory.

Key-image registration binds known output refs to their derived key images. It does not prove spend by itself. A spend is applied only when the scanner sees that key image in an on-chain transaction input, then calls the same key-image apply path with `spend=true`.

Tx-key registration validates the tx keys against daemon transaction data before persisting outbound audit metadata. The registered tx key is used to verify the outbound destination amount, fee, source vault, and presence of owned change. Change is identified so later key-image generation can cover the new vault-owned output; change audit is not part of the THORChain observation.

Rescan catch-up is Bifrost-driven:

1. Register vault metadata plus any Thorchain key-image and tx-key inventory.
2. Drive `scanner/sync` across canonical blocks to find inbounds and on-chain spent key images.
3. Apply matched spent key images to signer state.
4. Apply/validate registered tx keys for outbound audit state.

## Endpoints

### `POST /v1/monero/vaults/register`

Request:

```json
{
  "vault_pub_key": "thorpub1...",
  "group_key": "<32-byte-hex>",
  "address": "C...",
  "source": "bifrost",
  "spend_key_hex": "<optional-bootstrap-spend-key>",
  "birthday": 3652200,
  "key_images": [
    {
      "tx_hash": "<64-hex>",
      "output_index": 0,
      "key_image": "<64-hex>"
    }
  ],
  "tx_keys": [
    {
      "tx_hash": "<64-hex>",
      "tx_keys": ["<32-byte-hex>"],
      "from_address": "C...",
      "to_address": "C...",
      "amount": 1000000000000,
      "fee": 120000000,
      "memo": "OUT:..."
    }
  ]
}
```

Response:

```json
{
  "registered": true
}
```

The signer rejects vault registration when the supplied vault pubkey, group key, and address do not match each other.

### `POST /v1/monero/dkg/round1`

Request:

```json
{
  "vault_pub_key": "thorpub1...",
  "threshold": 2,
  "n": 3,
  "participant_index": 1,
  "context": "thorchain:XMR:...",
  "session_id": "dkg:..."
}
```

Response:

```json
{
  "commitments": "..."
}
```

### `POST /v1/monero/dkg/round2`

Request:

```json
{
  "session_id": "dkg:...",
  "all_commitments": {
    "1": "...",
    "2": "...",
    "3": "..."
  }
}
```

Response:

```json
{
  "encrypted_shares": {
    "1": "...",
    "2": "...",
    "3": "..."
  }
}
```

### `POST /v1/monero/dkg/round3`

Request:

```json
{
  "session_id": "dkg:...",
  "all_shares": {
    "1": "...",
    "2": "...",
    "3": "..."
  }
}
```

Response:

```json
{
  "address": "C...",
  "group_key": "..."
}
```

Successful round3 completion stages finalize payload only. Vault registration and key-share
persistence happen on explicit `dkg/finalize`.

### `POST /v1/monero/dkg/finalize`

Request:

```json
{
  "session_id": "dkg:...",
  "group_key": "...",
  "address": "C..."
}
```

Response:

```json
{
  "success": true,
  "key_bytes_hex": "<local-key-share>"
}
```

### `POST /v1/monero/sign/prepare`

Request:

```json
{
  "msg_id": "<idempotency-key>",
  "vault_pub_key": "thorpub1...",
  "to_address": "C...",
  "amount_atomic": 1000000000000,
  "max_gas_atomic": 100000000000,
  "sweep_if_final": true,
  "memo": "OUT:...",
  "fee_rate": 0,
  "decoy_context": "<deterministic-context>"
}
```

Response:

```json
{
  "session_id": "...",
  "preprocess": "..."
}
```

### `POST /v1/monero/sign/partial`

Request:

```json
{
  "msg_id": "<idempotency-key>",
  "session_id": "...",
  "all_preprocesses": {
    "1": "...",
    "2": "...",
    "3": "..."
  }
}
```

Response:

```json
{
  "signature_share": "..."
}
```

### `POST /v1/monero/sign/combine`

Request:

```json
{
  "msg_id": "<idempotency-key>",
  "session_id": "...",
  "all_shares": {
    "1": "...",
    "2": "...",
    "3": "..."
  }
}
```

Response:

```json
{
  "tx_hash": "...",
  "tx_hex": "...",
  "amount_atomic": 999000000000,
  "fee_atomic": 120000000,
  "tx_keys": ["..."]
}
```

### `POST /v1/monero/broadcast`

Request:

```json
{
  "msg_id": "<idempotency-key>",
  "tx_hex": "...",
  "vault_pub_key": "thorpub1...",
  "tx_hash": "..."
}
```

Response:

```json
{
  "tx_hash": "..."
}
```

`msg_id` and `vault_pub_key` are required for broadcast, and `msg_id` is required for sign endpoints. `msg_id` is used as the idempotency key.
Replay matching uses canonical payload hashing only.
`sign/prepare` additionally enforces scanner freshness and returns `503` when vault scan height lags behind chain tip beyond configured bounds.

### Scanner/Read endpoints

- `GET /v1/monero/balance/{vault_pub_key}`
- `POST /v1/monero/vaults/register`
- `GET /v1/monero/vaults`
- `POST /v1/monero/scanner/sync`
- `POST /v1/monero/outputs/apply-key-images`
- `GET /v1/monero/transfers?height={h}`
- `GET /v1/monero/transfers?mempool=true`
- `GET /v1/monero/addresses/{vault_pub_key}`
- `GET /v1/monero/outputs/{vault_pub_key}`
- `GET /v1/monero/fee-rate`
- `GET /v1/monero/chain/height`
- `GET /v1/monero/chain/block/{height}`
- `GET /v1/monero/chain/tx/{tx_hash}/exists`

Chain-query endpoint status semantics:

- `GET /v1/monero/chain/height`: `200`, `503`.
- `GET /v1/monero/chain/block/{height}`: `200`, `409` (height above tip), `503`.
- `GET /v1/monero/chain/tx/{tx_hash}/exists`: `200`, `400` (invalid hash), `503`, `500` (ambiguous daemon response, fail-closed).

`GET /v1/monero/outputs/{vault_pub_key}` supports optional lifecycle views:

- `view=spendable` (default): returns spendable outputs only (`state=unspent`).
- `view=all`: returns all tracked outputs (`state=unspent|reserved|spent`).

Example:

```text
GET /v1/monero/outputs/{vault_pub_key}?view=all
```

`GET /v1/monero/transfers` entries may include optional outbound attribution fields:

```json
{
  "from_address": "C...",
  "vault_pub_key": "thorpub1..."
}
```

`to_address` is strictly enforced as a valid Featured+Guaranteed Monero address.
Standard (`4...`/`8...`) and non-guaranteed featured destinations are rejected.

`POST /v1/monero/scanner/sync` is the canonical Bifrost-driven block scan API. Bifrost sends
the requested height, tip height, canonical block hash/parent hash, and reorg window. The sidecar
applies deterministic rollback/rescan for that exact height and returns `TransfersResponse`
(`incoming`/`outgoing`) for the synced height.

`POST /v1/monero/scanner/sync` status semantics:

- `200`: success
- `400`: malformed request (for example missing `block_hash`/`parent_hash`)
- `409`: canonical hash mismatch at requested height
- `503`: daemon/RPC unavailable
- `500`: internal persistence/scanner failure

Scanner confirmation/unlock math uses the sidecar-validated synced height (`height` and canonical
header) and ignores caller-provided `tip_height` for security hardening.

`POST /v1/monero/outputs/apply-key-images` accepts:

```json
{
  "vault_pub_key": "thorpub1...",
  "reconcile": true,
  "spend": false,
  "spent_height": 3652709,
  "key_images": [
    {
      "tx_hash": "<64-hex>",
      "output_index": 0,
      "key_image": "<64-hex>"
    }
  ]
}
```

Use `spend=false` when loading key images from Thorchain metadata or attestations. Use `spend=true` only for key images observed as spent by the on-chain scanner.

`GET /v1/monero/transfers` remains a read-only diagnostic query endpoint and is not the canonical
scan driver.

`GET /v1/monero/transfers?mempool=true` semantics:

- Returns `incoming: []` (inbound mempool attribution is intentionally unsupported for Monero).
- Returns only `outgoing` entries currently in daemon mempool, filtered by known outgoing tx records.

### Health

- `GET /health`

## Response style

Sidecar endpoints return direct JSON payloads. Bifrost client decoding also accepts an envelope
shape for compatibility with older deployments:

```json
{
  "result": { "...": "..." },
  "error": "",
  "message": ""
}
```
