# ADR 031: HaltOutbound and Pausing the Churn on a Signing Halt

## Changelog

- 06/11/2026: Created

## Status

Proposed

## Context

`HALTSIGNING` and `HaltSigning<Chain>` are the operators' least disruptive emergency stop: Bifrost stops signing every outbound on the chain, so no value can leave. They have two problems.

1. They also stop vault-to-vault transfers. A churn that starts during a signing halt turns the Active vaults into Retiring vaults whose funds must move by signed `MIGRATE` outbounds. Those never sign, so funds sit in Retiring vaults, and the next churn is blocked (`ValidatorMgr.BeginBlock` skips rotation while a Retiring vault holds funds above dust) until the halt is lifted. The churn itself is never paused: only `HaltChurning` and a chain halt (`Halt<Chain>Chain`) gate it.
2. Operators have no switch for "stop money leaving the network, but keep the churn running". Churn is often what an incident needs, for example to rotate out compromised or misbehaving nodes, and a signing halt blocks it.

## Alternative Approaches

- **Ask operators to set `HaltChurning` with every signing halt.** This is a manual step that is easy to forget. `HaltSigning<Chain>` is also set automatically by the slasher and the vault-deficit path, with nobody around to add the second mimir.
- **Pause the churn when any chain an Active vault supports has a signing halt.** This is simpler and matches how `Halt<Chain>Chain` is checked. But a halt on a chain the vaults hold nothing on (a deprecated or drained chain, for example) would block validator rotation without protecting any funds.
- **Classify internal transfers by destination alone.** EVM unstuck/cancel transactions and the Solana keep-alive send to the vault itself, so "to an Asgard address" would let a cancel through as if it were a migration. Requiring the memo as well closes that gap.
- **Teach THORChain about HaltOutbound** (skip scheduling external outbounds). That would be a consensus change for no gain: Bifrost already gates every signature, and the queue keeps the outbounds for when the halt lifts.

## Decision

1. Add `HALTOUTBOUND` (global) and `HaltOutbound<Chain>`, read by Bifrost only. Under them Bifrost refuses every outbound except an internal transfer. An internal transfer is defined strictly and fails closed: every memo on the TxOutItem (`Memo`, and `OriginalMemo` when set) parses as `MIGRATE` or `CONSOLIDATE`, and `ToAddress` is the address on that chain of an Active or Retiring Asgard vault. Anything else is blocked, including an empty memo and any mimir or vault read error.
2. Pause the churn automatically while `HALTSIGNING` or `HaltSigning<Chain>` is active for a chain the churn would migrate. A chain counts when an Active Asgard vault holds a non-RUNE coin on it above dust (any amount of a non-gas token). Gas-asset dust is burned during migration rather than signed out, so it does not count. The pause applies only to starting a churn. A churn whose keygen is already scheduled completes as before.
3. `HaltOutbound` never pauses the churn.
4. The pause sits behind the constant-backed mimir `PauseChurnOnSigningHalt`. It defaults to `1` (on), because the pause must happen without anyone acting, and setting it to `0` turns it off. This departs from the usual "new behaviour defaults off" rule on purpose, and is the reason the key exists at all.

## Detailed Design

Bifrost (`bifrost/pkg/chainclients/shared/signing/outbound_halt.go`):

- `CheckOutboundHalt` mirrors `CheckSigningHalt`: global key first, then the per-chain key, active when `0 < value <= height`.
- `IsInternalTxOut` / `OutboundHaltBlocks` classify a TxOutItem against `GetAsgards()` (Active and Retiring vaults), deriving each vault address with `Vault.GetAddress(chain)`. This is the same derivation THORChain's migrate uses, so it covers secp256k1, EdDSA (SOL) and FROST (XMR) vaults. Monero addresses compare case-sensitively.
- Signing entry points:

| Path                              | Class                                      | Under HaltOutbound                                                              |
| --------------------------------- | ------------------------------------------ | ------------------------------------------------------------------------------- |
| Outbound queue (`signer/sign.go`) | per item                                   | only internal items sign, signed-bytes rebroadcasts included                    |
| UTXO consolidation                | internal (CONSOLIDATE to self)             | signs; the built item is still classified, so a wrong destination fails closed  |
| Monero consolidation              | internal (CONSOLIDATE to self)             | signs; classified the same way                                                  |
| Solana keep-alive                 | internal (CONSOLIDATE to self)             | signs, which keeps the scanner able to observe migrations                       |
| Solana durable-nonce provisioning | external (funds a non-vault nonce account) | blocked; outbounds fall back to the recent-blockhash path, so the churn is fine |
| EVM / ETH unstuck (cancel)        | the stuck outbound it replaces             | only a stuck migration is cancelled; a stuck external outbound is left alone    |

A blocked queue item is dropped from the signer the way it is under `HALTSIGNING`, and THORChain reschedules it after `SigningTransactionPeriod`. `LackSigning` reschedules without slash points, so nodes are not penalised for honouring the halt.

THORChain (`x/thorchain/manager_validator_current.go`):

- `churnMigrationChains` and `churnSigningHaltedChain` run in `ValidatorMgr.BeginBlock` after the chain-halt check and before the Retiring-vault check. They run only on churn ticks. They iterate slices, not maps, and read mimirs in a fixed order. This is BeginBlock work, so no transaction's `gas_used` changes.
- Telemetry: the reason `signing_halted` with a `chain` label on `thornode_churn_skipped`, and a new `thornode_chain_outbound_halted` gauge next to `signing_halted`.

Tests: unit tests for the classification and each signing path, and `test/regression/suites/mimir/halt-outbound-churn.yaml`. That suite shows the churn skipped under `HaltSigningBTC` and a keygen scheduled once it is lifted and `HaltOutboundBTC` is set instead.

## Consequences

### Positive

- Operators can stop external outbounds and keep rotating nodes.
- A signing halt, including an automatic one from the slasher or the deficit path, no longer starts a churn that strands funds in Retiring vaults.

### Negative

- A churn now waits while a signing halt is active on a funded chain. If the halt is left on by mistake, rotation stalls until someone lifts it or sets `PauseChurnOnSigningHalt` to `0`.
- Under `HaltOutbound` a stuck external EVM outbound is not cancelled. It may still confirm, as it would under `HALTSIGNING`.

### Neutral

- `HaltOutbound` is Bifrost-only, so nodes enforce it once they run a Bifrost that has it. Mixed fleets behave as with any Bifrost-only switch: keysign parties form only from nodes that agree to sign.

## References

- `docs/mimir.md`, `docs/concepts/network-halts.md`
