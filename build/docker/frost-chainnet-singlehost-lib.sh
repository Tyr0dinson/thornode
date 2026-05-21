#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="${FROST_CHAINNET_COMPOSE_FILE:-docker-compose.frost-chainnet-timing.yml}"
COMPOSE_FILE_PATH="$SCRIPT_DIR/$COMPOSE_FILE"
ABORT_COMPOSE_FILE_PATH="$SCRIPT_DIR/docker-compose.frost-abort.yml"
COMPOSE_PROFILE_ARGS=()
if [ -n "${FROST_CHAINNET_COMPOSE_PROFILES:-local-bitcoin}" ]; then
  IFS=',' read -r -a _compose_profiles <<<"${FROST_CHAINNET_COMPOSE_PROFILES:-local-bitcoin}"
  for _compose_profile in "${_compose_profiles[@]}"; do
    COMPOSE_PROFILE_ARGS+=(--profile "$_compose_profile")
  done
fi
COMPOSE_ENV_FILE="$REPO_ROOT/.env"
if [ ! -f "$COMPOSE_ENV_FILE" ] && [ -f "$SCRIPT_DIR/.env" ]; then
  COMPOSE_ENV_FILE="$SCRIPT_DIR/.env"
fi
CHAIN_ID="thorchain"
KEYRING="file"
PASSWORD="password"
DOG_RPC="tcp://localhost:27147"
CAT_RPC="tcp://thornode-cat:27147"
FOX_RPC="tcp://thornode-fox:27147"
GOAT_RPC="tcp://thornode-goat:27147"
WHALE_RPC="tcp://thornode-whale:27147"
WOLF_RPC="tcp://thornode-wolf:27147"
EAGLE_RPC="tcp://thornode-eagle:27147"
BTC_RPC_USER="${BTC_RPC_USER:-thorchain}"
BTC_RPC_PASSWORD="${BTC_RPC_PASSWORD:-password}"
BTC_WALLET_NAME="${BTC_WALLET_NAME:-thornode-frost}"
BTC_MINING_ADDRESS="${BTC_MINING_ADDRESS:-bcrt1qf4l5dlqhaujgkxxqmug4stfvmvt58vx2h44c39}"
BTC_LP_AMOUNT="${BTC_LP_AMOUNT:-10000}"
POLL_SECONDS="${POLL_SECONDS:-30}"
FAST_POLL_SECONDS="${FAST_POLL_SECONDS:-5}"
INITIAL_CHURN_INTERVAL="${INITIAL_CHURN_INTERVAL:-10}"
INITIAL_CHURN_RETRY_INTERVAL="${INITIAL_CHURN_RETRY_INTERVAL:-10}"
FROST_CHURN_INTERVAL="${FROST_CHURN_INTERVAL:-10}"
FROST_CHURN_RETRY_INTERVAL="${FROST_CHURN_RETRY_INTERVAL:-10}"
CHURN_MIGRATE_ROUNDS="${CHURN_MIGRATE_ROUNDS:-2}"
# 20 chainnet blocks has been about two minutes in the FROST/BTC cluster.
FUND_MIGRATION_INTERVAL="${FUND_MIGRATION_INTERVAL:-20}"
INITIAL_KEYGEN_CHECKS="${INITIAL_KEYGEN_CHECKS:-20}"
FROST_KEYGEN_CHECKS="${FROST_KEYGEN_CHECKS:-50}"
FROST_ABORT_CHECKS="${FROST_ABORT_CHECKS:-30}"
SECOND_FROST_KEYGEN_CHECKS="${SECOND_FROST_KEYGEN_CHECKS:-70}"
RETIRING_EMPTY_CHECKS="${RETIRING_EMPTY_CHECKS:-80}"
RESULTS_DIR="${RESULTS_DIR:-$SCRIPT_DIR/frost-chainnet-results}"
RUN_ID="${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
RESULTS_CSV="${RESULTS_CSV:-$RESULTS_DIR/tss-timings.csv}"
RESULTS_JSONL="${RESULTS_JSONL:-$RESULTS_DIR/tss-timings.jsonl}"

DOG_NODE_ADDR="cthor1zf3gsk7edzwl9syyefvfhle37cjtql35layy8z"
BTC_LP_THOR_ADDRESS="${BTC_LP_THOR_ADDRESS:-$DOG_NODE_ADDR}"
CAT_SEED="cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat crawl"
FOX_SEED="fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox filter"
GOAT_SEED="goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat answer"
WHALE_SEED="whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale away"
WOLF_SEED="wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf analyst"
EAGLE_SEED="eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle bitter"
NODE_META_FILE="${FROST_CHAINNET_NODE_META-}"
if [ -z "$NODE_META_FILE" ]; then
  for candidate in \
    "$SCRIPT_DIR/frost-chainnet-generated/nodes-7-chainnet.tsv" \
    "$SCRIPT_DIR/frost-chainnet-generated/nodes-7.tsv" \
    "$SCRIPT_DIR/frost-chainnet-generated/nodes-7-mocknet.tsv"; do
    if [ -f "$candidate" ]; then
      NODE_META_FILE="$candidate"
      break
    fi
  done
