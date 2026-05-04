# ADR 028: Dynamic Arb Min Fee

## Changelog

- May 2026: Proposed

## Status

Proposed

## Context

THORChain pool prices are maintained by swaps and arbitrage. When a pool price differs from the external market price, arbitrageurs trade against the pool until the pool price approaches fair market price.

Minimum slip basis points (L1SlipMinBps, TradeAccountsSlipMinBps, SecuredAssetSlipMinBps, etc.) can prevent that final correction. If the minimum bps floor is approximately 10 bps, then arbitrage may become unprofitable once the pool is within approximately 10 bps of the external market in either direction, whether the pool price is above or below the reference price. This can leave pools consistently several basis points away from CEX or DEX market prices.

This price imprecision matters. A pool that is 10 to 15 bps away from fair price can lose swap quotes to other venues and routing designs, including intent-based systems. Lower quote competitiveness can reduce user swap volume, which can reduce total fees even if the per-swap minimum fee is higher.

This ADR is scoped to arbitrage-motivated THORChain Trade Account and Secured Asset swap flow. The design assumes that the small last-mile arbitrage trades targeted here are executed almost exclusively from trade-account or secured-asset balances rather than ordinary L1-only swaps. Current live Mimir values set the relevant minimum slip floors to 10 bps for L1, trade-account, and secured-asset swaps. The mechanism does not apply to ordinary L1-only swaps, synth swaps, derived-asset swaps, or stable-to-stable swaps.

