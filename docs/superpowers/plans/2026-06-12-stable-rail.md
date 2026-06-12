# Stable Rail (STAR) Implementation Plan (v1)

> **Status:** Ready for implementation review.
>
> **Short description:** Stable Rail is a new swap route for allowlisted
> stablecoins. It uses protocol-owned liquidity. It settles in one block by
> using reserved POL LP units. It does not use CLP slippage for the STAR fill.
>
> STAR has two modes:
>
> 1. **Normal mode.** Before fees, the user gets the lower of the pool spot
>    price output and the oracle price output. The protocol pays no subsidy.
>    STAR settlement legs do not move pool prices. STAR adds proportionally on
>    the source pool and withdraws proportionally from the destination pool.
> 2. **Subsidy mode.** STAR may add a top-up from the STAR fund. For
>    same-symbol swaps, the top-up targets a 1:1 same-symbol amount. For
>    different-symbol swaps, the top-up targets the oracle price ratio. The
>    top-up is budgeted. It can be turned off.

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

---

## 1. Goal

STAR should make stablecoin swaps faster, cheaper, and easier to use.

STAR does not allow a farmable profit.

Any subsidy is explicit, limited, and paid from the STAR fund.

---

## 2. Subsidy targets

Subsidy has two targets:

- same-symbol swaps can top up to 1:1,
- different-symbol swaps can top up to the oracle price ratio.

---

## 3. Important Words

These words are used throughout the document.

### 3.1 CLP route

This is the normal THORChain swap route.

Example:

```text
ETH.USDC -> RUNE -> BASE.USDC
```

The CLP route uses pools. It can use streaming swaps. It pays pool slip.

### 3.2 STAR route

This is the new route.

STAR uses reserved protocol-owned LP units. It does a proportional add on
the source side and a proportional withdraw on the destination side.

The settlement legs do not create normal CLP swap slip.

### 3.3 Best-quote routing

Best-quote routing compares routes and chooses the larger final output for
the user.

This is different from STAR pricing.

STAR pricing decides the STAR candidate output.
Best-quote routing decides whether the user gets CLP or STAR.

### 3.4 Pool spot output

`poolSpotOut` is the output implied by the two pool spot prices.

It is chain-specific. For example, the pool price can see that `BASE.USDC`
is trading cheaper than `ETH.USDC`.

### 3.5 Oracle output

`oracleOut` is the output implied by the Bifrost oracle prices.

For same-symbol swaps, like `USDC -> USDC`, this is the same amount.

For different-symbol swaps, like `USDC -> USDT`, this uses the oracle price
ratio between the two symbols.

The oracle price is not chain-specific.

### 3.6 Gross output

`grossOut` is the normal STAR output before fees and before any subsidy.

In normal mode:

```text
grossOut = min(poolSpotOut, oracleOut)
```

### 3.7 Top-up

`topUp` is a subsidy paid from the STAR fund.

Subsidy mode has an explicit target:

- same-symbol target: 1:1 after decimal adjustment,
- different-symbol target: `oracleOut`.

It is all-or-nothing. If the fund cannot pay the whole top-up, then the
top-up is zero and the swap uses normal pricing.

---

## 4. Scope

### 4.1 v1 Goals

STAR v1 should:

- support allowlisted stablecoin pairs,
- support different-symbol pairs, such as `ETH.USDC -> ETH.USDT`,
- support same-symbol cross-chain pairs, such as `ETH.USDC -> BASE.USDC`,
- use normal pricing based on `min(poolSpotOut, oracleOut)`,
- support same-symbol 1:1 subsidy, mimir-controlled,
- support different-symbol oracle-ratio subsidy, mimir-controlled,
- allow subsidy only when the swap improves STAR inventory toward target,
- use a dedicated `star` fund,
- charge a larger inventory fee when a swap worsens STAR inventory imbalance,
- use configured POL LP units as STAR inventory,
- report the selected swap type in a new quote `router` field,
- add quote alternatives when they are available,
- keep the normal `swap` memo for the v1 auto route,

### 4.2 v1 Non-Goals

STAR v1 should not:

- provide unlimited subsidy,
- allow the fund to go negative,
- subsidize different-symbol swaps at a fixed 1:1 amount,
- support trade assets, secured assets, synths, or derived assets,
- add multi-block STAR streaming,
- require a new memo type for existing wallets,
- solve outbound batching,
- replace normal RUNE pools,
- bypass vaults, outbounds, halts, or refunds,
- use off-chain price APIs in consensus,
- use floats or `cosmos.Dec` for consensus decisions.

---

## 5. Normal STAR Pricing

Normal STAR mode is the default mode.

Normal mode does not use subsidy.

### 5.1 Inputs

```text
amountIn = source asset amount

sourceRuneDepth = sourcePool.BalanceRune
sourceAssetDepth = sourcePool.BalanceAsset

targetRuneDepth = targetPool.BalanceRune
targetAssetDepth = targetPool.BalanceAsset

sourceOracleAmount = source oracle price Amount
sourceOracleDecimals = source oracle price Decimals

targetOracleAmount = target oracle price Amount
targetOracleDecimals = target oracle price Decimals
```

### 5.2 Output Formulas

STAR computes pool spot output with the same shared RUNE leg used by
settlement:

```text
runeLeg =
    amountIn * sourceRuneDepth / sourceAssetDepth

poolSpotOut =
    runeLeg * targetAssetDepth / targetRuneDepth

oracleOut =
    amountIn *
    sourceOracleAmount * 10^targetOracleDecimals /
    targetOracleAmount / 10^sourceOracleDecimals

grossOut = min(poolSpotOut, oracleOut)

userOut = grossOut - baseFee - inventoryFee - outboundFee - affiliateFee
```

All math uses integers. Integer division floors. Asset rounding is against the
user. Quote and execution use the same helper so the quoted `poolSpotOut`
matches settlement.

### 5.3 Why Normal Mode Uses The Lower Output

The pool price and the oracle price have different weaknesses.

The pool price is chain-specific, but it can be manipulated.

The oracle price is harder to manipulate with on-chain capital, but it is not
chain-specific.

So normal STAR uses the lower output.

