# ADR 026: Dynamic L1 Min Swap Fee Per Thorname & Pair

## Changelog

- 2026-10-19: **Measurement fixes, from a simulation review of the
  controller.** Five changes, plus one rule for multi-affiliate memos:

  1. **Signal excludes the thorname's own volume.** The signal is now
     `fees_tor / (pool_volume_tor − own_pool_volume_tor)`: fee revenue
     against other swappers' volume. Dividing by the whole pool made the
     ratio approach the fee rate itself whenever the thorname was most
     of a pair's flow, which always reads higher at a higher fee; a
     90%-share integrator whose revenue peaks at 7 bps settled near 15.
     An epoch whose other flow is under `L1DynamicFeeMinOtherFlowBPS`
     (default 10%) of the pool is recorded but is not a measurement.
  2. **A time cap on measurement spans.** A span below the liquidity
     gate (`L1DynamicFeeMinSwaps` swaps _and_ `L1DynamicFeeMinVolumeUSD`
     of affiliate volume, the latter now defaulting to $100,000) rolls
     into the next epoch, but only until it has run
     `L1DynamicFeeMaxEpochBlocks` (default 100,800 blocks, about 7
     days). The next epoch boundary then seals it with what it has. A
     span with no swaps at all is sealed empty, which reads as zero
     revenue. Either way, a fee so high that it cannot attract the
     gate's volume is measured and moved off rather than held there.
     Spans therefore vary in length; they stay comparable because the
     signal divides fees by other swappers' volume over the same span
     (a revenue rate per block would let market-wide swings back in).
     Each entry records its length in `blocks`. A span sealed by the cap
     below the gate is marked `capped`: too thin to justify a higher fee,
     it may only lower it. In the search a capped probe never displaces a
     lower-fee best (it still counts as tested); in the ±step controller
     a raise on a capped reading becomes `capped_hold` and a cold start
     probes down.
  3. **Global mode is measured on the pairs it trades.** A global
     accumulator records the pairs it was credited on, and its pool
     volume sums only those pairs, so volume on unrelated pairs no
     longer moves its signal.
  4. **Monitor records collect history only.** Their swaps pay the
     static minimum, so a search or step would measure noise. Monitor
     records never search or step; queries show `phase: "monitor"`.
  5. **The fee and the credit go to the same affiliate**, which is also
     the multi-affiliate rule: only the memo affiliate with the largest
     affiliate bps counts. It is charged its floor when its state is
     `1`/`2` and credited when it is `1`-`4`; otherwise the swap pays
     the static minimum and nothing is credited. A smaller affiliate
     never stands in for it.

  Adds `own_pool_volume_tor` to epoch records, accumulators and both
  query endpoints, `blocks` to epoch records and history entries,
  `span_start_height` and `pairs` to records, `pairs` to accumulators,
  and `span_blocks` to the in-progress query.

- 2026-10-19: **Bracket search, global mode, reset on change.** Three
  changes:

  1. **Bracket search.** A ±1 bps step moves the floor too little for
     the volume response to stand out from noise, so a record now
     starts by searching `[floor, ceiling]` in large jumps: it measures
     its starting bps, then the ceiling, then the midpoint, then keeps
     halving the gap next to the best value measured so far (1 → 20 →
     10 → … on defaults; about 7-9 tests to converge on `[1, 20]`).
     Each test holds for `L1DynamicFeeProbeEpochs` sealed (gated)
     epochs, default 1. Once the gaps beside the best value are empty
     the record hands off to the existing ±step controller at that
     value, which follows drift from there. See "Search rule" below.
  2. **Global mode.** The whitelist value now also selects the mode:
     `1` = per-pair (unchanged), `2` = global — one floor per thorname,
     tuned on all its L1 pairs together and applied on every L1 pair.
     Global mode pools a thorname's thin pairs into one signal, so it
     clears the liquidity gate far sooner. Monitor mode moves from `2`
     to `3` (per-pair) and `4` (global). The upgrade migration rewrites
     every stored `2`, and every node vote for `2`, to `3`, so a thorname
     enrolled for monitoring is not charged a floor by the upgrade.
  3. **Reset on change.** Any change to a thorname's effective
     `DYNAMICFEE-WHITELIST-{thorname}` value — enabling it, switching
     mode, entering or leaving monitor, or removing it — immediately
     deletes all of its dynamic-fee state, so it rediscovers its floor
     from scratch. This is also how to unstick a record: vote it to
     `0`, then back.

  The ±step controller's cold-start probe goes down instead of up when
  the record is already at the ceiling, so a search that converges on
  the ceiling still has its next move evaluated rather than sitting
  there until a reset.

  Adds `search_done` and `probes` to records, `phase` to both REST
  record endpoints, and `probes` to the per-thorname endpoint. Records
  that exist at the upgrade keep their current `dynamic_bps` and start
  a search from it.

- 2026-09-19: **Noise reduction.** Two changes, driven by live data from
  a small integration whose epochs held a median of 4 swaps against a
  noise-crossover near 70:

  1. **Liquidity gate.** An epoch must hold at least
     `L1DynamicFeeMinSwaps` swaps _and_ `L1DynamicFeeMinVolumeUSD` of
     affiliate volume before it may seal a decision. Otherwise the
     accumulator rolls into the next epoch, unbounded, and the floor
     does not move. Streaming swaps count once, not once per
     sub-swap. An integration that never clears the gate simply never
     steps, which is the correct behaviour at that size.
  2. **Pool-volume normalization.** The gradient now compares
     `fees_tor / pool_volume_tor` — the affiliate's fee revenue as a
     share of the pool's _total_ volume over the same span — rather
     than raw fee levels. Most of the previously-measured swing was
     market-wide activity, not a response to the floor. Note the
     denominator is deliberately the _pool's_ volume and not the
     affiliate's own `volume_tor`: because the floor binds, the latter
     ratio is identically `f / 10⁴`, which would read `continue_up`
     every epoch and pin the ceiling while carrying zero elasticity
     information.

  Adds pair-scoped `DynamicFeePoolAccumulator` state, `swap_count` and
  `pool_volume_tor` to epoch records, accumulators and both query
  endpoints. History written before this change carries raw fee levels
  in incompatible units and is dropped at the first post-upgrade seal;
  affected records keep their current `dynamic_bps` and re-enter
  cold start. Store migration `Migrate21to22` clears the in-flight
  accumulators so no sealed entry mixes a full epoch of fees with a
  partial epoch of pool volume (see "Migration" below).

- 2026-07-22: Status moved to Implemented — the feature is live on mainnet
  (`L1DynamicFeeEnabled = 1`).
