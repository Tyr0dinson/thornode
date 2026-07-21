# ADR 031: THORChain x Rujira - Path Forward

## Changelog

- 21/07/2026: Created

## Status

Proposed

## Intro

Following recent discussions around ADR27, the Rujira team wants to offer nodes
the opportunity to vote on their preferred path forward for the THORChain x
Rujira cooperation, and to confirm long-term alignment and a shared vision.

The recent exploit of one of the Asgard vaults, which resulted in approximately
$10m of losses, also highlighted the need to define clearer guidelines and
responsibilities in case of similar future events. The timing also coincides with
the end of the 12-month non-compete for Rujira, as well as discussions around
POL and bootstrapping liquidity for the App Layer.

This ADR addresses each of these points. It is a draft proposal, and may change
after community feedback ahead of a node vote.

The proposal presents three possible options:

- **Option 1 - Confirm and reinforce the cooperation:** work hand-in-hand as a
  joint venture to grow the overall system and advance a shared vision. Maintain
  the existing exclusivity arrangement, with revenue shared equally across
  THORChain, Rujira, and protocol-owned liquidity on Rujira. Set
  `WasmArbSlipMinBps` to `0` to minimize friction in liquidity transmission
  between the Base Layer and the App Layer. Protect Secured Assets and strengthen
  user confidence.
- **Option 2 - Limited cooperation:** treat Rujira as one of many THORChain
  users, rather than as an extension of THORChain. Open the door for other teams
  and reduce the share of revenue going to THORChain. Keep friction in liquidity
  transmission high by setting `WasmArbSlipMinBps` at the same level as
  `TradeAccountsSlipMinBps`. Do not provide particular protection for Secured
  Assets.
- **Option 3 - Discontinue the App Layer on THORChain:** decide that the App
  Layer is not part of THORChain's future. Rujira would continue building its
  products and pursuing its vision in a different place.

As a team, Rujira strongly supports Option 1. Chad and Aaluxx have expressed
their support too. This has been the intention from the beginning: Rujira should
not be treated as just one of many THORChain users, but as a project built to
support THORChain, expand its capabilities, attract a new wave of users, and
ultimately grow system income.

Option 2 would be a disappointing outcome, but Rujira could work with it. Option
3 would require a major rethink of Rujira's plans and is the least preferred
option. Based on general community and node sentiment, Rujira does not believe it
will have support, but includes it for completeness.

## Option 1 - Confirm and Reinforce the Cooperation

### Vision

This is the vision Rujira is advocating for: THORChain and Rujira as two sides
of the same coin, working as a joint venture, complementing each other, and
advancing a shared goal of building a truly censorship-resistant, permissionless
financial ecosystem. The goal is to separate money from the state, level the
playing field, and give people a fair shot at financial freedom.

THORChain's primary focus should be security and infrastructure. It protects the
native assets in the Asgard vaults, including liquidity in the Base Layer pools
and Secured Assets used on the App Layer. It provides the infrastructure
necessary for the App Layer to function, such as Bifrost for Secured Asset
deposits and withdrawals, compute, enshrined oracles, and the on-chain
scheduler. The Base Layer should optimize for robustness and security, and leave
more complex features such as true orderbook and concentrated liquidity to the
App Layer. Any changes impacting Base Layer swap mechanics should be done in
consultation with Rujira, as these mechanics are tightly coupled with App Layer
liquidity and can create system-wide consequences.