fi

tx() {
  local container="$1"
  shift
  docker exec "$container" bash -c "printf '${PASSWORD}\n' | thornode tx thorchain $* --from thorchain --keyring-backend $KEYRING --chain-id $CHAIN_ID --yes" 2>&1
}

compose() {
  if [ -f "$COMPOSE_ENV_FILE" ]; then
    docker compose "${COMPOSE_PROFILE_ARGS[@]}" --env-file "$COMPOSE_ENV_FILE" -f "$COMPOSE_FILE_PATH" "$@"
  else
    docker compose "${COMPOSE_PROFILE_ARGS[@]}" -f "$COMPOSE_FILE_PATH" "$@"
  fi
}

compose_abort() {
  if [ -f "$COMPOSE_ENV_FILE" ]; then
    docker compose "${COMPOSE_PROFILE_ARGS[@]}" --env-file "$COMPOSE_ENV_FILE" -f "$COMPOSE_FILE_PATH" -f "$ABORT_COMPOSE_FILE_PATH" "$@"
  else
    docker compose "${COMPOSE_PROFILE_ARGS[@]}" -f "$COMPOSE_FILE_PATH" -f "$ABORT_COMPOSE_FILE_PATH" "$@"
  fi
}

joiner_names() {
  if [ -n "$NODE_META_FILE" ] && [ -f "$NODE_META_FILE" ]; then
    awk -F'\t' 'NR>1 && $2!="dog" {print $2}' "$NODE_META_FILE"
    return
  fi
  printf '%s\n' cat fox goat whale wolf eagle
}

rpc_for_name() {
  if [ "$1" != "dog" ] && [ -n "$NODE_META_FILE" ] && [ -f "$NODE_META_FILE" ]; then
    echo "tcp://thornode-$1:27147"
    return
  fi
  case "$1" in
  dog) echo "$DOG_RPC" ;;
  cat) echo "$CAT_RPC" ;;
  fox) echo "$FOX_RPC" ;;
  goat) echo "$GOAT_RPC" ;;
  whale) echo "$WHALE_RPC" ;;
  wolf) echo "$WOLF_RPC" ;;
  eagle) echo "$EAGLE_RPC" ;;
  *) return 1 ;;
  esac
}

seed_for_name() {
  if [ -n "$NODE_META_FILE" ] && [ -f "$NODE_META_FILE" ]; then
    awk -F'\t' -v name="$1" 'NR>1 && $2==name {print $3; found=1} END {exit !found}' "$NODE_META_FILE" && return
  fi
  case "$1" in
  cat) echo "$CAT_SEED" ;;
  fox) echo "$FOX_SEED" ;;
  goat) echo "$GOAT_SEED" ;;
  whale) echo "$WHALE_SEED" ;;
  wolf) echo "$WOLF_SEED" ;;
  eagle) echo "$EAGLE_SEED" ;;
  *) return 1 ;;
  esac
}

joiner_label() {
  joiner_names | paste -sd/ -
}

node_names() {
  printf '%s\n' dog
  joiner_names
}

node_address_for_name() {
  local container live_addr
  if [ "$1" = "dog" ]; then
    container="docker-thornode-1"
  else
    container="docker-thornode-$1-1"
  fi
  live_addr=$(docker exec "$container" bash -lc "printf '${PASSWORD}\n' | thornode keys show thorchain --keyring-backend $KEYRING -a" 2>/dev/null | tail -1 || true)
  if [ -n "$live_addr" ]; then
    echo "$live_addr"
    return
  fi
  if [ -n "$NODE_META_FILE" ] && [ -f "$NODE_META_FILE" ]; then
    awk -F'\t' -v name="$1" 'NR>1 && $2==name {print $4; found=1} END {exit !found}' "$NODE_META_FILE" && return
  fi
  case "$1" in
  dog) echo "$DOG_NODE_ADDR" ;;
  cat) echo "cthor1uuds8pd92qnnq0udw0rpg0szpgcslc9p0cdl2t" ;;
  fox) echo "cthor13wrmhnh2qe98rjse30pl7u6jxszjjwl4pak8t6" ;;
  goat) echo "cthor1yvyh2uq8ly0aa44aretj0w0hnqx7rsfzl9rd6z" ;;
  whale) echo "cthor1rsq5yj5gv96f9aag8c42gmxyq63ee3pcsq3h6c" ;;
  wolf) echo "cthor1l8373d0wlhyjukqx60gxs0mck4f3utgxjaxj6l" ;;
  eagle) echo "cthor1lc4rwftmqqzr0tykf623uyqwzcrrltvnsjtxy8" ;;
  *) return 1 ;;
  esac
}

