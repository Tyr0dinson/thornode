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

Keep `DashInstantSendEnabled=0` pending resolution of the Dash daemon lock-loss
review. This development protocol requires a fresh test network and scanner
storage when replacing the earlier checkpoint/journal implementation.

When explicitly enabled, InstantSend lets a valid customer DASH swap enter the
swap queue before mining. A supermajority of active validators must attest the
same transaction. Core commits its pending entry, inbound voter, vault
credit and swap queue entry together. Non-swap memos, invalid swaps, inactive or
compromised vaults and incomplete certificates wait for ordinary ChainLock
observation.

The vault receives its economic credit immediately, allowing DASH-to-other-asset
swaps to execute. Core uses normal outbound selection, refunds, migration and
rescheduling. Bifrost can spend customer UTXOs only after a ChainLock covers their
block. A scheduled payment with insufficient eligible UTXOs retries normally;
longer delays use existing outbound rescheduling and any configured retry limit.
The pending ledger limits early exposure; it does not reserve spending funds.

Settlement requires a separate full supermajority certificate for the transaction
in a ChainLocked block. Core checks its identity, vault and amount, records the
block observation and removes its pending entry. This does not credit the vault,
execute the swap or reward observations twice. Early signatures and individual
legacy observations cannot release exposure. Disabling admission does not disable
settlement.

Ordinary solvency compares wallet funds against recorded vault funds minus pending
customer deposits that have not timed out. Once an entry is overdue, its funds
are no longer excused in either Bifrost or core solvency. The separate timeout
halt remains the reliable alarm: normal solvency also discounts pending outbounds,
including unsigned ones, which can hide a missing deposit.

Instead, BeginBlock checks the THORChain age of every pending credit, including
when admission is disabled. At age `DashInstantSendTimeout` (default 300 blocks),
core sets `HaltDASHTrading` and `HaltSigningDASH`. Invalid or unreadable ledger data
also sets those halts. The deadline uses the pending entry's immutable `height`, the THORChain block
that committed the credit. Outbound scheduling cannot reset it. Neither local
time nor Dash block progress controls it, so a stalled ChainLock cannot stop it.
Timeout does not prove loss, release allowance, or reverse a swap. It does not set
a scanner halt, so ChainLock settlement can still arrive.

Timeout halts require manual clearance, even after every overdue deposit settles
by ChainLock. Operators clear `HaltDASHTrading` and `HaltSigningDASH` through the
existing Mimir controls after resolving the cause. Clearing either halt while an
overdue entry remains causes the next check to halt again. Raising the timeout
does not remove an already recorded overdue cause. Existing active halts retain
their values; a timeout can bring a scheduled halt forward, and its earlier
schedule is not restored automatically.

Loss recovery is manual, as for other chains. Bifrost does not build conflict
proofs, archive deposits for rebroadcast, or trigger an InstantSend write-off.
Missing transactions, missing locks, RPC errors and elapsed time are not proof
of loss. Generic errata completes as a no-op for an InstantSend-credited inbound,
settling errata observation accounting without reversing funds;
there is no DASH-specific errata CLI or automatic financial correction.

A conflicting customer transaction cannot execute a second memo while input
ownership is retained, regardless of whether the ordinary observation or early
observation arrives first. A suppressed replacement is not automatically credited
to a vault or pool. Any actual DASH received through it must be reconciled during
manual recovery.

A confirmed loss requires a coordinated state repair, potentially through a
software-upgrade migration. The team must reconcile the vault and pool balances,
pending exposure and overdue status, input ownership, inbound voter, remaining
swap input, and any queued or already delivered outbound obligations. Compensation alone does not
clear the ledger or repair accounting. Until this is done, the pending deposit
continues to use allowance and may prevent the retiring vault from completing
migration. Ordinary swap expiry, refunds and outbound rescheduling still apply;
there is no recovery-specific queue freeze.

As part of that repair, set the permanent admission-disable flag and retain the
trading/signing halts. Preserve input ownership for the lost deposit so a later
replacement cannot execute another swap. The flag is checked before
`DashInstantSendEnabled`; Mimir cannot clear it. Operators decide when ordinary
ChainLocked DASH trading and signing can resume after all halt causes are
resolved. A later software upgrade would be needed to re-enable InstantSend.

