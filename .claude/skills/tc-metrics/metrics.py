#!/usr/bin/env python3
"""Rank THORNode metric series by how unusual their newest hour is.

  metrics.py                     ranked triage report (all series)
  metrics.py --top 60            show N rows (default 40, 0 = all)
  metrics.py --grep REGEX        restrict to matching series
  metrics.py --window 7d|14d     scoring window (default 14d)
  metrics.py --series NAME       full history for one series, oldest first
"""
import argparse
import json
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone

BASE = "https://gateway-lon.liquify.com/chain/thorchain_midgard/v2/metrics"
# ponytail: no fallback host -- only this deployment serves /v2/metrics today.

# Value alone is the signal on these; rank is irrelevant. Always shown.
ALWAYS = re.compile(r"^thornode_(invariant_broken|solvency_excess_e8)")
# |modifiedZ| this large means MAD collapsed to ~0: the series was flat across
# the window and its level differs from the window median. Real event or noise --
# the z magnitude cannot tell you which, so flag it for a --series drill-in rather
# than dropping it. z is distance from the median, so it stays this large every
# hour after one step; only a low deltaCommonness says the step was this hour.
MAD_FLAT = 100.0


def get(path, **params):
    url = path if not params else path + "?" + urllib.parse.urlencode(params)
    try:
        with urllib.request.urlopen(BASE + url, timeout=90) as r:
            return json.load(r)
    except urllib.error.HTTPError as e:
        sys.exit(
            f"{e.code} from {BASE + url}"
            + (
                " -- metric collection is disabled on this instance."
                if e.code == 404
                else ""
            )
        )
    except (urllib.error.URLError, TimeoutError) as e:
        sys.exit(f"cannot reach {BASE}: {e}")


def num(row, key):
    return float(row[key])


def when(row):
    return datetime.fromtimestamp(int(row["date"]) / 1e9, timezone.utc).strftime(
        "%Y-%m-%d %H:%MZ"
    )


def fmt(v):
    a = abs(v)
    if a and (a >= 1e7 or a < 1e-3):
        return f"{v:.4g}"
    return f"{v:,.4f}".rstrip("0").rstrip(".")


def note(row):
    z, n = num(row, "modifiedZ"), int(row["bucketCount"])
    bits = []
    if int(row["samples"]) == 0:
        bits.append("STALE: no samples this hour, value carried forward")
    if n < 8:
        bits.append(f"only {n} buckets of history -- score is not yet meaningful")
    if abs(z) > MAD_FLAT:
        if num(row, "deltaCommonness") <= max(0.05, 2.0 / n + 1e-12):
            bits.append(
                "was flat, moved this hour; z magnitude unreliable -- size it with --series"
            )
        else:
            bits.append("level shifted within window; --series for when")
    if num(row, "commonness") <= 2.0 / n + 1e-12:
        bits.append("at floor: most extreme hour in window")
    if abs(z) >= 3.5 and abs(z) <= MAD_FLAT:
        bits.append("|z|>=3.5")
    return "; ".join(bits)


def table(rows):
    hdr = ["commonness", "z", "pctile", "n", "samples", "value", "series", "notes"]
    out = [
        [
            f"{num(r,'commonness'):.3f}",
            fmt(num(r, "modifiedZ")),
            f"{num(r,'percentileRank'):.2f}",
            r["bucketCount"],
            r["samples"],
            fmt(num(r, "value")),
            r["series"],
            note(r),
        ]
        for r in rows
    ]
    w = [
        max(len(h), *(len(o[i]) for o in out)) if out else len(h)
        for i, h in enumerate(hdr)
    ]
    lines = [
        "  ".join(h.ljust(w[i]) for i, h in enumerate(hdr)).rstrip(),
        "  ".join("-" * w[i] for i in range(len(hdr))),
    ]
    lines += ["  ".join(c.ljust(w[i]) for i, c in enumerate(o)).rstrip() for o in out]
    return "\n".join(lines)


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--window", default="14d")
    p.add_argument("--top", type=int, default=40)
    p.add_argument("--grep")
    p.add_argument("--series")
    a = p.parse_args()

    if a.series:
        rows = get("/data", series=a.series, window=a.window)
        if not rows:
            sys.exit(
                f"no data for series {a.series!r} -- name must match exactly, labels included"
            )
        print(f"{a.series}  ({a.window}, oldest first)\n")
        print(
            "\n".join(
                f"{when(r)}  value={fmt(num(r,'value')):>18}  "
                f"commonness={num(r,'commonness'):.3f}  d={num(r,'deltaCommonness'):.3f}  "
                f"z={fmt(num(r,'modifiedZ')):>10}  samples={r['samples']}"
                for r in rows
            )
        )
        return

    rows = get("/scores", window=a.window)
    if not rows:
        sys.exit("no scored series -- collection may have just started")
    ns = sorted(int(r["bucketCount"]) for r in rows)
    common_n = max(set(ns), key=ns.count)
    print(f"# newest completed hour: {when(rows[0])}  (up to ~2h behind now)")
    print(
        f"# window {a.window}: {len(rows)} series, {ns[0]}-{ns[-1]} hourly buckets each "
        f"(most have {common_n}, floor {2.0/common_n:.3f})"
    )
    stale = sum(1 for r in rows if int(r["samples"]) == 0)
    if stale:
        print(
            f"# {stale} series STALE this hour (samples=0, value carried forward) -- listed first"
        )
    if 2.0 / common_n > 0.05:
        print(
            "# /anomalies at its 0.05 default can only return STALE series -- most series' floor is above it. "
            + (
                "At 24h it rejects below<0.083 outright; "
                if a.window == "24h"
                else "Needs n>=40 buckets; "
            )
            + "ranking /scores directly instead."
        )
    print(
        "# node-local (one thornode): go_*, process_*, promhttp_*, and under tendermint_ or cometbft_: "
        "*_p2p_*, *_mempool_*, *_blocksync_*, *_consensus_step_duration*"
    )
    print(
        "# seen by one node, corroborate before calling it network-wide: other tendermint_consensus_* / cometbft_consensus_*"
    )
    print("# network-wide: thornode_*, swap_*, pool_*, liquidity_*")
    print()

    always = [r for r in rows if ALWAYS.match(r["series"])]
    if always:
        print("## read by value, not by rank\n")
        print(table(sorted(always, key=lambda r: r["series"])))
        print()

    pool = rows if not a.grep else [r for r in rows if re.search(a.grep, r["series"])]
    if a.grep and not pool:
        sys.exit(f"no series matching {a.grep!r}")
    # samples==0 first: a carried-forward hour ties with the last real reading, so a series
    # whose source failed scores commonness 1.0 and would never survive --top. Rank cannot see it.
    pool.sort(
        key=lambda r: (
            int(r["samples"]) != 0,
            num(r, "commonness"),
            -abs(num(r, "modifiedZ")),
        )
    )
    shown = pool if a.top <= 0 else pool[: a.top]
    print(f"## {len(shown)} of {len(pool)} series, least common first\n")
    print(table(shown))


if __name__ == "__main__":
    main()
