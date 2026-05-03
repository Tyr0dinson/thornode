# ADR 027: App-Funded Min-Slip Switch Subsidy Vaults

## Changelog

- May 2026: Proposed

## Status

Proposed

## Context

THORChain uses minimum slip basis points (L1SlipMinBps, StableSlipMinBps, TradeAccountsSlipMinBps, SecuredAssetSlipMinBps, etc.) to preserve protocol income and prevent very small swaps from bypassing the minimum liquidity fee. This is generally correct for regular user swaps, but it can make fine-grained arbitrage unprofitable once the pool price is close to the external market price. As a result, pools can remain several basis points away from fair market price, making THORChain less competitive for downstream swap quotes.

The main goal of this ADR is to allow whitelisted apps to disable the minimum slip floor for specific arbitrage flows that keep THORChain pool prices more precise, while preserving or improving protocol income. Disabling the minimum slip floor does not make swaps free. The normal natural swap slip still applies.

Rujira's THORChain virtualization strategy is intended to virtualize THORChain base layer pools inside Rujira app-layer orderbooks. This canhelp app-layer liquidity arbitrage against base-layer pools, keep pool prices tighter, increase activity, and internalize more arbitrage revenue inside the THORChain/Rujira ecosystem.

Merge request !4731 proposed special handling for the Rujira virtualization contract. The original implementation introduced `WasmArbSlipMinBps`, defaulting to the current live L1 min-slip value of `10` bps, with node operators able to change it through operational Mimir. In follow-up discussion, Rujira indicated that it wants to modify this into a simple ON/OFF switch rather than a tunable bps override, and that change will probably be accepted. Under the binary model, the swap either uses the normal standard min-slip rule or uses no minimum slip floor and pays only natural swap slip. For eligible apps, the memo encoding should default to no-min-slip mode: omitted or `0` means no-min-slip, while `1` opts into the standard min-slip rule. This still raises a broader policy question: should any one app have privileged swap pricing, even when it shares revenue with THORChain?

This ADR generalizes the idea that originated in MR !4731. A Rujira-specific Mimir key is a narrow operational override. The proposed app-funded min-slip switch subsidy vaults turn the app-specific exception into a general mechanism: any approved app can use the no-min-slip mode only when it has prepaid the missing protocol income, and exhausted funding automatically falls back to normal pricing.

Rujira is expected to share 50% of its relevant app-layer revenue with THORChain. That makes preferential economic treatment understandable if THORChain is compensated. If viable for Rujira, part or all of that revenue-share flow could fund the subsidy vault directly, making the subsidy mechanism one of the accounting paths through which THORChain receives the agreed app-layer revenue share. However, other whitelisted apps or arbitrage systems could also share revenue or otherwise compensate THORChain for disabling the minimum slip floor. A general mechanism is preferable to a hardcoded Rujira-specific exception.

The design goals are:

1. Allow whitelisted apps to disable the minimum slip floor for arbitrage that tightens pool prices.
2. Keep the regular natural swap slip fee in place.
3. Preserve or improve THORChain protocol income when an app receives no-min-slip treatment.
4. Apply the same binary switch rules to any approved app, instead of creating a hardcoded app-specific exception.
5. Ensure missing minimum slip fee income is compensated by prepaid app funding before no-min-slip pricing is used.
6. Fall back to the normal standard min-slip rule when an app is not eligible, paused, or insufficiently funded.
7. Make subsidy usage and vault-balance accounting transparent to nodes, apps, and public dashboards.

## Decision

Introduce app-funded min-slip switch subsidy vaults.

Under this design, the standard min-slip rule remains the normal protocol rule for regular swaps. A whitelisted app defaults into no-min-slip mode for its eligible swaps, but the missing system income must be prepaid from that app's subsidy vault. No-min-slip mode disables only the minimum slip floor. It does not remove the natural swap slip produced by the swap calculation.

The mechanism has only two effective modes:

```text
normal mode      -> standard min-slip rule applies
no-min-slip mode -> minimum slip floor disabled, natural swap slip still applies
```