The query `GET /thorchain/instantsend/DASH` returns the agreed DASH height as
`observation_height`. Early observations round it down to a multiple of 12
(height 1 is used before block 12). Core checks this format and the 24-block
acceptance window. Nodes can still split at a step boundary and fall back to
ChainLock settlement. Only network-fee quorum advances the agreed height. DASH reports network fees even when the fee is unchanged.
InstantSend deposits and unconfirmed internal transfers do
not advance this height. It is an observation anchor, not the deposit's mined
height; neither mempool receipt time nor the lock's `cycleHash` supplies it.

Core accepts an early observation only within 24 agreed DASH blocks of the current
height. The minimum accepted anchor is stored and cannot decrease, and the query
returns it as `status.min_height` for Bifrost's admission check. Ordinary deposits
and settled early deposits retain input ownership until 24 agreed DASH blocks
after credit or settlement, using at least the current agreed height even when
the transaction was mined earlier. BeginBlock prunes expired credits in batches
of at most 5,000 keys, never splitting a credit from its input mappings. Errors
retain the records for retry. Pending deposits and input records explicitly
retained by a manual repair do not expire. Retaining an input record without
a pending entry requires the permanent admission-disable flag on genesis import. Genesis preserves these records and
the minimum anchor; import rebuilds the derived expiry index. These schema changes
require fresh development state; reset development databases created with the
removed recovery schema.

| Mimir                       | Units            | Default / unset       | Effect                                                                                                  |
| --------------------------- | ---------------- | --------------------- | ------------------------------------------------------------------------------------------------------- |
| `DashInstantSendEnabled`    | boolean          | disabled              | Positive enables early admission; zero, unset or a failed read disables it.                             |
| `DashInstantSendMaxAmount`  | duffs            | disabled              | Network-wide outstanding exposure across all DASH vaults. Nonpositive disables new early credit.        |
| `DashInstantSendMinAmount`  | duffs            | 1,000,000 (0.01 DASH) | Minimum early deposit; zero removes this extra minimum.                                                 |
| `DashInstantSendMinFeeRate` | duffs/vB         | 8                     | Positive Bifrost override of `instant_send.min_fee_rate`; missing or invalid fee data defers admission. |
| `DashInstantSendTimeout`    | THORChain blocks | 300                   | At this age, halt DASH trading and signing. Nonpositive uses the default.                               |

Each vault can have at most 128 pending InstantSend deposits. This fixed limit bounds storage and processing work independently of the network amount allowance. ChainLock settlement frees a slot; changing the count limit requires a software upgrade.

For example, a network allowance of 10 DASH with a pending 7 DASH deposit leaves
3 DASH for new early deposits. A 4 DASH deposit waits for a ChainLock; a 2 DASH
deposit can still qualify. A matching ChainLock certificate frees that deposit's
exposure.
Elapsed time, restart, mempool disappearance or a higher block alone never frees
capacity.

Bifrost checks network policy, active-node status, scanner/ChainLock health, chain
halts, an eligible active vault, a swap memo type, a fresh transaction lock and the configured minimum base fee (8 duffs/vB
by default) before delivery.
The `dash_instantsend_min_fee_rate` metric reports the effective Bifrost fee floor.
The low-fee simulation temporarily sets this floor to 8 duffs/vB and restores the previous
effective policy when the scenario finishes or fails.
Every input must come from a canonical ChainLocked parent transaction; deposits
spending unconfirmed ancestors wait for normal ChainLock observation. Local
mempool receipt age is not an eligibility rule. There is no per-deposit fairness cap. Local checks are advisory;
core enforces limits atomically across concurrent validators. The local database
stores delivery markers; the consensus pending ledger owns financial exposure.
Mimir is the only enable switch; the ineffective local `instant_send.enabled`
setting has been removed. Early customer observations do not enter block metadata
until their ChainLock observation supplies the actual mined height.
Restart reads that ledger and rechecks current daemon lock status before admission.
An RPC error or a missing lock pauses the local admission attempt. A missing-lock
response never generates automatic errata.
A recovered daemon flag permits another admission check but never frees exposure.

Early delivery requires persistent scanner storage. Delivery markers are written
synchronously in a batch before returning observations. The accepted crash gap
between that write and Observer persistence can lose the early handoff; independent
ChainLock delivery remains eligible. Observer persistence acknowledges early
markers, and later block delivery closes them. A late acknowledgement cannot
reopen a completed marker. Raw transactions are read for admission checks but
are not archived or rebroadcast by this path.

The scanner checks outputs before fetching parents and caches unrelated
transactions only while the vault-address set remains unchanged. Work is bounded
by the existing mempool batch settings. Vault outbounds, migrations and
consolidations retain ordinary mempool observation and later block observation;
they do not consume customer InstantSend exposure. Unconfirmed internal transfers
do not move the agreed DASH observation anchor.

