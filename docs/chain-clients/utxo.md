# UTXO Chain Clients

THORChain supports several UTXO-based chains: Bitcoin, Litecoin, Bitcoin Cash, Dogecoin, Zcash, and Dash. These chains share a common implementation based on `utxo/client.go`, which handles block scanning, mempool observations, signing, consolidation, and solvency reporting.

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
/bifrost/pkg/chainclients/utxo/dash.go
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

When Dash mempool scanning is enabled, it supplies early observations of vault
outbounds without requiring an InstantSend lock or ChainLock. These observations
require no additional confirmations, so an observation quorum can finalise the
outbound on THORChain and update vault accounting while the transaction is still
in Dash's mempool. A later ChainLock scan can report the mined transaction again;
THORChain does not wait for that report to finalise the earlier observation.

Customer deposits still require an eligible InstantSend lock or inclusion in a
ChainLocked block. The InstantSend path only observes transactions received by a
vault, including customer deposits, vault migrations and consolidation.
The signer skips its automatic observation after broadcast to avoid
a third observation variant when InstantSend or ChainLock finality is reported.
The first report waits for scanning and can be delayed by slow RPCs.
Disabling mempool scanning retains the existing signer auto-observe rules.

| Mimir                             | Units            | Default / unset                      | Effect                                                                                                                                                            |
| --------------------------------- | ---------------- | ------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `DashInstantSendEnabled`          | switch           | Bifrost config; see below            | Zero disables InstantSend observation, including vault senders. A positive value enables it.                                                                      |
| `DashInstantSendMaxAmount`        | duffs            | `0`                                  | Maximum outstanding customer InstantSend amount per node. Set `100000000000` for 1,000 DASH. Zero or a deleted value stops new customer InstantSend observations. |
| `DashInstantSendMinAmount`        | duffs            | `1000000` (0.01 DASH)                | Minimum customer deposit for InstantSend. Zero removes this extra minimum; the existing chain dust threshold still applies.                                       |
| `ObservationDelayFlexibilityDASH` | THORChain blocks | Global `ObservationDelayFlexibility` | Sets the late-observation slash-refund window and tally deadline for all DASH observation types. Zero is a valid override.                                        |

Both amount Mimirs use duffs, matching internal accounting: `100000000` duffs is
1 DASH (`common.One` in Go). This also supports amounts such as 0.01 DASH.

An unset or negative `DashInstantSendEnabled` restores Bifrost's startup
`instant_send.enabled` configuration, which defaults to `true` for DASH. Deleting
an override takes effect on the next settings refresh without a restart. A failed
read disables all InstantSend observations for that scan, including vault senders.
Ordinary vault-outbound observations and ChainLock settlement continue; existing
allowance reservations remain charged. The maximum mempool age policy still applies.
A failed read of either amount Mimir pauses customer admission until both reads
succeed. Lowering the limit below current usage leaves zero available; raising it
exposes the difference. Deleting either amount Mimir restores the default above.
Eligible vault-to-vault transfers, including migrations and consolidation,
bypass the allowance and minimum. They neither consume nor refill customer
allowance. Their InstantSend observations still require InstantSend to be enabled;
setting only the amount limit to zero does not disable them. Ordinary outbounds
to customers can finalise on THORChain through the early mempool observations
described above, before Dash ChainLock finality.
The allowance does not change signing or input selection.

InstantSend scans check transaction outputs before looking up locks or parent
transactions. Transactions with no vault output are cached in memory, up to
`mempool_tx_id_cache_size` entries; nonpositive values disable this cache. Entries
are removed when they leave the mempool, and the cache is cleared when the vault
address set changes. Lookup failures or uncertain scripts are not cached as
unrelated. This cache does not mark transactions as observed or reserve allowance.

For example, setting `DashInstantSendMaxAmount=1000000000` gives a 10 DASH allowance.
An early 7 DASH observation leaves 3 DASH.
A 4 DASH deposit waits for a ChainLock; a later 2 DASH deposit can still be reported
early. A new ChainLock restores the 7 DASH only if its block actually contains that
transaction. Merely advancing the height, losing the transaction from the mempool,
or reaching THORChain consensus does not restore capacity.

