# ADR 20: THORFi Unwind

## Changelog

- 27/01/2025: Created

## Status

Proposed

## Context

The recent actions and public dialogue of a few parties has manifested a self-fulfilling prophecy unwinding THORFi components with reflexive properties. It is especially unfortunate that subsequent discourse has been amplified instead of diffused, as the risk was well known and a plan was already in progress to shed these liabilities to the app layer. The net effect is nothing constructive and has resulted in user funds being temporarily locked by nodes and contributor time being redirected away from meaningful progress.

There have been many suggestions for approaches to unwinding, but most are over-complicated. Complex mechanics take time from meaningful progress, result in increased risk, and cause further time and frustration for affected users to understand. Forced unwinding ejects depth (which may also be abandoned) at a time when the majority of protocol revenue still comes from that depth being used to arbitrage the broader market - so any such solutions incur self-inflicted wounds on future system revenue. Similarly, any solutions based on some prescribed cadence can be predicted and gamed.

The ideal solution should:

- Require minimal developer time to implement and review so that progress can be resumed.
- Avoid logic/vulnerability risk for temporary and complex unwinding mechanics.
- Allow the market to determine specific cadence of unwind so it is less likely to be gamed - provide simple guardrails to ensure unwinding occurs within safe constraints.
- Allow affected users to easily understand when funds can be withdrawn.

## Detailed Design

The proposed design is trivial to implement and for all users to understand - determine a target RUNE price at which THORFi redemptions are enabled, and a target RUNE price at which they are disabled. The thresholds are defined in USD for cognitive ease, but can be adjusted by node mimir if there is need (and nodes retain the ability to pause via operational mimir in an emergency). If the lower price threshold is reached and redemption is disabled, the system burn is additionally increased to create buy pressure until the threshold to re-enable redemptions is reached.

### Implementation

There will be a subsequent PR linked in the description of the PR containing this ADR to the implementation of this approach to highlight the simplicity. The changes will be based on the following mimirs (values are suggestion) which should be voted by nodes if this path is chosen:

```text
SynthYieldBasisPoints=0              # disable saver yield
THORFiUnwindEnableRuneTOR=500000000  # enable redemptions above $5 RUNE
THORFiUnwindDisableRuneTOR=400000000 # disable redemptions below $4 RUNE
THORFiUnwindBurnBasisPoints=5000     # burn 50% of income on redemptions disabled
```

## Consequences

The expected outcome is that RUNE price remains relatively range bound while THORFi is unwound - in a timeline determined by the market and users, with the guardrails preventing runaway reflexivity. When RUNE price exceeds the target upper bound, redemptions are re-enabled and users are allowed to resume exiting. When RUNE price drops below the lower bound, redemptions are disabled and the system burn is increased to create positive pressure on price action. Over time the system sheds liability while oscillating in the determined range, increasing confidence in future valuation, and increased buy pressure slows price oscillation. Meanwhile, developers maintain focus on the current chain and feature roadmap - the work being meaningful in the long term and growth accelerating the unwind process.
