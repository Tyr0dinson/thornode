#!/usr/bin/env python3
"""Compare two unfiltered regtest runs (scripts/perf/lib.sh perf_regtest).

    regtest_diff.py <base-run-dir> <candidate-run-dir>

PASS when every suite's exports and blocks are identical. Differences confined
to tx gas_used and header last_results_hash (which hashes gas_used) PASS but are
flagged CONSENSUS-VISIBLE. Anything else (state, events, app_hash, a suite
missing or failing on one side) FAILS.

Exit status: 0 pass, 1 fail.
"""

import json
import os
import re
import sys

GAS_KEYS = {"gas_used", "last_results_hash"}


def load(path):
    with open(path) as f:
        return json.load(f)


def diff(a, b, path, out):
    if type(a) is not type(b):
        out.append((path, a, b))
    elif isinstance(a, dict):
        for k in sorted(set(a) | set(b)):
            if k not in a or k not in b:
                out.append((f"{path}.{k}", a.get(k), b.get(k)))
            else:
                diff(a[k], b[k], f"{path}.{k}", out)
    elif isinstance(a, list):
        if len(a) != len(b):
            out.append((f"{path}[len]", len(a), len(b)))
        for i, (x, y) in enumerate(zip(a, b)):
            diff(x, y, f"{path}[{i}]", out)
    elif a != b:
        out.append((path, a, b))


def files(root):
    res = {}
    for sub in ("exports", "blocks"):
        d = os.path.join(root, "mnt", sub)
        for dirpath, _, names in os.walk(d):
            for n in names:
                if n.endswith(".json"):
                    full = os.path.join(dirpath, n)
                    res[os.path.relpath(full, os.path.join(root, "mnt"))] = full
    return res


def failed_suites(root):
    try:
        log = open(os.path.join(root, "regtest.log"), errors="replace").read()
    except OSError:
        return set()
    m = re.findall(r"failed=\[([^\]]*)\]", log)
    return set(x for x in (m[-1].split() if m else []) if x)


def main():
    base, cand = sys.argv[1], sys.argv[2]
    bf, cf = files(base), files(cand)
    fails, gas = [], []
    for rel in sorted(set(bf) | set(cf)):
        if rel not in bf or rel not in cf:
            fails.append(f"{rel}: only in {'base' if rel in bf else 'candidate'}")
            continue
        out = []
        diff(load(bf[rel]), load(cf[rel]), "", out)
        for path, a, b in out:
            leaf = path.rsplit(".", 1)[-1]
            if leaf in GAS_KEYS:
                gas.append(f"{rel}{path}: {a} -> {b}")
            else:
                fails.append(
                    f"{rel}{path}: {json.dumps(a)[:120]} -> {json.dumps(b)[:120]}"
                )

    bfail, cfail = failed_suites(base), failed_suites(cand)
    for s in sorted(cfail - bfail):
        fails.append(f"suite failed only on candidate: {s}")
    for s in sorted(bfail):
        print(f"note: suite failed on base too: {s}")

    rcs = {}
    for name, root in (("base", base), ("candidate", cand)):
        try:
            rcs[name] = open(os.path.join(root, "rc")).read().strip()
        except OSError:
            rcs[name] = "?"
    print(
        f"regtest rc: base={rcs['base']} candidate={rcs['candidate']}; compared {len(set(bf) & set(cf))} files"
    )

    if fails:
        print(f"GUARD: FAIL ({len(fails)} behavior differences)")
        for f in fails[:50]:
            print("  ", f)
        if len(fails) > 50:
            print(f"   ... {len(fails) - 50} more")
        return 1
    print("GUARD: PASS")
    if gas:
        print(
            f"  CONSENSUS-VISIBLE: {len(gas)} gas_used/last_results_hash differences (state identical)"
        )
        for g in gas[:20]:
            print("   ", g)
    else:
        print("  gas_used identical")
    return 0


if __name__ == "__main__":
    sys.exit(main())
