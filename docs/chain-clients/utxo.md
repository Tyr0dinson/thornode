# UTXO Chain Clients

THORChain supports several UTXO-based chains: Bitcoin, Litecoin, Bitcoin Cash, Dogecoin, and Zcash. These chains share a common implementation based on `utxo/client.go`, which handles block scanning, mempool observations, signing, consolidation, and solvency reporting.

## Shared Architecture

UTXO chains use a shared client located at:

```text
/bifrost/pkg/chainclients/utxo/client.go
```

Each chain has a per-chain configuration file within the `utxo/` directory:

```text
/bifrost/pkg/chainclients/utxo/bitcoin.go
/bifrost/pkg/chainclients/utxo/litecoin.go
/bifrost/pkg/chainclients/utxo/bitcoincash.go
/bifrost/pkg/chainclients/utxo/dogecoin.go
/bifrost/pkg/chainclients/utxo/zcash.go
```

The shared logic handles:

- Block and mempool scanning
- UTXO input/output analysis
- Re-org tracking (BlockCache)
- Child-pays-for-parent (CPFP) support
- Fee rate reporting (sats/byte)
- Transaction signing with `btcutil`
- Vault balance tracking and solvency reporting

## Inbound Observations

Inbound UTXOs are detected by scanning new blocks and mempool data. A transaction is valid for observation if:

- It has at least one output to an Asgard vault
- It includes an `OP_RETURN` output with a valid memo

The observer parses these and pushes a `TxIn` to THORChain once confirmed.