This means:

- a pool push cannot make STAR pay above the oracle value,
- an oracle issue cannot make STAR pay above the pool value,
- if one signal looks too good, the other signal limits the payout.

### 5.4 Examples

Example 1: different-symbol swap.

```text
Swap: 1000 ETH.USDC -> ETH.USDT

oracleOut = 1001.00 USDT

If poolSpotOut = 1001.40, then grossOut = 1001.00.
If poolSpotOut = 1000.80, then grossOut = 1000.80.
```

Example 2: same-symbol swap.

```text
Swap: 1000 ETH.USDC -> BASE.USDC

oracleOut = 1000.00 USDC

If poolSpotOut = 998.50, then normal grossOut = 998.50.
```

The lower pool output may mean the destination chain or bridge is stressed.
Normal STAR does not hide that risk.

---

## 6. Optional Oracle Premium

Oracle Premium is an optional normal-mode setting.

It lets STAR pay slightly above `oracleOut` when `poolSpotOut` is higher,
but only up to the configured oracle premium.

Default value:

```text
0 bps
```

The mimir key is:

```text
StableAssetRailOraclePremiumBps
```

Formulas use the shorter local variable name:

```text
oraclePremiumBps
```

If it is nonzero, STAR can widen the oracle ceiling slightly:

```text
oraclePremiumOut = oracleOut * (10000 + oraclePremiumBps) / 10000
grossOut = min(poolSpotOut, oraclePremiumOut)
```

It exists only as a future tuning knob if real quote data shows that the
strict oracle cap loses too much honest flow to CLP.

Validation rule:

```text
oraclePremiumBps <= StableAssetRailBaseBps
```

This keeps the optional premium no larger than the normal base fee.

The oracle premium uses base-fee room. It should not become an unbudgeted
subsidy.

Example:

```text
StableAssetRailBaseBps = 5
oraclePremiumBps = 5
```

In this case, STAR may follow the pool spot price up to 5 bps above the oracle
price. The 5 bps base fee can offset that premium.

If:

```text
StableAssetRailBaseBps = 0
```

then:

```text
oraclePremiumBps = 0
```

The subsidy target never includes `oraclePremiumBps`.

When STAR calculates a subsidized top-up, it does not add the oracle premium.
The subsidy target is always the fair target, not the premium target.

---

## 7. Budgeted Subsidy Mode

Normal mode pays the bounded amount from the pricing rule.

Subsidy mode may add a top-up from the STAR fund.

### 7.1 What It Is For

Subsidy mode is for product goals where the protocol chooses to spend a
limited budget.

It can support two target types.

Same-symbol 1:1 target:

```text
ETH.USDC -> BASE.USDC
AVAX.USDT -> ETH.USDT
```

Different-symbol oracle-ratio target:

```text
ETH.USDC -> ETH.USDT
ETH.USDT -> ETH.USDC
```

This mode supports:

- free or near-free stable swaps,
- "unscrew my stablecoins" - consolidate stablecoins that are split across chains,
- loss-leader wallet integrations.

### 7.2 What It Does Not Do

Subsidy mode does not pay a fixed 1:1 amount for different symbols.

Example:

```text
USDC -> USDT using fixed 1:1 pricing is not allowed.
```

USDC and USDT can trade away from each other. Paying fixed 1:1 across
different symbols would give arbitrageurs a free spread.

This is allowed instead:

```text
USDC -> USDT at the oracle price ratio
```

If the oracle says 1 USDC is worth 1.001 USDT, then the subsidy target can be
1.001 USDT.

If the oracle says 1 USDC is worth 0.999 USDT, then the subsidy target can be
0.999 USDT.

### 7.3 Subsidy Target

First compute the normal STAR output from Section 5 and Section 6.

```text
grossOut = normal STAR output before fees and before subsidy
```

Then choose the subsidy target.

For same-symbol swaps:

```text
targetOut = amountIn
```

For different-symbol swaps:

```text
targetOut = oracleOut
```

Same-symbol subsidy uses 1:1 because the user is moving the same stable
symbol between chains.

Different-symbol subsidy uses oracle-ratio because the symbols can have
different market prices.

### 7.4 Subsidy Direction Rule

Subsidy is enabled only when the swap improves STAR inventory.

That means the swap reduces the total STAR inventory potential:

```text
Potential_after < Potential_before
```

The same potential function used for the inventory fee also decides subsidy
direction. This applies to same-symbol swaps and different-symbol swaps.

A STAR fill adds the source asset to STAR inventory and pays the destination
asset out of STAR inventory (Section 13.3).

Example:

```text
Target total STAR inventory weights:
- ETH.USDT 50%
- AVAX.USDT 50%

Current STAR inventory:
- ETH.USDT 70%
- AVAX.USDT 30%
```

In this state:

```text
AVAX.USDT -> ETH.USDT can receive subsidy
ETH.USDT -> AVAX.USDT cannot receive subsidy
```

Reason:

The first direction adds AVAX.USDT to STAR inventory and pays ETH.USDT out of
it. That reduces the AVAX.USDT shortage and reduces the ETH.USDT surplus.

The second direction adds to the ETH.USDT surplus and deepens the AVAX.USDT
shortage. It makes the imbalance worse.

If the swap does not improve inventory, STAR may still execute in normal mode,
but `topUp` is zero.

### 7.5 Top-Up Formula

Compute the amount needed to reach the target:

```text
neededTopUp = max(0, targetOut - grossOut)
```

Then check the subsidy budgets.

```text
if budget can pay neededTopUp in full:
    topUp = neededTopUp
else:
    topUp = 0
```

For a same-symbol swap, that means a full top-up to the 1:1 target.

For a different-symbol swap, that means a full top-up to `oracleOut`.

Partial top-ups are not supported.

The top-up is bounded by the pool/oracle deviation gate:

```text
topUpValue <= StableAssetRailMaxPoolOracleDeviationBps * notionalValue / 10000
```

The top-up is the gap between normal STAR pricing and the subsidy target.

### 7.6 Final User Output

By default, STAR subsidy targets are gross targets.

That means the top-up is calculated before fees.

```text
neededTopUp = subsidyTarget - grossOut
```

