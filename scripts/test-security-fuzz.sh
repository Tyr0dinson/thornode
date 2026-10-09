#!/usr/bin/env bash
set -euo pipefail

# FUZZ_TARGET selects one named target or "all". Every target gets its own go invocation.
target_filter=${FUZZ_TARGET:-all}
budget=${FUZZ_TIME:-5s}
corpus_dir=${FUZZ_CORPUS_DIR:-/tmp/thornode-security-fuzz-cache}
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

exec 9>/tmp/thornode-test-quality-build.lock
flock -w 1200 9 || {
  echo 'Timed out waiting for shared Go build lock' >&2
  exit 1
}
printf 'Fuzzing %d target(s), each for %s; corpus cache: %s\n' "$selected" "$budget" "$corpus_dir"
for spec in "${targets[@]}"; do
  name=${spec%%|*}
  package=${spec#*|}
  if [[ $target_filter != all && $target_filter != "$name" ]]; then
    continue
  fi
  printf '\nRunning %s in %s\n' "$name" "$package"
  GOTOOLCHAIN=go1.26.9 GOCACHE="$corpus_dir" GOMAXPROCS=4 go test -p 2 -parallel 4 -run '^$' -fuzz "^${name}$" -fuzztime "$budget" "$package"
done
echo 'Completed targets:'
for spec in "${targets[@]}"; do
  name=${spec%%|*}
  if [[ $target_filter == all || $target_filter == "$name" ]]; then
    printf '  %s\n' "$name"
  fi
done