The mechanism is intended to preserve or improve THORChain system income compared with applying the normal standard min-slip rule.

### Non-Goals

This ADR does not change swap queue ordering or grant priority based on vault funding or app identity.

This ADR does not remove the regular natural swap slip or liquidity fee from swaps.

This ADR does not allow apps to choose an arbitrary intermediate minimum slip value such as 1 bps, 5 bps, or any other custom basis-point floor.

### Whitelisted Apps

An app is eligible only if it is whitelisted for no-min-slip subsidy usage. Whitelisting should include:

1. App identifier, defined as `MsgSwap.Tx.FromAddress`.
2. Whether the app is currently enabled or paused.
3. The app's subsidy vault balance accounting.

The initial Rujira virtualization contract may be the first whitelisted app, but the mechanism is not Rujira-specific. Any app that passes the relevant app-layer and economic review may be added.

### Min-Slip Switch Request

The min-slip request is binary. The app can either use the default no-min-slip mode or explicitly request the normal standard min-slip rule. There is no requested basis-point value.

The request can be represented as a boolean value in the swap memo:

```text
use_standard_min_slip = false -> no minimum slip floor, natural swap slip only
use_standard_min_slip = true  -> normal standard min-slip rule
```

If the request is omitted or set to `0`, and `MsgSwap.Tx.FromAddress` is a whitelisted subsidy app, the swap requests no-min-slip mode by default.

If `MsgSwap.Tx.FromAddress` is not a whitelisted subsidy app, the swap uses the normal standard min-slip rule regardless of the memo value.

If no-min-slip mode is requested, explicitly or by omission, but the app is paused, globally disabled, or unable to cover the required subsidy, the swap uses the normal standard min-slip rule.

### Subsidy Accounting

For each no-min-slip swap, THORChain calculates the system-income difference between the standard min-slip rule and the natural-swap-slip-only path:

```text
subsidy_needed = max(0, fee_with_standard_min_slip - fee_with_natural_swap_slip_only)
```

The accounting invariant is:

```text
system_income_under_standard_min_slip == actual_natural_swap_slip_income + app_subsidy
```

The subsidy is deducted from the app's vault at execution time. If the natural swap slip already produces at least as much fee income as the standard min-slip rule, the required subsidy is zero and no vault balance is spent.

The subsidy should be denominated in RUNE and routed to the same system-income path that would have received the missing liquidity fee.

The app cannot create debt. Subsidy usage is binary for each swap or sub-swap. If the vault can cover the full subsidy required for no-min-slip mode, no-min-slip mode is applied and the subsidy is deducted. If the vault cannot cover the full subsidy, no subsidy is used and the swap uses the normal standard min-slip rule.

This produces graceful degradation:

```text
eligible app + covered or zero subsidy      -> no-min-slip mode
insufficient vault for nonzero subsidy      -> normal standard min-slip rule
empty vault with nonzero subsidy required   -> normal standard min-slip rule
paused or disabled app                      -> normal standard min-slip rule
```

### Streaming Swaps and Dynamic Fallback

Subsidized streaming swaps should resolve subsidy coverage dynamically at each sub-swap execution.

If the app vault can no longer support no-min-slip mode, the stream should fall back to the normal standard min-slip rule for subsequent sub-swaps. When fallback begins, the implementation should recompute the remaining maximum stream quantity using the effective min-slip mode and shrink the remaining quantity if necessary.

Because min-slip is a minimum fee/slip floor, turning the standard min-slip rule back on can increase the minimum sub-swap size and reduce the maximum number of remaining sub-swaps.

This is preferred over reserving the full subsidy upfront because streaming swaps can partially fill, fail, refund, or encounter changed pool state before later sub-swaps execute.

The recomputed quantity should be monotonic for a given queued swap. If a vault is refilled later, an already-shrunk stream should not expand again. New swaps can benefit from the refilled vault. This avoids oscillation and queue gaming.