For same-symbol swaps, the gross subsidy target is the 1:1 amount.

For different-symbol swaps, the gross subsidy target is `oracleOut`.

Then STAR subtracts fees:

```text
userOut = grossOut + topUp - baseFee - inventoryFee - outboundFee - affiliateFee
```

So the user does not receive exact net 1:1 or exact net oracle-ratio output by
default.

Example:

```text
gross subsidy target = 100.00
fees = 0.10
userOut = 99.90
```

Targets are gross, STAR subsidy doesn't covers fees. For small swaps the
outbound fee dominates, so a fully topped-up small swap can still lose to an
aggregator on net output. A net-target subsidy that covers fees is future
work (Section 24).

### 7.7 Budget Rules

The top-up is paid only if every budget line can cover it in full.

Budget checks include:

- per-swap subsidy cap,
- per-block subsidy cap,
- per-epoch subsidy cap,
- minimum fund balance,
- value of the top-up in RUNE.

Per-epoch caps use:

```text
StableAssetRailEpochBlocks
```

If any budget check fails:

```text
topUp = 0
fallback_reason = subsidy_budget_exhausted
```

The swap can still execute at normal bounded pricing.

Budget exhaustion is not a reason to refuse the whole swap.

### 7.8 Subsidy Economics

A STAR round trip should not be profitable to farm.

If STAR pays a top-up in one direction, the opposite direction can usually earn
a similar pool/oracle gap back into the fund.

The more important case is real one-way flow that helps inventory. STAR can pay
a small top-up to attract that flow. Later, CLP, arbitrage, or the rebalancer
can finish the clean-up.

In plain terms: STAR can buy useful inventory movement, but only at a capped
price.

That price is limited by:

- the subsidy direction rule,
- subsidy budgets,
- the pool/oracle deviation gate,
- inventory caps,
- `StableAssetRailRebalanceMaxCostBps`.

The subsidy should not be more expensive than the configured rebalancer cost
for moving the same dollar of inventory.

### 7.9 Gates Still Apply

Subsidy mode never overrides safety gates.

The following gates still apply:

- depeg halt,
- pool/oracle deviation gate,
- chain halt,
- pool halt,
- exposure caps,
- volume caps,
- inventory caps.

---

## 8. Best-Quote Routing

Best-quote routing compares the final user output from each route.

The routes are:

- CLP route,
- STAR route, if STAR is eligible.

Best-quote returns the route with the higher final user output.

On a tie, prefer CLP. There is no reason to spend STAR inventory for the
same output.

### 8.1 Important Distinction

STAR pricing uses:

```text
min(poolSpotOut, oracleOut)
```

Best-quote routing chooses:

```text
max(CLP final output, STAR final output)
```

### 8.2 Streaming Parameters

The CLP candidate should use the streaming parameters requested by the user.

If the user requests:

```text
streaming_quantity = 1
```

then CLP should be quoted as a single non-streaming swap.

If the user requests auto streaming, then CLP can be quoted as auto
streaming.

STAR should not compare itself against a CLP stream that the user did not
ask for.

STAR itself executes only as a single fill.

If the user explicitly requests a streaming quantity greater than `1`, execution
uses CLP.

If the user requests auto streaming, the quote engine may compare:

```text
CLP auto-streaming candidate
STAR single-fill candidate
```

If STAR wins, execution settles the swap as a single STAR fill.

---

## 9. Quote API

The current quote API returns one main quote.

STAR should keep that behavior for old wallets.

The normal quote result should be the best route.

Add a new `router` field to the main quote response. It names the swap type
that was selected or suggested for the main quote:

```text
clp_single | clp_rapid | clp_streaming | star_auto
```

The swap memo does not change. There is no STAR memo field. Execution
re-runs route selection when the inbound arrives.

### 9.1 Route Alternatives

The quote response should also return route alternatives when alternatives are
available.

```proto
repeated QuoteSwapRoute alternatives = ...;

message QuoteSwapRoute {
  string route = 1;             // clp_single | clp_rapid | clp_streaming | star_auto
  string memo = 2;
  string expected_amount_out = 3;
  QuoteFees fees = 4;
  int64 streaming_quantity = 5;
  int64 streaming_interval = 6;
  int64 total_swap_seconds = 7;
  string warning = 8;
  string reject_reason = 9;
  string price_target = 10;
}
```

Return only:

- the CLP route that was already computed by the normal quote path,
- the STAR route, if STAR is eligible.

The CLP `route` value should describe the route that was actually quoted:

```text
clp_single
clp_rapid
clp_streaming
```

The STAR route should use the direct STAR quote helper.

Old wallets can keep using the main quote result.

New wallets can show the alternatives for route visibility.

There is no STAR-specific memo field in v1. The executed route is selected
again at inbound time. An explicit streaming quantity greater than `1` still
forces CLP.

### 9.2 Router Values

The STAR `route` value is:

```text
star_auto
```

### 9.3 Quote Metadata

Add optional metadata fields where useful:

```text
quote_type
fallback_from
fallback_reason
price_target
oracle symbols
pool_spot_out
oracle_out
price_limit_source
base_bps
inventory_fee
subsidy_amount
spread_amount
total_swap_seconds
```

Use the same reason strings in quotes, execution, refunds, events, metrics,
and tests.

---

## 10. Oracle Parsing Rules

STAR should parse committed oracle prices into integer `Amount` and `Decimals`
values.

STAR should fail closed on:

- missing price,
- malformed price,
- zero price.

Do not use `cosmos.Dec` or floats for consensus decisions.

---

## 11. Safety Gates

Use one shared risk checker for:

- quotes,
- execution,
- rebalancer.

The checker should verify:

- global, group, and asset halts,
- asset is allowlisted,
- source and destination chain are healthy,
- source and destination pools are available, not halted,
- oracle halt is not active (`HaltOracle`),
- oracle prices exist this block,
- depeg halt,
- pool/oracle deviation gate,
- destination STAR liquidity availability,
- per-swap caps,
- per-block caps,
- per-epoch caps,
- inventory hard cap,
- subsidy mode is enabled when subsidy is used,
- subsidy target is valid for the pair,
- subsidy improves STAR inventory toward target,
- fixed 1:1 subsidy is used only for same-symbol pairs,
- different-symbol subsidy uses `oracleOut`,
- subsidy budget,
- minimum fund balance,
- oracle-value loss check: payout plus top-up must not exceed the oracle value
  of the input by more than the configured oracle premium.