Customer UTXOs can fund signatures only when their block is covered by a verified
ChainLock, regardless of local InstantSend flags or a zero minimum-confirmations
setting. Existing self-change and vault-transfer exceptions remain, including
mempool chain limits. The signer verifies a stable chain snapshot around input
collection. Signing health also checks fresh finality data and the completed
scanner height, so a stalled ChainLock cannot leave signing or early admission
healthy indefinitely.

Monitor `dash_instantsend_exposure_duffs`, `dash_instantsend_available_duffs`,
`dash_instantsend_pending_transactions`, `dash_instantsend_paused` and
`dash_instantsend_deferred_total{reason="lost_lock"}`. Exposure gauges describe the
last network query, not local financial state. Core publishes
`thornode_dash_pending_count` and `thornode_dash_pending_amount` per vault, plus
`thornode_instantsend_oldest_age_blocks` for settlement monitoring and
`thornode_instantsend_prune_error` for failed input cleanup. DASH observations
exceeding 2,500 inputs are skipped with a warning and counted in
`dash_observation_skipped_total{reason="input_limit"}`; block scanning continues. Timeout halts use the existing trading/signing indicators.

## Dash solvency

DASH uses ordinary solvency reports. A dedicated worker polls every ten THORChain
block intervals, independently of scanner progress and InstantSend admission.
Local time schedules polling only; each report carries a verified positive
ChainLock height. There are no accounting rounds or checkpoint payloads.

Bifrost collects settled wallet balances and compares them with the same queried
vault balances minus pending customer deposits that are not overdue. Customer outputs count only at or below
the ChainLock; internal vault change retains its existing treatment. A changed tip
or ChainLock during collection invalidates the pass. If any vault is insolvent,
only insolvent vaults are reported. Solvent recovery reports require a complete
successful collection and a halted chain. One vault's RPC error does not stop
checks of the remaining vaults, but prevents an all-clear. Identical local reports
are suppressed until their height or balance changes.

Core applies the same allowance for pending deposits that are not overdue. DASH insolvency
keeps the existing minority threshold; recovery requires a supermajority. A solvent
vault can clear its own flag, but the chain remains halted while any other vault
on DASH has an insolvency flag, including a retired vault. This cross-vault
recovery guard applies only to DASH. Existing pending
outbound and gas adjustments remain part of the ordinary solvency calculation.

`GET /thorchain/instantsend/DASH` returns network limits, the positive observation
height and vault entries containing `state` (chain, pubkey and
pending IDs, amounts and immutable THORChain credit heights), gross `coins`,
and `eligible` (an active Asgard vault for DASH without a compromised flag). Pending exposure is the sum of those
amounts; it may exceed the remaining vault balance when spending precedes delivery
of a settlement certificate. Genesis export/import preserves the ledger and its
original inbound voters.

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

### Reusing InstantSend for another chain

InstantSend scanning and accounting use shared types and helpers. Explicit
`Equals(DASHChain)` checks currently enable these paths only for DASH. A verified
second integration can extend the relevant checks to `OneOf`. Reusing the
code still requires an actual chain integration: its RPC format, addresses,
transaction encoding, finality rules and configuration must be verified first.
Creating an accounting record or setting a Mimir does not enable a new chain.

Consensus exposure records use `(chain, vault)` keys. Each
`InstantSendState` also carries its chain, and Bifrost rejects records for another
chain. The query is `GET /thorchain/instantsend/{chain}`. Genesis uses
`instant_send` records containing `chain`; the same vault can appear once per
chain. Mimir keys keep the chain prefix (`DashInstantSend...` for DASH), and metrics
have separate collectors per chain. Amounts are reported in duffs for DASH.
Bifrost's delivery markers and caches belong to one client and one chain database;
the client rejects a mismatched scanner ChainID before opening storage.

These names and storage keys are being finalized before production rollout.
The earlier development-only `dash_instant_send` genesis field and vault-only
accounting keys are not migrated automatically. Do not reuse that prototype
accounting state or mix the two protocol versions on a running test network.
Reset prototype network state and local scanner databases when switching from the
checkpoint schema. A supported export/import of the current schema preserves
pending exposure and its original credits; it does not release exposure.

The existing THORChain inbound voter store is still keyed by transaction ID
without a chain prefix. This predates InstantSend. InstantSend verifies a voter's
chain before using its credit, but this does not make the wider voter store
independent when identical transaction IDs are replayed across compatible chains.
A future integration must assess that existing limitation separately.
