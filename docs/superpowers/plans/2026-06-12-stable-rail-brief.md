# Stable Rail (STAR) Brief

Stable Rail (STAR) is a dedicated THORChain route for allowlisted L1
stablecoins.

It is built for stable-to-stable movement:

- same symbol across chains, like `ETH.USDC -> BASE.USDC`;
- different symbols, like `ETH.USDC -> ETH.USDT`;
- one inbound;
- one-block THORChain settlement when STAR is selected;
- one outbound;
- no normal CLP slip on the STAR fill.

The user-facing promise is simple:

- **same asset:** 1:1 target;
- **different assets:** oracle price ratio target;
- **normal mode:** almost free when pool/oracle pricing and fees allow it;
- **subsidy mode:** free when the STAR fund pays the gap;
- **fallback:** CLP/streaming still works when STAR is not better or not safe.

STAR is not a replacement for CLP. It is an extra rail. The quote engine can
compare CLP/streaming and STAR, then return the better safe quote.

## At A Glance

### What STAR Does

- Dedicated stable-to-stable rail.
- Same-symbol swaps target 1:1.
- Different-symbol swaps target the oracle price ratio.
- One inbound, one-block THORChain settlement, one outbound.
- No normal CLP slip on the STAR fill.
- Uses reserved protocol-owned liquidity.
- Best-quote routing compares STAR against CLP/streaming.
- Wallets use the normal swap memo.

### Why STAR

- Makes stablecoin movement faster, cheaper, and more predictable.
- Creates a simple product wedge: free or near-free stable swaps.
- Targets cross-chain USDC/USDT movement.
- Helps capture stable-to-stable volume from external intent and bridge routes.
- Can fund strategic subsidies for growth and awareness.
- Makes subsidy spend and STAR net PnL visible through a dedicated fund.

### Safety And Control

- Normal pricing uses the lower of pool spot and oracle output.
- Subsidies are explicit, budgeted, visible, and stoppable.
- Fixed 1:1 subsidy is only allowed for same-symbol swaps.
- Different-symbol swaps use oracle-ratio pricing.
- Subsidy is paid only when it improves STAR inventory.
- Caps limit per-swap, per-block, per-epoch, asset, group, and fund exposure.
- Oracle, depeg, pool, chain, liquidity, and halt checks fail closed.
- Inventory fees make imbalance-worsening flow more expensive.

## Why This Matters

Stablecoin users want movement, not exposure:

- move USDC from one chain to another;
- swap USDC into USDT;
- consolidate stablecoins split across chains;
- get a predictable stable amount out;
- avoid waiting for a streamed swap when a direct stable rail can work.

The normal THORChain route is:

```text
stable A -> RUNE -> stable B
```

That route can pay slip in two pools. Streaming can reduce the slip, but it
takes multiple blocks.

STAR gives THORChain a stablecoin-specific path:

- direct stable-to-stable route;
- CLP-slip-free STAR settlement legs;
- one-block settlement when selected;
- best-quote routing against CLP;
- optional subsidy for strategic flow.

## Target Product Shape

STAR should feel like:

- **almost free** when no subsidy is used;
- **free** when subsidy is enabled and funded;
- **1:1** for same-symbol swaps, after decimal adjustment;
- **oracle-ratio priced** for different-symbol swaps;
- **fast** because there is no multi-block STAR streaming;
- **safe enough** because every fill still passes pricing, liquidity,
  inventory, oracle, and halt checks.

The strongest starting wedge is same-symbol cross-chain movement:

```text
ETH.USDC -> BASE.USDC
ETH.USDT -> AVAX.USDT
```

Different-symbol stable swaps come next:

```text
ETH.USDC -> ETH.USDT
ETH.USDT -> ETH.USDC
```

Different-symbol routes need tighter controls because USDC and USDT can trade
away from each other.

## How STAR Works

STAR uses reserved protocol-owned LP units as inventory.

For each STAR fill:

- user sends the source stablecoin;
- STAR adds proportionally to the source pool;
- STAR withdraws proportionally from the destination pool;
- user receives the destination stablecoin;
- third-party LP units are not touched;
- the settlement legs do not move pool prices;
- the settlement legs do not create normal CLP swap slip.

High-level flow:

```text
source stable in
  -> source pool proportional add
  -> destination pool proportional withdraw
  -> destination stable out
```

## Pricing

Normal STAR pricing uses the lower of two outputs:

```text
grossOut = min(poolSpotOut, oracleOut)
```

Why:

- pool spot is chain-specific;
- pool spot can reflect bridge or chain stress;
- pool spot can also be manipulated;
- oracle price is harder to move with local pool capital;
- oracle price is not chain-specific;
- the lower output keeps STAR from overpaying.

Same-symbol swaps:

```text
oracleOut = 1:1 after decimal adjustment
```

Different-symbol swaps:

```text
oracleOut = oracle price ratio
```

User output:

```text
userOut =
  grossOut
  - baseFee
  - inventoryFee
  - outboundFee
  - affiliateFee
```

All consensus math uses integers.

## Subsidy Mode

Subsidy mode is how STAR gets as close to free as the imlementation allows.

The STAR fund can pay a top-up:

- same-symbol target: 1:1;
- different-symbol target: oracle price ratio;
- targets are pre-fee: outbound and other fees still apply;
- top-up paid only from the STAR fund;
- top-up paid only when it improves STAR inventory;
- top-up paid only when budget checks pass;
- top-up is all-or-nothing;
- partial top-ups are not used.

