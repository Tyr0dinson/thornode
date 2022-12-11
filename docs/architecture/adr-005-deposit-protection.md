# ADR 005: DEPOSIT PROTECTION

## Changelog

## Status

11 Dec 2022 - Proposed

## Context

THORChain pursues capital efficiency by re-using pool liquidity to support higher-level financial primitives. Liquidity synthetics emulate L1 assets by being 50% underwritten by the L1 itself, and the other 50% being RUNE, bonded together on a constant-product curve. Synths are allocated pool capital alongside LPs by using dynamic synth units. In the case that RUNE collapses against the L1 Asset, the synths are afforded more and more of the pool liquidity until reaching ~100% of pool liquidity. At this point, the entire pool underwrites the synth, and the LP capital share is ~0%. 

| Synth Proportion of Liquidity | Price Change to hit 0% LP Share |
| --- | --- |
| 15% | -98% | 
| 30% | -91% | 
| 50% | -75% |

> Synths are swapped to and from their L1 counterparts at a starting point of 1:1, minus a slip-based fee. This means even at ~100% of the pool, synths retain their 1:1 peg - the user chooses their fee based on how impatient they are. 

However the concern in question is respecting the deposits of LPs - they elect to provide liquidity on a constant-product curve; yet at some threshold on that curve through no fault of their own their LP share can go to 0. This ADR is to give LP's deposit protection so that their deposits are always no less than the curve - essentially countering the effects of Synths. This means LPs do not have to worry about the risks of Synths. The downside is capped - they get their LP share back at all times (minus earnt rewards).

> LPs can get their LP share + earnt rewards by adding-removing liquidity as often as they want (compounding yield into principle).

## Alternative Approaches

The alternative approach is to retain Impermanent Loss Protection, since it covers deposit based on original price. However the liability of this can be unbounded. Deposit Protection has 100x less liability to the protocol (based on current data). 

https://replit.com/@thorchain/THORChain-Liability-Tracker#index.js

## Decision

The decision on this should be set by a Temperature Check, then a Node Vote to turn on. 

Impermanent Loss Protection (ILP) is under active governance discussion and is likely to either be turned off or grandfathered. In either case, Deposit Protection should be queued in to become active shortly after ILP is disabled or grandfathered. 

## Detailed Design

Deposit Protection uses LUVI (liquidity unit value index), to compute an LP's capital value and is price independent. This means that it will always output the same value, no matter the price of the pool. Thus LUVI can be used to protect the deposit at all times on the curve.

```
LUVI = sqrt(rune_deposit_value * asset_deposit_value) / liquidity_units
```

THORNode V101 now computes LUVI for all LPs. 

https://thornode.ninerealms.com/thorchain/pool/btc.btc/liquidity_provider/thor15a4jcp8qtuy5k4f8qw2wntmfvxlxatg34lxpck

```
{
	"asset": "BTC.BTC",
	"rune_address": "thor15a4jcp8qtuy5k4f8qw2wntmfvxlxatg34lxpck",
	"asset_address": "bc1qw9htj84un6pr36py2008fg9h053we6dc092neg",
	"last_add_height": 5177624,
	"units": "992470314",
	"pending_rune": "0",
	"pending_asset": "0",
	"rune_deposit_value": "1204459917",
	"asset_deposit_value": "235095",
	"rune_redeem_value": "1905445202",
	"asset_redeem_value": "156815",
	"luvi_deposit_value": "1695509",
	"luvi_redeem_value": "1741704",
	"luvi_growth_pct": "0.027245505626923832"
}
```

When a member withdraws, compute:

```
if (luvi_redeem_value < luvi_deposit_value) {
    protection = (luvi_deposit_value - luvi_redeem_value) * rune_redeem_value * 2; //How much RUNE to subsidise
    let raw_cover = (height_now - lp.last_add_height) / blocks100; // Factor against 100days
    coverage = raw_cover > 1.0 ? 1.0 : raw_cover; // Don't mint more than 100%
    protection = coverage * protection; // Pay from the RESERVE
  }
```

This logic should replace or be in parallel at the same location as Impermanent Loss Protection code path. 
https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/withdraw.go#L112


## Consequences

All LPs are then reassured they will never claim less than what they should claim as an LP; no matter the amount of Synths in the pool, or price change. 