bifrost_container_for_name() {
  if [ "$1" = "dog" ]; then
    echo "docker-bifrost-1"
  else
    echo "docker-bifrost-$1-1"
  fi
}

bifrost_service_for_name() {
  if [ "$1" = "dog" ]; then
    echo "bifrost"
  else
    echo "bifrost-$1"
  fi
}

clear_bifrost_address_books() {
  echo "  clearing persisted bifrost P2P address books"
  for name in $(node_names); do
    container=$(bifrost_container_for_name "$name")
    docker exec "$container" sh -lc 'rm -f /root/.thornode/address_book.seed /var/data/bifrost/address_book.seed' 2>/dev/null || true
  done
}

print_bifrost_p2p_status() {
  echo "  bifrost P2P status:"
  for name in $(node_names); do
    container=$(bifrost_container_for_name "$name")
    peer_id=$(docker exec "$container" sh -lc 'curl -fsS --max-time 2 localhost:6040/p2pid 2>/dev/null || true' 2>/dev/null || true)
    peer_count=$(bifrost_peer_count "$container")
    echo "    $name: peer_id=${peer_id:-unknown} peers=$peer_count"
  done
}

bifrost_peer_count() {
  local container="$1"
  local raw
  raw=$(docker exec "$container" sh -lc 'curl -fsS --max-time 2 localhost:6040/status/p2p 2>/dev/null | jq -r ".peer_count // 0" 2>/dev/null || echo 0' 2>/dev/null || echo 0)
  raw=$(printf "%s\n" "$raw" | tail -n 1 | tr -dc '0-9')
  echo "${raw:-0}"
}

wait_for_bifrost_p2p() {
  local max_checks="${1:-30}"
  local min_peers="${2:-5}"
  local name container peer_count ok service

  echo "  waiting for bifrost P2P peers >= $min_peers"
  for i in $(seq 1 "$max_checks"); do
    ok=1
    for name in $(node_names); do
      container=$(bifrost_container_for_name "$name")
      peer_count=$(bifrost_peer_count "$container")
      printf "    [%s/%s] %s peers=%s\n" "$i" "$max_checks" "$name" "${peer_count:-0}"
      [ "${peer_count:-0}" -ge "$min_peers" ] || ok=0
    done
    [ "$ok" -eq 1 ] && return 0
    if [ $((i % 12)) -eq 0 ]; then
      for name in $(node_names); do
        container=$(bifrost_container_for_name "$name")
        peer_count=$(bifrost_peer_count "$container")
        if [ "${peer_count:-0}" -lt "$min_peers" ]; then
          service=$(bifrost_service_for_name "$name")
          echo "    restarting isolated $service peers=${peer_count:-0}"
          compose restart "$service" >/dev/null || true
        fi
      done
    fi
    sleep "$FAST_POLL_SECONDS"
  done
  return 1
}

restart_bifrosts_ordered() {
  local compose_cmd="${1:-compose}"
  local clear_books="${2:-0}"
  echo "  restarting bifrosts in bootstrap order"
  if [ "$clear_books" = "1" ]; then
    clear_bifrost_address_books
  fi
  "$compose_cmd" restart bifrost
  for i in $(seq 1 30); do
    if docker exec docker-bifrost-1 sh -lc 'curl -fsS --max-time 2 localhost:6040/p2pid >/dev/null' 2>/dev/null; then
      break
    fi
    sleep 2
  done
  for name in $(joiner_names); do
    "$compose_cmd" restart "$(bifrost_service_for_name "$name")"
    sleep 12
  done
  sleep 30
  print_bifrost_p2p_status
  wait_for_bifrost_p2p 60 5
}

active_vault_shape() {
  curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -r '[.[] | select(.status=="ActiveVault") | (.membership | length)] | sort | join(",")' 2>/dev/null || true
}

active_vault_pubkey() {
  local expected="${FROST_EXPECTED_VAULT_SIZE-}"
  if [ -n "$expected" ]; then
    curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -r --argjson expected "$expected" '
      [.[] | select(.status=="ActiveVault") | select((.membership | length)==$expected) | (.pub_key | if type=="object" then .value else . end)] | .[0] // ""
    ' 2>/dev/null
    return
  fi
  curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -r '
    [.[] | select(.status=="ActiveVault") | {size:(.membership | length), pubkey:(.pub_key | if type=="object" then .value else . end)}]
    | sort_by(.size)
    | reverse
    | .[0].pubkey // ""
  ' 2>/dev/null
}

