#!/usr/bin/env bash
# Measure real mainnet block execution: replay the same window of mainnet blocks
# through two thornode binaries, from the same mainnet state, on disk.
#
#   scripts/perf/mainnet-replay.sh statesync <binary>
#   scripts/perf/mainnet-replay.sh restore-local <binary> [height]
#   scripts/perf/mainnet-replay.sh sync <blocks> <binary>
#   scripts/perf/mainnet-replay.sh run <label> <rounds> <binary-a> <binary-b>
#
# statesync: initialise a node home and restore mainnet app state at some height H
#          by CometBFT state sync (Liquify state-sync peers), then bootstrap the
#          CometBFT state at H from the light client; the first start checks the
#          app hash against it. Downloaded tarball snapshots are not used: one
#          tested (Liquify pruned, height 28084001) did not match mainnet's app
#          hash. Keeps a pristine app db copy.
#          Timing (height 28124000, 76G app db): ~1.3h to fetch 1257 chunks, then
#          ~6h of IAVL import on disk. Chunks are kept in the local snapshot
#          store, so if the import fails or is killed use restore-local.
# restore-local: rebuild the app db from the local snapshot store and bootstrap
#          CometBFT as above. REPLAY_RESTORE_TMPFS=1 imports on /dev/shm (~3x
#          faster; needs RAM for the whole db).
# sync:    run <sync-binary> (must be consensus-identical to mainnet, i.e. the
#          release mainnet runs) with p2p until H+<blocks>, so the block store
#          holds real blocks H+1..H+<blocks>. Do this promptly: public RPCs and
#          peers keep ~10k blocks (~17h), and the light client (bootstrap) needs
#          the header at H too. A keyed archive RPC (Liquify with api=) still
#          serves it later; trust_height = H works then.
# run:     for each round and each binary: restore the pristine app db (height H)
#          next to the synced block store and start the node offline. CometBFT's
#          handshake replays H+1..H+N through the app, checking every block's
#          resulting app hash against the next real header, so a replay that
#          finishes also proves the binary reproduces mainnet state. Per-block
#          times come from the "Applying block" log lines.
#
# Binaries must be built with TAG=mainnet from the release mainnet runs (plus
# any changes under test). Changes that alter tx gas_used cannot be replayed:
# the next real header's LastResultsHash would not match.
#
# Env: REPLAY_DIR (default ~/.cache/thornode-perf/mainnet-replay)
#      STATESYNC_TRUST_HEIGHT (a height just below the snapshot peers offer),
#      STATESYNC_RPC (light client RPC; may carry an api key, stays in local config)
set -euo pipefail
source "$(dirname "$0")/lib.sh"

REPLAY_DIR=${REPLAY_DIR:-$PERF_CACHE/mainnet-replay}
NODE_HOME=$REPLAY_DIR/home   # node uses $NODE_HOME/.thornode
PRISTINE=$REPLAY_DIR/pristine-application.db
PORT_BASE=${REPLAY_PORT_BASE:-36000}

# THOR_LOG_FILTER_MODULES: the default thornode log filter raises "consensus" to
# WARN, which hides the handshake/replay lines ("Applying block", "Completed ABCI
# Handshake") that this script times.
node_env() {
  env HOME="$NODE_HOME" CHAIN_ID=thorchain-1 NET=mainnet \
    CHAIN_HOME_FOLDER="$NODE_HOME/.thornode" SIGNER_NAME=thorchain SIGNER_PASSWD=password \
    THOR_COSMOS_API_ADDRESS="tcp://127.0.0.1:$((PORT_BASE + 317))" \
    THOR_COSMOS_GRPC_ADDRESS="127.0.0.1:$((PORT_BASE + 90))" \
    THOR_COSMOS_EBIFROST_ADDRESS="127.0.0.1:$((PORT_BASE + 51))" \
    THOR_TENDERMINT_RPC_LISTEN_ADDRESS="tcp://127.0.0.1:$((PORT_BASE + 657))" \
    THOR_TENDERMINT_P2P_LISTEN_ADDRESS="tcp://0.0.0.0:$((PORT_BASE + 656))" \
    THOR_TENDERMINT_INSTRUMENTATION_PROMETHEUS=false THOR_LOG_FILTER_MODULES=p2p,pex \
    "$@"
}

CONFIG=$NODE_HOME/.thornode/config/config.toml

# offline config: no seeds, peers, peer exchange or state sync, so a started node
# can only replay what is on disk
use_config() {
  cp "$REPLAY_DIR/config.toml.$1" "$CONFIG"
}