Reservations and pending observation markers are written together in one
synchronous database batch before delivery. A deferred deposit gets no marker,
so the ordinary ChainLock scanner can observe it later. Reservations
survive restarts and settings changes. Block scans settle raw transaction IDs before
parsing and duplicate filtering, including while InstantSend is disabled. On
restart, pending reservations are also checked for inclusion in canonical
ChainLocked blocks, covering blocks the scanner may have passed while the node
was offline.

Early InstantSend observation requires a persistent block-scanner database, including
for vault transfers. Without it, ordinary outbound and ChainLock scanning continue.
With no allowance journal (including an upgrade from the earlier client), the node
starts with no available allowance. Once active and caught up, it saves a finite
snapshot of transactions reported as InstantSend-locked (`instantlock: "true"`) in
its local mempool and classifies it in bounded batches. Entries with
`instantlock: "false"` are excluded.
An unknown, missing or unexpected lock status postpones the entire snapshot;
the next attempt reads a fresh mempool before saving anything.
It counts relevant customer deposits regardless of old observation markers or the
new age/minimum policy. Completion requires both full classification and the
completed scanner height passing the initial ChainLock height. Classified snapshot
deposits remain ChainLock-only and stay reserved until included in a ChainLocked
block, even if their total exceeds the configured limit.
Only the remaining allowance admits new deposits. An intact journal resumes instead
of taking a fresh snapshot; invalid journal data does not silently reset usage.

An unresolved snapshot entry can be dropped after a newer ChainLocked block has
been fully scanned. Its transaction lookup must start after that scan and return
the explicit transaction-not-found error (`-5`). Bifrost then verifies that the
scanner is healthy and caught up to a fresh ChainLock height, and confirms the ID
is absent from a freshly fetched mempool. Other RPC errors, including missing
parent transactions, leave the entry pending. A failed or null mempool response
also leaves it pending. Successful removals are logged after the database write.
This rule also applies to existing unfinished snapshots. The wait uses the saved
snapshot height, not a full elapsed block interval or a new wait after restart.
Known reservations and observation markers are preserved; recovery makes only the
unused allowance available.

This startup approach has an accepted gap: a previously credited transaction that
is absent from the local snapshot, or dropped by this recovery rule, cannot be
accounted for from that snapshot. A newer ChainLock and local mempool absence do
not prove that the transaction was never credited elsewhere. A new or recovered
node may therefore make fresh allowance available while that older exposure remains.
Persisting the database avoids losing known reservations, but a local snapshot is
not a complete network exposure history.

InstantSend admission atomically saves an allowance reservation and a pending
observation marker. The pending marker prevents another InstantSend claim, but
allows the later ChainLock block scan to observe the transaction. Observer's
per-item callbacks complete accepted markers in one synchronous write after the
batch's callbacks finish. Rejected items remain pending. A missing callback or
failed acknowledgement write also leaves the block fallback available, including
after restart. Only verified ChainLock releases the allowance; local rejection
cannot prove that other nodes have not already observed the deposit.

There is no separate delivery queue or retry worker. A lost InstantSend handoff
waits for the normal ChainLock block scan, even when InstantSend or mempool
scanning is disabled. Chain halts still control scanning normally. A completed
marker suppresses the later block observation. Legacy markers remain completed;
this change cannot recover observations already lost under those markers.
Vault senders remain exempt from the amount limits.

Observer acceptance and block claims update markers under the same storage lock.
If Observer stores a transaction before its acknowledgement completes, the block
scan can still emit a second observation at the actual mined height. Existing
consensus handling prevents another payment. This has an accepted bounded cost:
a vault transfer may already have ordinary mempool and InstantSend observations,
so the additional ChainLock variant can retain an extra `ObserveSlashPoints`
charge per affected inbound/outbound voter. The ordinary block path still marks
before delivery and retains its existing crash trade-off; this change restores
the block fallback without making all block delivery restart-safe.

For rollout, keep `DashInstantSendEnabled=0` while upgrading nodes. Configure a
positive pending-amount limit, the desired minimum and a DASH observation window,
then enable InstantSend after the upgrade. Allow time for startup warm-up. The
observation window is in **THORChain blocks**, not Dash blocks, and does not postpone
the initial missing-observation penalty or guarantee that every delayed observation
will arrive in time. Other chains keep the global window unless their own
`ObservationDelayFlexibility<CHAIN>` override is configured. The override applies
to inbound, outbound, network-fee, solvency and errata late-credit checks and tally
scheduling, preserving existing slashing exemptions. It does not apply to TSS votes.
For example, with a global window of 20 and a DASH window of 50, a matching DASH
observation 25 blocks after consensus gets
its slash points refunded and is credited when the tally is scored 50 blocks after consensus.
Configure the window before enabling InstantSend: queued tally deadlines do not
change when Mimir changes, while late-credit checks use the current setting.
Changing a window while tallies are pending can temporarily mix old and new
deadlines; the shared-outage guard assumes a consistent window for each chain.

