# Funding checks for queued refunds

This Bifrost-only quick patch checks the original funding behind queued REFUND
outbounds. It is based on v3.21.0 and does not require the EVM or UTXO observation
recovery changes, their journals, canonical-view interfaces, or metadata schemas.
It does not change THORNode accounting or remove queued actions.
The observation fixes remain necessary before restoring full economic activity.
This refund-only check does not make the unfixed scanners safe.

## Coverage and rollout

The protected sources are ETH, AVAX, BSC, BASE, POL, BTC, BCH, LTC, DOGE and ZEC.
The source chain comes from the inbound voter, not from the refund's payout asset.
TRON is outside this patch. Unprotected vulnerable chains must remain halted
until their own protection is deployed. Ordinary swap OUT, migration, Ragnarok
and consolidation transactions are outside this guard.

The ETH, shared EVM and UTXO clients have compile-time checks for the funding
validator interface. A declared protected source with no loaded client/checker,
unavailable data or failed proof holds the refund. It never falls back to allow.
Deploy a consistent reviewed build and verify source RPC readiness before enabling
signing. Nodes with missing/pruned or different source data may hold refunds other
nodes can sign; mixed keysign batches and their blame outcomes require operational
attention. This patch makes no guarantee against keysign blame or jailing.

## What is checked

Before a fresh keysign and again before every broadcast, including saved signed
bytes, the signer reads a fresh inbound voter and verifies ownership of the refund
action. A reverted or incomplete external source cannot fund a refund. Native
THORChain deposits and consensus actions-only internal recovery swaps do not have
an external source transaction. The internal exemption requires an otherwise empty
source identity plus a finalised, matching action-owning voter.

The chain checker proves successful current canonical inclusion and compares the
transaction ID, sender, observed vault and deposited assets/amounts with the
credited inbound. Raw source memos and gas are excluded: THORNode can resolve or
rewrite a memo, and execution gas can change when a transaction is re-mined.
Receipt/block/transaction/log identity and actual execution effects remain bound.

EVM proofs can locate a deposit without a retained transaction-hash index. The
signer supplies at most 16 matching non-final voter heights; the shared EVM client
also tries one location derived from its current confirmation policy. These are
untrusted search hints, not proofs. A matching canonical block must contain the
exact transaction and supply its block receipts. Hash lookup remains a fallback
for re-mined deposits and missing hints. If ETH hash lookup is unavailable, the
checker additionally tries at most 64 block heights, F-2 through F-65, where F is
the signed finalise height. Missing hinted blocks do not suppress later hints.
Broken RPC responses stop the proof rather than widening the search.

ETH uses the block receipt set for sibling deposits when rebuilding whole-block
value, including when hash lookup located the funding transaction. Only an
explicit method-not-found response permits legacy per-hash receipt lookup;
missing, partial, duplicate or incorrectly bound consumed receipts hold the refund.

Router deposits with an uncached, unwhitelisted token retain the scanner's
exclusion of that event. Destination and memo compatibility is checked before
token resolution, in the same order as the scanner. Other metadata errors still
hold the proof. Previously cached tokens remain included and must match the
credited amounts. Existing default-decimals behavior is unchanged.

Retired original vaults are attributable only for this funding proof; ordinary
scanner membership is not changed.

The transaction's current mined occurrence must meet current confirmation policy.
ETH reconstructs the relevant source block's aggregate value using checked vault,
pool and Mimir snapshots. UTXO reconstructs all relevant deposits and the coinbase
reward, applies the multiplier/cap and configured minimum. Other EVM chains use
their chain policy; BASE uses its safe view outside mocknet, and AVAX accepts its
chain-reported block hash through the header index when geth omits extension fields.
This standalone patch cannot reuse historical confirmation floors that v3.21.0
never persisted; it does not import the later observation metadata for that purpose.

## Holds and retry ownership

The first check stays on the ordinary signing queue, retaining its original
height/index position while the proof runs. Only after failure can an authoritative
record with no signed bytes, checkpoint, terminal-round retry or observation yield.
Its existing retry-height field records the failed check and next background
eligibility, rather than changing its signing priority. Passing that height alone
does not return the item to signing.