If the recomputed maximum quantity is less than or equal to the number of already executed sub-swaps, the stream should be completed according to the implementation's existing stream-settlement rules.

### Vault Funding

Apps may top up their subsidy vault at any time.

Funding should be explicit and attributable to the app. This can be done by a dedicated memo or app-layer message. The exact funding path is an implementation detail, but it must produce deterministic on-chain accounting.

Vault funding should be RUNE-only. RUNE-only funding avoids conversion timing, swap slippage, failed conversion, refund, and price-source questions.

For Rujira specifically, the subsidy vault may be funded partially or entirely from the agreed THORChain/Rujira revenue-share flow if that is a viable integration path. This ADR does not require that arrangement. It only allows the subsidy vault to serve as one possible destination for app-layer revenue share that is intended to compensate THORChain.

Vault funding is one-way in the first implementation. Apps cannot withdraw unused funds. Unused funds carry forward until they are spent on future no-min-slip subsidy usage. A withdrawal mechanism can be considered later.

### Eligibility Model

All whitelisted and enabled apps with sufficient subsidy coverage are eligible to use no-min-slip mode. Access is not winner-takes-all.

If future congestion or abuse requires stricter allocation, an auctioned access layer or other allocation mechanism can be added later by a separate ADR.

## Detailed Design

### State

The implementation should introduce state similar to:

```go
type AppMinSlipSwitchConfig struct {
    AppID   common.Address
    Enabled bool
}

type AppMinSlipSwitchVault struct {
    AppID              common.Address
    BalanceRune        cosmos.Uint
}
```

Exact names and storage layout are implementation details.

The app ID is `MsgSwap.Tx.FromAddress`. `MsgSwap.Signer` must not be used as the app identifier for observed external swaps because it represents the observing signer, not the app. For native or wasm-originated swaps, the contract or account that wants the subsidy must be the `MsgSwap.Tx.FromAddress`.

The implementation should also add an explicit swap-level field or equivalent queue state for:

```go
UseStandardMinSlip bool
```

The memo syntax below should be treated as the recommended protocol encoding. The parsed value must survive memo parsing, queue storage, and execution.

### Memo Syntax

The recommended swap memo syntax is a new optional positional field after the existing dex target limit field:

```text
SWAP:ASSET:DESTADDR:LIM/INTERVAL/QUANTITY:AFFILIATE:AFFILIATEBPS:DEXAGG:DEXTARGETADDR:DEXTARGETLIMIT:MINSLIP
```

`MINSLIP` is an optional boolean-like flag:

```text
omitted or 0 -> request no-min-slip mode
1            -> use normal standard min-slip rule
```

Examples:

```text
=:BTC.BTC:thor1...:0
=:BTC.BTC:thor1...:0::::::0
=:BTC.BTC:thor1...:0/1/10::::::0
=:ETH.ETH:thor1...:0::::::1
```

The first example omits `MINSLIP` and therefore requests no-min-slip mode for an eligible app. The second explicitly requests no-min-slip mode for a normal swap. The third explicitly requests no-min-slip mode for a streaming swap. The fourth explicitly uses the normal standard min-slip rule.

The app identifier is not included in the memo. It is always `MsgSwap.Tx.FromAddress`.

The parser must preserve empty intermediate fields so apps can provide `MINSLIP` without using affiliates or dex aggregator fields.

### Quote and Preflight Behavior

The existing swap quote endpoint should return the actual quoted fee and whether the standard min-slip rule is used for that quote. This lets apps know whether the quote is using no-min-slip mode or falling back to normal min-slip pricing.

The quote response should include:

1. The actual quoted liquidity fee.
2. Whether the standard min-slip rule is used.

The quote is advisory only. It must not reserve vault balance, and execution remains authoritative because pool state, queue state, vault balance, and Mimir configuration can change before the swap executes.

A separate read-only endpoint should return the current subsidy vault balance for an app. This allows apps and dashboards to inspect available subsidy funding independently of swap quotes.

### Swap Execution

At swap execution:

