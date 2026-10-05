#!/usr/bin/env bash
# Interleaved A/B benchmark: <base-ref> (default HEAD) vs the working tree.
#
#   scripts/perf/ab.sh <label> [bench-regex] [base-ref]
#
# The base is built from a cached detached worktree, the candidate from the
# working tree. Runs alternate base/candidate ROUNDS times (default 10) with one
# sample each, so slow drift on the box hits both sides equally. Ends with the
# KEEP/REVERT verdict from verdict.py (exit 0 = KEEP, 1 = REVERT/inconclusive).
set -euo pipefail
source "$(dirname "$0")/lib.sh"

label=${1:?label required}
bench=${2:-$PERF_BENCH}
base_ref=${3:-HEAD}
rounds=${ROUNDS:-10}
out="$PERF_RESULTS/$label"
mkdir -p "$out" "$PERF_CACHE"

perf_env_check

base_sha=$(git -C "$PERF_ROOT" rev-parse "$base_ref")
base_tree="$PERF_CACHE/tree-$base_sha"
base_bin="$PERF_CACHE/thorchain-$base_sha-$PERF_TAGS.test"
if [[ ! -x $base_bin ]]; then
  if [[ ! -d $base_tree ]]; then
    perf_log "creating base worktree at $base_sha"
    git -C "$PERF_ROOT" worktree add --detach "$base_tree" "$base_sha" >/dev/null
  fi
  perf_log "building base test binary"
  perf_build_test_binary "$base_tree" "$base_bin"
fi
perf_log "building candidate test binary"
perf_build_test_binary "$PERF_ROOT" "$out/candidate.test"

echo "$base_sha" >"$out/base-commit"
git -C "$PERF_ROOT" diff "$base_sha" >"$out/diff.patch"
: >"$out/base.txt"
: >"$out/new.txt"
for ((i = 1; i <= rounds; i++)); do
  perf_log "round $i/$rounds"
  perf_run_bench "$base_tree" "$base_bin" "$bench" 1 >>"$out/base.txt"
  perf_run_bench "$PERF_ROOT" "$out/candidate.test" "$bench" 1 >>"$out/new.txt"
done
if grep -qE '^(--- FAIL|FAIL|panic)' "$out/base.txt" "$out/new.txt"; then
  perf_log "benchmark failures; see $out/{base,new}.txt"
  exit 2
fi

(cd "$out" && $BENCHSTAT base.txt new.txt | tee benchstat.txt)
(cd "$out" && $BENCHSTAT -format csv base.txt new.txt >benchstat.csv 2>/dev/null)
python3 "$(dirname "$0")/verdict.py" "$out/benchstat.csv" ${PERF_TARGET:+--target "$PERF_TARGET"} | tee "$out/verdict.txt"
exit "${PIPESTATUS[0]}"
