#!/usr/bin/env bash
# Behavior guard for a perf candidate (the working tree) against <base-ref>.
#
#   scripts/perf/guard.sh <label> [base-ref]
#
# 1. builds thornode and vets the touched packages
# 2. unit tests (-tags=mocknet) for touched packages
# 3. regtest, unfiltered, for base and candidate (cached by tree hash), then
#    regtest_diff.py: any state/event/app_hash difference FAILS; gas_used or
#    last_results_hash-only differences PASS but are flagged consensus-visible.
#
# Env: SKIP_REGTEST=1, RUN=<suite-regex>, REGTEST_PARALLELISM (default 12).
set -euo pipefail
source "$(dirname "$0")/lib.sh"

label=${1:?label required}
base_ref=${2:-HEAD}
out="$PERF_RESULTS/$label"
mkdir -p "$out" "$PERF_CACHE"
base_sha=$(git -C "$PERF_ROOT" rev-parse "$base_ref")

mapfile -t changed < <(
  git -C "$PERF_ROOT" diff --name-only "$base_sha" -- '*.go'
  git -C "$PERF_ROOT" ls-files --others --exclude-standard -- '*.go'
)
mapfile -t pkgs < <(printf '%s\n' "${changed[@]}" | grep -v '^$' | xargs -r -n1 dirname | sort -u | sed 's|^|./|')
perf_log "touched packages: ${pkgs[*]:-none}"

perf_log "build + vet"
(cd "$PERF_ROOT" && go build -tags=mocknet ./cmd/thornode && rm -f thornode)
if ((${#pkgs[@]})); then
  (cd "$PERF_ROOT" && go vet -tags=mocknet "${pkgs[@]}")
  perf_log "unit tests"
  if ! (cd "$PERF_ROOT" && go test -tags=mocknet -count=1 "${pkgs[@]}" >"$out/unit.log" 2>&1); then
    perf_log "unit tests FAILED; see $out/unit.log"
    grep -E '^(--- FAIL|FAIL|ok )' "$out/unit.log" | head -40 >&2
    exit 1
  fi
fi

if [[ -n ${SKIP_REGTEST-} ]]; then
  perf_log "SKIP_REGTEST set; not running regtest"
  exit 0
fi

base_tree="$PERF_CACHE/tree-$base_sha"
if [[ ! -d $base_tree ]]; then
  git -C "$PERF_ROOT" worktree add --detach "$base_tree" "$base_sha" >/dev/null
fi
base_hash=$(perf_tree_hash "$base_tree")
cand_hash=$(perf_tree_hash "$PERF_ROOT")
suffix=${RUN:+-$(echo "$RUN" | md5sum | cut -c1-8)}
perf_regtest "$base_tree" "$PERF_CACHE/regtest-$base_hash$suffix"
if [[ $cand_hash == "$base_hash" ]]; then
  perf_log "candidate tree == base tree"
fi
perf_regtest "$PERF_ROOT" "$PERF_CACHE/regtest-$cand_hash$suffix"

python3 "$(dirname "$0")/regtest_diff.py" \
  "$PERF_CACHE/regtest-$base_hash$suffix" "$PERF_CACHE/regtest-$cand_hash$suffix" | tee "$out/guard.txt"
exit "${PIPESTATUS[0]}"