- 2026-04-27: Switch the gradient signal from `fees_rune` to `fees_tor`
  (TOR-denominated, 1e8-precision USD value). Volume is also tracked in
  TOR. RUNE-denominated values are converted at the swap-completion
  hook via `DollarsPerRune`; when no TOR anchor pool is available
  (`DollarsPerRune == 0`), the credit is skipped rather than recorded
  as zero. Cancels the macro-noise term that nudged floors during
  RUNE/USD swings. Adds a query endpoint
  `GET /thorchain/dynamic_l1_fees_current` returning live (in-progress,
  not yet sealed) accumulators per `(thorname, pair)` for the current
  epoch.
- 2026-04-17: Expose the gradient window as the operational Mimir
  `L1DynamicFeeWindowEpochs` (default 3, clamped to `[1, 30]` at read
  time). Previously hardcoded. Larger windows trade response speed for
  noise rejection (√K).
- 2026-04-17: **Algorithm redesign.** Replace the share-based open-loop
  controller with a closed-loop gradient controller on fee revenue. The
  per-`(thorname, pair)` floor now moves each epoch based on whether the
  previous floor change increased or decreased the windowed-mean fee
  signal for that record. Drops the pair accumulator, pair history,
  and the fee/volume score weights; simplifies state schema and mimir
  surface. See the "Detailed Design" section for the new rule. Feature
  unshipped (`L1DynamicFeeEnabled` default still `0`) so the change is
  made in place without a v2 section.
- 2026-04-15: Initial draft.
- 2026-04-15: Post-review revisions: specify integer math via `common.GetSafeShare`;
  streaming sub-swaps credited per sub-swap (not aggregated); clarify multi-affiliate
  floor-selection uses the thorname with the largest affiliate-bps split; document
  record lifecycle and the worse-than-default ceiling implication.
- 2026-04-15: Tighten document: compress Context, drop the User Requirements section
  (redundant with Summary / Key definitions), collapse repeated "10 bps on mainnet"
  mentions to a single authoritative spot, and replace Disabled-state restatements
  with cross-references to the canonical subsection.
- 2026-04-15: Lower default `L1DynamicFeeMinFeesRune` from 100,000 to 10,000 RUNE.
  At current RUNE price (~$0.41) 100k implied ~$10M/day per `(thorname, pair)` which
  no affiliate clears today; 10k implies ~$1M/day and is achievable for the top
  affiliate+pair combinations while still filtering out small integrators.
- 2026-04-15: Replace the `L1DynamicFeeMinFeesRune` activation threshold with a
  governance-curated per-thorname Mimir whitelist (`DYNAMICFEE-WHITELIST-{thorname}`)
  with three states: `0` / absent (not enrolled), `1` (active), `2` (monitor —
  collect and compute but do not apply at swap time). Removal from whitelist
  immediately deletes all state for that thorname. No per-pair activity floor;
  any activity moves the floor. Simulation-test line removed per operator
  guidance.
- 2026-04-15: Initial `dynamic_bps` on record creation changes from `L1SlipMinBPS`
  to `L1DynamicFeeFloorBPS` (default 1 bps). Rationale: the share-based feedback
  rule's downward gradient from 10 → 9 → 8 bps may fall inside the 1% deadband
  every epoch and never fire, leaving the floor pinned at the default. Starting
  at the floor inverts the problem into an upward ratchet, which is the more
  reliable direction for this algorithm.

## Status

Implemented

## TL;DR

Replace the single network-wide `L1SlipMinBPS` with a per-`(thorname, pair)`
(or, in global mode, per-thorname) dynamic floor tuned on-chain. A new
record first runs a **bracket search** over `[floor, ceiling]` in large
jumps to find the bps that earns the most fee revenue, then hands off
to a **closed-loop revenue gradient** that follows drift. Each 24h
epoch, that controller asks a single question for each tracked record:
**did the previous floor change increase or decrease the fee signal
for this pair?** Fees up → continue in the same direction. Fees down →
reverse. Within `[floor, ceiling]` bps bounds and a configurable
percentage deadband.

Tracking is denominated in **TOR** (THORChain Oracle Rune — the on-chain
USD-pegged unit, 1e8-precision). The swap-completion hook converts
RUNE-denominated volume and fees to TOR via `DollarsPerRune` so the
controller signal is independent of RUNE/USD price movements.

Participation is governance-curated: a thorname is enrolled via the
operational Mimir `DYNAMICFEE-WHITELIST-{thorname}`, whose value picks
per-pair or global mode, each either live or monitor-only. Scope: L1↔L1 swaps only; registered thornames only.
A master Mimir (`L1DynamicFeeEnabled`) cleanly disables the entire
mechanism. Goal: charge more where demand is inelastic, less where it is
competitive — directly optimizing the protocol's fee-revenue objective.

## Context

Today, the minimum L1 swap slip floor is governed by a single network-wide
Mimir, `L1SlipMinBPS`. A single value cannot optimize fee revenue across
the full matrix of affiliates and trading pairs, because different flow
has fundamentally different price sensitivity.

THORChain's own front-end (swap.thorchain.org), currently the protocol's
second-largest affiliate by volume, attracts users who transact on
THORChain largely _regardless_ of price execution — demand is sticky.
Aggregator integrations such as SwapKit route price-sensitive flow
constantly compared against other venues; a floor that is even slightly
too high on a competitive pair loses the flow entirely. The same split
exists across pairs: THORChain is the only meaningful venue for some
asset pairs and one of many for others.

With one network-wide floor, the protocol must pick a single point on
this landscape. Lowering it to compete on SwapKit's price-sensitive
pairs cedes revenue from sticky flow; raising it to capture more sticky
flow loses competitive pairs. The protocol cannot do both.

A per-`(thorname, pair)` floor, tuned by an on-chain feedback loop, lets
the protocol charge high fees where THORChain is effectively the only
venue and low fees where it is competing. The earlier revision of this
ADR tuned that floor by watching each thorname's **share** of its pair's
weighted `(fees, volume)` score — an open-loop proxy for the underlying
objective. That approach had three structural weaknesses:

1. **Open-loop attribution.** The controller reads a signal (share) and
   acts on a lever (floor) without ever asking whether the lever moved
   the signal. Share can shift because a competitor disappeared, a
   promo ran, a new chain listed, or macro moved — none of which the
   floor caused, but all of which the algorithm attributes to pricing.
2. **Solo-on-pair dead zone.** A thorname that owns a pair has
   `share ≈ 1.0` permanently; the algorithm never fires.
3. **Scalar collapse and phantom signals.** A fees-plus-volume score
   requires operators to pick weights without a principled basis, and
   multi-affiliate co-listing changes move B's share without B doing
   anything.

This revision replaces the share signal with a **closed-loop gradient on
`fees_tor`** (TOR-denominated, USD-equivalent), the metric the protocol
actually cares about. The controller measures its own effect each epoch
and adjusts — so all three weaknesses above go away. Tracking in TOR
also cancels RUNE/USD price noise that a `fees_rune` signal would
carry. The attribution, whitelist, bounds, and governance surface are
unchanged; only the signal and the state it requires change.