App-funded subsidies in ADR-027 (https://gitlab.com/thorchain/thornode/-/merge_requests/4792) address one separate issue: an app such as Rujira may pay for lower bps through its own revenue share or subsidy vault. However, THORChain also has a direct protocol-level interest in keeping pool prices precise. A normal arbitrageur who moves a pool closer to oracle price can benefit THORChain even if that arbitrageur is not a whitelisted app (ADR-027).

The "ADR-026 Dynamic L1 min fee per thorname" in MR !4752 (https://gitlab.com/thorchain/thornode/-/merge_requests/4752) is an inspiration for this ADR. Both mechanisms adjust bps parameters, observe protocol revenue, and move toward the bps point that produces the best observed revenue for each controlled market.

The goals of this ADR are:

1. Improve THORChain pool price precision.
2. Make THORChain swap quotes more competitive.
3. Allow lower effective bps only for trade/secured-asset swaps that improve pool price.
4. Use per-asset active dynamic arb min fee state, with closed-loop total pool revenue feedback so lower fees exist only where they are useful.

This mechanism only targets price-correcting arbitrage that is blocked by the minimum slip fee floor (currently 10 bps).

The ADR-028 solution helps when:

1. The arbitrageur has inventory ready in trade-account or secured-asset.
2. The pool is close to the external reference price but not fully corrected.
3. Natural swap slip is below the normal min-slip floor.
4. The normal min-slip floor makes the final correction unprofitable.

### Simulation Evidence

The behavior above is reproduced by `docs/architecture/adr-028-dynamic-arb-min-fee_test.go`. The test mirrors the current swap slip calculation, including `max(natural_swap_slip_bps, effective_min_slip_bps)` and the existing post-swap liquidity-fee-in-RUNE pool deduction. It uses deterministic live-like BTC and ETH pool depths with Binance-derived reference prices from 2026-05-02, then starts each pool 15 bps below the reference price.

The test is a demonstration that the normal 10 bps floor can be the binding constraint for last-mile price-correcting arbitrage.

Fixture assumptions:

| Asset |     Reference price |  Initial pool price | Initial deviation | Arb minimum profit threshold |
| ----- | ------------------: | ------------------: | ----------------: | ---------------------------: |
| BTC   | 154,291.87 RUNE/BTC | 154,060.44 RUNE/BTC |           -15 bps |                    2.00 RUNE |
| ETH   |   4,544.91 RUNE/ETH |   4,538.09 RUNE/ETH |           -15 bps |                    2.00 RUNE |

With the current 10 bps minimum slip floor, the best possible price-correcting trade in the simulation is below the rational arbitrageur's assumed 2 RUNE profit threshold:

| Asset | 10 bps best possible profit | Result                                             |
| ----- | --------------------------: | -------------------------------------------------- |
| BTC   |                   1.97 RUNE | Trade skipped; pool remains -15 bps from reference |
| ETH   |                   0.76 RUNE | Trade skipped; pool remains -15 bps from reference |

Lower dynamic arb floors make the same last-mile correction economically executable:

| Asset | Dynamic arb floor | Profit-max input | Charged slip |    Profit | Post-trade deviation |
| ----- | ----------------: | ---------------: | -----------: | --------: | -------------------: |
| BTC   |             5 bps |   14,130.23 RUNE |        5 bps | 8.49 RUNE |            -6.01 bps |
| BTC   |             1 bps |   11,701.97 RUNE |        4 bps | 8.84 RUNE |            -7.55 bps |
| ETH   |             5 bps |    5,458.30 RUNE |        5 bps | 3.28 RUNE |            -6.01 bps |
| ETH   |             1 bps |    4,550.93 RUNE |        4 bps | 3.42 RUNE |            -7.51 bps |

The follow-up behavior is important:

| Asset | Dynamic arb floor | Follow-up result after first profit-max trade                                           |
| ----- | ----------------: | --------------------------------------------------------------------------------------- |
| BTC   |             5 bps | No second trade clears the 2 RUNE profit threshold; pool stays -6.01 bps from reference |
| BTC   |             1 bps | A second 5,880.54 RUNE trade earns 2.24 RUNE and moves the pool to -3.81 bps            |
| ETH   |             5 bps | No second trade clears the 2 RUNE profit threshold; pool stays -6.01 bps from reference |
| ETH   |             1 bps | No second trade clears the 2 RUNE profit threshold; pool stays -7.51 bps from reference |

These results support three design choices in this ADR:

1. Dynamic arb min fee adjustments are useful specifically when the normal min-slip floor, rather than natural slip, blocks the final correction.
2. The active dynamic arb floor should be per asset. BTC and ETH differ in how much additional correction a lower floor can unlock.
3. The configuration values that drive the controller should remain common across assets, while each enabled asset calculates its own active dynamic arb bps at epoch end.

### Historical Public-Gateway Estimate

A separate manual estimate in `TestDynamicArbMinFeeHistoricalPublicGatewayEstimate` replays recent public swaps and simulates the fee effect of lowering the min-slip floor on the exact same historical legs. This sample is only illustrative and provides a rough sense of the fee amounts that would be affected by implementing this ADR.

The following sample was produced with:

```text
RUN_DYNAMIC_ARB_MIN_FEE_GATEWAY_ESTIMATE=1 go test ./docs/architecture -run TestDynamicArbMinFeeHistoricalPublicGatewayEstimate -count=1 -v -timeout=30m
```

Sample window and filters:

```text
Gateway: https://gateway.liquify.com/chain/thorchain_api
Sample: 30 deterministic one-hour windows over the last 30 days (seed=28028)
Sampled blocks: 18,000 total (600 blocks/hour); covered height range 25609910 -> 26028109, latest=26035309
Nominal duration: 30 sampled hours at 6 seconds per block
Swap legs scanned: all=104915, basic ADR-028 floor-bound trade/secured=29971, with oracle prices=29059
Observed fee from all scanned swap legs: 202,089.32 RUNE
Oracle band: +/-10 bps pre-swap deviation
Minimum simulated improvement: 1 bps
```

The estimator samples one deterministic pseudo-random hour from each of the last 30 days. For each sampled hour it fetches the pool state at that hour's end height and reverses block events within that hour to reconstruct pre-swap pool state. All fee values are in RUNE. `Actual fee` is the observed `liquidity_fee_in_rune` for historical candidate legs. `Projected` is the simulated fee after replacing the 10 bps floor with the row's lower floor. `Fee diff` is the simulated difference between the historical 10 bps fee result and the lower-floor fee result for those same legs. `Diff/all` is `fee diff / all observed swap-leg fees in the sampled blocks`. `All candidates` means no near-oracle pre-swap band is applied; the next two row groups split the same candidate set by whether the pre-swap pool-vs-oracle deviation was within or outside +/-10 bps.

| Case                   |       Floor |   Legs |  Txids | Actual fee | Projected |  Fee diff | Diff/all | Leg share |
| ---------------------- | ----------: | -----: | -----: | ---------: | --------: | --------: | -------: | --------: |
| All candidates         | 10 to 0 bps | 12,544 | 10,237 |  48,534.74 | 19,941.95 | 28,592.78 |   14.15% |   100.00% |
| All candidates         | 10 to 1 bps | 12,544 | 10,237 |  48,534.74 | 19,942.50 | 28,592.24 |   14.15% |   100.00% |
| All candidates         | 10 to 2 bps | 12,543 | 10,237 |  48,534.60 | 20,424.14 | 28,110.46 |   13.91% |   100.00% |
| All candidates         | 10 to 3 bps | 12,543 | 10,237 |  48,534.60 | 21,610.75 | 26,923.85 |   13.32% |   100.00% |
| All candidates         | 10 to 4 bps | 12,543 | 10,237 |  48,534.60 | 23,328.98 | 25,205.62 |   12.47% |   100.00% |
| All candidates         | 10 to 5 bps | 12,543 | 10,237 |  48,534.60 | 26,603.86 | 21,930.74 |   10.85% |   100.00% |
| All candidates         | 10 to 6 bps | 12,543 | 10,237 |  48,534.60 | 30,478.80 | 18,055.80 |    8.93% |   100.00% |
| All candidates         | 10 to 7 bps | 12,542 | 10,236 |  48,532.90 | 34,565.38 | 13,967.52 |    6.91% |   100.00% |
| All candidates         | 10 to 8 bps | 12,542 | 10,236 |  48,532.90 | 38,864.59 |  9,668.30 |    4.78% |   100.00% |
| All candidates         | 10 to 9 bps | 12,543 | 10,237 |  48,532.97 | 43,664.94 |  4,868.03 |    2.41% |   100.00% |
| Within +/-10 bps band  | 10 to 0 bps |  1,738 |  1,735 |   3,423.06 |  1,101.32 |  2,321.74 |    1.15% |    13.86% |
| Within +/-10 bps band  | 10 to 1 bps |  1,738 |  1,735 |   3,423.06 |  1,101.33 |  2,321.73 |    1.15% |    13.86% |
| Within +/-10 bps band  | 10 to 2 bps |  1,737 |  1,734 |   3,422.92 |  1,160.68 |  2,262.24 |    1.12% |    13.85% |
| Within +/-10 bps band  | 10 to 3 bps |  1,737 |  1,734 |   3,422.92 |  1,313.49 |  2,109.43 |    1.04% |    13.85% |
| Within +/-10 bps band  | 10 to 4 bps |  1,737 |  1,734 |   3,422.92 |  1,483.76 |  1,939.16 |    0.96% |    13.85% |
| Within +/-10 bps band  | 10 to 5 bps |  1,737 |  1,734 |   3,422.92 |  1,764.62 |  1,658.30 |    0.82% |    13.85% |
| Within +/-10 bps band  | 10 to 6 bps |  1,737 |  1,734 |   3,422.92 |  2,079.92 |  1,343.00 |    0.66% |    13.85% |
| Within +/-10 bps band  | 10 to 7 bps |  1,737 |  1,734 |   3,422.92 |  2,399.09 |  1,023.83 |    0.51% |    13.85% |
| Within +/-10 bps band  | 10 to 8 bps |  1,737 |  1,734 |   3,422.92 |  2,737.93 |    684.99 |    0.34% |    13.85% |
| Within +/-10 bps band  | 10 to 9 bps |  1,738 |  1,735 |   3,422.99 |  3,080.45 |    342.53 |    0.17% |    13.86% |
| Outside +/-10 bps band | 10 to 0 bps | 10,806 |  9,104 |  45,111.68 | 18,840.64 | 26,271.04 |   13.00% |    86.14% |
| Outside +/-10 bps band | 10 to 1 bps | 10,806 |  9,104 |  45,111.68 | 18,841.16 | 26,270.52 |   13.00% |    86.14% |
| Outside +/-10 bps band | 10 to 2 bps | 10,806 |  9,104 |  45,111.68 | 19,263.46 | 25,848.22 |   12.79% |    86.15% |
| Outside +/-10 bps band | 10 to 3 bps | 10,806 |  9,104 |  45,111.68 | 20,297.26 | 24,814.42 |   12.28% |    86.15% |
| Outside +/-10 bps band | 10 to 4 bps | 10,806 |  9,104 |  45,111.68 | 21,845.22 | 23,266.46 |   11.51% |    86.15% |
| Outside +/-10 bps band | 10 to 5 bps | 10,806 |  9,104 |  45,111.68 | 24,839.24 | 20,272.44 |   10.03% |    86.15% |
| Outside +/-10 bps band | 10 to 6 bps | 10,806 |  9,104 |  45,111.68 | 28,398.88 | 16,712.80 |    8.27% |    86.15% |
| Outside +/-10 bps band | 10 to 7 bps | 10,805 |  9,103 |  45,109.98 | 32,166.28 | 12,943.70 |    6.40% |    86.15% |
| Outside +/-10 bps band | 10 to 8 bps | 10,805 |  9,103 |  45,109.98 | 36,126.66 |  8,983.32 |    4.45% |    86.15% |
| Outside +/-10 bps band | 10 to 9 bps | 10,805 |  9,103 |  45,109.98 | 40,584.49 |  4,525.49 |    2.24% |    86.14% |

The top three pools by 10 to 0 bps simulated fee difference in this sample were BTC.BTC, ETH.ETH, and ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7.

**BTC.BTC:**

| Case                   |       Floor |  Legs | Txids | Actual fee | Projected |  Fee diff | Diff/all | Leg share |
| ---------------------- | ----------: | ----: | ----: | ---------: | --------: | --------: | -------: | --------: |
| All candidates         | 10 to 0 bps | 3,732 | 3,552 |  31,479.00 | 11,535.81 | 19,943.19 |    9.87% |   100.00% |
| All candidates         | 10 to 1 bps | 3,732 | 3,552 |  31,479.00 | 11,536.30 | 19,942.69 |    9.87% |   100.00% |
| All candidates         | 10 to 2 bps | 3,732 | 3,552 |  31,479.00 | 11,917.63 | 19,561.37 |    9.68% |   100.00% |
| All candidates         | 10 to 3 bps | 3,732 | 3,552 |  31,479.00 | 12,885.63 | 18,593.37 |    9.20% |   100.00% |
| All candidates         | 10 to 4 bps | 3,732 | 3,552 |  31,479.00 | 14,163.11 | 17,315.89 |    8.57% |   100.00% |
| All candidates         | 10 to 5 bps | 3,732 | 3,552 |  31,479.00 | 16,704.99 | 14,774.01 |    7.31% |   100.00% |
| All candidates         | 10 to 6 bps | 3,732 | 3,552 |  31,479.00 | 19,429.95 | 12,049.05 |    5.96% |   100.00% |
| All candidates         | 10 to 7 bps | 3,731 | 3,551 |  31,477.30 | 22,237.46 |  9,239.83 |    4.57% |   100.00% |
| All candidates         | 10 to 8 bps | 3,731 | 3,551 |  31,477.30 | 25,089.89 |  6,387.40 |    3.16% |   100.00% |
| All candidates         | 10 to 9 bps | 3,731 | 3,551 |  31,477.30 | 28,327.64 |  3,149.66 |    1.56% |   100.00% |
| Within +/-10 bps band  | 10 to 0 bps |   293 |   293 |   1,932.15 |    543.85 |  1,388.30 |    0.69% |     7.85% |
| Within +/-10 bps band  | 10 to 1 bps |   293 |   293 |   1,932.15 |    543.85 |  1,388.30 |    0.69% |     7.85% |
| Within +/-10 bps band  | 10 to 2 bps |   293 |   293 |   1,932.15 |    578.21 |  1,353.95 |    0.67% |     7.85% |
| Within +/-10 bps band  | 10 to 3 bps |   293 |   293 |   1,932.15 |    686.24 |  1,245.92 |    0.62% |     7.85% |
| Within +/-10 bps band  | 10 to 4 bps |   293 |   293 |   1,932.15 |    785.66 |  1,146.49 |    0.57% |     7.85% |
| Within +/-10 bps band  | 10 to 5 bps |   293 |   293 |   1,932.15 |    968.55 |    963.60 |    0.48% |     7.85% |
| Within +/-10 bps band  | 10 to 6 bps |   293 |   293 |   1,932.15 |  1,160.41 |    771.74 |    0.38% |     7.85% |
| Within +/-10 bps band  | 10 to 7 bps |   293 |   293 |   1,932.15 |  1,351.42 |    580.74 |    0.29% |     7.85% |
| Within +/-10 bps band  | 10 to 8 bps |   293 |   293 |   1,932.15 |  1,545.67 |    386.48 |    0.19% |     7.85% |
| Within +/-10 bps band  | 10 to 9 bps |   293 |   293 |   1,932.15 |  1,738.95 |    193.21 |    0.10% |     7.85% |
| Outside +/-10 bps band | 10 to 0 bps | 3,439 | 3,261 |  29,546.85 | 10,991.96 | 18,554.89 |    9.18% |    92.15% |
| Outside +/-10 bps band | 10 to 1 bps | 3,439 | 3,261 |  29,546.85 | 10,992.46 | 18,554.39 |    9.18% |    92.15% |
| Outside +/-10 bps band | 10 to 2 bps | 3,439 | 3,261 |  29,546.85 | 11,339.42 | 18,207.42 |    9.01% |    92.15% |
| Outside +/-10 bps band | 10 to 3 bps | 3,439 | 3,261 |  29,546.85 | 12,199.40 | 17,347.45 |    8.58% |    92.15% |
| Outside +/-10 bps band | 10 to 4 bps | 3,439 | 3,261 |  29,546.85 | 13,377.45 | 16,169.40 |    8.00% |    92.15% |
| Outside +/-10 bps band | 10 to 5 bps | 3,439 | 3,261 |  29,546.85 | 15,736.43 | 13,810.41 |    6.83% |    92.15% |
| Outside +/-10 bps band | 10 to 6 bps | 3,439 | 3,261 |  29,546.85 | 18,269.54 | 11,277.30 |    5.58% |    92.15% |
| Outside +/-10 bps band | 10 to 7 bps | 3,438 | 3,260 |  29,545.14 | 20,886.05 |  8,659.10 |    4.28% |    92.15% |
| Outside +/-10 bps band | 10 to 8 bps | 3,438 | 3,260 |  29,545.14 | 23,544.23 |  6,000.92 |    2.97% |    92.15% |
| Outside +/-10 bps band | 10 to 9 bps | 3,438 | 3,260 |  29,545.14 | 26,588.69 |  2,956.46 |    1.46% |    92.15% |

**ETH.ETH:**

| Case                   |       Floor |  Legs | Txids | Actual fee | Projected | Fee diff | Diff/all | Leg share |
| ---------------------- | ----------: | ----: | ----: | ---------: | --------: | -------: | -------: | --------: |
| All candidates         | 10 to 0 bps | 2,661 | 2,659 |  12,231.95 |  5,673.34 | 6,558.61 |    3.25% |   100.00% |
| All candidates         | 10 to 1 bps | 2,661 | 2,659 |  12,231.95 |  5,673.34 | 6,558.61 |    3.25% |   100.00% |
| All candidates         | 10 to 2 bps | 2,661 | 2,659 |  12,231.95 |  5,724.64 | 6,507.31 |    3.22% |   100.00% |
| All candidates         | 10 to 3 bps | 2,661 | 2,659 |  12,231.95 |  5,850.94 | 6,381.01 |    3.16% |   100.00% |
| All candidates         | 10 to 4 bps | 2,661 | 2,659 |  12,231.95 |  6,164.00 | 6,067.95 |    3.00% |   100.00% |
| All candidates         | 10 to 5 bps | 2,661 | 2,659 |  12,231.95 |  6,711.87 | 5,520.08 |    2.73% |   100.00% |
| All candidates         | 10 to 6 bps | 2,661 | 2,659 |  12,231.95 |  7,644.76 | 4,587.19 |    2.27% |   100.00% |
| All candidates         | 10 to 7 bps | 2,661 | 2,659 |  12,231.95 |  8,671.48 | 3,560.47 |    1.76% |   100.00% |
| All candidates         | 10 to 8 bps | 2,661 | 2,659 |  12,231.95 |  9,809.73 | 2,422.22 |    1.20% |   100.00% |
| All candidates         | 10 to 9 bps | 2,661 | 2,659 |  12,231.95 | 11,006.00 | 1,225.96 |    0.61% |   100.00% |
| Within +/-10 bps band  | 10 to 0 bps |   211 |   211 |     793.47 |    320.19 |   473.28 |    0.23% |     7.93% |
| Within +/-10 bps band  | 10 to 1 bps |   211 |   211 |     793.47 |    320.19 |   473.28 |    0.23% |     7.93% |
| Within +/-10 bps band  | 10 to 2 bps |   211 |   211 |     793.47 |    326.68 |   466.79 |    0.23% |     7.93% |
| Within +/-10 bps band  | 10 to 3 bps |   211 |   211 |     793.47 |    340.03 |   453.44 |    0.22% |     7.93% |
| Within +/-10 bps band  | 10 to 4 bps |   211 |   211 |     793.47 |    371.15 |   422.32 |    0.21% |     7.93% |
| Within +/-10 bps band  | 10 to 5 bps |   211 |   211 |     793.47 |    418.02 |   375.45 |    0.19% |     7.93% |
| Within +/-10 bps band  | 10 to 6 bps |   211 |   211 |     793.47 |    487.54 |   305.93 |    0.15% |     7.93% |
| Within +/-10 bps band  | 10 to 7 bps |   211 |   211 |     793.47 |    556.52 |   236.96 |    0.12% |     7.93% |
| Within +/-10 bps band  | 10 to 8 bps |   211 |   211 |     793.47 |    634.55 |   158.92 |    0.08% |     7.93% |
| Within +/-10 bps band  | 10 to 9 bps |   211 |   211 |     793.47 |    714.12 |    79.35 |    0.04% |     7.93% |
| Outside +/-10 bps band | 10 to 0 bps | 2,450 | 2,449 |  11,438.48 |  5,353.15 | 6,085.33 |    3.01% |    92.07% |
| Outside +/-10 bps band | 10 to 1 bps | 2,450 | 2,449 |  11,438.48 |  5,353.15 | 6,085.33 |    3.01% |    92.07% |
| Outside +/-10 bps band | 10 to 2 bps | 2,450 | 2,449 |  11,438.48 |  5,397.96 | 6,040.52 |    2.99% |    92.07% |
| Outside +/-10 bps band | 10 to 3 bps | 2,450 | 2,449 |  11,438.48 |  5,510.92 | 5,927.56 |    2.93% |    92.07% |
| Outside +/-10 bps band | 10 to 4 bps | 2,450 | 2,449 |  11,438.48 |  5,792.85 | 5,645.63 |    2.79% |    92.07% |
| Outside +/-10 bps band | 10 to 5 bps | 2,450 | 2,449 |  11,438.48 |  6,293.85 | 5,144.63 |    2.55% |    92.07% |
| Outside +/-10 bps band | 10 to 6 bps | 2,450 | 2,449 |  11,438.48 |  7,157.22 | 4,281.26 |    2.12% |    92.07% |
| Outside +/-10 bps band | 10 to 7 bps | 2,450 | 2,449 |  11,438.48 |  8,114.97 | 3,323.51 |    1.64% |    92.07% |
| Outside +/-10 bps band | 10 to 8 bps | 2,450 | 2,449 |  11,438.48 |  9,175.18 | 2,263.30 |    1.12% |    92.07% |
| Outside +/-10 bps band | 10 to 9 bps | 2,450 | 2,449 |  11,438.48 | 10,291.87 | 1,146.61 |    0.57% |    92.07% |

**ETH.USDT:**

| Case                   |       Floor | Legs | Txids | Actual fee | Projected | Fee diff | Diff/all | Leg share |
| ---------------------- | ----------: | ---: | ----: | ---------: | --------: | -------: | -------: | --------: |
| All candidates         | 10 to 0 bps |  853 |   853 |   1,720.19 |    944.68 |   775.51 |    0.38% |   100.00% |
| All candidates         | 10 to 1 bps |  853 |   853 |   1,720.19 |    944.71 |   775.48 |    0.38% |   100.00% |
| All candidates         | 10 to 2 bps |  853 |   853 |   1,720.19 |    956.20 |   763.99 |    0.38% |   100.00% |
| All candidates         | 10 to 3 bps |  853 |   853 |   1,720.19 |    980.46 |   739.73 |    0.37% |   100.00% |
| All candidates         | 10 to 4 bps |  853 |   853 |   1,720.19 |  1,017.56 |   702.64 |    0.35% |   100.00% |
| All candidates         | 10 to 5 bps |  853 |   853 |   1,720.19 |  1,089.17 |   631.02 |    0.31% |   100.00% |
| All candidates         | 10 to 6 bps |  853 |   853 |   1,720.19 |  1,175.05 |   545.15 |    0.27% |   100.00% |
| All candidates         | 10 to 7 bps |  853 |   853 |   1,720.19 |  1,270.73 |   449.46 |    0.22% |   100.00% |
| All candidates         | 10 to 8 bps |  853 |   853 |   1,720.19 |  1,394.25 |   325.94 |    0.16% |   100.00% |
| All candidates         | 10 to 9 bps |  853 |   853 |   1,720.19 |  1,543.36 |   176.83 |    0.09% |   100.00% |
| Within +/-10 bps band  | 10 to 0 bps |  225 |   225 |     285.11 |    110.41 |   174.70 |    0.09% |    26.38% |
| Within +/-10 bps band  | 10 to 1 bps |  225 |   225 |     285.11 |    110.43 |   174.69 |    0.09% |    26.38% |
| Within +/-10 bps band  | 10 to 2 bps |  225 |   225 |     285.11 |    115.92 |   169.19 |    0.08% |    26.38% |
| Within +/-10 bps band  | 10 to 3 bps |  225 |   225 |     285.11 |    125.65 |   159.46 |    0.08% |    26.38% |
| Within +/-10 bps band  | 10 to 4 bps |  225 |   225 |     285.11 |    138.72 |   146.40 |    0.07% |    26.38% |
| Within +/-10 bps band  | 10 to 5 bps |  225 |   225 |     285.11 |    158.44 |   126.67 |    0.06% |    26.38% |
| Within +/-10 bps band  | 10 to 6 bps |  225 |   225 |     285.11 |    179.14 |   105.98 |    0.05% |    26.38% |
| Within +/-10 bps band  | 10 to 7 bps |  225 |   225 |     285.11 |    201.76 |    83.35 |    0.04% |    26.38% |
| Within +/-10 bps band  | 10 to 8 bps |  225 |   225 |     285.11 |    227.95 |    57.16 |    0.03% |    26.38% |
| Within +/-10 bps band  | 10 to 9 bps |  225 |   225 |     285.11 |    256.37 |    28.74 |    0.01% |    26.38% |
| Outside +/-10 bps band | 10 to 0 bps |  628 |   628 |   1,435.08 |    834.27 |   600.81 |    0.30% |    73.62% |
| Outside +/-10 bps band | 10 to 1 bps |  628 |   628 |   1,435.08 |    834.29 |   600.79 |    0.30% |    73.62% |
| Outside +/-10 bps band | 10 to 2 bps |  628 |   628 |   1,435.08 |    840.28 |   594.80 |    0.29% |    73.62% |
| Outside +/-10 bps band | 10 to 3 bps |  628 |   628 |   1,435.08 |    854.81 |   580.27 |    0.29% |    73.62% |
| Outside +/-10 bps band | 10 to 4 bps |  628 |   628 |   1,435.08 |    878.84 |   556.24 |    0.28% |    73.62% |
| Outside +/-10 bps band | 10 to 5 bps |  628 |   628 |   1,435.08 |    930.73 |   504.35 |    0.25% |    73.62% |
| Outside +/-10 bps band | 10 to 6 bps |  628 |   628 |   1,435.08 |    995.91 |   439.17 |    0.22% |    73.62% |
| Outside +/-10 bps band | 10 to 7 bps |  628 |   628 |   1,435.08 |  1,068.97 |   366.11 |    0.18% |    73.62% |
| Outside +/-10 bps band | 10 to 8 bps |  628 |   628 |   1,435.08 |  1,166.30 |   268.78 |    0.13% |    73.62% |
| Outside +/-10 bps band | 10 to 9 bps |  628 |   628 |   1,435.08 |  1,286.99 |   148.09 |    0.07% |    73.62% |

**Takeaways from this sample:**

1. Within the +/-10 bps estimator-only analysis band, changing the effective min-slip floor from the default 10 bps to 0 bps has a maximum simulated fee-collection drawdown of ~2,321 RUNE over the 30 sampled hours.
2. For all simulated candidate legs, the fee difference ranges from 14.15% of all observed swap-leg fees at a 0 bps dynamic arb floor to 2.41% at a 9 bps floor; at 5 bps it is 10.85%.
3. For candidates that begin within the +/-10 bps estimator-only analysis band, the fee difference ranges from 1.15% of all observed swap-leg fees at a 0 bps dynamic arb floor to 0.17% at a 9 bps floor; at 5 bps it is 0.82%.
4. The 0 bps and 1 bps projected rows are nearly identical in this sample. Lowering the floor from 1 bps to 0 bps changes the simulated fee result by 0.54 RUNE across all candidates and 0.01 RUNE within the +/-10 bps estimator-only analysis band.

## Decision

Introduce Dynamic Arb Min Fee for eligible trade-account and secured-asset swap legs.

A swap leg is eligible for lower effective min-slip bps when:

1. The leg is a trade-account or secured-asset leg.
2. The leg moves the underlying pool price closer to the external reference price.

The rule is based on price improvement and per-asset dynamic arb eligibility, not app identity.

The fee treatment remains limited to eligible trade-account and secured-asset legs, but the epoch controller evaluates total observed pool protocol fee revenue from all swap legs for that pool asset.

### Core Eligibility

For a given pool and oracle/reference price:

```text
pre_deviation  = abs(pool_price_before - reference_price)
post_deviation = abs(pool_price_after - reference_price)
```

A swap leg is price-correcting if:

```text
post_deviation_bps + min_improvement_bps <= pre_deviation_bps
```

Crossing the reference price is allowed. The important condition is that the final absolute deviation is smaller than the starting absolute deviation by at least the configured minimum improvement (e.g., 1 bps).

Example:

```text
before: pool is 10 bps above reference price
after:  pool is 2 bps below reference price
```

This qualifies because the pool is closer to the reference price after the swap.

The lower fee does not apply if:

1. The swap makes the pool price farther from the reference price.
2. The current block has no accepted Bifrost oracle price for the pool asset or the RUNE/USD denominator.
3. The pool is halted or otherwise in an unsafe state.
4. The trade depends on an asset that is not enabled for dynamic arb min fee adjustments.
5. The leg is not a trade-account or secured-asset leg.

### Dynamic Arb Min Fee Shape

Dynamic arb bps is maintained as per-asset active state by an epoch controller. A qualifying trade-account or secured-asset leg uses the active dynamic arb min fee value for the non-RUNE pool asset whose price is being corrected.

Only the active dynamic arb value is per asset. The controller configuration is common across all assets, and per-asset configuration is limited to feature enablement and oracle/reference-price mapping.

The moving parts are intentionally small. For example:

```text
base_trade_or_secured_min_slip_bps:  10
active_dynamic_arb_min_fee_bps:      epoch-calculated state for pool asset
dynamic_arb_step_bps:                Mimir controlled
window_epoch_duration_in_blocks:     Mimir controlled
min_improvement_bps:                 Mimir controlled
```

The effective bps for a qualifying price-correcting leg is:

```text
effective_min_slip_bps = active_dynamic_arb_min_fee_bps_for_pool_asset
```

The effective bps for a non-qualifying leg remains:

```text
effective_min_slip_bps = base_min_slip_bps
```

Actual fee calculation must still use the existing THORChain rule:

```text
effective_swap_slip_bps = max(natural_swap_slip_bps, effective_min_slip_bps)
```

Therefore lost minimum-slip revenue is not simply `base_bps - dynamic_arb_bps`. It is the deterministic difference between the base-fee calculation and the effective-fee calculation for the same leg.

The mechanism uses one active dynamic arb value per enabled asset. These values are controller state, not per-asset Mimir settings.

### Whole Sub-Swap Eligibility Only

The mechanism must not attempt partial price-correcting portion accounting.

A qualifying lower fee applies only when the whole executed sub-swap leg improves final absolute pool-vs-reference price deviation by at least `min_improvement_bps`.

If the sub-swap is too large and ends farther from the reference price, the entire leg uses normal bps. If the sub-swap crosses the reference price but ends closer than it started, the leg may qualify.

Streaming swaps already provide the natural way to split large trades into smaller sub-swaps. The dynamic arb min fee mechanism evaluates each executed sub-swap independently.

### Execution Safety

The implementation must ensure that the lower fee is granted only if the post-swap state satisfies the price-correcting condition.

The implementation is:

1. Load the pre-swap pool state.
2. Calculate the normal/base min-slip result.
3. Calculate a candidate dynamic arb min fee result from the same pre-swap state.
4. Derive the candidate post-swap pool state, including the liquidity-fee-in-RUNE deduction that current swap execution applies before storing the pool.
5. Compare pre-swap and candidate post-swap pool price deviation against the current reference price.
6. Execute the leg with the dynamic arb min fee only if the candidate post-state is price-correcting.
7. Otherwise execute the leg with normal/base min slip.

### Double Swaps

For asset-to-asset double swaps, each pool leg is evaluated separately.

If both legs are eligible trade/secured legs, price-correcting, and have valid reference prices, both legs may receive dynamic arb bps.

If only one leg is eligible and price-correcting, only that leg receives dynamic arb bps.

If either leg lacks an accepted current-block reference price or is not an eligible trade/secured leg, that leg uses normal bps.

### Oracle and Reference Price Requirements

Dynamic arb min fee adjustments depend on reference prices. The reference source is Bifrost's enshrined oracle price feed.

A reference price is valid only when it is present in the current block's oracle price store. Under this current-block-only rule, no additional persisted oracle height or age metadata is required: if the price is not present, the dynamic arb min fee is unavailable.

This ADR does not define an additional oracle quality score. It relies on the existing Bifrost and Thornode oracle acceptance path: provider outlier filtering, signed and non-stale price feeds, medianization, and supermajority persistence. If that path does not produce a price in the current block, the dynamic arb min fee is unavailable.

Each asset has:

1. A deterministic mapping from the whitelisted THORChain pool asset to the Bifrost oracle symbol.
2. An accepted-current-block oracle price.
3. A Mimir or governance control to enable or disable the feature for that asset.

RUNE is required as the reference denominator when comparing pool price to the external reference price. The implementation requires an accepted-current-block Bifrost RUNE/USD oracle price, but RUNE itself is not an eligible dynamic arb trade-account or secured asset. Dynamic arb asset enablement applies to the non-RUNE pool asset whose price is being corrected.

### Closed-Loop Epoch Controller

The mechanism uses the same closed-loop principle as THORChain MR !4752/ADR-026 "Dynamic L1 min fee per thorname" (https://gitlab.com/thorchain/thornode/-/merge_requests/4752): move the fee lever, measure whether total pool protocol fee revenue improved, continue when the move helped, and reverse when it did not. This is the same kind of mechanism for finding the bps point that produces the best observed total pool protocol revenue.

The controller optimizes each enabled asset's active dynamic arb min fee value independently. Each asset initializes at the applicable base min-slip bps, so the first controller movement is a conservative downward probe.

Each epoch accumulates the following values per enabled asset:

1. `pool_fees_tor`: actual protocol fee revenue from all swap legs executed against that pool asset, converted from RUNE to TOR through `DollarsPerRune`.

Only `pool_fees_tor` is the primary controller objective. Other values such as pool volume and fee-reduction cost are left to events and telemetry.

When the previous controller move lowered the dynamic arb floor, that lower floor is treated as successful only if the completed epoch produces higher `pool_fees_tor` than the prior higher-bps epoch. If total pool fees do not improve, the next controller move reverses upward by `DynamicArbMinFeeStepBps`.

Current-block oracle unavailability and other operational diagnostics can be exposed through telemetry. They do not need to be persisted in the controller's epoch history unless a future controller rule consumes them directly.

When `DollarsPerRune` is zero, TOR-denominated revenue is not credited into the asset's controller history for that swap. Recording zero would create a false negative revenue signal.

At each epoch boundary, defined by `DynamicArbMinFeeWindowEpochDurationInBlocks`, for each enabled asset:

1. Append an epoch record containing the asset, active dynamic arb bps, and accumulated metrics.
2. If there is no prior active-bps change for that asset, probe downward by `DynamicArbMinFeeStepBps` from the current active value and stop evaluating that asset for this epoch.
3. Find the most recent epoch where that asset's active dynamic arb bps changed.
4. Determine the direction of that change: `+1` means the floor moved up, `-1` means the floor moved down.
5. Compare `pool_fees_tor` before and after that change using the completed epoch windows for that asset.
6. Compute `delta_pct_bps = abs(pool_fees_after - pool_fees_before) * 10000 / pool_fees_before`.
7. If there is no baseline or `pool_fees_before` is zero, hold the active dynamic arb bps unchanged.
8. If pool fees increased, continue in the previous direction by `DynamicArbMinFeeStepBps`.
9. If pool fees decreased, reverse the previous direction by `DynamicArbMinFeeStepBps`.
10. If pool fees are unchanged, hold the active dynamic arb bps unchanged.
11. Clamp the asset's active dynamic arb bps into the implicit range `0..current_base_min_slip_bps`.

This defines the net-positive threshold per asset. A lower dynamic arb floor for an asset that does not generate enough additional pool activity to increase total pool protocol fees causes that asset's controller to reverse toward a higher floor.

### Precedence and Base Min-Slip Interaction

This ADR is independent from `ADR 027 App-Funded Min-Slip Subsidy Vaults` (https://gitlab.com/thorchain/thornode/-/merge_requests/4792).

For each swap leg, the implementation first reads the applicable base min-slip value from the existing config path: `TradeAccountsSlipMinBps` for trade-account legs and `SecuredAssetSlipMinBps` for secured-asset legs.

The dynamic arb min fee never replaces those base Mimir values. It can only lower the effective min-slip bps for an eligible price-correcting leg.

The two mechanisms may coexist:

```text
app subsidy path:
  lower bps funded by an app's prepaid vault

dynamic arb path:
  lower bps granted because the trade improves pool price
```

Evaluation order is:

1. Calculate the base-fee result using the current base min-slip bps.
2. If dynamic arb eligibility is satisfied, calculate the dynamic-arb-fee result using the active dynamic arb bps, capped at the current base min-slip bps.
3. If an app-funded subsidy also applies, calculate the app-fee result according to ADR-027.
4. Execute the leg with the lowest valid effective bps among base, dynamic arb, and app-funded subsidy.

Accounting attribution remains separate:

- If the app-funded bps is lower than or equal to the dynamic arb bps, the app subsidy covers the full difference between the base-fee result and the app-fee result, and protocol dynamic arb cost is zero.
- If the dynamic arb bps is lower than the app-funded bps, the app subsidy covers the difference between the base-fee result and the app-fee result, while protocol dynamic arb cost covers only the additional difference between the app-fee result and the dynamic-arb-fee result.
- If no app subsidy applies, protocol dynamic arb cost is the difference between the base-fee result and the dynamic-arb-fee result.

If the base min-slip Mimir changes during an epoch, swap execution uses the current block's base value. At epoch boundaries, the active dynamic arb bps is clamped into `0..current_base_min_slip_bps`.

This separation avoids confusing commercial app subsidies with protocol-level price-quality incentives.

If natural swap slip is already above the dynamic arb floor, the protocol cost may be lower than the bps difference suggests. For example, with base min slip 10 bps, dynamic arb min fee 5 bps, and natural swap slip 7 bps, the effective dynamic arb slip is 7 bps, not 5 bps.

## Detailed Design

### Parameters

The implementation uses parameters equivalent to:

```go
type DynamicArbMinFeeConfig struct {
    Enabled                     bool
    StepBps                     uint64
    WindowEpochDurationInBlocks uint64
    MinImprovementBps           uint64
}

type DynamicArbMinFeeAssetState struct {
    Asset            common.Asset
    ActiveSlipMinBps uint64
    History          []DynamicArbMinFeeEpochRecord
}

type DynamicArbMinFeeEpochRecord struct {
    Epoch               uint64
    Asset               common.Asset
    ActiveSlipMinBps    uint64
    PoolFeesTor         cosmos.Uint
}

type DynamicArbMinFeeAssetConfig struct {
    Asset        common.Asset
    Enabled      bool
    OracleSymbol string
}
```

Exact names and storage layout are implementation details.

### Swap Evaluation

For each eligible swap leg:

1. Load pool state before execution.
2. Load the pool asset reference price and the RUNE/USD denominator reference price.
3. Reject dynamic arb min fee eligibility if the current-block reference price is unavailable or disabled.
4. Calculate the base-fee result using the current base min-slip value.
5. Calculate the candidate dynamic arb fee result using the active dynamic arb min fee value for the pool asset, capped at the current base min-slip value.
6. Derive the candidate dynamic arb post-swap pool state.
7. Calculate `pre_deviation` and candidate dynamic arb `post_deviation`.
8. If `post_deviation + min_improvement_bps <= pre_deviation`, mark the dynamic arb fee result as valid.
9. If an app-funded subsidy applies, calculate the app-fee result according to ADR-027.
10. Execute or commit the lowest valid effective bps among base, dynamic arb, and app-funded subsidy.
11. If the dynamic arb fee result is used, emit or attach compact fee adjustment attributes with asset, pre-deviation, post-deviation, base bps, effective bps, and fee reduction value.

### Crossing Rules

Crossing the reference price is permitted if the final absolute deviation is lower than the initial absolute deviation.

The crossing guard is:

```text
post_deviation_bps + min_improvement_bps <= pre_deviation_bps
```

`min_improvement_bps` is read from `DynamicArbMinFeeMinImprovementBps`. Operators may set it to `1` as a pure rounding and dust guard or raise it to require a more material pool-price improvement before a trade receives the lower fee.

### Queue Ordering

Dynamic arb eligibility does not allow a swap to bypass existing economic scoring. This ADR changes fee treatment, not priority execution.

### Mimir Controls

Nodes control the mechanism through these Mimir values:

```text
DynamicArbMinFeeEnabled
DynamicArbMinFeeAssetEnabled-{asset}
DynamicArbMinFeeStepBps
DynamicArbMinFeeWindowEpochDurationInBlocks
DynamicArbMinFeeMinImprovementBps
```

`DynamicArbMinFeeStepBps`, `DynamicArbMinFeeWindowEpochDurationInBlocks`, and `DynamicArbMinFeeMinImprovementBps` are common Mimir values. Per-asset Mimir controls only enable or disable eligibility for a specific asset.

### Observability

The chain-level observability should stay close to ADR-026 / MR !4752: expose the controller state and emit compact events needed to reconstruct controller behavior, while leaving broader analytics to indexers.

Required chain-visible data:

1. Queryable per-asset controller state: active dynamic arb bps, current-epoch `pool_fees_tor`, and bounded epoch history.
2. A compact per-adjustment event or swap-event attributes when the dynamic arb min fee is granted: asset, pre-deviation, post-deviation, base bps, effective bps, and fee reduction value.
3. An epoch-boundary update event when active dynamic arb bps changes: asset, epoch, old bps, new bps, `pool_fees_before`, `pool_fees_after`, `delta_pct_bps`, and reason: cold-start probe, hold, continue down, continue up, reverse down, or reverse up.

Recommended THORChain telemetry counters and gauges:

1. Dynamic arb min fee granted and denied counts by asset and reason.
2. Current-block oracle unavailability count by asset.
3. Total pool fee revenue by asset.
4. Dynamic arb fee-reduction cost by asset and epoch.
5. Eligible-flow fee and volume split between dynamic-arb-eligible flow and other swap flow.
6. Epoch decision reason counts by asset.

## Consequences

### Positive

1. Helps pools trade closer to external market prices.
2. Improves swap quote competitiveness.
3. Makes the benefit available to any price-correcting trade/secured-asset arbitrageur, not only whitelisted apps.
4. Uses per-asset active fee behavior while retaining common controller configuration and simple per-asset enablement.
5. Uses closed-loop revenue feedback so lower bps must prove itself through higher TOR-denominated total pool protocol fees.

### Negative

1. Requires reliable reference prices.
2. Adds complexity to swap execution.
3. Relies on Bifrost and Thornode oracle acceptance and current-block price availability.
4. May reduce per-swap minimum fee revenue if not offset by higher volume or better price quality.
5. Requires additional per-asset controller state and telemetry to evaluate the revenue signal.
6. Total pool fee revenue can be noisy because unrelated market flow can affect the epoch-to-epoch signal.

### Neutral

1. This mechanism does not replace app-funded subsidy vaults (ADR 027). It addresses pool price precision, not commercial app revenue sharing.
2. The mechanism is limited to trade/secured assets with reliable reference prices and meaningful liquidity.
3. Node operators retain the ability to disable or raise bps through Mimir.

## Open Questions

1. Should ordinary L1 legs become eligible?

## References

1. THORChain MR !4731: https://gitlab.com/thorchain/thornode/-/merge_requests/4731
2. Advanced swap queue execution: https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/manager_adv_swap_queue_current.go
3. Advanced swap queue sorting: https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/manager_swap_queue.go
4. ADR 020 App-layer: https://gitlab.com/whys1/thornode/-/blob/d0409bee3b8df11219241863bdff58976cb5b0f6/docs/architecture/adr-020-app-layer.md
5. Live THORChain Mimir endpoint: https://gateway.liquify.com/chain/thorchain_api/thorchain/mimir
6. THORChain MR !4752 Dynamic L1 Min Fee Per Thorname & Pair: https://gitlab.com/thorchain/thornode/-/merge_requests/4752
7. ADR 026 Dynamic L1 Min Fee Per Thorname & Pair: https://gitlab.com/thorchain/thornode/-/blob/develop/docs/architecture/adr-026-dynamic-l1-min-fee-per-thorname.md
8. ADR 027 App funded min-slip subsidy vaults: https://gitlab.com/thorchain/thornode/-/merge_requests/4792
9. Dynamic arb min fee simulation and public-gateway estimator: `docs/architecture/adr-028-dynamic-arb-min-fee_test.go`