# app_height <binary>: start offline until the ABCI handshake reports the app height
app_height() {
  use_config offline
  local log=$REPLAY_DIR/height.log
  node_env "$1" start </dev/null >"$log" 2>&1 &
  local pid=$!
  while kill -0 "$pid" 2>/dev/null && ! grep -aq "Completed ABCI Handshake" "$log"; do sleep 0.5; done
  # $! is the subshell running node_env; stop the node under it too
  pkill -P "$pid" 2>/dev/null || true
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  while pgrep -P "$pid" >/dev/null 2>&1; do sleep 0.5; done
  sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -a "Completed ABCI Handshake" | grep -oE "appHeight=[0-9]+" | cut -d= -f2
}

BOOTSTRAPPED=$REPLAY_DIR/bootstrapped-cometbft

# bootstrap_comet <binary> <height>: write light-client verified CometBFT state at
# <height> next to a restored app db (state sync leaves none once it is killed).
# The block store stays empty; CometBFT marks the height for blocksync and clears
# the mark on the next start, so a copy is kept and put back before each sync.
bootstrap_comet() {
  local data=$NODE_HOME/.thornode/data
  rm -rf "$data/state.db" "$data/blockstore.db"
  use_config online
  # load-balanced RPCs may route to a backend that pruned <height>: retry
  local i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    rm -rf "$data/state.db" "$data/blockstore.db"
    if node_env "$1" comet bootstrap-state --height "$2" </dev/null >"$REPLAY_DIR/bootstrap.log" 2>&1; then
      break
    fi
    perf_log "bootstrap-state attempt $i failed: $(grep -a "failure when running app" "$REPLAY_DIR/bootstrap.log" | tail -1 | cut -c1-160)"
    [[ $i == 10 ]] && return 1
    sleep 5
  done
  rm -rf "$BOOTSTRAPPED"
  mkdir -p "$BOOTSTRAPPED"
  cp -a "$data/state.db" "$data/blockstore.db" "$BOOTSTRAPPED/"
}

