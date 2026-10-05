#!/usr/bin/env python3
"""KEEP/REVERT verdict from `benchstat -format csv base.txt new.txt`.

KEEP when, for at least one target benchmark, sec/op or allocs/op improves by at
least --min-gain (default 5%) with p < --alpha (benchstat's own significance
test, default 0.05), and no benchmark's sec/op regresses significantly by more
than --max-regress (default 5%).

Any gas/op difference is reported as CONSENSUS-VISIBLE: gas_used is hashed into
LastResultsHash, so such a change must ship as a coordinated upgrade. It does
not by itself block KEEP.

Exit status: 0 KEEP, 1 REVERT.
"""

import argparse
import csv
import re
import sys


def parse(path):
    """Return {unit: {bench: (base, new, delta_pct or None, p)}}."""
    out = {}
    unit = None
    with open(path) as f:
        for row in csv.reader(f):
            if not row or not row[0] and len(row) > 1 and row[1] in ("",):
                continue
            if row[0] == "" and len(row) > 1 and row[1] and "CI" in row:
                unit = row[1]
                out.setdefault(unit, {})
                continue
            if unit is None or row[0] in ("", "geomean") or len(row) < 7:
                continue
            name, base, _, new, _, delta, p = row[:7]
            m = re.search(r"p=([0-9.]+)", p)
            pval = float(m.group(1)) if m else 1.0
            d = None
            if delta not in ("~", ""):
                d = float(delta.rstrip("%"))
            try:
                out[unit][name] = (float(base or 0), float(new or 0), d, pval)
            except ValueError:
                continue
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("csv")
    ap.add_argument(
        "--target",
        default="",
        help="regex of benchmarks the idea targets (default: all)",
    )
    ap.add_argument("--min-gain", type=float, default=5.0)
    ap.add_argument("--max-regress", type=float, default=5.0)
    ap.add_argument("--alpha", type=float, default=0.05)
    args = ap.parse_args()

    data = parse(args.csv)
    target = re.compile(args.target) if args.target else None
    wins, regressions, gas = [], [], []

    for unit, rows in data.items():
        for name, (base, new, d, p) in rows.items():
            is_target = target is None or target.search(name)
            if unit == "gas/op":
                if base != new:
                    gas.append(f"{name}: gas/op {base:.0f} -> {new:.0f}")
                continue
            if d is None or p >= args.alpha:
                continue
            if unit in ("sec/op", "allocs/op") and is_target and d <= -args.min_gain:
                wins.append(f"{name}: {unit} {d:+.2f}% (p={p:.3f})")
            if unit == "sec/op" and d >= args.max_regress:
                regressions.append(f"{name}: sec/op {d:+.2f}% (p={p:.3f})")

    keep = bool(wins) and not regressions
    print("VERDICT:", "KEEP" if keep else "REVERT")
    for w in wins:
        print("  win:       ", w)
    for r in regressions:
        print("  regression:", r)
    if not wins:
        print(
            f"  no target benchmark improved >= {args.min_gain}% with p < {args.alpha}"
        )
    if gas:
        print("  CONSENSUS-VISIBLE: gas_used changes (needs coordinated upgrade):")
        for g in gas:
            print("   ", g)
    else:
        print("  gas/op unchanged")
    return 0 if keep else 1


if __name__ == "__main__":
    sys.exit(main())
