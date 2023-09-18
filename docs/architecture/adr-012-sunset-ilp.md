# ADR 012: Sunset Impermanent Loss Protection (ILP)

## Context

Impermanent Loss Protection (ILP) was a feature introduced in the early stages of the protocol to incentivize and bootstrap liquidity. The core idea behind ILP was to mitigate the potential downside for liquidity providers (LPs) by compensating them in the event of impermanent loss.

**Key Points:**

- Total payout to LPs under the ILP feature: 6,536,602 RUNE.
- ILP deprecated since block: 9450000, dated 2023-01-07.

**Resources:**

- ILP data can be found on this dashboard: https://dashboards.ninerealms.com/#thorchain
- python code: https://gitlab.com/-/snippets/2533191

## Rationale

1. **Successful Bootstrapping:** The primary purpose of introducing ILP was to attract early liquidity providers. With over 6.5 million RUNE paid out, it's evident that the feature served its intended purpose and helped in amassing the desired liquidity.

1. **Diminishing ILP Liability:** In the initial stages post-deprecation, LPs that deposited liquidity before the cut-off block were still covered by ILP. This ensured continued liquidity for the protocol. However, as the protocol matured, the ILP liability (i.e., the potential RUNE that the protocol might need to pay out due to impermanent loss) has shown a significant reduction and is now nearing zero. This indicates that most of the eligible LPs aren't experiencing impermanent loss.

1. **Reduced Need for ILP:** As the protocol matured and gained traction, the risks and market fluctuations that necessitated ILP have diminished. Most early LP participants have now seen profits, making ILP less relevant.

1. **Future Liability Concerns:** Keeping the ILP feature intact, even when it's not actively being used, poses a potential liability risk for the protocol. Sunsetting ILP would ensure that the protocol isn't exposed to unforeseen liabilities in the future.

## Criteria for Sunsetting

Before discontinuing ILP, it's imperative to ensure that the protocol's liability towards ILP is minimal. Therefore, the criteria set for sunsetting the feature is:

- ILP liability should be less than 100k RUNE.

## Actions

Once the above criterion is met:

1. **Node Voting:** Nodes will vote on the proposal to sunset ILP.

   > Mimir Action: Update “ILPCUTOFF" from block 9450000 to block 0.

1. **Code Cleanup:** A subsequent Pull Request (PR) will be raised to remove all ILP-related code from the thornode repository. This step ensures that there's no residual code that could cause potential issues or conflicts in the future.

## Consequences

1. **Reduced Protocol Liability:** (major) By sunsetting ILP, the protocol eliminates a potential source of liability.

1. **Simplified Codebase:** (minor) Removing ILP-related code will make the thornode codebase leaner and reduce complexity.

## Conclusion

The Impermanent Loss Protection feature played a crucial role in the early stages of the protocol. However, given its reduced relevance and potential future liabilities, it's in the best interest of the protocol and its stakeholders to sunset ILP.
