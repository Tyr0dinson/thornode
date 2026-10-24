# Standalone Chain Clients

Most chains inherit a shared implementation: UTXO chains wrap `utxo/`, EVM chains wrap `evm/`, and Cosmos-based BFT chains share a common client. A few chains share no such model and implement the [`ChainClient`](./README.md#chainclient-interface) interface directly, each with its own scanner, address handling, and signing.

THORChain runs four standalone clients: Solana, XRP, Tron, and Monero.

## Solana

Folder `solana/`, gas asset SOL. The client runs its own block scanner and shares neither the UTXO nor the EVM model.

A SOL deposit is a top-level System Program transfer to the vault, with the memo in a Memo program instruction in the same transaction. Other System Program transfers that touch no vault, such as a Jito tip or a frontend fee, are ignored. A transaction with more than one transfer from or to a vault is not observed.

The client supports native SOL and whitelisted SPL tokens of the classic SPL Token program. It scans each vault's associated token account for every whitelisted mint alongside the vault itself, and builds token outbounds as `CreateIdempotent` + `TransferChecked`. See [Token Support on Solana](./token-lists.md#token-support-on-solana).

## XRP

Folder `xrp/`, gas asset XRP. Memos are read from the XRP Ledger transaction's `Memos` field. If a transaction carries more than one memo, only the first is used.

The asset mapping acts as a whitelist. Only native XRP is mapped today; it can be extended to issued currencies.

## Tron

Folder `tron/`, gas asset TRX. The client supports native TRX and TRC-20 token transfers — it embeds the TRC-20 contract ABI and builds and decodes TRC-20 transactions directly. Signing uses a local secp256k1 key manager.

## Monero (XMR)

Folder `monero/`, gas asset XMR. Like every other chain client it is written in Go and handles observation, scanning, consolidation, signing, and solvency reporting.

What sets Monero apart is that this Go client hands the Monero-specific cryptography — FROST key generation and signing — and its wallet-scanning state to a local `xmr-frost-signer` **sidecar**: a separate process that runs alongside Bifrost on each node and answers a local HTTP API. The sidecar exists because that cryptography is built on the Rust [Serai](https://github.com/serai-dex/serai) libraries rather than reimplemented in Go. The sidecar is consumed as a pre-built Docker image from `registry.gitlab.com/thorchain/devops/serai/xmr-frost-signer`, pinned in `build/docker/docker-compose.yml` and overridable via `XMR_FROST_SIGNER_IMAGE`.

- Keygen uses a custom FROST DKG path with explicit finalize (`/v1/monero/dkg/finalize`) before vault mapping/key persistence.
- Outbound signing is a 3-step FROST API flow (`prepare` -> `partial` -> `combine`) with idempotent `msg_id`.
- Scanner sync is canonical-hash driven via `POST /v1/monero/scanner/sync`; `GET /v1/monero/transfers` is diagnostic/read-only.
- Inbound mempool attribution is intentionally unsupported; mempool queries are used for tracked outgoing visibility only.

For the full architecture and the sidecar API:

- [XMR Integration Design](./monero-design.md)
- [XMR TSS Signer API](https://gitlab.com/thorchain/thornode/-/blob/develop/bifrost/pkg/chainclients/monero/XMR_TSS_SIGNER_API.md)

## Related Pages

- [Chain Clients Overview](./README.md)
- [How Bifrost Works](../bifrost/how-bifrost-works.md)
- [Sending Transactions](../concepts/sending-transactions.md)
