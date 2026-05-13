# Monero

## Nodes

- Signing nodes: active nodes with keyshares to a vault; keygen, keysign, keyimage-gen, attest.
- Non-signing nodes: active nodes watch-only to a vault; attest.
- Standby nodes: standby nodes with keyshares to a vault; keysign.

## Scanner Modes

### Normal

Scanning the Monero daemon as blocks appear at tip, processing the active work stream.

Priority: keyimage-gen, keysign.

### Rescan

Rescanning all or some vaults from birthday, inhibited from the normal work stream, focused on scanner inventory re-sync.

Priority:

- Get birthdays, pubkeys, viewkey, and keyimages from THOR for all nodes.
- Collect inbounds and spent keyimages from each block.
- Commit scan batches to the sidecar; it persists outputs, scan metadata, applies known keyimages, and prunes old scan metadata.

## Scanner Inventory

- Pubkey and birthday.
- Outputs are unspent or spent, where spent means a spent keyimage was found on-chain.
- Scanner balance is total unspent outputs and is used for solvency.
- Txkeys are used to audit outgoing transactions against the THOR schedule, with slash if invalid.
- Txkeys are persisted as outbound audit inventory, but are not part of rescan mode.

## Solvency Checker

When the scanner is at tip and total unspent is less than THOR minus margin, slash.

## Txkey Validation

If non-signing nodes validate a txkey and find an incorrect amount, destination address, or excessive gas, slash.

## Gas

- Gas limit is how many units; gas rate is how expensive those units are; gas used is limit times rate.
- Gas budget is `12 * 10000 = 120000`.
- Every gas cycle, active nodes observe gas rate and update.
- Gas is not multiplied by input count.
- Gas is a ceiling; signers must use less than or equal to the ceiling.
- Consolidate can use whatever gas it needs.
- Final migrate sends the remainder into the vault.

## 10-Block Lock

If signers try to sign but outputs are immature, surface blocks remaining in the error log.

Signers then inhibit keysign rescheduling until enough Monero blocks have passed, stopping excessive re-attempts. Keyimage-gen and attention work-streams are not inhibited.

## Vaults

- Deterministic viewkey.
- FROST pubkey.
- Birthday, as Monero block height to scan from.
- Txkeys, one per outbound.
- Keyimages, one per inbound.

## Keygen

- Signing nodes produce keyshares.
- FROST keyshare is encrypted and attested to THOR, allowing emergency recovery, same as ECDSA and EDDSA.
- THOR stores FROST pubkey, FROST keyshare backup, and observed Monero height.
- Birthday is saved on-chain.

## Inbounds

- Observed: inbound is observed to the viewkey.
- Finalized: keyimage is produced and validated after 1 block, or conf-counting limit applies, so this can be 1 or more blocks up to max 30.
- Non-signing nodes wait.
- Signing nodes coordinate to produce the keyimage and signature.
- Keyimage and signature are propagated in the attestation; non-signing nodes verify the keyimage and signature, then attest.
- Observation must carry keyimage; signature is discarded. Thus keyimages on THOR are valid and final.
- Keyimages accrue in the vault.
- Keyimage ceremonies are top priority.

## Outbounds

- Observed: outbound is in the Monero mempool or on-chain with txkey.
- Finalized: keyimage for change output is produced and validated after 1 block. If there is no change output, then no keyimage is needed.
- Leader appointed, starts session.
- Joiners join, begin.
- Change is always returned to self for `OUT`, `REFUND`, `CONSOLIDATE`, and `MIGRATE`.
- Signers attest with txkey.
- Non-signers validate txkey and attest on first observation.
- Valid txkeys are persisted as outbound audit inventory.
- Signers wait one block, then see the inbound change.
- Inbound is processed. If coming from self, signers realize it is already observed and needs finalization, then kick off keyimage-gen.
- Non-signing nodes validate keyimage and attest.

## Change

- Monero outputs have a minimum of two outputs.
- All outbounds send change to self.
- If final migration sweep, or there is no change, the second output is a zero-value output to the same recipient.
- Zero-value outputs can be ignored for keyimage-gen.

## Consolidate

- Consolidate from 10 unlocked outputs down to 5.
- Second output is zero-amount and can be ignored for keyimage-gen.

## Zero-Output Change

Two cases:

- `CONSOLIDATE`.
- Final `MIGRATE`.

Ignore keyimage-gen for this output.

## Rescan Mode

Important for repairing inventory: inbounds, spent or unspent state, and total balance.

- Get viewkey, pubkeys, birthdays, and keyimages from THOR, saved per vault.
- Scan Monero daemon, finding inbounds and spent keyimages.
- Inbounds are spent or unspent, and contain hash plus amount.
- Keyimages are pending or applied to an inbound. If applied, the inbound is spent.
- Total balance can be validated on every node, signing or non-signing. Do not rely on scanner balance view; it will not be correct for watch-only vaults.
- Rescan does not replay txkeys.
- Scan batches apply known keyimages to found outputs.
- If a spent keyimage is found before the inbound, a later scan batch will apply it.

## Normal Mode

- No work stream if not yet at tip.
- At tip when scan for all vaults is at tip.
- Rescan finds inbounds and spent keyimages, then commits them through sidecar scan batch state.
- Signing nodes produce keyimages and signatures and attest. Non-signing nodes validate and attest just with keyimage and inbound.
- All nodes can cache keyimage signatures, but signatures are not attested to THOR. Finalization only happens if validated, so inbounds finalized on THOR means correct validation.

## Old Vault Refunds

- Active nodes continue to attest to inbounds to old vaults.
- If enough active nodes finalize an inbound, 67 percent of active, then it can be processed. Default is `REFUND`.
- If user did not add a refund address, the memo is empty.
- Empty memo automatically adds funds to vault accounting, not pool accounting.
- Some signing nodes will be standby signing nodes, and they need to join the keysign ceremony even if standby.
- `REFUND` needs txkey.
- Inbound to new vault needs to be handled.

## Reorg

- If Monero reorgs and an inbound or outbound disappears, nodes will see it and need to do `ERRATA`.
- `ERRATA` will undo the inbound or outbound and reverse accounting.
- Scanner stores block metadata and detects parent-hash mismatch.
- On reorg, scanner reconfirms affected txs, emits errata for missing txs, and rescans the reorg window.