restore_bootstrapped() {
  local data=$NODE_HOME/.thornode/data
  rm -rf "$data/state.db" "$data/blockstore.db"
  cp -a "$BOOTSTRAPPED"/* "$data/"
}

# save_pristine <binary>: check the handshake (CometBFT compares the app hash with
# the bootstrapped state), record the height and keep a copy of the app db
save_pristine() {
  local h
  h=$(app_height "$1")
  restore_bootstrapped
  perf_log "restored and verified app state at height $h"
  echo "$h" >"$REPLAY_DIR/start-height"
  rm -rf "$PRISTINE"
  cp -a "$NODE_HOME/.thornode/data/application.db" "$PRISTINE"
}

cmd=${1:?statesync|restore-local|sync|run}
shift
case $cmd in
statesync)
  bin=${1:?binary}
  rm -rf "$NODE_HOME"
  mkdir -p "$NODE_HOME/.thornode"
  node_env "$bin" init local --chain-id thorchain-1 >/dev/null 2>&1
  rm -f "$NODE_HOME/.thornode/config/genesis.json"
  # render-config with auto statesync writes trust height/hash and the
  # state-sync peers into config.toml
  node_env THOR_AUTO_STATE_SYNC_ENABLED=true "$bin" render-config >"$REPLAY_DIR/render.log" 2>&1
  # Verify against a trust point just below the snapshot: verifying backwards
  # from the auto-chosen height fetches every header in between and times out.
  if [[ -n ${STATESYNC_TRUST_HEIGHT:-} ]]; then
    rpc=${STATESYNC_RPC:-https://gateway.liquify.com/chain/thorchain_rpc}
    base=${rpc%%\?*}
    query=
    [[ $rpc == *\?* ]] && query="&${rpc#*\?}"
    hash=
    for _ in 1 2 3 4 5; do # load-balanced RPCs: some backends pruned the height
      hash=$(curl -sf "$base/block?height=$STATESYNC_TRUST_HEIGHT$query" |
        python3 -c "import json,sys; print(json.load(sys.stdin)['result']['block_id']['hash'])" 2>/dev/null) && break
      sleep 2
    done
    [[ -n $hash ]] || { perf_log "no block $STATESYNC_TRUST_HEIGHT at $base"; exit 1; }
    sed -i -E "/^\[statesync\]/,/^\[/ { s|^trust_height = .*|trust_height = $STATESYNC_TRUST_HEIGHT|; s|^trust_hash = .*|trust_hash = \"$hash\"|; s|^rpc_servers = .*|rpc_servers = \"$rpc,$rpc\"| }" "$CONFIG"
  fi
  # the state-sync peers are few and flaky: discover longer, wait longer per chunk
  sed -i -E '/^\[statesync\]/,/^\[/ { s|^discovery_time = .*|discovery_time = "60s"|; s|^chunk_request_timeout = .*|chunk_request_timeout = "60s"| }' "$CONFIG"
  cp "$CONFIG" "$REPLAY_DIR/config.toml.online"
  sed -E 's/^(seeds|persistent_peers) = .*/\1 = ""/; s/^pex = .*/pex = false/; /^\[statesync\]/,/^\[/ s/^enable = .*/enable = false/' \
    "$CONFIG" >"$REPLAY_DIR/config.toml.offline"
  echo '{"height":"0","round":0,"step":0}' >"$NODE_HOME/.thornode/data/priv_validator_state.json" 2>/dev/null ||
    { mkdir -p "$NODE_HOME/.thornode/data" && echo '{"height":"0","round":0,"step":0}' >"$NODE_HOME/.thornode/data/priv_validator_state.json"; }
  perf_log "state syncing (log: $REPLAY_DIR/statesync.log)"
  node_env "$bin" start </dev/null >"$REPLAY_DIR/statesync.log" 2>&1 &
  pid=$!
  while kill -0 "$pid" 2>/dev/null && ! grep -aq "Snapshot restored" "$REPLAY_DIR/statesync.log"; do sleep 2; done
  pkill -P "$pid" 2>/dev/null || true
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  if ! grep -aq "Snapshot restored" "$REPLAY_DIR/statesync.log"; then
    perf_log "state sync failed; see $REPLAY_DIR/statesync.log"
    exit 1
  fi
  h=$(sed 's/\x1b\[[0-9;]*m//g' "$REPLAY_DIR/statesync.log" | grep -a "Snapshot restored" | grep -oE "height=[0-9]+" | tail -1 | cut -d= -f2)
  bootstrap_comet "$bin" "$h"
  save_pristine "$bin"
  ;;

restore-local)
  # Resume after a state sync that downloaded every chunk but failed or was
  # killed during the (multi-hour) IAVL import: the chunks are kept in the
  # local snapshot store. Rebuild the app db from them, then bootstrap the
  # CometBFT state at that height from the light client (online config).
  # REPLAY_RESTORE_TMPFS=1 runs the import on /dev/shm (needs ~2x the db in RAM).
  bin=${1:?binary}
  h=${2:-$(node_env "$bin" snapshots list 2>/dev/null | awk '/height:/ {print $2}' | sort -n | tail -1)}
  data=$NODE_HOME/.thornode/data
  rm -rf "$data/application.db"
  if [[ -n ${REPLAY_RESTORE_TMPFS:-} ]]; then
    scratch=/dev/shm/thornode-replay-application.db
    rm -rf "$scratch"
    mkdir -p "$scratch"
    ln -s "$scratch" "$data/application.db"
  fi
  perf_log "restoring app state at $h from the local snapshot store"
  node_env "$bin" snapshots restore "$h" 3 </dev/null
  if [[ -L $data/application.db ]]; then
    rm "$data/application.db"
    cp -a "$scratch" "$data/application.db"
    rm -rf "$scratch"
  fi
  bootstrap_comet "$bin" "$h"
  save_pristine "$bin"
  ;;

sync)
  blocks=${1:?blocks}
  bin=${2:?sync binary}
  start_h=$(cat "$REPLAY_DIR/start-height")
  halt=$((start_h + blocks))
  rm -rf "$NODE_HOME/.thornode/data/application.db"
  cp -a "$PRISTINE" "$NODE_HOME/.thornode/data/application.db"
  restore_bootstrapped
  use_config online
  perf_log "syncing $start_h -> $halt with $bin (log: $REPLAY_DIR/sync.log)"
  node_env THOR_COSMOS_HALT_HEIGHT=$halt "$bin" start --halt-height "$halt" </dev/null >"$REPLAY_DIR/sync.log" 2>&1 || true
  grep -E "halt|Halting" "$REPLAY_DIR/sync.log" | tail -3 >&2 || true
  end_h=$(app_height "$bin")
  perf_log "synced to app height $end_h"
  echo "$end_h" >"$REPLAY_DIR/end-height"
  # keep the synced CometBFT state and block store; app db is restored per run
  rm -rf "$REPLAY_DIR/synced-cometbft"
  mkdir -p "$REPLAY_DIR/synced-cometbft"
  for d in blockstore.db state.db evidence.db tx_index.db priv_validator_state.json; do
    if [[ -e $NODE_HOME/.thornode/data/$d ]]; then
      cp -a "$NODE_HOME/.thornode/data/$d" "$REPLAY_DIR/synced-cometbft/"
    fi
  done
  ;;