### 11.1 USD Conversion

All USD caps use enshrined oracle prices.

Use fixed-point integer math at `1e8` USD precision.

### 11.2 Deviation Gate

The pool/oracle deviation gate uses integer cross multiplication:

```text
delta = abs(poolRatioNum * oracleRatioDen - oracleRatioNum * poolRatioDen)
pass  = delta * 10000 <= MaxDeviationBps * oracleRatioNum * poolRatioDen
```

It fails closed for:

- real depeg,
- bridge stress,
- pool manipulation,
- thin liquidity,
- missing oracle.

---

## 12. Fees

### 12.1 Base Fee

`StableAssetRailBaseBps` is the base route fee.

It can be zero.

It is scoped by:

```text
asset > symbol > global
```

When nonzero, it helps pay for:

- adverse selection,
- oracle risk,
- execution risk,
- STAR fund growth.

### 12.2 Quadratic Inventory Fee

STAR should charge more when a swap makes inventory worse.

If the swap helps inventory balance -> no inventory fee.

If the swap hurts inventory balance a little -> small fee.

If the swap hurts inventory balance a lot -> much larger fee.

Use a potential function:

```text
currentWeightBps_i = asset_i_oracle_usd_value * 10000 / totalSTARInventoryUSD
targetWeightBps_i  = configured target share of asset i in total STAR inventory

allowedDeviationWeightBps_i = targetWeightBps_i * deviationBandBps_i / 10000

sBps_i = |currentWeightBps_i - targetWeightBps_i| * 10000 /
         allowedDeviationWeightBps_i
phiBps_i = K * sBps_i * sBps_i / 10000 / 10000

PotentialUSD = sum_i(phiBps_i * BandValueUSD_i / 10000)

inventoryFeeUSD = max(0, PotentialUSD_after - PotentialUSD_before)
```

Definitions:

- `currentWeightBps_i` is computed from oracle USD value.
- `targetWeightBps_i` is the target share of asset `i` in total STAR inventory.
- target weights across all allowlisted STAR assets sum to 10,000 bps.
- `deviationBandBps_i` is the allowed distance from target, in bps of
  `targetWeightBps_i`, before the hard cap.
- `allowedDeviationWeightBps_i` is the resulting weight distance from target.
- `sBps_i` is in `[0, 10000]`.
- fills that would push an asset beyond its deviation band are rejected by the
  inventory hard cap.
- `BandValueUSD_i = allowedDeviationWeightBps_i * totalSTARInventoryUSD / 10000`.
- `K` is `StableAssetRailInventoryFeeK`, in bps of `BandValueUSD_i` at
  `sBps_i = 10000`.

If a swap improves inventory, the fee is zero.

The same potential function drives the subsidy direction rule:

```text
improves inventory = Potential_after < Potential_before
```

### 12.3 Why This Fee Is Split-Resistant

The fee is based on the change in potential:

```text
Potential_after - Potential_before
```

If a user splits one swap into many chunks, the changes add up to the same
total.

So one large swap and many small swaps should pay the same inventory fee.

Implementation rule:

Inventory, caps, and budget are read and written live inside the block.

Do not use a block-start inventory snapshot for all STAR fills.

Add a test:

```text
one large swap
N chunks in the same block
N chunks across blocks
chunked totals are never below the single-swap fee
```

### 12.4 Other Fees

Outbound fees and affiliate fees use the existing machinery.

For small swaps, outbound fees dominate. For example, a 1 USD outbound fee
on a 100 USD payment is 100 bps.

Batching several user outbounds into one chain transaction could reduce the
per-user outbound cost and partly reduce this problem.

STAR v1 does not solve outbound batching.

---

## 13. LP Accounting And Settlement

### 13.1 LP Source

The protocol already owns LP positions.

Existing POL positions are owned by module addresses.

Possible owners:

- `reserve`,
- `pol_reserve`.

The current `ETH.USDT` POL position is expected to be around 600k USD.

### 13.2 STAR LP Owner

Do not share the existing POL LP record directly.

Move the configured STAR units into a separate LP record owned by:

```text
star module address
```

This is an ownership relabel. It does not change pool balances.

Benefits:

- STAR inventory is easy to audit,
- normal POL is isolated,
- third-party LPs are not touched,
- STAR accounting is simpler.

Initial distribution from `ETH.USDT` into other stable pools can happen:

- manually,
- or through the rebalancer in bootstrap mode.

### 13.3 Settlement Legs

For every STAR fill:

```text
runeLeg =
  amountIn * sourceRuneDepth / sourceAssetDepth

poolSpotOut =
  runeLeg * targetAssetDepth / targetRuneDepth

source pool:
  add amountIn asset
  add runeLeg RUNE
  mint STAR LP units

destination pool:
  withdraw poolSpotOut asset
  withdraw runeLeg RUNE
  burn STAR LP units
```

So `runeDelta` is zero by construction:

```text
runeDelta = 0
```

Asset rounding is floor, against the user.

These proportional settlement legs do not move pool prices.

### 13.4 Fee Skim And Subsidy Conversion

The fee skim and subsidy conversion are separate from the proportional
settlement legs.

They can move the destination pool slightly, like existing fee-disbursement
logic.

The withdrawn `poolSpotOut` asset is split like this:

```text
user base pay  = grossOut - baseFee - inventoryFee - outboundFee - affiliateFee
fund inflow    = (poolSpotOut - grossOut) + baseFee + inventoryFee
outbound fee   -> existing fee path
affiliate fee  -> existing affiliate path
```

Example:

```text
poolSpotOut = 100.20 USDC
oracleOut   = 100.00 USDC
grossOut    = min(poolSpotOut, oracleOut) = 100.00 USDC

baseFee      = 0.03 USDC
inventoryFee = 0.02 USDC
outboundFee  = 0.10 USDC
affiliateFee = 0.05 USDC
```

