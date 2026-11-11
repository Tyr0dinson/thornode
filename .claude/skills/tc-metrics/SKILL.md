---
name: tc-metrics
description: Investigate THORChain mainnet using Midgard's metric anomaly scores - which THORNode/CometBFT series are behaving unusually against their own recent history. Use when something feels off on mainnet but there is no specific transaction to chase, when correlating a known incident to a time window, or when asked what changed, what is anomalous, or what is drifting.
allowed-tools: Bash, Read, Write, Edit, AskUserQuestion, Grep, Glob, WebFetch
---

# THORChain Metric Anomaly Triage

**Improvement policy**: When the user corrects behavior or gives feedback about this skill, always update THIS file (SKILL.md) directly — never save feedback memories. This skill is a shared artifact; improvements belong here so all users benefit.

## What this is

Midgard scrapes one THORNode's Prometheus endpoint plus a few facts it fetches from THORNode's REST API instead (invariants, per-asset solvency, mimir, vault balances: every `thornode_*` series), rolls samples into hourly buckets, and scores every bucket against every other bucket of its own series. It answers **"is this hour unusual for this series?"** — not "is this value bad". You supply the "bad".

Companion skills: `health` for a point-in-time network checklist, `tc-debug` when you have a tx hash / address / vault to chase. Use `tc-metrics` when you only have a symptom and a rough time.

## Endpoint

|      |                                                                      |
| ---- | -------------------------------------------------------------------- |
| Base | `https://gateway-lon.liquify.com/chain/thorchain_midgard/v2/metrics` |

