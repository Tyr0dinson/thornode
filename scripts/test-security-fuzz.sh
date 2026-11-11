#!/usr/bin/env bash
set -euo pipefail

# FUZZ_TARGET selects one named target or "all". Every target gets its own go invocation.
target_filter=${FUZZ_TARGET:-all}
budget=${FUZZ_TIME:-5s}
corpus_dir=${FUZZ_CORPUS_DIR:-/tmp/thornode-security-fuzz-corpus}
mkdir -p "$corpus_dir"
corpus_dir=$(cd "$corpus_dir" && pwd)

targets=(
  'FuzzParseMemo|./x/thorchain/memo'
  'FuzzNewAddress|./common'
  'FuzzRPCVersion|./bifrost/pkg/chainclients/solana/rpc'
  'FuzzFrostEvidenceDecode|./bifrost/p2p/messages'
  'FuzzFrostSignedPayload|./bifrost/p2p/messages'
)

selected=0
for spec in "${targets[@]}"; do
  name=${spec%%|*}
  if [[ $target_filter == all || $target_filter == "$name" ]]; then
    selected=$((selected + 1))
  fi
done
if ((selected == 0)); then
  printf 'No fuzz targets selected for %q\n' "$target_filter" >&2
  exit 2
fi

# Keep the build cache (CI's /go-build-cache volume, or the local default) and
# persist only the generated corpus. go test points the fuzzer at
# $GOCACHE/fuzz/<import path>, which every job on a runner host shares, so pass
# -test.fuzzcachedir after -args to use $corpus_dir/<package path> instead.
# Each run then reads and writes only its own corpus, and no other branch's
# corpus is replayed. Runs on one corpus directory take turns; in CI every job
# has its own, so this never waits there.
exec 9>"$corpus_dir/.lock"
rc=0
flock -n 9 || rc=$?
if ((rc == 1)); then
  echo "Waiting for another run using $corpus_dir" >&2
  rc=0
  flock -w 1200 9 || rc=$?
fi
if ((rc != 0)); then
  echo "Could not lock $corpus_dir/.lock (flock exit $rc)" >&2
  exit 1
fi
# The fuzz commands close fd 9 so a leftover fuzz worker cannot keep the lock.
# -test.fuzzcachedir is meant for cmd/go only, and works because go test puts
# its own value first and the test binary keeps the last one. Each target's
# exact command is checked with -n first, so a toolchain change that breaks this
# fails the run instead of fuzzing against the shared cache.
fuzz_test() {
  GOTOOLCHAIN=go1.26.11 GOMAXPROCS=4 go test "$@" -p 2 -parallel 4 -run '^$' -fuzz "^${name}$" -fuzztime "$fuzztime" "$package" -args -test.fuzzcachedir="$fuzz_cache" 9>&-
}
printf 'Fuzzing %d target(s), each for %s; corpus cache: %s\n' "$selected" "$budget" "$corpus_dir"
for spec in "${targets[@]}"; do
  name=${spec%%|*}
  package=${spec#*|}
  if [[ $target_filter != all && $target_filter != "$name" ]]; then
    continue
  fi
  # Go replays each cached corpus entry once before mutating and counts those
  # runs toward an Nx budget, so add them to keep N mutated executions.
  fuzztime=$budget
  fuzz_cache=$corpus_dir/${package#./}
  cached=$fuzz_cache/$name
  if [[ $budget =~ ^([0-9]+)x$ && -d $cached ]]; then
    fuzztime="$((BASH_REMATCH[1] + $(find "$cached" -type f | wc -l)))x"
  fi
  plan=$(fuzz_test -n 2>&1) || {
    printf '%s\n' "$plan" | tail -n 20 >&2
    exit 1
  }
  if [[ $plan$'\n' != *" -test.fuzzcachedir=$fuzz_cache"$'\n'* ]]; then
    echo "go test would not use $fuzz_cache as the fuzz cache for $name" >&2
    exit 1
  fi
  printf '\nRunning %s in %s for %s\n' "$name" "$package" "$fuzztime"
  fuzz_test
done
echo 'Completed targets:'
for spec in "${targets[@]}"; do
  name=${spec%%|*}
  if [[ $target_filter == all || $target_filter == "$name" ]]; then
    printf '  %s\n' "$name"
  fi
done