Then:

```text
user base pay = 100.00 - 0.03 - 0.02 - 0.10 - 0.05
              = 99.80 USDC
```

The STAR fund keeps:

```text
(poolSpotOut - grossOut) + baseFee + inventoryFee

= (100.20 - 100.00) + 0.03 + 0.02
= 0.25 USDC
```

If subsidy mode is used, the `topUp` asset is bought separately by the
STAR fund.

The top-up is not taken from the original `poolSpotOut` withdrawal.

---

## 14. STAR Fund

Create a module account:

```text
star
```

The fund holds RUNE only.

### 14.1 Fund Inflows

Every STAR fill can send value to the fund:

```text
fund inflow asset = (poolSpotOut - grossOut) + baseFee + inventoryFee
```

Convert this asset to RUNE using the existing pool disbursement style:

```text
Pool.RuneDisbursementForAssetAdd
```

This is the same kind of mechanism CLP uses for liquidity-fee accounting.

### 14.2 Fund Outflows

The fund pays for:

- subsidy top-ups,
- rebalancer costs,
- future STAR expenses.

For a top-up, the fund sends RUNE into the destination pool and takes out the
top-up asset using the reverse disbursement operation.

### 14.3 Fund Top-Ups

The fund can also receive:

- manual treasury transfers,
- a configured share of protocol revenue,
- automatic top-up from protocol revenue while the fund is below its target.

### 14.4 Fund Invariants

Add checks and queries:

- module balance never negative,
- module balance matches fund ledger,
- genesis export/import works,
- one query shows fees earned, subsidies paid, rebalancer costs, and net PnL.

---

## 15. Execution Flow

Quotes can become stale.

Execution re-checks everything when the inbound is observed.

STAR executes only as a single fill. If an inbound memo has an explicit
streaming quantity greater than `1`, execution uses CLP in v1.

STAR-eligible swaps keep CLP-equivalent queue scoring.

Do not score them as zero-slip STAR swaps.

Reason:

The current queue scores swaps by CLP-equivalent liquidity fee and slip.
Large swaps execute before small swaps. This makes same-block pool pushing
harder.

The active price limit is still the main safety rule. Queue scoring is an
extra guard, not the only protection.

---

## 16. Events, Metrics, And PnL

### 16.1 Events

Keep normal swap, fee, and outbound events.

Add `star_swap` with:

- source asset,
- destination asset,
- oracle symbols,
- oracle prices,
- `pool_spot_out`,
- `oracle_out`,
- price limit source,
- deviation bps,
- base bps,
- inventory fee,
- skew before,
- skew after,
- subsidy amount,
- settlement PnL,
- amount in,
- amount out,
- outbound fee,
- affiliate fee,
- post-fill LP units,
- post-fill inventory,
- reason.

### 16.2 Oracle-Value PnL

Each fill computes:

```text
settlement_pnl = oracle value in - oracle value out
```

In normal mode, with default `oraclePremiumBps`:

```text
settlement_pnl >= 0
```

This is true because normal mode does not pay above `oracleOut`.

If the oracle premium is enabled, normal mode may allow up to
`oraclePremiumBps` of negative mark-to-fair PnL. That is why
`oraclePremiumBps` is no larger than the base fee.

The full STAR ledger also includes:

- rebalancer slip,
- outbound underpricing risk,
- inventory carry, including source-side acquisition at pool marks that
  `settlement_pnl` does not capture,
- subsidies,
- fees.

Subsidy mode records the top-up as an explicit fund debit.

### 16.3 Loss-Leader Metrics

Suggested metrics:

- stable volume from integrators (affiliates),
- non-stable volume from integrators (affiliates),
- STAR volume by route type,
- subsidy spend,
- fund USD value,
- quote losses by reason,
- missing-oracle blocks,
- price-limit-bound fills,
- inventory deviation from target,
- CLP handoff count.

---

## 17. Why Not These Other Designs?

### 17.1 Why Not Free Swaps Directly On CLP Pools?

CLP pools always have slip.

Even if the explicit fee is zero, the user still pays pool slip.

The route also still goes:

```text
stable A -> RUNE -> stable B
```

So the user pays two pool paths.

STAR gives a different product: one-block settlement against reserved
protocol-owned liquidity.

### 17.2 Why Not Pool-Only Pricing?

Pool-only pricing can be farmed.

Example with a `USDT/RUNE` destination pool that has 10M USDT and 10M RUNE:

```text
1. Attacker swaps 10,000 USDT to RUNE.
2. USDT looks about 20 bps cheaper.
3. Attacker uses STAR for a 100,000 USDC -> USDT fill.
4. STAR could overpay about 200 USDT.
5. Attacker swaps the RUNE from step 1 back to USDT.
6. The two pool swaps can cost about 20 USDT in CLP slip fees.
```

The attacker pays CLP fees on the smaller pool-push amount, but extracts the
manipulated price on the larger STAR fill.

This is an attack only against pool-only STAR pricing.

In STAR, normal mode also checks `oracleOut`. If the pool is pushed above the
oracle value, `grossOut` uses `oracleOut`, so this overpayment is not available.

Caps reduce damage, but they do not remove the farming logic.

The lower-of rule removes the farming logic:

```text
grossOut = min(poolSpotOut, oracleOut)
```

Now a pool push cannot make STAR pay above the oracle value.

### 17.3 Why Not Oracle-Only Pricing?

Oracle-only pricing is also not safe enough.

For same-symbol swaps:

```text
USDC -> USDC
```

the oracle ratio is basically 1.

But the oracle does not know which chain or bridge is stressed.

If the pool says `BASE.USDC` is worth less, oracle-only pricing may not fully
reflect that signal, or may reflect it later.

A chain-specific stablecoin can trade weak for reasons such as:

- bridge withdrawal stress,
- chain congestion or halted outbound flow,
- issuer freeze or blacklist risk on that chain,
- token-contract or bridge-wrapper risk,
- low local liquidity for that chain asset.

### 17.4 Why Not Fixed 1:1 For Every Stable Pair?

Same-symbol 1:1 can be a bounded marketing subsidy.