1. Resolve whether `MsgSwap.Tx.FromAddress` is a whitelisted subsidy app.
2. Determine the standard min-slip bps for the asset and swap type.
3. Read the use-standard-min-slip flag from the parsed swap message or queue item.
4. If the flag is true, use the normal standard min-slip rule.
5. If the flag is absent or false but the app is not enabled, use the normal standard min-slip rule.
6. If the flag is absent or false and the app is enabled, calculate the subsidy required to disable the minimum slip floor using the exact swap calculation path used for execution.
7. If the vault covers the subsidy, deduct it and execute with the minimum slip floor disabled.
8. If the vault cannot cover the full subsidy, use no subsidy and apply the normal standard min-slip rule.
9. For streaming swaps, recompute and shrink remaining quantity if fallback changes the effective min-slip mode.
10. Emit an event showing app ID, use-standard-min-slip flag value, applied mode, standard min-slip bps, subsidy used, vault balance, and fallback reason if any.

For double swaps, the single use-standard-min-slip flag applies to both legs. The implementation should calculate the subsidy for the complete swap path and use no-min-slip mode only when the flag is absent or false and the vault can cover the full required subsidy.

### Mimir Controls

Nodes should be able to control:

```text
MinSlipSwitchSubsidiesEnabled
MinSlipSwitchPause-{appID}
```

Exact Mimir names may vary.

Dynamic per-app Mimir keys such as `MinSlipSwitchPause-{appID}` require explicit validation beyond the existing generic Mimir key validation. Validation should ensure the app ID is valid and the value is within the expected range for the key.

### Observability

The chain should emit telemetry and events for:

1. Subsidy vault top-ups.
2. No-min-slip swaps.
3. Fallback to the normal standard min-slip rule when the vault cannot fully cover a subsidy.
4. Per-app subsidy used.

## Consequences

### Positive

1. Removes the need for a Rujira-specific hardcoded discount.
2. Lets Rujira receive preferential economics only when THORChain is compensated.
3. Allows other whitelisted apps to participate under the same rules.
4. Protects THORChain system income from lost minimum slip fee revenue.

### Negative

1. Adds new accounting state.
2. Requires careful fee-delta calculation so the subsidy matches the true difference between the standard min-slip rule and natural swap slip only.
3. Requires new memo parsing or app-layer message conventions.
4. Requires governance or operational review for app eligibility.
5. Requires apps to handle fallback from no-min-slip mode to the normal standard min-slip rule.

## Open Questions

1. Should unused vault balances ever be forfeited, or should the base design permanently carry them forward until spent?
2. If forfeiture is added, should it use a flat `SubsidyVaultForfeitBps`, or should it scale with utilization such as `EpochSubsidyUsed / EpochDepositedRune`?
3. Should the vault amount affect swap queue sorting? For example, should it become a tie-breaker before the current final lexical `TxID` tie-breaker takes over?

### Design Option: Epoch Forfeiture

An optional policy layer could forfeit some unused subsidy vault balance at epoch end. This is not part of the base design.

Adding this option would require additional state, events, and Mimir controls such as `SubsidyVaultForfeitBps`. Those are intentionally omitted from the base design sections above.

This would avoid the timing risk of a hard epoch reset. Apps would not be suddenly unfunded at the epoch boundary, but they would still pay a cost for reserving no-min-slip capacity that they did not use.

An alternative forfeiture formula could scale with vault utilization instead of applying a flat percentage to all remaining funds. For example, an app that used most of its available vault balance during the epoch could forfeit less unused balance than an app that heavily overfunded and barely used the subsidy.

## References

1. THORChain MR !4731: https://gitlab.com/thorchain/thornode/-/merge_requests/4731
2. Rujira THORChain swap contract: https://gitlab.com/thorchain/rujira/-/tree/main/contracts/rujira-thorchain-swap
3. ADR 020 App-layer: https://gitlab.com/whys1/thornode/-/blob/d0409bee3b8df11219241863bdff58976cb5b0f6/docs/architecture/adr-020-app-layer.md