run)
  label=${1:?label}
  rounds=${2:?rounds}
  bin_a=${3:?binary a}
  bin_b=${4:?binary b}
  out="$PERF_RESULTS/$label"
  mkdir -p "$out"
  start_h=$(cat "$REPLAY_DIR/start-height")
  end_h=$(cat "$REPLAY_DIR/end-height")
  use_config offline
  perf_log "replaying $((end_h - start_h)) mainnet blocks ($((start_h + 1))..$end_h), $rounds rounds"
  : >"$out/runs.tsv"
  for ((r = 1; r <= rounds; r++)); do
    for side in a b; do
      bin=$bin_a
      [[ $side == b ]] && bin=$bin_b
      data=$NODE_HOME/.thornode/data
      rm -rf "$data/application.db"
      cp -a "$PRISTINE" "$data/application.db"
      for d in "$REPLAY_DIR"/synced-cometbft/*; do
        rm -rf "${data:?}/$(basename "$d")"
        cp -a "$d" "$data/"
      done
      sync
      log="$out/replay-$side-$r.log"
      perf_log "round $r side $side: $(basename "$bin")"
      # every log line gets a wall-clock prefix (node log times have minute resolution)
      setsid bash -c 'exec "$@" 2>&1 | python3 -u -c "import sys,time
for l in sys.stdin: sys.stdout.write(f\"{time.time():.3f} {l}\")"' _ \
        env HOME="$NODE_HOME" CHAIN_ID=thorchain-1 NET=mainnet \
        CHAIN_HOME_FOLDER="$NODE_HOME/.thornode" SIGNER_NAME=thorchain SIGNER_PASSWD=password \
        THOR_COSMOS_API_ADDRESS="tcp://127.0.0.1:$((PORT_BASE + 317))" \
        THOR_COSMOS_GRPC_ADDRESS="127.0.0.1:$((PORT_BASE + 90))" \
        THOR_COSMOS_EBIFROST_ADDRESS="127.0.0.1:$((PORT_BASE + 51))" \
        THOR_TENDERMINT_RPC_LISTEN_ADDRESS="tcp://127.0.0.1:$((PORT_BASE + 657))" \
        THOR_TENDERMINT_P2P_LISTEN_ADDRESS="tcp://0.0.0.0:$((PORT_BASE + 656))" \
        THOR_TENDERMINT_INSTRUMENTATION_PROMETHEUS=false THOR_LOG_FILTER_MODULES=p2p,pex \
        taskset -c "${REPLAY_CPUS:-8-23}" "$bin" start </dev/null >"$log" &
      pid=$!
      # wait for the handshake to finish (or the node to die)
      while kill -0 "$pid" 2>/dev/null; do
        if grep -q "Completed ABCI Handshake" "$log"; then
          break
        fi
        sleep 0.5
      done
      kill -- -"$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      # the node may still be flushing its dbs; the next run replaces them
      while pgrep -s "$pid" >/dev/null 2>&1; do sleep 0.5; done
      if ! grep -q "Completed ABCI Handshake" "$log"; then
        perf_log "replay did not complete; see $log"
        grep -iE "panic|app hash|error" "$log" | tail -5 >&2 || true
        exit 1
      fi
      applied=$(grep -c "Applying block" "$log" || true)
      secs=$(awk '/Applying block/ && !f {f=$1} /Completed ABCI Handshake/ {e=$1} END {printf "%.3f", e-f}' "$log")
      printf '%s\t%s\t%s\t%s\n' "$side" "$r" "$applied" "$secs" | tee -a "$out/runs.tsv"
    done
  done
  python3 - "$out" "$bin_a" "$bin_b" <<'PY'
import glob, os, statistics, sys
out, a, b = sys.argv[1:4]
rows = [l.split("\t") for l in open(os.path.join(out, "runs.tsv")) if l.strip()]
tot = {"a": [], "b": []}
for side, r, n, secs in rows:
    tot[side].append((int(n), float(secs)))

def per_block(side):
    # durations between consecutive "Applying block" lines across all rounds
    ds = []
    for path in sorted(glob.glob(os.path.join(out, f"replay-{side}-*.log"))):
        t = [float(l.split(" ", 1)[0]) for l in open(path, errors="replace") if "Applying block" in l]
        ds += [y - x for x, y in zip(t, t[1:])]
    ds.sort()
    q = lambda p: ds[min(len(ds) - 1, int(p * len(ds)))] * 1000
    return f"p50 {q(.5):.1f}ms p90 {q(.9):.1f}ms p99 {q(.99):.1f}ms max {ds[-1]*1000:.0f}ms"

print(f"\na: {a}\nb: {b}")
for side in "ab":
    n = max(n for n, _ in tot[side])
    secs = [s for _, s in tot[side]]
    med = statistics.median(secs)
    print(f"{side}: {n} blocks, replay s {', '.join(f'{s:.1f}' for s in secs)}; median {med:.1f}s = {med/n*1000:.1f} ms/block; {per_block(side)}")
ma = statistics.median(s for _, s in tot["a"])
mb = statistics.median(s for _, s in tot["b"])
print(f"b vs a: {100*(mb-ma)/ma:+.1f}% replay wall time")
PY
  ;;
*)
  echo "unknown command $cmd" >&2
  exit 2
  ;;
esac