Fixed 1:1 for different symbols is different.

Example:

```text
USDC -> USDT
```

USDC and USDT may trade 10 bps apart.

If STAR pays fixed 1:1 across different symbols, arbitrageurs can drain
that spread until the budget or inventory is gone.

So fixed 1:1 for different symbols is not allowed.

Different-symbol subsidy can still be allowed.

But it targets the oracle price ratio, not fixed 1:1.

Different-symbol subsidy should have its own smaller subsidy caps because it
depends more directly on oracle pricing and the market price difference between
different stablecoins.

### 17.5 Why L1 Assets Only?

STAR is for users moving real L1 stablecoins between chains.

### 17.6 Why No New Memo In v1?

The loss-leader goal needs low integration cost.

Existing wallets already use `swap`/`=` swaps.

Keeping `swap`/`=` as auto route lets existing wallets get STAR without work.

A strict `star_strict` route may be reconsidered later.

### 17.7 Why A Dedicated Fund?

The Reserve is global protocol accounting.

STAR needs a visible runway for subsidies and rebalancing.

A dedicated fund gives:

- one balance,
- one ledger,
- one invariant,
- one query for STAR net PnL.

### 17.8 Why RUNE-Only Fund?

Existing protocol fee plumbing is RUNE-denominated.

A RUNE-only fund is simpler.

Top-ups are paid immediately when they happen, so there is no long-lived USD
payable.

### 17.9 Why Not App-Layer CCL?

App-layer CCL can be useful as a fast experiment.

It is built at a different layer and by a different team.

It does not replace:

- protocol-owned inventory isolation,
- one-block STAR settlement at size,
- STAR subsidy accounting.

The two efforts can run in parallel.

### 17.10 Why No Outbound Batching In v1?

Outbound batching is important for small payments.

At a 100 USD swap size, a 1 USD outbound fee is 100 bps.

But outbound batching is a separate outbound-system change.

STAR documents the problem and keeps batching as future work.

---

## 18. Inventory Rebalancer

The rebalancer is an optional EndBlock backstop for persistent one-directional
flow.

It is not part of user settlement.

```text
user STAR swap:      atomic accounting and outbound commitment in the execution block
inventory rebalance: bounded protocol-only maintenance over later blocks
```

### 18.1 Why It Exists

Normal STAR flow can be self-balancing.

A fill adds the source asset to STAR inventory and pays the destination asset
out of it. So when users send the asset STAR is short of and receive the
asset STAR holds in surplus, the swap improves inventory and can stay cheap.

If flow is one-directional for too long, STAR inventory can drift away from
target. The inventory fee, caps, and best-quote handoff to CLP limit that drift.
The rebalancer is the slower repair tool after those live user-flow controls.

### 18.2 What It Does

The rebalancer moves protocol-owned surplus inventory toward deficit inventory.

High-level process:

```text
1. Find STAR assets above target and below target.
2. Choose a small bounded rebalance slice.
3. Use internal pool-disbursement accounting to rotate surplus into deficit.
4. Credit the output back to STAR inventory.
5. Record cost, inventory change, and skip reason.
```

The rebalance is internal protocol maintenance over assets already custodied in
Asgard. It does not move funds on L1, create an L1 outbound, or wait for new L1
deposits.

Implementation should reuse the existing pool accounting helpers:

```text
Pool.RuneDisbursementForAssetAdd(assetAmount)
Pool.AssetDisbursementForRuneAdd(runeAmount)
```

These helpers calculate the RUNE amount released when asset is added to a pool,
or the asset amount released when RUNE is added to a pool, while preserving
the pool invariant.

For STAR rebalancing, the conceptual flow is:

```text
surplus asset -> source pool -> RUNE
RUNE -> destination pool -> deficit asset
deficit asset stays in STAR inventory
```

Existing POL/RUNEPool code is also a useful model for internal module-owned
liquidity movement: `addPOLLiquidity`, `removePOLLiquidity`,
`reserveEnterRUNEPool`, and `reserveExitRUNEPool`. STAR should use its own
STAR-owned inventory records, but the rebalancer has the same shape: internal
module accounting and pool balance changes, not user outbounds.

### 18.3 Open Question: App-Layer CCL-Assisted Counter-Flow

App-layer CCL may help STAR inventory by steering balance-improving counter-flow.

This can be cheaper than internal rebalancing when the CCL route earns revenue
that is partly shared back to THORChain.

### 18.4 Controls

The rebalancer should run only when useful and cheap.

Controls:

- enabled flag, default off,
- dry-run before live execution,
- deadband before any rebalance starts,
- minimum order size,
- maximum order size,
- daily rebalance budget,
- maximum cost bps,
- oracle, depeg, pool, chain, and halt checks,
- deterministic scan order,
- rebalance and rebalance-skip events.

If the cost is too high, the pool/oracle check fails, a chain is halted, or the
daily budget is exhausted, the rebalancer skips.

---

## 19. Mimir Keys

Scope order for normal asset settings:

```text
asset override > symbol override > global default
```

Subsidy settings also need ordered pair scope for different-symbol pairs.

Example:

```text
StableAssetRailSubsidyOracleRatio-USDC-USDT
```

This key enables `USDC -> USDT`. It does not automatically enable
`USDT -> USDC`.

Keys:

```text
HaltStableAssetRail[-<symbol>|-<asset>]

StableAssetRailBaseBps[-<symbol>|-<asset>]

StableAssetRailInventoryFeeK[-<symbol>|-<asset>]

StableAssetRailOraclePremiumBps[-<symbol>|-<asset>]

StableAssetRailTargetWeightBps-<asset>

StableAssetRailWeightBandBps-<asset|symbol>

StableAssetRailSubsidySameSymbol1to1[-<symbol>]
StableAssetRailSubsidyOracleRatio-<source_symbol>-<dest_symbol>

StableAssetRailSubsidyMaxSwapUSD[-<symbol>|-<source_symbol>-<dest_symbol>]
StableAssetRailSubsidyMaxBlockUSD[-<symbol>|-<source_symbol>-<dest_symbol>]
StableAssetRailSubsidyMaxEpochUSD[-<symbol>|-<source_symbol>-<dest_symbol>]

StableAssetRailFundMinRune
StableAssetRailFundTargetUSD

StableAssetRailEpochBlocks

StableAssetRailMaxSwapUSD[-<symbol>|-<asset>]
StableAssetRailMaxSwapPoolDepthBps[-<symbol>|-<asset>]
StableAssetRailMaxSwapProtocolLPDepthBps[-<symbol>|-<asset>]
StableAssetRailMinDestinationLiquidityUSD[-<symbol>|-<asset>]
StableAssetRailMaxBlockVolumeUSD[-<symbol>|-<asset>]
StableAssetRailMaxEpochVolumeUSD[-<symbol>|-<asset>]
StableAssetRailMaxAssetExposureUSD[-<symbol>|-<asset>]
StableAssetRailMaxGroupExposureUSD-<symbol>
StableAssetRailMaxPoolOracleDeviationBps[-<symbol>|-<asset>]
StableAssetRailMinProtocolLPUnits[-<symbol>|-<asset>]
StableAssetRailDepegWarningBps-<symbol>
StableAssetRailDepegHaltBps-<symbol>

StableAssetRailRebalanceEnabled[-<symbol>|-<asset>]
StableAssetRailRebalanceMinOrderUSD[-<symbol>|-<asset>]
StableAssetRailRebalanceDeadbandBps[-<symbol>|-<asset>]
StableAssetRailRebalanceMaxOrderUSD[-<symbol>|-<asset>]
StableAssetRailRebalanceMaxDailyUSD[-<symbol>|-<asset>]
StableAssetRailRebalanceMaxCostBps[-<symbol>|-<asset>]
```

`StableAssetRailEpochBlocks` defines the block length used by all per-epoch
subsidy and volume caps.

Validation rules:

- no negative values,
- bps values below hard maxima,
- `DepegWarningBps < DepegHaltBps`,
- `StableAssetRailTargetWeightBps-<asset>` is the target share of that asset in
  total STAR inventory,
- target weights are normalized at read time across all allowlisted STAR assets,
- default config does not silently enable an asset without an allowlist entry,
- if `StableAssetRailOraclePremiumBps` is nonzero, it is less than or
  equal to `StableAssetRailBaseBps`,
- `StableAssetRailSubsidySameSymbol1to1` applies only when the source and
  destination oracle symbol are the same,
- `StableAssetRailSubsidyOracleRatio` applies only to the exact ordered pair in
  the key.

Mimir values are voted `int64` values, so these rules are enforced as read-time
clamps on resolved effective values after scope resolution:

```text
asset override > symbol override > global default
```

If an effective value cannot be clamped safely, STAR fails closed. For example:

```text
effectiveOraclePremiumBps = min(resolvedOraclePremiumBps, effectiveBaseBps)
```

---

## 20. Implementation Phases

1. Discovery.
2. Add types, config, reason constants, keeper state, genesis.
3. Add oracle and pricing helpers.
4. Add the `star` module account and fund ledger.
5. Add POL unit migration.
6. Add the shared risk checker.
7. Add quote support and alternatives.
8. Add execution path and atomic settlement.
9. Add events, metrics, and PnL.
10. Add optional rebalancer, off by default.
11. Add mimir, OpenAPI, and docs.
12. Add tests.
13. Roll out gradually.

---

## 21. Required Tests

Add tests for:

- normal price is never above `poolSpotOut`,
- normal price is never above `oracleOut` when `oraclePremiumBps` is zero,
- optional oracle premium is bounded by base fee,
- pool push cannot make STAR pay above the active oracle price limit,
- `poolSpotOut` quote uses the same shared `runeLeg` helper as settlement,
- same-symbol subsidy targets 1:1,
- different-symbol subsidy targets `oracleOut`, not fixed 1:1,
- subsidy is paid only when the swap improves STAR inventory toward target,
- subsidy direction uses `Potential_after < Potential_before`,
- every subsidy top-up is all-or-nothing,
- partial top-up is impossible,
- top-up is bounded by the pool/oracle deviation gate up to rounding and dust,
- subsidy budget exhaustion falls back to normal pricing,
- symmetric STAR round trip is not profitable to farm before normal fees and rounding,
- configured subsidy cost per rebalanced dollar does not exceed
  `StableAssetRailRebalanceMaxCostBps`,
- fund never goes negative,
- missing oracle makes STAR ineligible,
- `HaltOracle` makes STAR ineligible,
- pool/oracle deviation gate fails closed,
- L1-only rule rejects trade, secured, synth, and derived assets,
- chunked swaps never pay less total inventory fee than one large swap,
- chunk totals equal the single-swap fee on paths that stay on one side of the
  target,
- same-block chunks do not bypass the fee,
- inventory, caps, and budget update live inside the block,
- STAR LP units are owned by `star`,
- third-party LP units are not touched,
- shared-rune-leg settlement has `runeDelta = 0` by construction,
- fee skim and subsidy conversion are accounted,
- queue scoring stays CLP-equivalent,
- quote alternatives are returned when available,
- the `route` value `star_auto` is reported honestly,
- the swap memo is unchanged, the route is selected at quote time and
  selected again at execution,
- explicit streaming quantity greater than `1` executes through CLP in v1,
- per-epoch caps use `StableAssetRailEpochBlocks`,
- invalid mimir values are clamped or fail closed at read time,
- STAR fallback to CLP respects the price target,

---

## 22. Rollout

Roll out in phases.

### Phase 1: Quote-Only Telemetry

Start with two same-symbol pairs, for example:

```text
ETH.USDC <-> BASE.USDC
```

Subsidy off.

Watch:

- quote win rate,
- price limit source,
- missing oracle blocks,
- would-be volume,
- inventory stress.

### Phase 2: Tiny-Cap Execution

Enable execution with very small caps.

Keep subsidy off.

Use nonzero `StableAssetRailBaseBps` so the fund starts collecting.

### Phase 3: More Same-Symbol Pairs

Add more same-symbol pairs.

Same-symbol cross-chain is the larger part of the benchmark sample.

### Phase 4: Different-Symbol Pairs

Add different-symbol pairs with small caps.