This ADR applies only to L1↔L1 swaps (no trade assets, no secured
assets, no synths) and only to registered thornames — raw affiliate
addresses continue to use the default floor.

## Decision

Introduce a per-`(thorname, pair)` dynamic minimum L1 slip fee tuned by
a closed-loop gradient controller on `fees_tor` (TOR-denominated,
1e8-precision USD value). Each epoch, for every tracked record, the
controller looks at the most recent bps change in the record's history,
compares the mean `fees_tor` before and after that change (windowed
across `L1DynamicFeeWindowEpochs` epochs on each side — default 3),
and moves the floor in whichever direction fees responded positively —
or reverses when fees responded negatively.

### Summary of the decision

- **Scope:** L1↔L1 swaps only (RUNE counts as a valid endpoint).
- **Pair identity:** non-RUNE endpoints, direction-agnostic, alphabetically
  normalized. Chain-distinct (`BTC.BTC` and `BSC.BTCB` are different pairs).
- **Attribution unit:** registered thornames that are explicitly
  whitelisted via Mimir (`DYNAMICFEE-WHITELIST-{thorname}`). Raw-address
  affiliates, unregistered thornames, and registered-but-unwhitelisted
  thornames receive `L1SlipMinBPS` and are not tracked.
- **Whitelist states:** `1` = per-pair (dynamic floor per L1 pair,
  applied at swap time), `2` = global (one dynamic floor across all of
  the thorname's L1 pairs, applied at swap time), `3` / `4` = monitor
  for per-pair / global (state computed but not applied — swaps use
  `L1SlipMinBPS`), `0` or absent = not enrolled. Any change to the
  value resets the thorname's state.
- **Signal:** `fees_tor` (TOR-denominated, 1e8-precision USD value)
  accumulated per `(thorname, pair)` per epoch, divided by other
  swappers' volume on the same pools over the same span
  (`pool_volume_tor − own_pool_volume_tor`), and compared across fee
  levels. The ratio does not depend on how long a span ran. RUNE-denominated values are
  converted at the swap-completion hook via `DollarsPerRune`. Skipped
  entirely when `DollarsPerRune == 0` (no TOR anchors).
- **Epoch:** block-count based, default ~14,400 blocks (≈24h at 6s blocks).
- **Movement:** a bracket search in large jumps (floor or starting bps,
  ceiling, midpoint, then halving) until it converges, then ±1 bps per
  epoch, all within `[floor, ceiling]` bps bounds and with a
  configurable percentage deadband on the fee delta (default 10%).
- **Objective:** directly maximize protocol fee revenue per record,
  measured in USD-equivalent terms (TOR). Volume matters only
  instrumentally — a floor change that reduces volume more than it
  increases per-swap fees shows up as a fees drop and the controller
  reverses.
- **Feature master switch:** operational Mimir.

## Detailed Design

### Key definitions

- **Pair**: The normalized tuple of the two non-RUNE assets on a swap,
  sorted alphabetically by their asset string (chain + symbol + optional
  id). If either side of the swap is RUNE, the pair is `ASSET↔RUNE`.
  Pairs are direction-agnostic.
- **Epoch**: A fixed number of consecutive blocks, governed by the
  operational Mimir `L1DynamicFeeEpochBlocks` (default 14,400). Epoch
  boundaries occur at `block_height % epoch_blocks == 0`.
- **Affiliate thorname**: A thorname listed in a swap memo's affiliate
  field(s). Raw-address affiliates are ignored by this mechanism.
- **Whitelist state**: The value of the operational Mimir
  `DYNAMICFEE-WHITELIST-{thorname}`. Values:
  - `1` = **per-pair**: the thorname participates fully, with one
    record per L1 pair. Accumulators run, `dynamic_bps` is maintained
    each epoch, and the value is applied at swap-time.
  - `2` = **global**: as `1`, but with a single record for the
    thorname, keyed by the pair placeholder `*|*`. Every in-scope swap
    on any L1 pair credits it, and its `dynamic_bps` applies to every
    L1 pair. Its normalization denominator is other swappers' volume on
    the pairs it traded.
  - `3` / `4` = **monitor** (per-pair / global): the thorname
    participates _in data collection only_. Accumulators run and
    history is recorded as in `1` / `2`, but no floor is searched for
    or stepped, and swaps use `L1SlipMinBPS`. Intended for checking a
    thorname's attribution and volume before it goes live.
  - `0`, absent, or any other value = **not enrolled**: no tracking,
    no state, no dynamic floor. Swaps use `L1SlipMinBPS`.
- **Dynamic bps**: The per-`(thorname, pair)` (or per-thorname, in
  global mode) minimum slip floor, in
  basis points, maintained by the algorithm. Starts at
  `L1DynamicFeeFloorBPS` (default 1 bps) when a record is created,
  and is bounded by `L1DynamicFeeFloorBPS` and
  `L1DynamicFeeCeilingBPS`. The ceiling is not capped at
  `L1SlipMinBPS` — a whitelisted thorname whose fees degrade can
  experience a _worse_ floor than the default, by design.
- **Anchor**: The most-recent index in a record's history at which
  `bps_at_close` differs from its predecessor. The controller
  evaluates "did my last move help?" by comparing the fees window
  starting at the anchor to the fees window ending at the anchor.
- **Window (K)**: The number of epochs averaged on each side of the
  anchor when estimating the gradient. Governed by the operational
  Mimir `L1DynamicFeeWindowEpochs` (default 3) — short enough to react
  quickly, long enough to cut per-epoch fee variance by roughly √K.
  Clamped into `[1, MaxDynamicFeeHistory]` at read time.

### Record lifecycle

- A per-`(thorname, pair)` record (one `*|*` record in global mode) is
  created lazily on the first in-scope swap carrying that thorname
  _while the thorname's whitelist state is `1`-`4`_. At creation, the
  accumulator is zero and `dynamic_bps` is initialized to
  `L1DynamicFeeFloorBPS`.
- A new record is in the **search** phase. Each sealed epoch is
  credited to the probe for the bps that governed it; once a probe
  holds `L1DynamicFeeProbeEpochs` epochs the record jumps to the next
  value per the search rule. From the floor that is the ceiling, then
  the midpoint, then halving around the best value. When the search
  converges, the record moves to the **track** phase at the best
  value: its history is cleared and the gradient rule takes over from
  a cold start there. The probes stay on the record for inspection.
- When a thorname's effective whitelist value **changes** — to `0`,
  between `1` and `2`, into or out of monitor — **all** state for that
  thorname is deleted immediately (in the Mimir handler, not at the
  next epoch): every record and every in-progress accumulator. The
  next swap starts a fresh record at the floor and a fresh search. A
  vote that leaves the effective value unchanged does nothing; to
  reset a live thorname, vote it to `0`, then back. A key deleted
  outright (rather than set to `0`) is not a change the handler sees;
  its records are pruned after 30 idle epochs.
- While a thorname is in `3` or `4` (monitor), its record collects
  history only: `dynamic_bps` stays at the floor and is never applied.
  Promoting to `1` or `2` is a change, so it resets the thorname and
  the live controller starts from scratch.
- After 30 consecutive epochs with no activity (while still
  whitelisted), the record is pruned in full (see Epoch-boundary
  algorithm §3).

### State

**Per `(thorname, pair)` record** (pair `*|*` in global mode):

- `dynamic_bps: uint64` — current floor maintained by the algorithm.
  Only consulted at swap-time when the owning thorname is in
  whitelist state `1` or `2`.
- `search_done: bool` — `false` while the bracket search runs, `true`
  once it has converged and the gradient rule is in charge. The zero
  value means "searching", so records that predate the search start
  one from their current `dynamic_bps`.
- `probes: []Probe` — one entry per bps value the search has tested:
  `bps`, `signal_sum` (sum of the per-epoch normalized signal) and
  `epochs` (sealed epochs measured). The probe's mean is
  `signal_sum / epochs`.
- `span_start_height: int64` — the block at which the current
  measurement span began (the record's creation or its last seal).
  Once the span reaches `L1DynamicFeeMaxEpochBlocks` the next epoch
  boundary seals it, gate or not.
- `pairs: []string` — for a global record, the pairs it traded as of
  its last seal, so a span with no swaps can still be measured
  against those pools.
- `last_active_epoch: uint64` — most recent epoch in which the
  thorname had any in-scope activity on this pair. Used for the
  30-epoch prune rule.
- `history: []EpochRecord` (up to 30 entries, one per epoch):
  - `epoch`, `volume_tor`, `fees_tor`, `bps_at_close`, `swap_count`,
    `pool_volume_tor`, `own_pool_volume_tor`, `blocks` (the span's
    length).
  - `volume_tor` is kept for observability and event emission; it
    is not read by the controller. It _is_ read by the liquidity
    gate before the entry is written.
  - `pool_volume_tor` is the pool's total volume over the same span,
    regardless of affiliate (for `*|*`, over every pair the thorname
    traded during the span, each over the whole span). `own_pool_volume_tor` is the thorname's own part of it,
    counting every swap leg. The controller divides fees by the
    difference: other swappers' volume.
  - `swap_count` counts user swaps, not sub-swaps: a streaming swap
    contributes 1, and on a global (`*|*`) record so does a multi-leg
    swap, whose legs all land on that record. An entry may span several epochs if earlier ones
    rolled, in which case all four quantities are sums over the
    rolled span.
  - `bps_at_close` records what the floor was set to at the end of
    this epoch; the controller's "find the most recent change" walk
    inspects this field.

**In-progress (current epoch) accumulators:**

- Per `(thorname, pair)`: running `volume_tor`, `fees_tor`,
  `swap_count`, `pool_volume_tor`, `own_pool_volume_tor` for the current
  epoch, and for a global (`*|*`) accumulator the list of `pairs` it was
  credited on plus `pair_pool_volumes`, the span's pool volume on every
  pair that traded. A global accumulator's `pool_volume_tor` is re-summed
  from those over its `pairs` (or, for an idle span, the record's
  last-seal pairs) at each boundary, so a pair first traded late in the
  span still counts its earlier epochs. Sealed and
  cleared at each epoch boundary — unless the liquidity gate rejects
  the epoch and the span is under `L1DynamicFeeMaxEpochBlocks`, in
  which case the accumulator survives and keeps accumulating into the
  next one.
- Per `pair`: running `volume_tor` covering _every_ swap through the
  pair, affiliate or not. This is the normalization denominator. It is
  folded into each enrolled record's accumulator `pool_volume_tor` at
  every epoch boundary, swaps or not, and then cleared unconditionally,
  so an accumulator carries a pool volume spanning exactly the epochs of
  its span.

Note: protocol-internal swaps that call `swap()` directly (POL,
affiliate-fee conversion) bypass the swap-completion hook and so are
excluded from the pair denominator. This is deliberate — the
denominator is meant to measure market activity.

### Mimir keys (all operational)

| Key                               | Default | Meaning                                                                                                                                                               |
| --------------------------------- | ------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `L1DynamicFeeEnabled`             | 0       | Master on/off. 0 disables the entire mechanism.                                                                                                                       |
| `L1DynamicFeeEpochBlocks`         | 14400   | Epoch length in blocks (~24h at 6s).                                                                                                                                  |
| `DYNAMICFEE-WHITELIST-{thorname}` | absent  | Per-thorname enrollment and mode: `1` = per-pair, `2` = global, `3`/`4` = per-pair/global monitor (collect only), other = not enrolled.                               |
| `L1DynamicFeeFloorBPS`            | 1       | Lower bound of dynamic bps.                                                                                                                                           |
| `L1DynamicFeeCeilingBPS`          | 20      | Upper bound of dynamic bps.                                                                                                                                           |
| `L1DynamicFeeStepBPS`             | 1       | Magnitude of movement per epoch.                                                                                                                                      |
| `L1DynamicFeeDeadbandBPS`         | 1000    | Percentage change in the windowed signal below which the controller holds, and the margin a higher fee must win by in the search (in bps: 1000 = 10%).                |
| `L1DynamicFeeWindowEpochs`        | 3       | Epochs averaged on each side of the last bps change when measuring the gradient. Clamped into `[1, 30]` at read time.                                                 |
| `L1DynamicFeeMinSwaps`            | 30      | Swaps an epoch must hold before it may seal a decision. Below this the accumulator rolls into the next epoch. `0` disables this half of the gate.                     |
| `L1DynamicFeeMinVolumeUSD`        | 100000  | Whole USD of affiliate volume an epoch must hold before it may seal. Converted to TOR internally. `0` disables this half of the gate.                                 |
| `L1DynamicFeeProbeEpochs`         | 1       | Sealed epochs the bracket search measures at each tested bps before choosing the next one. Values below 1 read as 1.                                                  |
| `L1DynamicFeeMinOtherFlowBPS`     | 1000    | Share of a pair's volume (bps of 10,000) that must come from other swappers for an epoch to count as a measurement.                                                   |
| `L1DynamicFeeMaxEpochBlocks`      | 100800  | Longest a span may roll below the liquidity gate (~7 days). The next epoch boundary after it seals the span regardless; with no swaps, as zero revenue. `0` disables. |

The whitelist key's `{thorname}` suffix is the registered thorname
name, normalized per thornode's existing Mimir-key conventions
(typically upper-cased). The thorname must be a currently-registered
thorname; an entry for a non-existent thorname has no effect and is
ignored.

**Hardcoded:**

- 30-epoch prune horizon (records for a `(thorname, pair)` with no
  activity for 30 consecutive epochs are deleted). This also bounds
  the `L1DynamicFeeWindowEpochs` clamp at read time.

The window size (`L1DynamicFeeWindowEpochs`, default 3) trades noise
rejection for response speed: a larger window cuts per-epoch variance
by roughly √K but takes more epochs to confirm a trend, and eats into
the fixed `MaxDynamicFeeHistory = 30` budget (the window must leave
room for before + after samples). Values outside `[1, 30]` are
clamped at read time so misconfiguration cannot crash the
controller.

### Attribution rules (per swap)

When an L1↔L1 swap completes (or at the moment slip fees are recorded),
attribute to the in-progress epoch counters as follows:

1. **In-scope check.** Both swap endpoints must be L1 assets (RUNE is
   eligible). Trade assets, secured assets, and synths disqualify the
   swap from tracking. Out-of-scope swaps are ignored.
2. **Pair normalization.** Compute the pair key from the two non-RUNE
   endpoints (or `ASSET↔RUNE` if one side is RUNE), sorted.
3. **TOR conversion.** Convert the swap's `volume_rune` and `fees_rune`
   to TOR via `DollarsPerRune` (`value_tor = value_rune *
DollarsPerRune / 1e8`). When `DollarsPerRune == 0` (no TOR anchor
   pool, or all halted), skip the credit entirely — recording zeros
   would drag the windowed-mean signal toward zero and provoke
   spurious controller reversals.
4. **Thorname credit.** Only the memo affiliate with the **largest
   affiliate bps** is considered (first listed wins a tie), the same
   one that sets the fee (see Fee floor selection):
   - If it is a registered thorname whose whitelist state is `1`-`4`,
     add the swap's `volume_tor`, `fees_tor` and leg volume
     (`own_pool_volume_tor`) to its current-epoch accumulator:
     `(thorname, pair)` in per-pair mode (`1`, `3`), `(thorname, *|*)`
     in global mode (`2`, `4`), which also records the pair. In global
     mode a multi-leg swap credits its volume and swap count once, on
     its first leg, and every leg's fees and leg volume.
   - Otherwise (raw address, unregistered thorname, or a registered
     thorname with whitelist state `0` / absent), nothing is credited,
     even if a smaller affiliate in the memo is enrolled.
5. **Streaming swaps.** Each sub-swap is credited independently as it
   completes, into the epoch in which its completion block lands. A
   streaming swap that spans an epoch boundary is therefore split
   naturally: sub-swaps before the boundary land in epoch N, sub-swaps
   after land in epoch N+1. This increases the weight of high-sub-swap
   streams but keeps epoch-boundary accounting clean.

### Fee floor selection (at slip fee calculation)

For an in-scope L1 swap, at the point slip fees are computed:

1. If `L1DynamicFeeEnabled != 1` → use `L1SlipMinBPS`. See
   [Disabled state](#disabled-state) for the full contract.
2. If the swap has no affiliate, or the affiliate is a raw address, or
   the affiliate thorname is not registered → use `L1SlipMinBPS`.
3. Look up the affiliate thorname's whitelist state:
   - State `1` (per-pair) or `2` (global): if the thorname's record
     for this pair (`*|*` in global mode) exists →
     use `record.dynamic_bps`. Otherwise → use `L1DynamicFeeFloorBPS`,
     the value a new record starts at.
   - State `3` or `4` (monitor): use `L1SlipMinBPS`. Attribution still
     runs, so the record collects history, but it holds no floor.
   - State `0` or absent: use `L1SlipMinBPS`.
4. For multi-affiliate memos, the thorname with the **largest
   affiliate-bps split** is the one whose record applies (ties broken
   by order of appearance). This prevents a free-rider exploit where
   an integrator co-lists an active thorname as a token secondary to
   inherit its discount — to benefit from a thorname's dynamic bps,
   you must give that thorname the largest share of the affiliate
   revenue. If the largest-split thorname is in a monitor state or
   unenrolled, step 3 falls through to `L1SlipMinBPS` for this
   swap — secondary listed thornames do not "rescue" the floor. The
   same affiliate is the only one credited (Attribution rules §4), so
   the record that sets a fee is the one that measures its result.
   The flip side: an integrator can list a non-enrolled affiliate
   (even its own address) at a higher bps to opt a swap out of a
   dynamic floor above the static minimum. The governance-curated
   whitelist is the control for that.

### Epoch-boundary algorithm

At block heights where `block_height % L1DynamicFeeEpochBlocks == 0`:

1. **Cleanup removed thornames.** For every thorname whose
   `DYNAMICFEE-WHITELIST-{thorname}` holds a value outside `1`-`4`,
   delete all dynamic-fee state for that thorname. (A change of value
   already did this in the Mimir handler; this is the backstop.)

2. **Seal or roll each record's span.** Iterate all enrolled records
   (not accumulators, so a record that drew no swaps is still visited).
   For each, with its accumulator (empty if none):

   - Fold the pair's pool accumulator into
     `accumulator.pool_volume_tor` — for a global (`*|*`) accumulator,
     the sum of the pool accumulators of the pairs it was credited on
     (or, before its first swap in the span, the pairs from its last
     seal). This runs every epoch, swaps or not, so the span's pool
     volume covers every epoch its `blocks` counts, including idle ones
     before the thorname's first swap. Dropping those would shrink the
     denominator exactly when a high fee delays flow, and read as a
     higher signal.
   - With no swaps in the span: keep the accumulator (pool volume only;
     nothing to store if that is zero too) and move on, unless the span
     has reached `L1DynamicFeeMaxEpochBlocks` and the record is not in a
     monitor state, in which case seal it empty (zero revenue) below. An
     idle epoch or an empty seal does not update `last_active_epoch`, so
     an idle record still ages towards pruning, and pruning a record
     deletes its accumulator too.
   - **Liquidity gate.** If `accumulator.swap_count < MinSwaps` or
     `accumulator.volume_tor < MinVolumeUSD * 1e8`, the span is too
     thin to carry signal. Unless it has run `MaxEpochBlocks` (counted
     from `record.span_start_height`), roll it: set
     `record.last_active_epoch = current_epoch` (so a gated record is
     not pruned out from under its own accumulator), leave
     `dynamic_bps` alone, keep the accumulator, and move on. No history
     entry, no event. A span at the cap is sealed anyway.
   - Drop any history entry with `pool_volume_tor == 0` — pre-upgrade
     data in incompatible units. Safe to run on every seal because the
     pool credit precedes the affiliate credit for the same swap leg,
     so any entry with fees necessarily has non-zero pool volume.
   - `fees_now = accumulator.fees_tor`.
   - Load the record; append a new history entry
     `{epoch, volume_tor=accumulator.volume_tor,
fees_tor=fees_now, bps_at_close=TBD}`.
   - If the thorname is in a monitor state, save the record (history
     only) and stop here: no decision, no event.
   - Compute the next `bps`: via the search rule below while
     `search_done` is false, otherwise via the gradient rule.
   - Write `bps_at_close = new_bps` into the just-appended entry.
   - Set `record.dynamic_bps = new_bps`,
     `record.last_active_epoch = current_epoch`.
   - If `new_bps != old_bps`, or the search just converged, emit
     `dynamic_l1_fee_update`.
   - Delete the accumulator.

   After the loop, delete every pool accumulator: they are per-epoch by
   construction and each thorname accumulator that needed this epoch's
   slice has already folded it in.

3. **Prune.** For every record where
   `current_epoch - last_active_epoch >= 30`, delete the record in
   full.

**Gradient rule** (concrete pseudo-code for step 2):

```text
changeIdx, hasChange = last index j in history[:-1] where
                       history[j].bps_at_close != history[j-1].bps_at_close

if not hasChange:
    # Cold start — no prior move to evaluate. Probe upward by one step, or
    # downward when already at the ceiling: a clamped probe records no
    # change, so the record would otherwise cold-start in place forever.
    up = clamp(old_bps + step, floor, ceiling)
    if up != old_bps: return "cold_start_probe", up
    return "cold_start_probe", clamp(old_bps - step, floor, ceiling)

dir       = sign(history[changeIdx].bps_at_close - history[changeIdx-1].bps_at_close)
# The signal is fee revenue against other swappers' volume:
#   other(e)  = e.pool_volume_tor - e.own_pool_volume_tor
#   signal(e) = e.fees_tor * 1e12 / other(e)
# An entry is skipped when pool_volume_tor is 0 or other(e) is under
# MinOtherFlowBPS of pool_volume_tor; an empty window reads as 0.
before = signals(history[changeIdx-K : changeIdx])   # up to K epochs before change
after  = signals(history[changeIdx-or-later : ])    # last K epochs since change, incl. this one
feesBefore, feesAfter = mean(before), mean(after)

if feesBefore == 0:
    # No baseline to compare against; hold.
    return "hold", old_bps

deltaPctBps = abs(feesAfter - feesBefore) * 10000 / feesBefore  # via GetSafeShare

if deltaPctBps < deadband:
    return "hold", old_bps

feesIncreased = feesAfter > feesBefore
# (Then, if the newest entry is capped and the result would raise the fee:
#  a cold start probes down instead, anything else is "capped_hold".)
if dir == +1 and feesIncreased:  return "continue_up",   clamp(old_bps + step, floor, ceiling)
if dir == +1 and not feesIncreased: return "reverse_down", clamp(old_bps - step, floor, ceiling)
if dir == -1 and feesIncreased:  return "continue_down", clamp(old_bps - step, floor, ceiling)
if dir == -1 and not feesIncreased: return "reverse_up",   clamp(old_bps + step, floor, ceiling)
```

**Search rule** (step 2, while `search_done` is false):

```text
signal = signal(newest history entry)       # hold if it cannot be normalized
probes[old_bps].signal_sum += signal; probes[old_bps].epochs += 1
if probes[old_bps].epochs < ProbeEpochs:
    return "search_measure", old_bps

P    = completed probes with floor <= bps <= ceiling, sorted by bps
best = P[0]; for p in P[1:]:                # a higher bps must win by more
    if p.capped: continue                    # than the deadband, and a capped
    if mean(p) > mean(best) * (1 + deadband): best = p   # probe never wins

left  = untested ints between best and the nearest tested bps below it,
        or [floor, best) if none is below (test the floor itself first)
right = untested ints between best and the nearest tested bps above it,
        or (best, ceiling] if none is above (test the ceiling itself first)

if left and right are both empty:
    search_done = true; history = []
    return "search_converged", best.bps
next = the larger side (ties → left): its untested floor/ceiling if
       open-ended, else the midpoint (lo + hi) / 2
return "search_probe", next
```

Assuming revenue is unimodal in bps, the optimum lies between the best
value's nearest tested neighbours, so only those two gaps are searched
and each test roughly halves one. The deadband preference means a
higher fee is only adopted when it is clearly better; on a near-tie the
lower fee stands. Every test is a single (gated) reading and a losing
side is discarded for good, which is why the gradient rule takes over
after convergence — and why a governance reset (vote `0`, then back)
exists for a search that settled badly. If the floor or ceiling moves
mid-search, probes outside the new range are ignored.

Worked example on `[1, 20]` with revenue `b * (30 - b)` (peak at 15):
tests 1, 20, 10, 15, 12, 17, 13, 14, 16, then converges on 15.

The window averaging handles the case where the most recent bps change
is several epochs behind the current epoch: `feesBefore` draws up to 3
epochs ending just before the change, `feesAfter` draws up to 3 of the
most recent epochs starting at the change (including the one just
appended). If the record has fewer than K epochs on one side, the
window shortens rather than dropping the comparison — the controller
works with as little as one sample on each side, and noise is
controlled by the deadband.

<a id="disabled-state"></a>

### Disabled state (`L1DynamicFeeEnabled != 1`)

When the master Mimir is not `1`, the feature is completely inert:
every L1 swap uses `L1SlipMinBPS`, no attribution runs, no epoch
processing runs, and no events are emitted. Existing records
remain in state but are frozen — not consulted, updated, or pruned.

Re-enabling resumes attribution and epoch processing from that
point. Prior records persist but their history is stale; the
algorithm's cold-start rule (no prior bps change → probe up) handles
this naturally, so the first movement after re-enabling requires two
consecutive epochs of activity under the re-enabled feature.

### Events

`dynamic_l1_fee_update` — emitted per `(thorname, pair)` on each bps
change at an epoch boundary:

```json
{
  "thorname":      "...",
  "pair":          "BTC.BTC|ETH.ETH",
  "epoch":         12345,
  "old_bps":       47,
  "new_bps":       48,
  "fees_before":   "1000000000",
  "fees_after":    "1250000000",
  "swap_count":    "142",
  "pool_volume_tor": "980000000000",
  "delta_pct_bps": "2500",
  "reason":        "cold_start_probe" | "continue_up" | "continue_down" |
                   "reverse_up" | "reverse_down" | "search_probe" |
                   "search_converged" | "capped_hold"
}
```

For the search reasons, `fees_before` is the best probe's mean signal,
`fees_after` the mean of the probe just completed, and `delta_pct_bps`
is `0`. `search_converged` is emitted even when the best value equals
the bps just tested, so indexers see the hand-off.

`fees_before` and `fees_after` are **normalized signals**
(`fees_tor * 1e12 / pool_volume_tor`), not TOR fee levels. The
attribute names are kept for continuity with existing consumers.

### Migration

At the first post-upgrade seal a record's entire history is discarded,
because pre-upgrade entries carry raw fee levels rather than the
normalized signal. The record keeps its current `dynamic_bps` and
starts a bracket search from it (see below). There is no store
migration; the filter is a condition at seal time.

Accumulators alive at the upgrade height are a separate problem: one
carries a full epoch of fees but pool volume only from post-upgrade
blocks, so its sealed entry would hold an inflated signal — and being
the first entry, it is the whole comparison baseline when the first
real gradient is evaluated, costing one spurious step. Store migration
`Migrate21to22` (consensus version 22) deletes every in-flight thorname
accumulator at the upgrade height, so the first sealed epoch measures
fees and pool volume over the same span. Records keep their state.

The bracket search needs no store migration either. `search_done`
unmarshals as `false` on records that predate it, so each existing
record starts a search from its current `dynamic_bps` (testing it
first, then the larger untested side) rather than dropping back to the
floor. Whitelist value `2` changes meaning from monitor to global; no
mainnet thorname held `2` at the upgrade, so no record changes mode.
`own_pool_volume_tor`, `blocks` and `pairs` read as zero or empty on
rows written before them; in-flight accumulators are cleared at the
upgrade anyway. `Migrate21to22` also sets every record's
`span_start_height` to the upgrade height, so the time cap counts from
the upgrade rather than sealing every record at the first boundary.

### REST endpoints

- `GET /thorchain/dynamic_l1_fees`
  Returns all currently-tracked `(thorname, pair)` records with their
  current `dynamic_bps`, whitelist state (`1`-`4`), `phase`
  (`search`, `track` or `monitor`), `last_active_epoch`, and the latest epoch's
  `fees_tor` (1e8-precision USD value). A global-mode record's pair is
  `*|*`.

- `GET /thorchain/dynamic_l1_fees/{thorname}`
  Returns the thorname's whitelist state and all pairs tracked for
  it, each with its `phase`, its search `probes` (`bps`, mean
  `signal`, `epochs`), and up to 30 epochs of history
  (`volume_tor`, `fees_tor`, `bps_at_close`, `swap_count`,
  `pool_volume_tor`, `own_pool_volume_tor`).

- `GET /thorchain/dynamic_l1_fees_current`
  Returns the in-progress (current epoch, not yet sealed) accumulators
  for every `(thorname, pair)` plus the current epoch number. Each
  entry has `volume_tor`, `fees_tor`, `swap_count`, `pool_volume_tor`
  and `own_pool_volume_tor` running totals so far this epoch, before
  they are sealed into the next history record at the next epoch
  boundary.

### Integer math and determinism

All arithmetic for the gradient, window mean, and deadband evaluation
uses `cosmos.Uint` (big.Int-backed, consensus-safe). No floating-point
is used anywhere in the algorithm.

- **Window mean**: straight integer sum divided by the window size
  (an integer between 1 and 3 in practice).
- **|delta %|**: `deltaPctBps = common.GetSafeShare(|feesAfter - feesBefore|, feesBefore, NewUint(10000))`.
  `GetSafeShare` returns `0` if either argument is zero, which
  correctly handles the "no baseline" case (treated as hold).
- **Sign of delta**: tracked separately by comparing `feesAfter` and
  `feesBefore` with `GT/LT` on `cosmos.Uint`.
- **Deadband check**: `deltaPctBps.LT(L1DynamicFeeDeadbandBPS)` → hold.
  A mimir value of `0` is a valid, supported configuration: it disables
  the deadband so that every non-zero fee delta moves the floor.

### Efficiency considerations

- State per `(thorname, pair)`: up to 30 epoch records (~60 bytes each)
  plus a small header. A few kilobytes per tracked pair.
- Epoch-boundary work is O(tracked records), bounded by the
  whitelist size + 30-epoch prune. In practice, tens to low
  hundreds of records.
- Gradient calculation per record walks at most 30 history entries
  backwards plus two small means — O(history) ≈ 60 integer
  additions.
- Attribution on each swap is O(number of affiliates in memo), which
  is small (typically 1, occasionally 2–3).

### Observability / monitoring

- Per-change events (above) let indexers reconstruct history and build
  dashboards.
- REST endpoints support operational monitoring and affiliate
  introspection.
- Recommended external metrics: number of whitelisted thornames (in
  each state), number of active `(thorname, pair)` records,
  distribution of `dynamic_bps` values, count of records pinned at
  floor/ceiling, reason-code distribution over the last 30 epochs
  (a healthy system reverses occasionally rather than drifting
  monotonically to a bound).

### Security considerations

- **Sybil resistance** relies on thorname registration cost. Raw
  addresses cannot earn dynamic bps.
- **Wash trading.** The closed-loop controller is harder to game than
  the share-based design because the only lever the affiliate can
  pull — paying artificial fees — directly counts toward the objective
  the controller optimizes. A wash-trading affiliate who continues to
  generate fees at a higher floor is genuinely profitable for the
  protocol; if they stop the moment the floor gets too high, the
  controller sees fees drop and reverses. Additional mitigations:
  (a) **governance-curated whitelisting** — a thorname cannot
  participate at all without being explicitly added via Mimir,
  (b) **monitor mode** (`3` / `4`) which lets operators observe a
  thorname's attribution and volume before promoting it, (c) the
  search's preference for the lower fee, and the slow ±1 bps step
  after it, (d) the percentage deadband, and (e) the configurable
  window averaging (`L1DynamicFeeWindowEpochs`) which smooths
  single-epoch spikes.
- **Self-referential signal.** Dividing by other swappers' volume
  means an affiliate cannot raise its signal by trading more against
  itself; an epoch where it is nearly the whole pool is not used.
- **Floor and ceiling** guarantee the mechanism cannot push fees
  outside `[1, 100]` bps regardless of signal behavior.
- **Master switch** (`L1DynamicFeeEnabled = 0`) is an immediate,
  total kill. See [Disabled state](#disabled-state).
- **Determinism:** all arithmetic uses integer math on TOR-denominated
  values and bps. No floating point.

### Testing

- Unit tests on the gradient-decision helper (`decideNextBps`)
  covering every reason branch: cold-start probe, continue_up,
  continue_down, reverse_up, reverse_down, hold (in deadband), and
  the "no baseline" edge where `fees_before` is zero.
- Unit tests on attribution: L1-only filtering, pair normalization,
  RUNE-endpoint handling, multi-affiliate credit, streaming sub-swap
  crediting, raw-address / unregistered-thorname exclusion,
  whitelist state gating (state `0`/absent, `1`-`4`), and global-mode
  crediting into one `*|*` record.
- Unit tests on epoch rollover: sealing into history, floor/ceiling
  clamping at either bound, pruning at 30 epochs, cold start,
  whitelist removal deleting state.
- Unit test confirming the solo-on-pair regression: a thorname that
  is the only affiliate active on its pair still sees its floor
  move, unlike the previous share-based design, as long as other
  swappers also use the pair; with no outside flow the epoch is held.
- Unit tests on fee-floor selection: whitelist states `1` and `2`
  apply `dynamic_bps` (per pair / from the `*|*` record), states `3`
  and `4` use `L1SlipMinBPS` but still collect; in multi-affiliate
  memos only the largest-bps affiliate is charged or credited, with no
  fallback to a smaller one.
- Unit tests on measurement: the signal excludes the thorname's own
  volume and holds under `L1DynamicFeeMinOtherFlowBPS`; a 90%-share
  integrator converges near its revenue peak; monitor records collect
  history only; a global record is measured on the pairs it traded.
- Unit tests on the time cap: a span below the gate rolls and is
  sealed once it reaches `L1DynamicFeeMaxEpochBlocks`; a span with no
  swaps is sealed empty, reads as zero revenue, moves the search on and
  does not count as activity; monitor records are never sealed empty;
  a record without a span start begins one; a capped reading never
  displaces a lower-fee best in the search, and in the ±step controller
  turns a raise into `capped_hold` and a cold start into a step down.
- Unit tests on the search: the next-value rule over every gap shape,
  the deadband's lower-fee preference, a full run converging on a
  unimodal curve, `L1DynamicFeeProbeEpochs`, and a pre-existing
  record searching from its current bps.
- A Mimir-handler test that a change of effective whitelist value
  wipes the thorname's state, while a sub-threshold vote, a re-vote of
  the value in effect, and other thornames' state are left alone.

### Rollout

1. Deploy with `L1DynamicFeeEnabled = 0` and no whitelist entries.
   No behavior change.
2. Tune operational Mimir defaults if needed in stagenet.
3. Flip `L1DynamicFeeEnabled = 1`. Still no effect: no thornames
   are whitelisted.
4. Optionally add a small set of thornames to the whitelist in state
   `3` or `4` (monitor). Observe their accrual via REST over a few
   epochs to validate attribution and volume (including the share of
   their pairs that comes from other swappers) against real flow.
5. Set selected thornames to `1` (per-pair) or `2` (global). This
   resets them, so the live search starts from the floor. Prefer `2`
   for a thorname whose individual pairs are too thin to clear the
   liquidity gate.
6. Adjust bounds / step / deadband as needed based on observed
   behavior.

## Consequences

### Positive

- **Directly optimizes the stated objective.** The controller measures
  `fees_tor` (USD-equivalent fees) and moves the lever that affects
  `fees_tor` — there is no proxy between the signal and the goal.
  "Higher volume" shows up automatically: when a floor is too high on
  a price-sensitive pair, volume drops, fees drop, and the controller
  reverses.
- **Closed-loop self-correction.** The algorithm asks "did my last
  move help?" and reverses when it didn't. Exogenous noise that
  would have misled the open-loop share design — a competitor
  appearing, a promo ending, a macro move — still causes one wrong
  step, but the next epoch's evaluation catches it and reverses.
- **Macro-noise-resistant signal.** Tracking in TOR (USD-equivalent
  via `DollarsPerRune`) cancels the dominant macro noise term that a
  pure `fees_rune` signal would carry. RUNE/USD price moves no longer
  push the floor in spurious directions.
- **Solo-on-pair works.** A thorname that owns a pair is no longer in
  a dead zone; their `fees_tor` is a live signal regardless of who
  else trades the pair.
- **No scalar collapse.** The signal is `fees_tor`; there are no
  fee/volume weights to tune by taste.
- **No phantom multi-affiliate signal.** Each thorname's signal is
  its own `fees_tor`, not a share that shifts when co-listed
  thornames change behavior.
- **Simpler state.** No per-pair record, no per-pair accumulator,
  fewer mimirs, smaller proto surface.
- **Monitor mode** (whitelist value `2`) remains a cheap, reversible
  validation step.
- **Failure mode is graceful**: the master Mimir is an immediate,
  total kill switch (see [Disabled state](#disabled-state)).

### Negative

- **Tor-denominated signal still moves with total network activity.**
  Tracking in `fees_tor` cancels the dominant macro noise term —
  RUNE/USD price moves no longer contaminate the gradient. What
  remains is genuine activity-driven variance (a quiet trading day
  shows up as lower `fees_tor` in absolute terms). Mitigated by (a)
  the configurable window averaging (`L1DynamicFeeWindowEpochs`,
  default 3) and (b) the 10%-default deadband. ±1 bps/epoch × clamp
  box means any mis-step cost is bounded regardless.
- **TOR oracle dependency.** When the TOR anchor pools are unavailable
  (no whitelisted USD anchors, or all halted), `DollarsPerRune`
  returns zero and dynamic-fee crediting is skipped for affected
  swaps. Existing dynamic_bps values are preserved until the next
  epoch boundary, but no new gradient signal accrues during the
  outage. Re-enabling anchors resumes accrual on the next swap.
- **Adds state and per-epoch computation.** Bounded by the whitelist
  size and the 30-epoch prune horizon, but non-zero.
- **Adds attribution logic to the swap hot path** (whitelist state
  lookup, accumulator updates, affiliate parsing). Small cost, but
  not free.
- **Curation burden.** Participation requires explicit governance
  action; new affiliates that grow organically don't automatically
  benefit — they must be added. This is by design (operators have
  wanted this control) but it is work.
- **Streaming swaps crediting each sub-swap independently** means a
  stream with many sub-swaps carries proportionally more weight than
  a single one-shot swap of equal total size. Predictable and splits
  naturally across epoch boundaries, but it does amplify a single
  user's impact on a thorname's signal.
- **Ceiling can exceed the network default.** The dynamic ceiling
  (`L1DynamicFeeCeilingBPS`, default 20) is not capped at
  `L1SlipMinBPS`. A whitelisted thorname whose fees degrade can
  drift _above_ `L1SlipMinBPS` — meaning swaps attributed to them
  incur a stricter floor than non-affiliated swaps. This is by
  design: it keeps the feedback signal symmetric. A thorname can
  escape the penalty by being demoted to monitor mode, removed from
  the whitelist, or by the controller finding a better local
  optimum in subsequent epochs.

### Neutral

- Raw-address affiliates keep getting `L1SlipMinBPS`. This is by
  design but means integrators who never adopted thornames see no
  change either positive or negative.
- Newly-whitelisted thornames start with `dynamic_bps =
L1DynamicFeeFloorBPS` on record creation — that is, at the most
  discounted possible floor. The first real move is always a probe
  upward, and the controller takes over from the second evaluated
  move onward. Changes to `L1SlipMinBPS` do not affect the dynamic
  value for any whitelisted thorname.

## References

- [Mimir reference documentation](../mimir.md)
- [ADR 016: Affiliate revenue share](./adr-016-aff-rev-share.md)
- [ADR 010: Streaming swaps](./adr-010-streaming-swaps.md)