One signer-owned worker sweeps failed unsigned records serially, then waits one
THORChain block interval before another sweep. Failures are eligible again from
the next THORChain height; a slow check does not spawn overlapping work. Success
clears only the marker under the same storage write lock as signing and removal,
provided the original record and marker still match and no signing evidence has
appeared. Removed items are not recreated and newer state is not overwritten.
The persisted marker restores retry ownership after restart; no separate queue,
positive-proof cache or new storage schema is needed.

A recovered item returns at its original height/index and gets fresh pre-sign and
pre-broadcast proofs. Expired unsigned attempts reenter the normal signing queue
for local attempt cleanup and THORChain rescheduling, without another source
proof. Signed bytes, either checkpoint, terminal rounds and saved observations
retain their original ordering; signed/terminal retries retain the existing
exclusive vault/chain selection.
A second check failing after signing never yields from the stale caller copy.
Saved signatures retry broadcast without re-signing. Checkpoints retain the
original signing parameters for another signing attempt.

Duplicate admission preserves existing records for every chain. Admission, signing
state writes, deferral and removal share the storage write lock. The transient
TxOutItem checkpoint representation is copied into its persisted checkpoint field
before storage. No hold deletes a queued obligation or marks it paid.

## Source history and bounded work

Block receipts bypass an expired hash index, not missing historical data. The BSC
launcher retains 90,000 blocks of transaction indexing. BASE full snapshots can
omit older transaction bodies and receipts. Operators must provide the history
needed by the oldest refunds: complete receipt/body data, and a mined-location hint
or hash index when bounded discovery cannot locate the source. An old finalized-only
ETH voter outside the search window still needs such history. A re-mined source
outside retained hints also needs discoverable current location. Missing data
always holds; changing a lookup method or flag does not restore omitted data.

Source requests use a 30-second proof context. Existing metadata requests use
their own contexts, and contextless THORChain policy HTTP requests can wait longer
on an unresponsive endpoint. Signer shutdown waits for the worker's active check
before closing its store. Fresh signing performs two proofs; saved-byte broadcast
retry performs one. Background recovery adds one proof without replacing either
signing boundary; no positive proof is retained across them.

UTXO filters dust, oversized memos and irrelevant outputs before parent work. It
reuses same-block parents and batches missing distinct first-input parents. A batch
contains at most 500 requests, or the smaller configured positive transaction
batch size, and honors the caller context. Every response ID, per-item result,
parent transaction ID and referenced output is checked. Each batch response is
limited to 32 MiB before decoding. An oversized multi-parent reply, or Zebra's
explicit top-level aggregate-capacity error, halves the proof-local batch size
and retries the same position; successful smaller batches
release full parent bodies before advancing. An oversized single parent or a
malformed reply holds the refund. ZEC batches use JSON-RPC 2.0 with numeric verbosity
for Zebra; other supported daemons retain their existing protocol conventions.
The implementation retains derived senders between batches, not every full fetched
parent body. Work is one full block, approximately seven fixed RPCs and ceil(P/B)
parent batch requests normally, for P missing distinct parents and batch size B;
oversized replies add a bounded sequence of size reductions. Memory is
proportional to block/candidate data plus the current bounded parent batch.

EVM discovery examines at most 17 hinted locations and, for ETH only when hash
lookup is unavailable, the additional 64-location window. ETH whole-block policy
loads one indexed receipt set rather than fetching each sibling by hash. Each
consumed receipt remains bound to the block and the final canonical header check.

Each background sweep materializes the signer store once, with its existing
sorting cost, and performs at most one check/write per due hold. Recovery latency
is the remaining serial proof work plus the interval between sweeps; checking
eligibility each block does not guarantee recovery in one block. One worker bounds
extra concurrency without a per-item worker pool or retry index.

Very large blocks or slow/pruned/inconsistent source RPCs can still keep a refund
held when proof cannot complete. Wholly unsigned holds yield to waiting work;
exposed retries retain serialization. No recovery journal, generation state or
accounting migration is added.