Watch issuer spread and inventory stress.

### Phase 5: Subsidy Mode

Enable same-symbol 1:1 subsidy only after the fund has a real buffer.

The buffer should cover multiple weeks of worst-case subsidy at configured
caps.

Only enable subsidy in directions that improve STAR inventory toward target.

Enable different-symbol oracle-ratio subsidy later, only if same-symbol
subsidy works well and the fund still has a real buffer.

Use separate smaller caps for different-symbol subsidy.

### Phase 6: Higher Caps And Rebalancer

Raise caps slowly.

Run the rebalancer in dry-run first.

Then enable it with strict cost caps.

If the business metrics do not improve in the agreed window, set the subsidy
budget to zero.

---

## 23. Market Benchmark

THORChain pricing snapshot: 2026-06-03.

This was taken while THORChain trading was halted. Treat the THORChain pricing
edge as a pricing thought experiment, not as a consensus input.

Oracle prices:

```text
USDC = 0.9996
USDT = 0.9986
```

Raw THORChain stable-route edge vs the public NEAR Intents 1Click quote:

```text
USDT -> USDC: about 11.2 bps before STAR fees and outbound cost
USDC -> USDT: about 10.9 bps before STAR fees and outbound cost
```

This margin is thin.

STAR beats that quote only if STAR fee plus outbound cost is below about
11 bps.

The 2026-06-03 NEAR sample kept only aggregate counts, aggregate volume, and
percentiles. It did not retain category counts by swap amount.

Fresh NEAR Intents public Explorer sample, successful USDC/USDT
stable-to-stable swaps, 24h from 2026-06-11 09:49:24 UTC to
2026-06-12 09:49:24 UTC:

| Route type | Count | 24h volume | Average | Median |
| --- | ---: | ---: | ---: | ---: |
| Different-symbol `USDC <-> USDT` | 1,577 | 5.05M USD | 3,199 USD | 134 USD |
| Same-symbol cross-chain | 2,491 | 7.65M USD | 3,070 USD | 90 USD |
| All stable-to-stable | 4,068 | 12.69M USD | 3,120 USD | 100 USD |

Tail:

```text
p75 = 969 USD
p90 = 4,994 USD
p95 = 11,222 USD
max = 599,333 USD
```

Direction mix:

| Direction | Count | 24h volume | Median |
| --- | ---: | ---: | ---: |
| `USDT -> USDT` | 1,535 | 5.36M USD | 105 USD |
| `USDC -> USDC` | 956 | 2.29M USD | 50 USD |
| `USDC -> USDT` | 943 | 2.89M USD | 219 USD |
| `USDT -> USDC` | 634 | 2.15M USD | 72 USD |

Amount buckets:

| Swap amount | Count | 24h volume |
| --- | ---: | ---: |
| 0-10 USD | 846 | 3.6K USD |
| 10-50 USD | 822 | 20.4K USD |
| 50-100 USD | 376 | 29.5K USD |
| 100-250 USD | 463 | 75.2K USD |
| 250-500 USD | 294 | 111.5K USD |
| 500-1,000 USD | 410 | 340.6K USD |
| 1,000-5,000 USD | 471 | 1.30M USD |
| 5,000-10,000 USD | 166 | 1.32M USD |
| 10,000-50,000 USD | 189 | 4.09M USD |
| Over 50,000 USD | 31 | 5.40M USD |

Subsidy sizing proxy:

```text
baseFee = 0
inventoryFee = 0
affiliateFee = 0
subsidy only covers fixed outbound shortfall
edge = 10.9 bps
subsidy = max(0, outboundFeeUSD - amountUSD * edge / 10000)
```

This uses the tighter measured `USDC -> USDT` edge as a conservative proxy.
Actual same-symbol and per-pair edges should be measured before final budgets.
Inventory-worsening flow adds the inventory fee to the fixed outbound cost.

Estimated daily STAR fund spend needed to make every included swap beat NEAR:

| Include swaps above | Count | 24h volume | 0.25 USD outbound | 0.50 USD outbound | 1.00 USD outbound |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 0 USD | 4,068 | 12.69M USD | 487 USD/day | 1,141 USD/day | 2,592 USD/day |
| 10 USD | 3,222 | 12.69M USD | 280 USD/day | 722 USD/day | 1,750 USD/day |
| 50 USD | 2,400 | 12.67M USD | 96 USD/day | 333 USD/day | 950 USD/day |
| 100 USD | 2,024 | 12.64M USD | 34 USD/day | 178 USD/day | 606 USD/day |
| 250 USD | 1,561 | 12.56M USD | 0 USD/day | 28 USD/day | 225 USD/day |
| 500 USD | 1,267 | 12.45M USD | 0 USD/day | 0 USD/day | 53 USD/day |
| 1,000 USD | 857 | 12.11M USD | 0 USD/day | 0 USD/day | 0 USD/day |

Implications:

- median users are small,
- volume is dominated by larger transfers,
- category counts matter: more than 40% of swap count is under 50 USD, but
  this bucket is economically small,
- caps should be sized from p90, p95, and max, not from the median,
- same-symbol cross-chain should launch first,
- with a 0.25 USD outbound fee, STAR can win most volume with modest subsidy,
- with a 1.00 USD outbound fee, the 100 USD payment user remains hard to win
  without explicit subsidy or outbound batching.

---

## 24. Future Work

Not v1:

- strict `star_strict` route value,
- net-target subsidy that covers outbound fee,
- outbound batching,
- per-block STAR netting,
- more stable symbols,
- broader payment product,
- app-layer CCL pilot feedback,
- rapid-streaming UX review.

---

## 25. Final Principle

Low fees are allowed.

Zero fees are allowed.

Subsidies are allowed.

But every subsidy is:

- explicit,
- budgeted,
- visible in the fund ledger,
- stoppable by mimir,
- protected by gates.

Normal STAR should not be farmable.

Subsidy mode is allowed to spend money, but only from the STAR fund and
only inside clear limits.

For same-symbol pairs, subsidy can target 1:1.

For different-symbol pairs, subsidy can target the oracle ratio.

It should never target fixed 1:1 across different symbols.
