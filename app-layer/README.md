# APP LAYER

## Overview

The app-layer is based on COSM-WASM.

Contract code is written in rust, the compiled to WASM bytecode with a checksum.
The bytecode can be deployed on THORChain using nominated accounts.

## Raw Cosmos storage queries

Contracts can read a single key from any mounted persistent Cosmos SDK store
through the CosmWasm `CustomQuery` interface:

```json
{ "cosmos_kv": { "store": "thorchain", "key": "cG9vbC8vQlRDLkJUQw==" } }
```

This example reads the `pool//BTC.BTC` key. Both the request key and response
value are base64-encoded bytes. A successful response is `{"value":"<base64>"}`;
a missing key returns `{"value":null}`, and a stored empty value returns
`{"value":""}`. Values retain their native storage encoding, usually protobuf,
and contracts must decode them with the corresponding module's schema.

Reads use the current contract execution or query context and consume normal SDK
KV read gas in addition to CosmWasm query and serialization costs. They expose
stored data directly; they do not apply keeper defaults, derived calculations,
or trading and halt policy checks. Contracts must implement those checks when
needed. Native key layouts and value schemas can change during chain upgrades.

Requests are limited to 8,192 JSON bytes, keys to 1–4,096 decoded bytes, and
values to 1 MiB. Oversized values are rejected after the metered read. Unknown
stores, transient and memory stores, unknown fields, malformed requests and
trailing JSON are rejected. The binding supports point reads only, with no
writes or range queries.

Rejections reach contracts with fixed text. Any variant other than `cosmos_kv`
returns the `UnsupportedRequest` system error, and other failures return a query
error from the `cosmos_kv` codespace:

| Code | Text                                    |
| ---- | --------------------------------------- |
| 2    | `invalid cosmos_kv request`             |
| 3    | `cosmos_kv request exceeds 8192 bytes`  |
| 4    | `cosmos_kv requires a 1..4096 byte key` |
| 5    | `unknown persistent cosmos store`       |
| 6    | `cosmos_kv value exceeds 1 MiB`         |

## App Layer Process

### Create

Create new WASM contract here. Check out the docs.

### Compile

Run the following script to deploy. It should produce the WASM bytecode and checksum.

```text
//deployment script
```

### Audit

Get the code audited and submit into `/audits`

### STAGENET

Apply to have it deployed on THORChain Stagenet. Provide:

1. The checksum
2. The link to the audit and code
3. The nominated deployer address

Once approved, deploy on stagenet.

### Integration Testing

Deploy and link your app, conduct thorough testing.

### Mainnet

Once the process is complete, apply through THORChain's ADR governace to have the code approved for mainnet.
