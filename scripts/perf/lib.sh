#!/usr/bin/env bash
# Shared settings for the perf scripts. Source, don't execute.

PERF_ROOT="$(git rev-parse --show-toplevel)"
PERF_RESULTS="${PERF_RESULTS:-$PERF_ROOT/test/performance/results}"
# worktrees and regtest outputs live outside the repo so go ./... and linters never see them
PERF_CACHE="${PERF_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/thornode-perf}"

# Benchmarks run pinned to a fixed core set with a fixed GOMAXPROCS so runs are
# comparable. Pick cores away from 0 (interrupts) and leave the rest of the box
# for builds and other work.
PERF_CPUS="${PERF_CPUS:-8-15}"
PERF_GOMAXPROCS="${PERF_GOMAXPROCS:-8}"
PERF_BENCH="${PERF_BENCH:-^BenchmarkPerf}"
# Chain performance is measured on the release build (TAG=mainnet). Use
# PERF_TAGS=mocknet for ideas that target test speed: mocknet-only code paths
# (e.g. env mimir overrides) run in unit/regtest/simulation but never on mainnet.
PERF_TAGS="${PERF_TAGS:-mainnet}"
PERF_PKG="${PERF_PKG:-./x/thorchain}"
PERF_BENCHTIME="${PERF_BENCHTIME:-1s}"
PERF_MAX_LOAD="${PERF_MAX_LOAD:-12}"
PERF_SHAPE="${PERF_SHAPE:-$PERF_ROOT/test/performance/testdata/mainnet-shape.json}"
export PERF_SHAPE

BENCHSTAT="${BENCHSTAT:-$(command -v benchstat || echo "go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260929162123-406019bb8b68")}"

perf_log() { echo "[perf] $*" >&2; }

# perf_env_check aborts when the box is too busy for meaningful numbers.
perf_env_check() {
  local load
  load=$(cut -d' ' -f1 /proc/loadavg)
  if awk -v l="$load" -v m="$PERF_MAX_LOAD" 'BEGIN{exit !(l>m)}'; then
    perf_log "1-min load $load > PERF_MAX_LOAD=$PERF_MAX_LOAD; refusing to benchmark (override with PERF_MAX_LOAD)"
    exit 3
  fi
  local gov
  gov=$(cat /sys/devices/system/cpu/cpu"${PERF_CPUS%%-*}"/cpufreq/scaling_governor 2>/dev/null || echo unknown)
  perf_log "load=$load governor=$gov cpus=$PERF_CPUS gomaxprocs=$PERF_GOMAXPROCS"
}

# perf_build_test_binary <src-tree> <out-binary>
perf_build_test_binary() {
  (cd "$1" && go test -c -tags="$PERF_TAGS" -o "$2" "$PERF_PKG")
}

# perf_run_bench <src-tree> <binary> <bench-regex> <count> [extra test flags...]
# Runs from the package dir of src-tree (fixtures use relative paths) and keeps
# only benchmark result lines.
perf_run_bench() {
  local tree=$1 bin=$2 bench=$3 count=$4
  shift 4
  (cd "$tree/$PERF_PKG" &&
    GOMAXPROCS=$PERF_GOMAXPROCS taskset -c "$PERF_CPUS" "$bin" \
      -test.run='^$' -test.bench="$bench" -test.benchmem \
      -test.count="$count" -test.benchtime="$PERF_BENCHTIME" "$@" 2>&1) |
    grep -E '^(Benchmark|goos:|goarch:|pkg:|cpu:|--- FAIL|FAIL|panic)' || true
}

# perf_tree_hash <src-tree>: content hash of the tree including uncommitted and
# untracked files, used to cache regtest results per exact source state.
perf_tree_hash() {
  local idx
  idx=$(mktemp)
  cp "$(git -C "$1" rev-parse --git-path index)" "$idx" 2>/dev/null || true
  GIT_INDEX_FILE=$idx git -C "$1" add -A >/dev/null 2>&1
  GIT_INDEX_FILE=$idx git -C "$1" write-tree
  rm -f "$idx"
}

# perf_regtest <src-tree> <out-dir>
# Builds the regtest binaries from src-tree into out-dir/bin and runs the suites
# with EXPORT + UNFILTER_EXPORT into a fresh mnt copy, so blocks keep app_hash,
# last_results_hash and gas_used. Skips work if out-dir/done exists.
perf_regtest() {
  local src=$1 out=$2
  if [[ -f $out/done ]]; then
    perf_log "regtest cached: $out"
    return 0
  fi
  rm -rf "${out:?}"
  mkdir -p "$out/bin" "$out/home" "$out/pprof"
  cp -r "$src/test/regression/mnt" "$out/mnt"
  rm -rf "${out:?}/mnt/exports" "${out:?}/mnt/blocks" "${out:?}/mnt/coverage"
  mkdir -p "$out/mnt/exports" "$out/mnt/blocks" "$out/mnt/coverage"
  perf_log "building regtest binaries from $src"
  (cd "$src" && GOBIN="$out/bin" REGTEST_BIN_DIR="$out/bin" make _build-test-regression) >"$out/build.log" 2>&1
  perf_log "running regtest (RUN=${RUN:-all}, PARALLELISM=${REGTEST_PARALLELISM:-12}) -> $out/regtest.log"
  local start end rc=0
  start=$(date +%s)
  local vars=(
    PATH="$out/bin:$PATH" RUN="${RUN-}" PARALLELISM="${REGTEST_PARALLELISM:-12}" TIME_FACTOR=2 CI=1
    EXPORT=1 UNFILTER_EXPORT=1
    REGTEST_HOME_BASE="$out/home" REGTEST_CREATE_BLOCK_PORT_BASE="${REGTEST_CREATE_BLOCK_PORT_BASE:-21080}"
    REGTEST_BIN_DIR="$out/bin" REGTEST_MNT_DIR="$out/mnt" GOCOVERDIR="$out/mnt/coverage"
    REGTEST_BLOCK_TIMES="$out/block-times.csv"
  )
  if [[ -n ${PERF_REGTEST_PROFILE-} ]]; then
    vars+=(REGTEST_CPUPROFILE_DIR="$out/pprof")
  fi
  (cd "$src/test/regression" && env "${vars[@]}" "$out/bin/regtest") >"$out/regtest.log" 2>&1 || rc=$?
  end=$(date +%s)
  echo "$((end - start))" >"$out/seconds"
  echo "$rc" >"$out/rc"
  rm -rf "${out:?}/home"
  touch "$out/done"
  perf_log "regtest rc=$rc in $((end - start))s"
}