Monitor `dash_instantsend_reserved_duffs`, `dash_instantsend_available_duffs`,
`dash_instantsend_pending_transactions`, `dash_instantsend_warming_up`, and
`dash_instantsend_deferred_total{reason=...}`. Amounts/counts are refreshed on mempool
scans; the deferral counter counts attempts, so repeated retries can count the same
deposit more than once. Investigate persistent warm-up or storage errors before
reenabling customer observations; do not delete the journal to clear reservations.

## Confirmation Counting

For UTXO chains other than Dash, THORChain dynamically delays finality based on
transaction value and miner incentives. Dash customer deposits instead require an
eligible InstantSend lock or inclusion in a ChainLocked block, without a value-based
confirmation delay. Vault outbounds can finalise on THORChain from early mempool
observations without either lock, as described in [Dash InstantSend allowance](#dash-instantsend-allowance).
Dash quotes use a conservative estimate of one inbound block; an eligible InstantSend
deposit can be observed sooner.

See [Finality & Confirmation Counting](../bifrost/how-bifrost-works.md#finality--pre-confirmation).

### UTXO Specifics

The value-based calculation for chains other than Dash uses:

- `txValue` is the sum of all vault-receiving outputs
- `blockReward` includes the subsidy + miner fees
- If no coinbase tx is found, defaults to 3.125 (for BTC). Rewards for each UTXO chain are defined `DefaultCoinbase()`.

## Gas Tracking

Fee is calculated as:

```go
Fee = total inputs - total outputs
```

Reported to THORChain as `sats/byte` (duffs per byte for Dash). Dash uses the scanned
block's average fee rate, applies the configured fee floors, and takes the maximum
over its `gas_cache_blocks` window (10 by default). Dash's fee-reporting path does
not read `gas_price_resolution`; changing it does not round or change the reported rate.

See [Gas Tracking](../bifrost/how-bifrost-works.md#gas-tracking).

---

## Vault Address Derivation

UTXO vaults derive addresses from compressed ECDSA public keys. The address type
depends on the chain: BTC and LTC use P2WPKH, while Dash uses P2PKH.

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

## Zcash Network Upgrades

Matching Zebra upgrade schedules are an operator requirement. Before the agreed ZEC signing height reaches `activation height - 40`, operators must ensure their daemons report the same schedule. Different schedules can produce different transaction expiries and prevent a signing party from forming, even before activation. Update comfortably before this boundary; identical Zebra versions are not required if their schedules match.

### Transparent transaction version switch (v5 → v6)

The vault signs fresh transparent outbounds as **v6 (ZIP-229)**, the standard format on mainnet since NU6.3. v5 and v6 produce **different** ZIP-244 signing digests (the version/version-group id is committed in the header digest and the empty-shielded digest uses v6 personalizations), so a fleet running a mix of v5-signing and v6-signing builds **cannot form a keysign party** for ZEC.

Any release that changes the signed transaction version is therefore a coordinated switch gated operationally by `HALTSIGNINGZEC`:

1. Set `HALTSIGNINGZEC=1` **before** any node upgrades.
2. Roll the new build out to the whole fleet.
3. Release the halt (`HALTSIGNINGZEC=0`) **only after the last node** is on the new build.

In-flight v5 checkpoints signed before the switch are still accepted and finished as v5 on resume (the version is pinned in the checkpoint, not re-chosen), but over a multi-block halt they will reach their `ZecTxExpiry` (40 blocks) and be retired and rebuilt as v6. The v5 resume support is a short-lived safety net, not something the rollout depends on.

## Related Pages

- [Chain Clients Overview](./README.md)
- [How Bifrost Works](../bifrost/how-bifrost-works.md)
- [Sending Transactions](../concepts/sending-transactions.md)
- [Memos](../concepts/memos.md)