See [Inbound Transactions](../bifrost/how-bifrost-works.md#observing-inbound-transactions).
For wallet developers, see [Sending UTXO Transactions](../concepts/sending-transactions.md#utxo-chains) for required transaction structure, dust thresholds, OP_RETURN handling, and examples of short and long memos.

### Dash InstantSend allowance

Dash normally observes deposits from ChainLocked blocks. When InstantSend is enabled,
each Bifrost can report eligible deposits sooner, subject to a persistent allowance
shared across all of its vaults. This limits the amount that node reports before
ChainLock settlement. It is a local soft limit, not a network-wide cap or a guarantee
against an InstantSend failure.

| Mimir | Units | Default / unset | Effect |
| --- | --- | --- | --- |
| `DashInstantSendMaxAmount` | duffs | `0` | Maximum outstanding customer InstantSend amount per node. Set `100000000000` for 1,000 DASH. Zero or a deleted value stops new customer InstantSend observations. |
| `DashInstantSendMinAmount` | duffs | `1000000` (0.01 DASH) | Minimum customer deposit for InstantSend. Zero removes this extra minimum; the existing chain dust threshold still applies. |
| `ObservationDelayFlexibilityDASH` | THORChain blocks | Global `ObservationDelayFlexibility` | Allows later matching transaction observations to recover slash points. Zero is a valid override. |

Both amount Mimirs use duffs, matching internal accounting: `100000000` duffs is
1 DASH (`common.One` in Go). This also supports amounts such as 0.01 DASH.

`DashInstantSendEnabled` and the existing maximum mempool age policy still apply.
A failed read of either amount Mimir pauses customer admission until both reads
succeed. Lowering the limit below current usage leaves zero available; raising it
exposes the difference. Deleting either amount Mimir restores the default above.
Vault senders bypass the new allowance and minimum, including vault migrations,
consolidation and outbounds. They neither consume nor refill customer allowance.
The allowance does not change signing or input selection.

For example, setting `DashInstantSendMaxAmount=1000000000` gives a 10 DASH allowance.
An early 7 DASH observation leaves 3 DASH.
A 4 DASH deposit waits for a ChainLock; a later 2 DASH deposit can still be reported
early. A new ChainLock restores the 7 DASH only if its block actually contains that
transaction. Merely advancing the height, losing the transaction from the mempool,
or reaching THORChain consensus does not restore capacity.

Reservations and final observation markers are written together in one synchronous
database batch before delivery. A deferred deposit gets no final marker, so the
ordinary ChainLock scanner can observe it later. Reservations survive restarts and
settings changes. Block scans settle raw transaction IDs before parsing and duplicate
filtering, including while InstantSend is disabled. On restart, pending reservations
are also checked for inclusion in canonical ChainLocked blocks, covering blocks
the scanner may have passed while the node was offline.

Customer InstantSend requires a persistent block-scanner database. With no journal
(including an upgrade from the earlier client), the node starts with no available
allowance. Once active and caught up, it saves a finite snapshot of potential
InstantSend transactions in its local mempool and classifies it in bounded batches.
It counts relevant customer deposits regardless of old observation markers or the
new age/minimum policy. RPC errors leave work pending for retry. Completion requires
both full classification and the completed scanner height passing the initial
ChainLock height. Snapshot deposits remain ChainLock-only and stay reserved until
included in a ChainLocked block, even if their total exceeds the configured limit.
Only the remaining allowance admits new deposits. An intact journal resumes instead
of taking a fresh snapshot; invalid journal data does not silently reset usage.

This startup approach has an accepted gap: a previously credited transaction that
is absent from the local snapshot cannot be reconstructed from it. A new or recovered
node may therefore make fresh allowance available while that older exposure remains.
Persisting the database avoids losing known reservations, but a local snapshot is
not a complete network exposure history. A crash between persisting a final marker
and handing the batch to Observer can still miss that node's observation; this
retains the existing preference for avoiding duplicate delivery.

For rollout, keep `DashInstantSendEnabled=0` while upgrading nodes. Configure a
positive pending-amount limit, the desired minimum and a DASH observation window,
then enable InstantSend after the upgrade. Allow time for startup warm-up. The
observation window is in **THORChain blocks**, not Dash blocks, and does not postpone
the initial missing-observation penalty or guarantee that every delayed observation
will arrive in time. Other chains keep the global window unless their own
`ObservationDelayFlexibility<CHAIN>` override is configured. The override applies
to transaction observations, not network-fee, solvency or TSS votes.

Monitor `dash_instantsend_reserved_duffs`, `dash_instantsend_available_duffs`,
`dash_instantsend_pending_transactions`, `dash_instantsend_warming_up`, and
`dash_instantsend_deferred_total{reason=...}`. Amounts/counts are refreshed on mempool
scans; the deferral counter counts attempts, so repeated retries can count the same
deposit more than once. Investigate persistent warm-up or storage errors before
reenabling customer observations; do not delete the journal to clear reservations.

## Confirmation Counting

THORChain dynamically delays finality based on transaction value and miner incentives.

See [Finality & Confirmation Counting](../bifrost/how-bifrost-works.md#finality--pre-confirmation).

### UTXO Specifics

- `txValue` is the sum of all vault-receiving outputs
- `blockReward` includes the subsidy + miner fees
- If no coinbase tx is found, defaults to 3.125 (for BTC). Rewards for each UTXO chain are defined `DefaultCoinbase()`.

## Gas Tracking

Fee is calculated as:

```go
Fee = total inputs - total outputs
```

Reported to THORChain as `sats/byte` based on the previous block and max of last 20.

See [Gas Tracking](../bifrost/how-bifrost-works.md#gas-tracking).

---

## Vault Address Derivation

UTXO vaults use compressed ECDSA public keys to derive P2WPKH addresses.

```go
btcec.PublicKey.SerializeCompressed()
```

## Reorg Handling

UTXO clients cache the last `BlockCacheSize = 144` blocks. When a block reappears at an existing height, previously observed transactions are checked. If missing, they are re-orged and an `ErrataTx` is submitted to revert state.

See [Re-orgs & Errata](../bifrost/how-bifrost-works.md#re-org-detection).

## UTXO Consolidation

Bitcoin and similar chains limit unconfirmed transaction ancestry to \~20 ancestors. Additionally, signing large transactions with many inputs leads to long, resource-heavy ceremonies. While TSS signing is parallelized, this can still create bottlenecks.

To manage this:

- **Trigger**: Consolidation is initiated when a vault holds more than `MaxUTXOsToSpend` UTXOs. This threshold is chain-specific and controlled via Mimir.
- **Frequency**: Evaluated once per block, but only if no consolidation is already pending.
- **Process**: Bifrost generates a consolidation transaction that sends funds from the vault back to itself with memo `consolidate`.

THORChain validates these transactions using the `MsgConsolidate` handler, ensuring:

- Inputs belong to the vault
- Output address matches the vault
- Memo is correct

If validation fails, the vault is slashed for the attempted consolidation amount.

Consolidation is **initiated by Bifrost** but **validated by THORChain**. This logic ensures inputs are managed proactively to prevent large, unmanageable signing payloads.

## RBF Handling

Replace-by-fee (RBF) is supported for user-initiated deposits to Asgard vaults. This allows users to bump the fee on a stuck transaction by rebroadcasting it with a higher fee. Vault-generated transactions do not use RBF. Instead, fee bumping (if required) is handled via other mechanisms such as child-pays-for-parent (CPFP).

## Related Pages

- [Chain Clients Overview](./README.md)
- [How Bifrost Works](../bifrost/how-bifrost-works.md)
- [Sending Transactions](../concepts/sending-transactions.md)
- [Memos](../concepts/memos.md)
