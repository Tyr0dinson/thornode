# ADR 028: Bond Yield Claims and Auto-Claim

## Changelog

- 2026-05-11: Created

## Status

Proposed

## Context

Bond providers can accrue yield when node bond grows above provider principal.
Providers need a way to claim that yield manually or automatically. Operators
also need bounded accrual and a pause control for rollout and incident response.

## Decision

Add `MsgBondYield` for manual claims and auto-claim configuration. Track provider
principal, claimable yield, auto-claim basis points, and payout settings.

Claimable yield accrues during validator reward distribution and is capped by
`BondYieldClaimableBpsCap`. Claims are disabled by `PauseBondYield`.

Auto-claim runs after validator rewards and claims the configured share of
claimable yield.

## Implementation Details

Two payout integration options are supported.

### THORName Affiliate Collector

Provider state includes `PayoutAddress`.

An empty payout target pays RUNE to the provider. A THORChain address pays RUNE
to that address. Any non-address payout target must be a valid THORName with a
non-RUNE preferred asset, available pool, and alias for the preferred asset
chain. In that case claimed RUNE is moved into the affiliate collector and paid
by the existing affiliate collector rules.

### Direct Payout Asset

Provider state includes `PayoutAsset` and `PayoutAddress`.

RUNE payouts go directly to the provider or configured THORChain address.
Non-RUNE payouts first collect RUNE in a per-node/provider `BondYieldCollector`
and swap once the collector exceeds the outbound-fee threshold multiplied by
`BondYieldOutboundFeeMultiplier`.

## Consequences

### Positive

- Providers can realize bond yield without removing principal.
- Auto-claim lets providers realize yield without manual transactions.
- Non-RUNE payouts can be thresholded before swap execution.

### Negative

- Adds provider accounting fields and a new bond-yield message path.
- Non-RUNE payout depends on pool availability and payout configuration.

### Neutral

- Governance controls accrual cap and pause state.