**No fallback.** `gateway.liquify.com` and `midgard.thorchain.network` both 404 — this is deployed on one Midgard instance ([midgard!733](https://gitlab.com/thorchain/midgard/-/merge_requests/733)). A 404 means metric collection is disabled on the instance you hit, not that the network is fine.

| Route               |                                                                                                             |
| ------------------- | ----------------------------------------------------------------------------------------------------------- |
| `/scores`           | every series' newest completed hour, least common first                                                     |
| `/anomalies?below=` | `/scores` cut at a commonness threshold (default 0.05), plus every series whose newest hour has `samples=0` |
| `/data?series=`     | every scored hour for one series, oldest first                                                              |

All take `?window=24h\|7d\|14d` (default `14d`). At the default `below=0.05`, `/anomalies` rejects `24h` with a 400 (24 buckets cannot score below `2/24 ≈ 0.083`); pass an explicit `below` of at least that, e.g. `?window=24h&below=0.2`.

## Script

`metrics.py` in this directory. Use it instead of raw curl — it handles the string-typed numbers, ns timestamps, per-series bucket counts, and the artifacts below.

```text
./metrics.py                          ranked triage report, top 40 of 736
./metrics.py --top 0                  all 736 series
./metrics.py --grep 'vault_balance'   restrict to matching series
./metrics.py --window 7d
./metrics.py --series '<exact name>'  full hourly history for one series
```

Rank order is commonness ascending, then `|modifiedZ|` descending. Every series is ranked; nothing is filtered out.

## Fields

| field               | meaning                                                                                                                 |
| ------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| `commonness`        | share of the window at least as extreme as this hour, two-tailed. `1` = ordinary, `2/n` = the single most extreme hour. |
| `deltaCommonness`   | same, for the hour-over-hour _change_. Low here + high `commonness` = a jump to a normal value.                         |
| `percentileRank`    | position in the window, `0` low end … `1` high end. Direction only.                                                     |
| `modifiedZ`         | `0.6745·(value − median)/MAD`. Magnitude and sign. Conventional cutoff \|z\| ≥ 3.5.                                     |
| `samples`           | real readings in the hour. **`0` = carried forward: this series' source failed this hour.** Other series may be fresh.  |
| `bucketCount` (`n`) | hours of history for _this series_. Varies per series.                                                                  |
| `value`             | gauges = hour average; counters (`_total`/`_count`/`_sum`) = increase within the hour.                                  |

## Procedure

1. **Run `./metrics.py`.** Read the header first — it prints the newest completed hour, the per-series bucket range, and whether `/anomalies` is currently degenerate.
2. **Read the "by value, not by rank" block.** `thornode_invariant_broken` (`1` = broken) and `thornode_solvency_excess_e8` (negative = insolvent by that many e8) mean something regardless of how they score. A permanently-broken invariant scores as perfectly common. For anything nonzero, cross-check `GET /thorchain/invariant/{name}` on the THORNode API for the broken amounts, and `--series` for when it broke — several of these are long-standing and not news.
3. **Scan the ranked table**, reading the notes column (see below). Note the `n` column: a row with `n < 8` has almost no history and its score is noise.
4. **Attribute each candidate, series by series.** Every `tendermint_*` / `cometbft_*` series is what the single scraped node observed, not a chain-wide measurement.
   - **Node-local** — describes the Liquify node only: `go_*`, `process_*`, `promhttp_*`, `*_p2p_*`, `*_mempool_*`, `*_blocksync_*` (e.g. `blocksync_syncing` is set while _this_ node catches up after a restart), and `*_consensus_step_duration*` (this node's own time per consensus step). A GC pause, restart or slow disk there is not a mainnet incident.
   - **Seen by one node, needs corroboration** — the other `*_consensus_*` series (rounds, block interval, validators, byzantine, per-proposer delays). They usually reflect the chain, but a lagging or restarting scraped node distorts them too. Count one as a network finding only once something independent agrees: `/thorchain/lastblock` heights and timestamps from another node, a matching `thornode_*` series, or the same shape on several unrelated consensus series while the node-local ones above are quiet.
   - **Network-wide** — `thornode_*` (chain state the node reports from consensus), `swap_*`, `pool_*`, `liquidity_*` (Midgard's own DB).
5. **Drill in with `--series`** on anything that survives, to see whether it is a spike, a step change, a monotone trend (not an anomaly — see below), or a slow drift, and when it started.
6. **Confirm the current level against the source API before quoting it.** `value` is the _hour average_, so a mid-hour step lands between the old and new levels and matches neither. A `thornode_vault_balance_e8` row averaging 258,700 DAI was really 201,100 until ~:46 and 441,072 after; only `/thorchain/vaults/asgard` showed the real number. Use the metric for _when_ and _how unusual_; use the API for _what_.
7. **Hand off.** A time window plus a suspect series is the input to `tc-debug` (logs, txs) or `health` (current state). This skill does not itself explain _why_.

## What the numbers cannot tell you

- **Rank cannot measure distance.** 1% past the window maximum and 100× it both score `2/n`. Read `modifiedZ` alongside.
- **Rank cannot see a dead source.** Carried-forward hours are ties, and every tie pushes commonness _up_, so a frozen series looks healthier the longer it has been dead. `samples = 0` is the only tell; the script flags it `STALE`, counts them in the header, and sorts them above the ranking so `--top` cannot hide them. Staleness is per series: Midgard keeps successful samples when another source fails, skips individual failed invariant queries, and carries each series forward on its own. One stale invariant says nothing about the freshness of the Prometheus or REST series next to it; treat the whole run as stale only when every series from a source (e.g. all Prometheus series) is stale together.
- **Nothing here is real-time.** "Newest completed hour" is up to ~2h behind. Frame findings as "what was unusual last hour", never "what is happening now". For now, use `health`.

## Rows that need a second look

Ranking is never filtered — every series is scored and sorted. These are the shapes that mislead if you read the score alone.

| Symptom                                                                                       | What it means                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    |
| --------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| \|z\| in the hundreds to 1e14                                                                 | MAD collapsed to ~0: the series was flat across the window and its level now differs from the median. z stays this large every hour after one step, so the script says "moved this hour" only when `deltaCommonness` is also low, and "level shifted within window; --series for when" otherwise. On `thornode_vault_balance_e8` that is an inbound or outbound — exactly what you are looking for. On a per-proposer delay it is usually noise. The z magnitude cannot tell them apart; `--series` shows the size of the step. Never dismiss on the flag alone. |
| Every `tendermint_consensus_*_delay{proposer_address=…}` at the floor                         | 249 per-proposer series; several sit at their extreme every hour by construction. Only meaningful when you already suspect a named validator — resolve the address via `health`'s node map.                                                                                                                                                                                                                                                                                                                                                                      |
| `thornode_mimir{key=…}` at the floor                                                          | 277 series, flat for weeks. A low `deltaCommonness` here is the real signal: that mimir just changed.                                                                                                                                                                                                                                                                                                                                                                                                                                                            |
| A series at the floor **every** hour, with `\|z\|` walking smoothly from positive to negative | A monotone trend, not an anomaly. Its newest value is always the window extreme by construction. `thornode_mimir{key="MAXRUNESUPPLY"}` (decays continuously) and `tendermint_consensus_height` both do this permanently. `--series` shows a straight line — dismiss it.                                                                                                                                                                                                                                                                                          |
| Everything tied at the same floor value                                                       | The series only have a handful of buckets (the header prints how many). Ties at the floor are not a ranking. Compare `modifiedZ` instead until history fills.                                                                                                                                                                                                                                                                                                                                                                                                    |

## Cold start

Most series were only recently collected, so their floor `2/n` sits above the `/anomalies` default of 0.05 until they reach n ≥ 40 (about 40 hours of history). Until then `/anomalies` returns only the `samples=0` series, for reasons that have nothing to do with the network. The four Midgard-native series (`swap_count`, `swap_volume_usd_e8`, `liquidity_fees_rune_e8`, `pool_balance_rune_e8`) are backfilled from Midgard's own DB and already have full history. `metrics.py` prints the current bucket range and floor in its header and ranks `/scores` directly when `/anomalies` is degenerate; trust the header, not a remembered number, and never conclude "no anomalies" from an empty `/anomalies`.

## Common mistakes

- Reporting a node-local `go_*` / `process_*` spike as a mainnet issue.
- Treating an empty `/anomalies` as an all-clear (see Cold start).
- Comparing `commonness` across series with different `n`.
- Reading a counter's `value` as a level — it is the increase during that hour.
- Passing a partial name to `--series`: it needs the exact series string, labels included. Copy it from the ranked table.