Rujira builds the core applications that make up the App Layer: a fully
integrated suite of DeFi primitives including an orderbook DEX, advanced
automated market-making strategies such as Custom Concentrated Liquidity and
Dynamic Concentrated Liquidity, lending and borrowing, bRUNE, perps, a
stablecoin, and additional products on the [roadmap](http://rujira.network/roadmap).
The orderbook DEX sits at the center of this ecosystem. It is connected to the
Base Layer pools through the Virtualization Strategy, which helps support Base
Layer quotes and settlement time by injecting liquidity from concentrated
strategies on the App Layer.

The App Layer products are deeply connected, with each new product strengthening
the others and bringing in more users, activity, and revenue across the
ecosystem. The first year of building was mainly about getting the foundation
right. Now that the products are getting closer to broader adoption, activity is
expected to increase materially. This had already started to show in the days
before the Asgard vault exploit, with bRUNE and the early stages of arbitraging
the Base Layer against CCL liquidity.

In the last 10 days before the exploit, RUJI Trade generated the same amount of
revenue as in the preceding roughly 75 days. This growth is expected to increase
further as more liquidity moves into the App Layer and with the launch of DCL and
Perps v2.

### WasmArbSlipMinBps

To achieve this vision, friction should be minimized as much as possible when
moving liquidity between the Base Layer and the App Layer through the
Virtualization Strategy. This can be achieved by setting the newly introduced
`WasmArbSlipMinBps` to `0`.

This does not remove the slip fee entirely. Instead, it sets the minimum floor to
`0`, rather than the standard `SecuredAssetSlipMinBps`, allowing more frequent
and smaller swaps between the App Layer and the Base Layer. This should keep
prices more tightly aligned with market prices and internalize a share of the
arbitrage profits.

An important second-order effect is that this should increase trading volumes
between the Base Layer and CCL positions on the App Layer. This would make the
APR on CCL positions more attractive for users, helping attract additional
liquidity, which in turn would generate more revenue for THORChain and Rujira,
and support pricing of Base Layer pools.

Some community members and arbitrageurs have raised the concern that this could
potentially penalize THORChain's revenue. While Rujira does not expect this to
be the case, Rujira would support revisiting the parameter if data shows that it
negatively impacts THORChain's total revenue. The goal is not to take anything
away from the Base Layer, but to grow the total system income pie together.

Even at scale, the system will not fully replace external arbitrageurs. Liquidity
on the App Layer acts as a first-line buffer to rebalance the Base Layer pools,
but once it is exhausted, external arbitrageurs are still required to rebalance
the pools.

### KPIs

Measuring success is key to good decision-making. In the case of
`WasmArbSlipMinBps`, however, the system is highly interconnected and does not
allow for clean A/B testing. It will therefore never be possible to prove with
certainty what would have happened under an alternative parameter setting. That
said, the health of the system can still be monitored through a set of
directional KPIs, which can be used to identify whether the parameter should be
revisited.

The proposed dashboards would track:

- THORChain total gross system income, with a separate breakdown showing how
  much of that comes from Rujira revenue share over time.
- Rujira Virtualization Strategy volume as a percentage of THORChain total
  volume over time.
- THORChain gross system income per $1bn of total crypto exchange volume
  (CEXs + DEXs) over time.
- THORChain volume market share over time, with a breakdown showing the share of
  main competitors.
- Execution quality metrics, notably deviation between Base Layer pool prices
  and oracle prices over time.

THORChain total gross system income is the most important KPI: if it is trending
up on a market-adjusted basis, the system is moving in the right direction.

This should be assessed in the context of the broader crypto market. If
market-wide volumes and valuations are falling, THORChain revenue may also
decline regardless of internal improvements. THORChain gross system income per
$1bn of total exchange volume attempts to address this.

Ideally, THORChain total gross system income, market-adjusted gross system
income, and Rujira's contribution to that income would all trend upward over
time.

However, if THORChain total gross system income trends down while both Rujira's
share of that income and Rujira's absolute dollar contribution are trending up,
this could be a cannibalization signal and would justify reviewing
`WasmArbSlipMinBps`. It would not prove cannibalization by itself, since it could
also reflect Rujira products gaining traction while the broader market weakens,
but it would be a clear signal that further investigation is needed.

These dashboards should be reviewed over a sufficiently long period to avoid
overreacting to short-term volatility, with any changes based on sustained
trends. The initial review should take place after the Virtualization Strategy
has been upgraded, DCL has launched, and enough time has passed to generate
meaningful data.

In any case, material impact is not expected in the short term. Liquidity on the
App Layer will need to scale, the Virtualization Strategy will need to be
upgraded, and DCL strategies will need to launch before the full effect of the
system can be properly evaluated.

### Rujira POL and Revenue Share

To grow Rujira's revenue, including THORChain's share, and improve pricing of
Base Layer pools, it is key to scale liquidity on the App Layer. To do that, a
significant share of Rujira's revenue should be redirected to a POL Fund, where
it will be used to build protocol-owned liquidity.

In this scenario, the new revenue distribution would be:

- 33.34% to Rujira POL, 50/50 owned by Rujira and THORChain.
- 33.33% to THORChain.
- 33.33% to RUJI stakers. The RUJI/RUNE LP staking plan would be scrapped, so
  100% of this share would go to single-sided RUJI stakers.

The POL build-up on Rujira would equally benefit THORChain and Rujira by
bootstrapping App Layer liquidity and facilitating sticky fee-generating
economic activity. It would be diversified across multiple products and assets
to reduce the risk and potential losses in the event of a smart contract issue.

POL is the most robust way for the protocol to generate recurring revenue over
the long term, as long as market volatility continues to exist. It should have a
compounding impact over time.

Importantly, the POL built on Rujira would act as a compensation buffer, being
first in line to absorb losses in the event of a smart contract exploit. This is
particularly important to rebuild user confidence following the recent Base Layer
exploit.

For clarity, "Rujira POL" means POL deployed on Rujira, as opposed to Base Layer
pools. The POL would be held in a dedicated POL Fund, structured as a DAO DAO
3-of-6 multisig managed by representatives of the Rujira and THORChain teams
(Hans, PM, Zefiro, Chad, Aaluxx, and Slambammer). PM would lead data-driven
capital allocation decisions, deploying liquidity where it maximizes revenue
generation for Rujira and THORChain. All assets in the POL Fund would be owned
50/50 by Rujira and THORChain.

### Responsibilities in Case of Future Exploits

DeFi users accept risk when interacting with the protocol, and there is no
guarantee that sufficient funds will be available to compensate users in the
event of a hack.

That said, if a hack occurs, the system should look to compensate users if
resources allow. This is key to rebuilding trust, particularly after recent
events.

Responsibility for covering user losses, to the extent possible, should fall to
either THORChain or Rujira depending on where the issue occurs:

- Smart contract exploits are the responsibility of Rujira.
- Base Layer exploits that result in losses to Secured Asset backing are the
  responsibility of THORChain.

In the event of a smart contract exploit, it is not expected that THORChain would
contribute to covering users' losses. However, node operators should try to
contain the exploit to the best of their ability using the appropriate Mimir
settings, such as `HaltSecuredWithdraw` if the attacker has not yet signed the
withdrawal, or `HaltSigning` if the attacker has already signed and the
transaction is in the outbound queue. To improve security, Rujira will work on
implementing a dedicated buffer system for Secured Asset withdrawals. The system
would automatically pause withdrawals above a certain threshold, giving the team
and community time to investigate suspicious activity. Once the risk has been
cleared, three nodes would be required to vote to resume the withdrawal process.

In the event of a Base Layer exploit, such as funds being stolen from an Asgard
vault, it is THORChain's primary responsibility to provide security in exchange
for its share of App Layer revenue. Under this proposal, Secured Asset users
should be treated as senior to POL and nodes in the compensation waterfall, and
THORChain should aim to make App Layer users whole to the extent reasonable. If
POL is used to absorb the loss, there should be a 20% cap on the total remaining
POL value at the time of the exploit that may be used, so THORChain's long-term
survival is not endangered.

If the 20% POL buffer is fully consumed following a hack, Secured Assets actively
deployed in smart contracts should have priority for compensation over Secured
Assets idle in wallets. The goal is to prioritize users whose assets are
contributing to fee-generating activity on Rujira. These are guidelines, and the
actual compensation plan should be assessed case by case. The App Layer might
also use some of its POL to cover a shortfall in such an event.

Rujira also supports THORChain materially increasing the share of its system
income going to POL, with a 20-30% target, to rebuild a buffer in case of future
exploits, reinforce App Layer users' trust, and maximize long-term revenue
generation.

### Collaboration on Development and Marketing

THORChain and Rujira development teams should strengthen their collaboration,
working proactively on initiatives that help each other grow. Examples include
making the Virtualization Strategy aware of the swap queue so that it can be a
counterparty to rapid swaps, or making the THORChain router capable of tapping
into App Layer liquidity directly.

Similarly, on the marketing and business development front, both teams should
work together to maximize adoption, which should lead to increased revenue. The
ecosystem should move away from "THORChain users" and "Rujira users" toward "our
users", and should generally aim to present THORChain and Rujira as a united
front.

### Continuation of ADR 020

Option 1 should be understood as an indefinite continuation of
[ADR 020](./adr-020-app-layer.md), with a stronger focus on alignment,
long-term cooperation, and the following updates:

- The revenue split remains in place, with Rujira and THORChain continuing to
  earn an equal net share while avoiding double taxation. A third share now goes
  toward building protocol-owned liquidity and investing in long-term growth and
  revenue.
- Rujira and THORChain operate as aligned long-term partners, recognizing that
  certain responsibilities and operational requirements for Rujira may differ
  from those of a regular user in order to support the continued growth and
  success of the App Layer.
- There is a stronger emphasis on the division of roles between the Base Layer
  and App Layer, where both build on each other's strengths and complement one
  another. The Base Layer's primary focus should be security and safety, chain
  integrations, and strengthening the existing foundation, while innovation takes
  place on the App Layer, with a focus on building consumer products that attract
  users, generate revenue, and drive growth across the broader ecosystem.

This is the path Rujira strongly advocates for as a team. This proposal has also
been reviewed by Chad and Aaluxx from the THORChain development team, who have
expressed support for this direction.

## Option 2 - Limited Cooperation

### Vision

Under this option, Rujira would not be treated as an extension of THORChain, but
as one of many THORChain users. Competition on the App Layer would be opened,
revenue sharing with THORChain would be reduced, and Secured Assets would not
receive any special protection.

### WasmArbSlipMinBps

Under this option, liquidity transmission efficiency between the Base Layer and
the App Layer would not be prioritized. External arbitrageurs would remain the
primary mechanism for rebalancing, and no special fee rate would be applied to
the Virtualization Strategy.

Rujira would still require `WasmArbSlipMinBps` to have a predictable fee for the
Virtualization Strategy, as a dynamic fee would make quoting too complex for the
contract. In practice, `WasmArbSlipMinBps` could be set at the same level as
`TradeAccountsSlipMinBps`.

### Rujira POL and Revenue Share

In this scenario, the new revenue distribution would be reduced to 15% to
THORChain, with the balance going 42.5% to Rujira POL and 42.5% to RUJI stakers.

### Responsibilities in Case of Future Exploits

Smart contract hacks would remain the responsibility of Rujira.

In the event of a Base Layer exploit, such as funds being stolen from an Asgard
vault, Secured Assets would receive no special priority. Treatment would depend
on a node vote and would be expected to be the same as for Trade Assets.

### Collaboration on Development and Marketing

Collaboration would be limited, mostly on an as-needed basis on the development
front.

Marketing and business development would also remain mostly separated, with no
particular effort to unite the THORChain and Rujira communities.

It would no longer be Rujira's responsibility to guide and vet the work of new
teams onboarded to the App Layer.

## Option 3 - Discontinue the App Layer on THORChain

If this scenario passes, Rujira will accept the outcome and continue building
products and pursuing its vision through a different structure, in a different
place.

This is not what Rujira has been working toward over the past approximately 1.5
years, and it is not an outcome Rujira would like to see, but it is included for
completeness.

If such a decision were ever made in the future, after Option 1 had passed,
THORChain would be entitled to receive 1/3 of the POL built from Rujira revenue
redirected to POL, but not any portion of the POL built from the RUJI sell-down
from the Ecosystem Fund.