Top-up rule:

```text
neededTopUp = max(0, targetOut - grossOut)
```

If the fund can pay the whole amount:

```text
topUp = neededTopUp
```

If the fund cannot pay it:

```text
topUp = 0
```

Budget exhaustion does not break the swap. It just means STAR falls back to
normal pricing, or CLP wins best-quote.

Important rule:

- fixed 1:1 is allowed only for the same stable symbol;
- fixed 1:1 is not allowed for USDC <-> USDT;
- different symbols use the oracle ratio.

## Fees And Inventory

Normal mode can be almost free, but it is not uncontrolled.

STAR still accounts for:

- base fee;
- inventory fee;
- outbound fee;
- affiliate fee;
- fund inflows;
- subsidy outflows.

Inventory decides whether STAR stays cheap:

- flow toward target inventory can be cheap;
- flow away from target inventory pays more;
- severe imbalance can make STAR lose to CLP;
- hard caps can make STAR unavailable.

The inventory fee is designed to be split-resistant:

- one large swap should not be cheaper than many small swaps;
- same-block chunks should not bypass the fee;
- inventory updates live inside the block.

## STAR Fund

The STAR fund is a dedicated RUNE fund.

It pays for:

- subsidy top-ups;
- rebalancer costs;
- future STAR expenses.

It can earn from:

- STAR base fees;
- STAR inventory fees;
- conservative pricing when pool spot output is above STAR gross output;
- manual or configured protocol top-ups.

Fund basics:

- fund balance cannot go negative;
- every subsidy is visible;
- every subsidy counts against budget;
- fund ledger matches module balance;
- one query should show fees, subsidies, rebalancer costs, and net PnL.

## Routing And Wallets

Existing wallets do not need a new memo to benefit from STAR auto-routing.

Normal flow:

```text
wallet asks for a normal swap quote
quote engine compares CLP/streaming and STAR
wallet receives the best main quote and its route type
user sends the normal swap memo
execution re-checks current state
```

There is one optional new integration surface:

- quote alternatives can show both CLP and STAR candidates;
- this is useful for wallets that want route visibility;
- it is not required for old wallets;
- it is not a separate "premium route" product.

## Safety Gates

STAR is available only when checks pass:

- asset allowlist;
- L1 stablecoin only;
- chain health;
- pool availability;
- current oracle price;
- depeg checks;
- pool/oracle deviation checks;
- destination liquidity;
- reserved STAR LP units;
- per-swap caps;
- per-block caps;
- per-epoch caps;
- asset exposure caps;
- group exposure caps;
- inventory caps;
- subsidy budget, when subsidy is used.

Execution re-checks everything. Quotes do not reserve liquidity.

## API And Events

The main quote remains the best route.

A new `router` field on the quote names the selected or suggested swap type:

- `clp_single`;
- `clp_rapid`;
- `clp_streaming`;
- `star_auto`.

Optional route alternatives expose the same `route` values per candidate.

The swap memo does not change. There is no STAR memo field. Execution
re-runs route selection when the inbound arrives.

Events should show:

- route selected;
- fallback reason;
- source and destination assets;
- pool spot output;
- oracle output;
- fees;
- subsidy amount;
- fund impact;
- inventory before/after;
- post-fill STAR LP units.

Use the same reason strings in quotes, execution, refunds, events, metrics, and
tests.

## Rollout

Roll out in small steps:

1. Quote-only telemetry.
2. Tiny-cap execution.
3. Same-symbol cross-chain pairs.
4. Different-symbol pairs with smaller caps.
5. Same-symbol subsidy after the fund has a real buffer.
6. Different-symbol oracle-ratio subsidy later.
7. Higher caps and optional rebalancer after dry-run.

If the metrics do not justify subsidy, set subsidy budget to zero.

## Market Signal

Public NEAR Intents sample:

- period: 2026-06-11 09:49 UTC to 2026-06-12 09:49 UTC;
- all sampled stable-to-stable volume: `12.69M USD` across `4,068` swaps;
- different-symbol `USDC <-> USDT`: `5.05M USD`;
- same-symbol cross-chain: `7.65M USD`;
- median swap: about `100 USD`; p90 about `5,000 USD`; p95 about `11,200 USD`;
- raw checked THORChain edge: about `11 bps` before STAR fees and outbound cost.

Implications:

- same-symbol cross-chain should launch first;
- outbound fees matter a lot for small swaps;
- caps should be sized from p90/p95 volume, not the median;
- STAR fees and subsidy budget determine competitiveness;
- STAR v1 top-ups target pre-fee output and do not cover outbound fees, so the
  smallest swaps stay hard to win in v1.

## Non-Goals

STAR v1 does not include:

- unlimited subsidy;
- negative fund balance;
- fixed 1:1 for different stable symbols;
- trade assets;
- secured assets;
- synths;
- derived assets;
- multi-block STAR streaming;
- required new wallet memo;
- outbound batching;
- bypassing vaults, outbounds, halts, or refunds;
- off-chain price APIs in consensus.

## Key Principle

STAR should make stable swaps:

- CLP-slip-free on the STAR fill;
- almost free without subsidy when conditions allow;
- free with subsidy when funded;
- 1:1 for the same stable symbol;
- oracle-ratio priced for different stable symbols;
- kept inside visible budgets.