retiring_vault_btc_amount() {
  local pubkey="$1"
  curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -r --arg pubkey "$pubkey" '
    [.[] | select(.status=="RetiringVault") | select((.pub_key | if type=="object" then .value else . end)==$pubkey) | ((.coins // []) | map(select(.asset=="BTC.BTC") | .amount | tonumber) | add // 0)] | .[0] // 0
  ' 2>/dev/null
}

chain_height() {
  curl -s localhost:1317/thorchain/block 2>/dev/null | jq -r '.header.height // 0' 2>/dev/null
}

active_node_count() {
  curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '[.[] | select(.status=="Active")] | length' 2>/dev/null || echo 0
}

standby_node_count() {
  curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '[.[] | select(.status=="Standby" or .status=="Ready")] | length' 2>/dev/null || echo 0
}

ready_node_count() {
  curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '[.[] | select(.status=="Ready" or (.status=="Standby" and .preflight_status.status=="Ready"))] | length' 2>/dev/null || echo 0
}

wait_for_validator_shape() {
  local label="$1"
  local active_target="$2"
  local standby_target="$3"
  local ready_target="${4:-0}"
  local max_checks="${5:-60}"
  local active standby ready

  echo "  waiting for validator shape: $label active=$active_target standby=$standby_target ready>=$ready_target"
  for i in $(seq 1 "$max_checks"); do
    active=$(active_node_count)
    standby=$(standby_node_count)
    ready=$(ready_node_count)
    echo "    [$i/$max_checks] active=$active standby=$standby ready=$ready"
    if [ "$active" = "$active_target" ] && [ "$standby" = "$standby_target" ] && [ "$ready" -ge "$ready_target" ]; then
      return 0
    fi
    sleep "$FAST_POLL_SECONDS"
  done

  dump_chain_state "validator shape timeout: $label"
  return 1
}

init_results() {
  mkdir -p "$RESULTS_DIR"
  if [ ! -f "$RESULTS_CSV" ]; then
    echo "run_id,recorded_at,phase,engine,label,vault_pubkey,msg_id,tss_elapsed_ms,start_ts,start_container,stop_ts,stop_container,wait_elapsed_s,height,txid,active_nodes,standby_nodes,notes" >"$RESULTS_CSV"
  fi
  touch "$RESULTS_JSONL"
  echo "  results csv=$RESULTS_CSV"
  echo "  results jsonl=$RESULTS_JSONL"
}

append_timing_result() {
  local phase="$1"
  local engine="$2"
  local label="$3"
  local vault_pubkey="$4"
  local wait_elapsed_s="${5-}"
  local height="${6-}"
  local txid="${7-}"
  local notes="${8-}"
  local recorded_at active_nodes standby_nodes

  recorded_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  active_nodes=$(active_node_count)
  standby_nodes=$(standby_node_count)

  python3 - "$RESULTS_CSV" "$RESULTS_JSONL" \
    "$RUN_ID" "$recorded_at" "$phase" "$engine" "$label" "$vault_pubkey" \
    "${LAST_TSS_MSG_ID-}" "${LAST_TSS_ELAPSED_MS-}" "${LAST_TSS_START_TS-}" "${LAST_TSS_START_CONTAINER-}" \
    "${LAST_TSS_STOP_TS-}" "${LAST_TSS_STOP_CONTAINER-}" "$wait_elapsed_s" "$height" "$txid" \
    "$active_nodes" "$standby_nodes" "$notes" <<'PY'
import csv
import json
import sys

csv_path, jsonl_path, *values = sys.argv[1:]
fields = [
    "run_id", "recorded_at", "phase", "engine", "label", "vault_pubkey",
    "msg_id", "tss_elapsed_ms", "start_ts", "start_container", "stop_ts",
    "stop_container", "wait_elapsed_s", "height", "txid", "active_nodes",
    "standby_nodes", "notes",
]
row = dict(zip(fields, values))
with open(csv_path, "a", newline="") as f:
    csv.DictWriter(f, fieldnames=fields).writerow(row)
with open(jsonl_path, "a") as f:
    f.write(json.dumps(row, separators=(",", ":")) + "\n")
PY
  echo "  recorded timing: $phase $engine $label elapsed=${LAST_TSS_ELAPSED_MS:-unknown}ms"
}

dump_chain_state() {
  local label="${1:-state}"
  echo ""
  echo "---- $label ----"
  echo "height=$(chain_height)"
  echo "mimir:"
  for key in EnableFrostBTC DisableXMRMonovaultKeygen ChurnInterval ChurnRetryInterval ChurnMigrateRounds FundMigrationInterval HaltChurning NumberOfNewNodesPerChurn; do
    printf "  %s=" "$key"
    curl -s "localhost:1317/thorchain/mimir/key/$key" 2>/dev/null || true
    echo ""
  done
  echo "nodes:"
  curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '.[].status' 2>/dev/null | sort | uniq -c || true
  echo "vaults:"
  curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -r '.[] | [.status, (.status_since // 0), (.membership|length), (if (.pub_key|type)=="object" then .pub_key.value else .pub_key end), (((.coins // []) | map(select(.asset=="BTC.BTC") | .amount) | join(",")) // "")] | @tsv' 2>/dev/null || true
  echo "txout queue:"
  curl -s localhost:1317/thorchain/queue/outbound 2>/dev/null | jq -r '. // empty' 2>/dev/null || true
  echo "---- end $label ----"
  echo ""
}

preflight() {
  echo "  preflight: compose=$COMPOSE_FILE_PATH env=${COMPOSE_ENV_FILE:-none}"
  command -v docker >/dev/null
  command -v curl >/dev/null
  command -v jq >/dev/null
  command -v python3 >/dev/null
  [ -f "$COMPOSE_FILE_PATH" ] || {
    echo "ERROR: missing compose file $COMPOSE_FILE_PATH"
    exit 1
  }
  echo "  preflight OK"
  init_results
}
btc_rpc_base_url() {
  local host="${BTC_HOST-}"
  if [ -z "$host" ]; then
    for env_file in "$COMPOSE_ENV_FILE" "$SCRIPT_DIR/.env"; do
      if [ -f "$env_file" ]; then
        host=$(grep -E '^BTC_HOST=' "$env_file" | tail -1 | cut -d= -f2- | tr -d '"' || true)
      fi
      [ -n "$host" ] && break
    done
  fi
  host="${host:-127.0.0.1:18443}"
  host="${host%/}"
  host="${host%%/wallet/*}"
  if [[ $host == http://* || $host == https://* ]]; then
    echo "$host"
  else
    echo "http://$host"
  fi
}

btc_rpc_wallet_url() {
  local base
  base=$(btc_rpc_base_url)
  echo "$base/wallet/$BTC_WALLET_NAME"
}

btc_rpc_call() {
  local url="$1"
  shift
  local method="$1"
  shift
  local params="${1:-[]}"
  local body response result error
  body="{\"jsonrpc\":\"1.0\",\"id\":\"chainnet\",\"method\":\"$method\",\"params\":$params}"
  response=$(curl -sS --user "$BTC_RPC_USER:$BTC_RPC_PASSWORD" \
    --data-binary "$body" \
    -H 'content-type:text/plain;' "$url") || {
    echo "ERROR: bitcoin RPC transport failed: method=$method" >&2
    return 1
  }
  error=$(printf "%s" "$response" | jq -r '.error // empty')
  if [ -n "$error" ] && [ "$error" != "null" ]; then
    echo "ERROR: bitcoin RPC failed: method=$method error=$error" >&2
    return 1
  fi
  result=$(printf "%s" "$response" | jq -r '.result')
  printf "%s\n" "$result"
}

btc_base_rpc() {
  local method="$1"
  shift
  btc_rpc_call "$(btc_rpc_base_url)" "$method" "${1:-[]}"
}

ensure_btc_wallet_loaded() {
  local wallets
  btc_base_rpc getblockchaininfo >/dev/null
  wallets=$(btc_base_rpc listwallets)
  if ! printf '%s\n' "$wallets" | jq -e --arg wallet "$BTC_WALLET_NAME" 'index($wallet)' >/dev/null; then
    if ! btc_base_rpc loadwallet "[\"$BTC_WALLET_NAME\"]" >/dev/null 2>&1; then
      btc_base_rpc createwallet "[\"$BTC_WALLET_NAME\",false,false,\"\",false,true]" >/dev/null
    fi
  fi
  echo "  BTC wallet ready name=$BTC_WALLET_NAME url=$(btc_rpc_wallet_url)"
}

btc_rpc() {
  local method="$1"
  shift
  ensure_btc_wallet_loaded >/dev/null
  btc_rpc_call "$(btc_rpc_wallet_url)" "$method" "${1:-[]}"
}

ensure_btc_wallet_funded() {
  local balance immature addr
  balance=$(btc_rpc getbalance 2>/dev/null || echo 0)
  immature=$(btc_rpc getwalletinfo 2>/dev/null | jq -r '.immature_balance // 0' 2>/dev/null || echo 0)
  if awk "BEGIN { exit !($balance >= $BTC_LP_AMOUNT) }"; then
    return 0
  fi
  echo "  BTC wallet balance=$balance immature=$immature; mining spendable regtest funds"
  addr=$(btc_rpc getnewaddress)
  btc_rpc generatetoaddress "[120,\"$addr\"]" >/dev/null
  balance=$(btc_rpc getbalance)
  echo "  BTC wallet funded balance=$balance"
}

add_btc_lp_funds() {
  local inbound="$1"
  local memo="ADD:BTC.BTC:${BTC_LP_THOR_ADDRESS}"
  local memo_hex
  memo_hex=$(printf "%s" "$memo" | xxd -p -c 256)

  echo "  BTC LP amount=$BTC_LP_AMOUNT inbound=$inbound"
  echo "  BTC LP memo=$memo"

  local raw funded signed txid txid_upper
  ensure_btc_wallet_funded
  raw=$(btc_rpc createrawtransaction "[[],{\"$inbound\":$BTC_LP_AMOUNT,\"data\":\"$memo_hex\"}]")
  funded=$(btc_rpc fundrawtransaction "[\"$raw\"]" | jq -r '.hex')
  signed=$(btc_rpc signrawtransactionwithwallet "[\"$funded\"]" | jq -r '.hex')
  txid=$(btc_rpc sendrawtransaction "[\"$signed\"]")
  txid_upper=$(printf "%s" "$txid" | tr '[:lower:]' '[:upper:]')
  btc_rpc generatetoaddress "[6,\"$BTC_MINING_ADDRESS\"]" >/dev/null

  echo "  BTC LP tx=$txid"
  for i in $(seq 1 60); do
    if docker logs --since 10m docker-thornode-1 2>&1 | grep -q "$txid_upper"; then
      echo "  BTC LP observed by thornode (${i}x${FAST_POLL_SECONDS}s)"
      return 0
    fi
    sleep "$FAST_POLL_SECONDS"
  done

  echo "  WARN: BTC LP tx not seen in thornode logs after 5m: $txid"
}

btc_inbound_address() {
  curl -s localhost:1317/thorchain/inbound_addresses 2>/dev/null | jq -r '.[] | select(.chain=="BTC") | .address' 2>/dev/null
}

wait_for_checksign_log() {
  local label="$1"
  local since="$2"
  local since_ms since_epoch elapsed
  since_ms=$(ts_ms "$since")
  since_epoch=$((since_ms / 1000))
  for i in $(seq 1 60); do
    if bifrost_logs_since "$since" | grep "secp256k1 check signature verified" >/dev/null; then
      elapsed=$(($(date +%s) - since_epoch))
      echo "  $label check-sign verified after ${elapsed}s"
      return 0
    fi
    sleep "$FAST_POLL_SECONDS"
  done
  echo "  WARN: $label check-sign log not found"
}

ts_ms() {
  python3 - "$1" <<'PY'
import datetime
import re
import sys

value = sys.argv[1]
value = re.sub(r"\.(\d{6})\d+(Z|[+-]\d\d:\d\d)$", r".\1\2", value)
value = value.replace("Z", "+00:00")
dt = datetime.datetime.fromisoformat(value)
print(int(dt.timestamp() * 1000))
PY
}

bifrost_logs_since() {
  local since="$1"
  local name container
  if [ -n "${FROST_TIMING_LOG_FILE-}" ]; then
    cat "$FROST_TIMING_LOG_FILE"
    return 0
  fi
  for name in $(node_names); do
    container=$(bifrost_container_for_name "$name")
    docker logs --timestamps --since "$since" "$container" 2>&1 | sed -E 's/\x1b\[[0-9;]*m//g' | sed -E "s/^([^ ]+) /\1 $container /"
  done
}

extract_msg_id() {
  sed -n 's/.*"msg_id":"\([^"]*\)".*/\1/p; s/.*msg_id=\([^ ]*\).*/\1/p' | head -1
}

print_ceremony_timing() {
  local label="$1"
  local start_pattern="$2"
  local end_pattern="$3"
  local since="$4"
  local filter="${5-}"
  local filter_start="${6:-0}"
  local logs start_line msg_id end_line start_ts end_ts start_ms end_ms
  LAST_TSS_MSG_ID=""
  LAST_TSS_ELAPSED_MS=""
  LAST_TSS_START_TS=""
  LAST_TSS_START_CONTAINER=""
  LAST_TSS_STOP_TS=""
  LAST_TSS_STOP_CONTAINER=""

  logs=$(mktemp)
  bifrost_logs_since "$since" >"$logs"
  if [ "$filter_start" != "1" ] && [ -n "$filter" ]; then
    end_line=$(grep -F "$end_pattern" "$logs" | grep -F "$filter" | sort | head -1 || true)
    if [ -n "$end_line" ]; then
      msg_id=$(echo "$end_line" | extract_msg_id)
      start_line=$(grep -F "$start_pattern" "$logs" | grep -F "msg_id=$msg_id" | sort | head -1 || true)
      if [ -z "$start_line" ]; then
        start_line=$(grep -F "$start_pattern" "$logs" | grep -F "\"msg_id\":\"$msg_id\"" | sort | head -1 || true)
      fi
    fi
  elif [ "$filter_start" = "1" ] && [ -n "$filter" ]; then
    start_line=$(grep -F "$start_pattern" "$logs" | grep -F "$filter" | sort | head -1 || true)
  else
    start_line=$(grep -F "$start_pattern" "$logs" | sort | head -1 || true)
  fi
  if [ -z "$start_line" ]; then
    rm -f "$logs"
    echo "  WARN: $label TSS ceremony start log not found"
    return 0
  fi

  msg_id="${msg_id:-$(echo "$start_line" | extract_msg_id)}"
  if [ -z "$msg_id" ]; then
    rm -f "$logs"
    echo "  WARN: $label TSS ceremony msg_id not found"
    return 0
  fi

  if [ -z "${end_line-}" ] && [ -n "$filter" ]; then
    end_line=$(grep -F "$end_pattern" "$logs" | grep -F "msg_id=$msg_id" | grep -F "$filter" | sort | head -1 || true)
    if [ -z "$end_line" ]; then
      end_line=$(grep -F "$end_pattern" "$logs" | grep -F "\"msg_id\":\"$msg_id\"" | grep -F "$filter" | sort | head -1 || true)
    fi
  elif [ -z "${end_line-}" ]; then
    end_line=$(grep -F "$end_pattern" "$logs" | grep -F "msg_id=$msg_id" | sort | head -1 || true)
    if [ -z "$end_line" ]; then
      end_line=$(grep -F "$end_pattern" "$logs" | grep -F "\"msg_id\":\"$msg_id\"" | sort | head -1 || true)
    fi
  fi
  rm -f "$logs"

  if [ -z "$end_line" ]; then
    echo "  WARN: $label TSS ceremony stop log not found for msg_id=$msg_id"
    return 0
  fi

  start_ts=$(echo "$start_line" | awk '{print $1}')
  end_ts=$(echo "$end_line" | awk '{print $1}')
  start_ms=$(ts_ms "$start_ts")
  end_ms=$(ts_ms "$end_ts")
  LAST_TSS_MSG_ID="$msg_id"
  LAST_TSS_ELAPSED_MS=$((end_ms - start_ms))
  LAST_TSS_START_TS="$start_ts"
  LAST_TSS_START_CONTAINER=$(echo "$start_line" | awk '{print $2}')
  LAST_TSS_STOP_TS="$end_ts"
  LAST_TSS_STOP_CONTAINER=$(echo "$end_line" | awk '{print $2}')
  echo "  $label TSS leader-start-to-first-party-produced=${LAST_TSS_ELAPSED_MS}ms msg_id=$msg_id"
  echo "    start: $(echo "$start_line" | cut -d' ' -f1-2)"
  echo "    stop:  $(echo "$end_line" | cut -d' ' -f1-2)"
}

print_keygen_ceremony_timing() {
  local label="$1"
  local engine="$2"
  local pubkey="$3"
  local since="$4"

  if [ "$engine" = "frost" ]; then
    print_ceremony_timing "$label keygen" "FROST keygen ceremony leader started" "FROST keygen ceremony party produced" "$since"
  else
    print_ceremony_timing "$label keygen" "GG20 keygen ceremony leader started" "GG20 keygen ceremony party produced" "$since" "$pubkey"
  fi
}

print_keysign_ceremony_timing() {
  local label="$1"
  local engine="$2"
  local pool_pubkey="$3"
  local since="$4"

  if [ "$engine" = "frost" ]; then
    print_ceremony_timing "$label keysign" "FROST keysign ceremony leader started" "FROST keysign ceremony party produced" "$since" "$pool_pubkey" 1
  else
    print_ceremony_timing "$label keysign" "GG20 keysign ceremony leader started" "GG20 keysign ceremony party produced" "$since" "$pool_pubkey" 1
  fi
}

wait_for_migration_broadcast() {
  local label="$1"
  local engine="$2"
  local pool_pubkey="$3"
  local since="$4"
  local txid_out="${5-}"
  for i in $(seq 1 90); do
    local line
    line=$(bifrost_logs_since "$since" | grep -i "broadcasted tx to chain" | grep -i "memo=.*MIGRATE" | sort | tail -1 || true)
    if [ -n "$line" ]; then
      echo "  $label migration keysign+broadcast after $(($(date +%s) - $(date -d "$since" +%s)))s"
      print_keysign_ceremony_timing "$label migration" "$engine" "$pool_pubkey" "$since"
      echo "  $line" | sed -E 's/\x1b\[[0-9;]*m//g'
      if [ -n "$txid_out" ]; then
        printf -v "$txid_out" '%s' "$(echo "$line" | sed -E 's/\x1b\[[0-9;]*m//g' | sed -n 's/.*txid=\([^ ]*\).*/\1/p; s/.*"txid":"\([^"]*\)".*/\1/p' | head -1)"
      fi
      return 0
    fi
    sleep 10
  done
  echo "  WARN: $label migration broadcast not found"
}

wait_for_retiring_vault_empty() {
  local label="$1"
  local pubkey="$2"
  local amount

  echo "  waiting for retiring vault to empty: $label $pubkey"
  for i in $(seq 1 "$RETIRING_EMPTY_CHECKS"); do
    amount=$(retiring_vault_btc_amount "$pubkey")
    echo "  [${i}/${RETIRING_EMPTY_CHECKS}] $label retiring BTC amount=${amount:-unknown}"
    if [ -z "$amount" ] || [ "$amount" = "0" ]; then
      echo "  $label retiring vault empty"
      return 0
    fi
    sleep "$POLL_SECONDS"
  done

  echo "TIMEOUT: $label retiring vault still has BTC after $RETIRING_EMPTY_CHECKS checks"
  dump_chain_state "$label retiring-vault-timeout"
  return 1
}

print_keyshare_engines() {
  local pubkey="$1"
  echo "  localstate engines for $pubkey:"
  for name in $(node_names); do
    container=$(bifrost_container_for_name "$name")
    engine=$(docker exec "$container" sh -lc '
      file="/root/.thornode/localstate-'"$pubkey"'.json"
      if [ ! -f "$file" ]; then
        echo missing
      elif grep -q "\"signing_engine\":\"schnorr\"" "$file"; then
        echo frost
      elif grep -q "\"signing_engine\":\"gg20\"" "$file"; then
        echo gg20
      else
        echo gg20-default
      fi
    ' 2>/dev/null || echo missing)
    echo "    $name: $engine"
  done
}

mimir() {
  local key="$1"
  local val="$2"
  echo "  mimir $key = $val"
  tx docker-thornode-1 "mimir $key $val --node $DOG_RPC" | grep -E "code:|txhash:" || true
  sleep 7
}

# Vote for a live Mimir with exactly 2/3 active nodes + 1.
mimir_quorum() {
  local key="$1"
  local val="$2"
  local active_names=()
  local active_count threshold votes name node_addr container rpc
  echo "  mimir(quorum) $key = $val"

  for name in $(node_names); do
    node_addr=$(node_address_for_name "$name")
    if curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -e --arg addr "$node_addr" '.[] | select(.node_address==$addr and .status=="Active")' >/dev/null; then
      active_names+=("$name")
    fi
  done

  active_count=${#active_names[@]}
  threshold=$(((active_count * 2) / 3 + 1))
  votes=0
  for name in "${active_names[@]}"; do
    if [ "$name" = "dog" ]; then
      container="docker-thornode-1"
    else
      container="docker-thornode-${name}-1"
    fi
    rpc=$(rpc_for_name "$name")
    tx "$container" "mimir $key $val --node $rpc" | grep -E "code:|txhash:" || true
    votes=$((votes + 1))
    [ "$votes" -ge "$threshold" ] && break
    sleep 4
  done
}

wait_for_health() {
  local container="$1"
  local max_wait="${2:-120}"
  echo -n "  waiting for $container"
  for i in $(seq 1 "$max_wait"); do
    local status
    status=$(docker inspect --format='{{.State.Health.Status}}' "$container" 2>/dev/null)
    if [ "$status" = "healthy" ]; then
      echo " OK (${i}s)"
      return 0
    fi
    if docker exec "$container" sh -lc 'curl -fsS --max-time 2 "localhost:${PORT_API:-1317}/thorchain/block" >/dev/null' >/dev/null 2>&1; then
      echo " OK (${i}s api)"
      return 0
    fi
    echo -n "."
    sleep 1
  done
  echo " TIMEOUT after ${max_wait}s"
  return 1
}

set_node_keys() {
  local container="$1"
  local seed="$2"
  local rpc="$3"

  echo "  set-node-keys on $container"
  docker exec -e SIGNER_SEED_PHRASE="$seed" "$container" bash -c '
    NODE_PUB_KEY=$(echo "'"$PASSWORD"'" | thornode keys show thorchain --pubkey --keyring-backend '"$KEYRING"' | thornode pubkey)
    NODE_PUB_KEY_ED25519=$(thornode ed25519)
    VALIDATOR=$(thornode tendermint show-validator | thornode pubkey --bech cons)
    printf "'"$PASSWORD"'\n" | thornode tx thorchain set-node-keys "$NODE_PUB_KEY" "$NODE_PUB_KEY_ED25519" "$VALIDATOR" --from thorchain --keyring-backend '"$KEYRING"' --chain-id '"$CHAIN_ID"' --node '"$rpc"' --yes
  ' 2>&1 | grep -E "code:|txhash:" || true
}

set_ip() {
  local container="$1"
  local rpc="$2"

  local ip
  ip=$(docker inspect "$container" -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
  echo "  set-ip-address $ip on $container"
  tx "$container" "set-ip-address $ip --node $rpc" | grep -E "code:|txhash:" || true
}

set_version() {
  local container="$1"
  local rpc="$2"

  echo "  set-version on $container"
  tx "$container" "set-version --node $rpc" | grep -E "code:|txhash:" || true
}

if [ "${FROST_CHAINNET_LIB_ONLY:-0}" = "1" ]; then
  # shellcheck disable=SC2317
  return 0 2>/dev/null || exit 0
fi

echo "ERROR: frost-chainnet-singlehost-lib.sh is an internal library." >&2
echo "Use frost-chainnet-blame.sh or frost-chainnet-timing-run.sh." >&2
exit 1
