#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-docker-compose.dkls-chainnet-timing.yml}"
COMPOSE_FILE_PATH="$SCRIPT_DIR/$COMPOSE_FILE"
ABORT_COMPOSE_FILE_PATH="$SCRIPT_DIR/docker-compose.dkls-abort.yml"
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
NODE_META_FILE="${NODE_META_FILE:-$SCRIPT_DIR/dkls-chainnet-generated/nodes-${DKLS_NODE_COUNT:-7}.tsv}"
DKLS_NODE_COUNT="${DKLS_NODE_COUNT:-$(awk 'NR>1{c++} END{print c+0}' "$NODE_META_FILE" 2>/dev/null || echo 7)}"
DKLS_ACTIVE_COUNT="${DKLS_ACTIVE_COUNT:-$((DKLS_NODE_COUNT - 1))}"
DKLS_STANDBY_COUNT="${DKLS_STANDBY_COUNT:-1}"
DKLS_NEW_NODES_PER_CHURN="${DKLS_NEW_NODES_PER_CHURN:-$((DKLS_ACTIVE_COUNT - 1))}"
DKLS_MIN_P2P_PEERS="${DKLS_MIN_P2P_PEERS:-$((DKLS_NODE_COUNT - 2))}"
DOGE_RPC_USER="${DOGE_RPC_USER:-thorchain}"
DOGE_RPC_PASSWORD="${DOGE_RPC_PASSWORD:-password}"
DOGE_MINING_ADDRESS="${DOGE_MINING_ADDRESS:-mnaioCtEGdw6bd6rWJ13Mbre1kN5rPa2Mo}"
DOGE_LP_AMOUNT="${DOGE_LP_AMOUNT:-10000}"
POLL_SECONDS="${POLL_SECONDS:-30}"
FAST_POLL_SECONDS="${FAST_POLL_SECONDS:-5}"
INITIAL_CHURN_INTERVAL="${INITIAL_CHURN_INTERVAL:-10}"
INITIAL_CHURN_RETRY_INTERVAL="${INITIAL_CHURN_RETRY_INTERVAL:-40}"
DKLS_CHURN_INTERVAL="${DKLS_CHURN_INTERVAL:-10}"
DKLS_CHURN_RETRY_INTERVAL="${DKLS_CHURN_RETRY_INTERVAL:-10}"
CHURN_MIGRATE_ROUNDS="${CHURN_MIGRATE_ROUNDS:-2}"
GG20_KEYGEN_RETRY_INTERVAL="${GG20_KEYGEN_RETRY_INTERVAL:-40}"
GG20_JAIL_TIME_KEYGEN="${GG20_JAIL_TIME_KEYGEN:-45}"
DKLS_KEYGEN_RETRY_INTERVAL="${DKLS_KEYGEN_RETRY_INTERVAL:-10}"
DKLS_JAIL_TIME_KEYGEN="${DKLS_JAIL_TIME_KEYGEN:-11}"
# 20 chainnet blocks has been about two minutes in the DKLS/DOGE cluster.
FUND_MIGRATION_INTERVAL="${FUND_MIGRATION_INTERVAL:-20}"
INITIAL_KEYGEN_CHECKS="${INITIAL_KEYGEN_CHECKS:-40}"
DKLS_KEYGEN_CHECKS="${DKLS_KEYGEN_CHECKS:-50}"
DKLS_ABORT_CHECKS="${DKLS_ABORT_CHECKS:-30}"
SECOND_DKLS_KEYGEN_CHECKS="${SECOND_DKLS_KEYGEN_CHECKS:-70}"
RETIRING_EMPTY_CHECKS="${RETIRING_EMPTY_CHECKS:-80}"
RESULTS_DIR="${RESULTS_DIR:-$SCRIPT_DIR/dkls-chainnet-results}"
RUN_ID="${RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)}"
RESULTS_CSV="${RESULTS_CSV:-$RESULTS_DIR/tss-timings.csv}"
RESULTS_JSONL="${RESULTS_JSONL:-$RESULTS_DIR/tss-timings.jsonl}"

DOG_NODE_ADDR="cthor1zf3gsk7edzwl9syyefvfhle37cjtql35layy8z"
DOGE_LP_THOR_ADDRESS="${DOGE_LP_THOR_ADDRESS:-$DOG_NODE_ADDR}"
CAT_SEED="cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat crawl"
FOX_SEED="fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox filter"
GOAT_SEED="goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat answer"
WHALE_SEED="whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale away"
WOLF_SEED="wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf analyst"
EAGLE_SEED="eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle bitter"

tx() {
  local container="$1"
  shift
  docker exec "$container" bash -c "printf '${PASSWORD}\n' | thornode tx thorchain $* --from thorchain --keyring-backend $KEYRING --chain-id $CHAIN_ID --yes" 2>&1
}

compose() {
  if [ -f "$COMPOSE_ENV_FILE" ]; then
    docker compose --env-file "$COMPOSE_ENV_FILE" -f "$COMPOSE_FILE_PATH" "$@"
  else
    docker compose -f "$COMPOSE_FILE_PATH" "$@"
  fi
}

compose_abort() {
  if [ -f "$COMPOSE_ENV_FILE" ]; then
    docker compose --env-file "$COMPOSE_ENV_FILE" -f "$COMPOSE_FILE_PATH" -f "$ABORT_COMPOSE_FILE_PATH" "$@"
  else
    docker compose -f "$COMPOSE_FILE_PATH" -f "$ABORT_COMPOSE_FILE_PATH" "$@"
  fi
}

joiner_names() {
  if [ -f "$NODE_META_FILE" ]; then
    awk 'NR>2 {print $2}' "$NODE_META_FILE"
    return
  fi
  printf '%s\n' cat fox goat whale wolf eagle
}

rpc_for_name() {
  if [ -f "$NODE_META_FILE" ]; then
    if [ "$1" = "dog" ]; then
      echo "$DOG_RPC"
    else
      echo "tcp://thornode-$1:27147"
    fi
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
  if [ -f "$NODE_META_FILE" ]; then
    awk -F '\t' -v name="$1" '$2==name {print $3}' "$NODE_META_FILE"
    return
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
  if [ -f "$NODE_META_FILE" ]; then
    awk 'NR>1 {print $2}' "$NODE_META_FILE"
    return
  fi
  printf '%s\n' dog
  joiner_names
}

node_address_for_name() {
  if [ -f "$NODE_META_FILE" ]; then
    awk -F '\t' -v name="$1" '$2==name {print $4}' "$NODE_META_FILE"
    return
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

clear_bifrost_address_books() {
  echo "  clearing persisted bifrost P2P address books"
  for name in $(node_names); do
    container=$(bifrost_container_for_name "$name")
    docker exec "$container" sh -lc 'rm -f /root/.thornode/address_book.seed /var/data/bifrost/address_book.seed' 2>/dev/null || true
  done
}

stop_joiner_bifrosts() {
  local name
  echo "  stopping joiner bifrosts until node setup is complete"
  for name in $(joiner_names); do
    compose stop "bifrost-$name" >/dev/null 2>&1 || true
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
  local min_peers="${2:-$DKLS_MIN_P2P_PEERS}"
  local name container peer_count ok

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
    sleep "$FAST_POLL_SECONDS"
  done
  return 1
}

restart_bifrosts_ordered() {
  local compose_cmd="${1:-compose}"
  local clear_books="${2:-0}"
  echo "  starting/restarting bifrosts in bootstrap order"
  if [ "$clear_books" = "1" ]; then
    clear_bifrost_address_books
  fi
  "$compose_cmd" up -d --no-build bifrost >/dev/null
  "$compose_cmd" restart bifrost
  sleep 10
  for name in $(joiner_names); do
    "$compose_cmd" up -d --no-build "bifrost-$name" >/dev/null
    sleep 12
  done
  sleep 10
  print_bifrost_p2p_status
  wait_for_bifrost_p2p 30 "$DKLS_MIN_P2P_PEERS"
}

active_vault_shape() {
  curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -r '[.[] | select(.status=="ActiveVault") | (.membership | length)] | sort | join(",")' 2>/dev/null || true
}

active_vault_pubkey() {
  curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -r --argjson active "$DKLS_ACTIVE_COUNT" '[.[] | select(.status=="ActiveVault") | select((.membership | length)==$active) | (.pub_key | if type=="object" then .value else . end)] | .[0] // ""' 2>/dev/null
}

retiring_vault_doge_amount() {
  local pubkey="$1"
  curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -r --arg pubkey "$pubkey" '
    [.[] | select(.status=="RetiringVault") | select((.pub_key | if type=="object" then .value else . end)==$pubkey) | ((.coins // []) | map(select(.asset=="DOGE.DOGE") | .amount | tonumber) | add // 0)] | .[0] // 0
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
    if [ $((i % 10)) -eq 0 ]; then
      curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '.[] | select(.jail.release_height > 0 or (.preflight_status.status // "") != "Ready") | "      node=\(.node_address) status=\(.status) preflight=\(.preflight_status.status // "") reason=\(.preflight_status.reason // "") jail_release=\(.jail.release_height // 0) jail_reason=\(.jail.reason // "")"' 2>/dev/null || true
    fi
    if [ "$active" = "$active_target" ] && [ "$standby" = "$standby_target" ] && [ "$ready" -ge "$ready_target" ]; then
      return 0
    fi
    sleep "$FAST_POLL_SECONDS"
  done

  dump_chain_state "validator shape timeout: $label"
  return 1
}

node_field() {
  local node_addr="$1"
  local jq_expr="$2"
  curl -s "localhost:1317/thorchain/node/$node_addr" 2>/dev/null | jq -r "$jq_expr" 2>/dev/null || true
}

wait_for_node_field() {
  local label="$1"
  local node_addr="$2"
  local jq_expr="$3"
  local expected="$4"
  local max_checks="${5:-24}"
  local value

  for i in $(seq 1 "$max_checks"); do
    value=$(node_field "$node_addr" "$jq_expr")
    if [ "$value" = "$expected" ]; then
      echo "    $label confirmed: $value"
      return 0
    fi
    echo "    [$i/$max_checks] waiting for $label: got '${value:-}' want '$expected'"
    sleep "$FAST_POLL_SECONDS"
  done
  return 1
}

wait_for_node_exists() {
  local node_addr="$1"
  local max_checks="${2:-24}"
  local status

  for i in $(seq 1 "$max_checks"); do
    status=$(node_field "$node_addr" '.status // "Unknown"')
    if [ "$status" != "Unknown" ] && [ -n "$status" ]; then
      echo "    node $node_addr exists: $status"
      return 0
    fi
    echo "    [$i/$max_checks] waiting for bonded node account $node_addr"
    sleep "$FAST_POLL_SECONDS"
  done
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
  for key in EnableDKLS ChurnInterval ChurnRetryInterval KeygenRetryInterval JailTimeKeygen JailTimeKeysign ChurnMigrateRounds FundMigrationInterval HaltChurning NumberOfNewNodesPerChurn; do
    printf "  %s=" "$key"
    curl -s "localhost:1317/thorchain/mimir/key/$key" 2>/dev/null || true
    echo ""
  done
  echo "nodes:"
  curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '.[].status' 2>/dev/null | sort | uniq -c || true
  curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '.[] | select(.jail.release_height > 0 or (.preflight_status.status // "") != "Ready") | [.node_address, .status, (.preflight_status.status // ""), (.preflight_status.reason // ""), (.jail.release_height // 0), (.jail.reason // "")] | @tsv' 2>/dev/null || true
  echo "vaults:"
  curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -r '.[] | [.status, (.status_since // 0), (.membership|length), (if (.pub_key|type)=="object" then .pub_key.value else .pub_key end), (((.coins // []) | map(select(.asset=="DOGE.DOGE") | .amount) | join(",")) // "")] | @tsv' 2>/dev/null || true
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
  doge_rpc getblockchaininfo >/dev/null
  echo "  preflight OK: dogecoin RPC reachable"
  init_results
}
doge_rpc_url() {
  local host="${DOGE_HOST-}"
  if [ -z "$host" ]; then
    for env_file in "$COMPOSE_ENV_FILE" "$SCRIPT_DIR/.env"; do
      if [ -f "$env_file" ]; then
        host=$(grep -E '^DOGE_HOST=' "$env_file" | tail -1 | cut -d= -f2- | tr -d '"' || true)
      fi
      [ -n "$host" ] && break
    done
  fi
  host="${host:-127.0.0.1:18332}"
  if [[ $host == http://* || $host == https://* ]]; then
    echo "$host"
  else
    echo "http://$host/"
  fi
}

doge_rpc() {
  local method="$1"
  shift
  local params="${1:-[]}"
  local body response result error
  body="{\"jsonrpc\":\"1.0\",\"id\":\"chainnet\",\"method\":\"$method\",\"params\":$params}"
  response=$(curl -sS --user "$DOGE_RPC_USER:$DOGE_RPC_PASSWORD" \
    --data-binary "$body" \
    -H 'content-type:text/plain;' "$(doge_rpc_url)") || {
    echo "ERROR: dogecoin RPC transport failed: method=$method" >&2
    return 1
  }
  error=$(printf "%s" "$response" | jq -r '.error // empty')
  if [ -n "$error" ] && [ "$error" != "null" ]; then
    echo "ERROR: dogecoin RPC failed: method=$method error=$error" >&2
    return 1
  fi
  result=$(printf "%s" "$response" | jq -r '.result')
  printf "%s\n" "$result"
}

ensure_doge_wallet_funded() {
  local balance immature addr
  balance=$(doge_rpc getbalance 2>/dev/null || echo 0)
  immature=$(doge_rpc getwalletinfo 2>/dev/null | jq -r '.immature_balance // 0' 2>/dev/null || echo 0)
  if awk "BEGIN { exit !($balance >= $DOGE_LP_AMOUNT) }"; then
    return 0
  fi
  echo "  DOGE wallet balance=$balance immature=$immature; mining spendable regtest funds"
  addr=$(doge_rpc getnewaddress)
  doge_rpc generatetoaddress "[120,\"$addr\"]" >/dev/null
  balance=$(doge_rpc getbalance)
  echo "  DOGE wallet funded balance=$balance"
}

add_doge_lp_funds() {
  local inbound="$1"
  local memo="ADD:DOGE.DOGE:${DOGE_LP_THOR_ADDRESS}"
  local memo_hex
  memo_hex=$(printf "%s" "$memo" | xxd -p -c 256)

  echo "  DOGE LP amount=$DOGE_LP_AMOUNT inbound=$inbound"
  echo "  DOGE LP memo=$memo"

  local raw funded signed txid txid_upper
  ensure_doge_wallet_funded
  raw=$(doge_rpc createrawtransaction "[[],{\"$inbound\":$DOGE_LP_AMOUNT,\"data\":\"$memo_hex\"}]")
  funded=$(doge_rpc fundrawtransaction "[\"$raw\"]" | jq -r '.hex')
  signed=$(doge_rpc signrawtransaction "[\"$funded\"]" | jq -r '.hex')
  txid=$(doge_rpc sendrawtransaction "[\"$signed\"]")
  txid_upper=$(printf "%s" "$txid" | tr '[:lower:]' '[:upper:]')
  doge_rpc generatetoaddress "[6,\"$DOGE_MINING_ADDRESS\"]" >/dev/null

  echo "  DOGE LP tx=$txid"
  for i in $(seq 1 60); do
    if docker logs --since 10m docker-thornode-1 2>&1 | grep -q "$txid_upper"; then
      echo "  DOGE LP observed by thornode (${i}x${FAST_POLL_SECONDS}s)"
      return 0
    fi
    sleep "$FAST_POLL_SECONDS"
  done

  echo "  WARN: DOGE LP tx not seen in thornode logs after 5m: $txid"
}

doge_inbound_address() {
  curl -s localhost:1317/thorchain/inbound_addresses 2>/dev/null | jq -r '.[] | select(.chain=="DOGE") | .address' 2>/dev/null
}

wait_for_checksign_log() {
  local label="$1"
  local since="$2"
  for i in $(seq 1 60); do
    if bifrost_logs_since "$since" | grep "secp256k1 check signature verified" >/dev/null; then
      echo "  $label check-sign verified after $(($(date +%s) - $(date -d "$since" +%s)))s"
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
  if [ -n "${DKLS_TIMING_LOG_FILE-}" ]; then
    cat "$DKLS_TIMING_LOG_FILE"
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

  if [ "$engine" = "dkls" ]; then
    print_ceremony_timing "$label keygen" "DKLS keygen ceremony leader started" "DKLS keygen ceremony party produced" "$since"
  else
    print_ceremony_timing "$label keygen" "GG20 keygen ceremony leader started" "GG20 keygen ceremony party produced" "$since" "$pubkey"
  fi
}

print_keysign_ceremony_timing() {
  local label="$1"
  local engine="$2"
  local pool_pubkey="$3"
  local since="$4"

  if [ "$engine" = "dkls" ]; then
    print_ceremony_timing "$label keysign" "DKLS keysign ceremony leader started" "DKLS keysign ceremony party produced" "$since" "$pool_pubkey" 1
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
    line=$(bifrost_logs_since "$since" | grep -i "broadcasted tx to chain" | grep -Ei 'memo=.?MIGRATE|"memo":"MIGRATE' | sort | tail -1 || true)
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
    amount=$(retiring_vault_doge_amount "$pubkey")
    echo "  [${i}/${RETIRING_EMPTY_CHECKS}] $label retiring DOGE amount=${amount:-unknown}"
    if [ -z "$amount" ] || [ "$amount" = "0" ]; then
      echo "  $label retiring vault empty"
      return 0
    fi
    sleep "$POLL_SECONDS"
  done

  echo "TIMEOUT: $label retiring vault still has DOGE after $RETIRING_EMPTY_CHECKS checks"
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
      elif grep -q "\"signing_engine\":\"dkls\"" "$file"; then
        echo dkls
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
  local node_addr
  node_addr=$(docker exec "$container" bash -c "printf '${PASSWORD}\n' | thornode keys show thorchain --keyring-backend $KEYRING -a")

  echo "  set-node-keys on $container"
  docker exec -e SIGNER_SEED_PHRASE="$seed" "$container" bash -c '
    NODE_PUB_KEY=$(echo "'"$PASSWORD"'" | thornode keys show thorchain --pubkey --keyring-backend '"$KEYRING"' | thornode pubkey)
    NODE_PUB_KEY_ED25519=$(thornode ed25519)
    VALIDATOR=$(thornode tendermint show-validator | thornode pubkey --bech cons)
    printf "'"$PASSWORD"'\n" | thornode tx thorchain set-node-keys "$NODE_PUB_KEY" "$NODE_PUB_KEY_ED25519" "$VALIDATOR" --from thorchain --keyring-backend '"$KEYRING"' --chain-id '"$CHAIN_ID"' --node '"$rpc"' --yes
  ' 2>&1 | grep -E "code:|txhash:" || true
  wait_for_node_field "node keys $container" "$node_addr" '.pub_key_set.secp256k1 // ""' "$(docker exec "$container" bash -c "printf '${PASSWORD}\n' | thornode keys show thorchain --pubkey --keyring-backend $KEYRING | thornode pubkey")"
}

set_ip() {
  local container="$1"
  local rpc="$2"
  local node_addr
  node_addr=$(docker exec "$container" bash -c "printf '${PASSWORD}\n' | thornode keys show thorchain --keyring-backend $KEYRING -a")

  local ip
  ip=$(docker inspect "$container" -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
  echo "  set-ip-address $ip on $container"
  tx "$container" "set-ip-address $ip --node $rpc" | grep -E "code:|txhash:" || true
  wait_for_node_field "ip $container" "$node_addr" '.ip_address // ""' "$ip"
}

set_version() {
  local container="$1"
  local rpc="$2"
  local node_addr version
  node_addr=$(docker exec "$container" bash -c "printf '${PASSWORD}\n' | thornode keys show thorchain --keyring-backend $KEYRING -a")
  version=$(docker exec "$container" thornode version 2>/dev/null | head -1)

  echo "  set-version on $container"
  tx "$container" "set-version --node $rpc" | grep -E "code:|txhash:" || true
  wait_for_node_field "version $container" "$node_addr" '.version // ""' "$version"
}

if [ "${DKLS_CHAINNET_LIB_ONLY:-0}" = "1" ]; then
  # shellcheck disable=SC2317
  return 0 2>/dev/null || exit 0
fi

usage() {
  cat <<'EOF'
Usage:
  build/docker/dkls-chainnet-timing-run.sh [run|hcloud]

Single-host 7-node GG20 -> DKLS timing evaluation.

Commands:
  run     Run against the local single-host docker-compose chainnet.
  hcloud  Provision one Hetzner Cloud server and run the 7-node single-host evaluation there.

Useful env for hcloud:
  HCLOUD_TOKEN=...
  DKLS_HCLOUD_SSH_KEY=dkls-codex
  DKLS_HCLOUD_TYPE_7=cpx62
EOF
}

case "${1:-run}" in
run | "") ;;
hcloud)
  DKLS_TIMING_COUNTS=7 DKLS_PROVISION_HCLOUD=1 DKLS_PROVISION_PROVIDER=hcloud exec "$SCRIPT_DIR/dkls-chainnet-singlehost-hcloud-lib.sh"
  ;;
-h | --help | help)
  usage
  exit 0
  ;;
*)
  usage >&2
  exit 1
  ;;
esac

echo "============================================"
echo "DKLS/DOGE Single-Host Timing Run"
echo "============================================"
echo ""
echo "NOTE: This script assumes a fresh deploy. It clears persisted Bifrost"
echo "      address books before ordered restarts so Docker IP reuse does not"
echo "      leave stale peer-id mappings."
echo "      This deployment starts DOGE in GG20 mode, then enables DKLS and waits"
echo "      for the next ECDSA vault to be DKLS-backed."
echo "      Config: node_count=$DKLS_NODE_COUNT active=$DKLS_ACTIVE_COUNT standby=$DKLS_STANDBY_COUNT new_nodes_per_churn=$DKLS_NEW_NODES_PER_CHURN"
echo "              churn_migrate_rounds=$CHURN_MIGRATE_ROUNDS fund_migration_interval=$FUND_MIGRATION_INTERVAL"

echo ""
echo "[0/10] Preflight..."
preflight
stop_joiner_bifrosts

echo ""
echo "[1/10] Waiting for thornode health..."
wait_for_health docker-thornode-1 120

echo ""
echo "[2/10] Setting bootstrap live mimirs..."
# Static chainnet config is baked into constants_chainnet.go. This section only
# uses single-node operational Mimir before the validator set is active.
mimir HaltChurning 1
mimir P2PGateDisabled 1
mimir NumberOfNewNodesPerChurn "$DKLS_NEW_NODES_PER_CHURN"
mimir ChurnRetryInterval "$INITIAL_CHURN_RETRY_INTERVAL"
mimir KeygenRetryInterval "$GG20_KEYGEN_RETRY_INTERVAL"
mimir JailTimeKeygen "$GG20_JAIL_TIME_KEYGEN"
mimir JailTimeKeysign 11

echo ""
dog_bifrost_doge_tip() {
  docker logs --tail 800 docker-bifrost-1 2>&1 |
    sed -E $'s/\x1b\\[[0-9;]*m//g' |
    awk '
      /scan block/ {
        h = ""
        doge = 0
        for (i = 1; i <= NF; i++) {
          if ($i ~ /^height=/) {
            h = $i
            sub(/^height=/, "", h)
          }
          if ($i == "chain=DOGE") {
            doge = 1
          }
        }
        if (doge && h != "") {
          print h
        }
      }
    ' |
    tail -n 1
}

echo "[3/10] Waiting for dog bifrost to report DOGE fee + scanner tip..."
for i in $(seq 1 60); do
  fee_rate=$(curl -s localhost:1317/thorchain/inbound_addresses 2>/dev/null | jq -r '.[] | select(.chain=="DOGE") | .observed_fee_rate' 2>/dev/null)
  scanner_tip=$(dog_bifrost_doge_tip)
  thor_observed=$(curl -s localhost:1317/thorchain/lastblock 2>/dev/null | jq -r '.[] | select(.chain=="DOGE") | .last_observed_in' 2>/dev/null)
  if [ -n "$fee_rate" ] && [ "$fee_rate" != "0" ] && [ "$fee_rate" != "null" ] && [ -n "$scanner_tip" ] && [ "$scanner_tip" != "0" ]; then
    echo "  DOGE fee=$fee_rate scanner_tip=$scanner_tip thor_observed=${thor_observed:-0} OK (${i}x10s)"
    break
  fi
  echo -n "  [$i] fee=$fee_rate scanner_tip=${scanner_tip:-0} thor_observed=${thor_observed:-0}"
  echo " waiting..."
  sleep 10
done

echo ""
echo "[4/10] Waiting for joiner thornodes ($(joiner_label))..."
while read -r name; do
  wait_for_health "docker-thornode-${name}-1" 120
done < <(joiner_names)

echo ""
echo "[5/10] Topping up bond for dog and joiners ($(joiner_label))..."
get_node_addr() {
  local container="$1"
  docker exec "$container" bash -c "printf '${PASSWORD}\n' | thornode keys show thorchain --keyring-backend $KEYRING -a"
}
echo "  dog=$DOG_NODE_ADDR"
for name in $(joiner_names); do
  addr=$(get_node_addr "docker-thornode-${name}-1")
  echo "  $name=$addr"
  echo "  bonding 70T RUNE to $name"
  tx docker-thornode-1 "deposit 70000000000000 THOR.RUNE BOND:$addr --node $DOG_RPC --gas auto" | grep -E "code:|txhash:" || true
  wait_for_node_exists "$addr"
done
echo "  bonding 70T RUNE to dog"
tx docker-thornode-1 "deposit 70000000000000 THOR.RUNE BOND:$DOG_NODE_ADDR --node $DOG_RPC --gas auto" | grep -E "code:|txhash:" || true
sleep 7

echo ""
echo "[6/10] Setting node keys, IPs, versions for joiners ($(joiner_label))..."
while read -r name; do
  seed=$(seed_for_name "$name")
  rpc=$(rpc_for_name "$name")
  set_node_keys "docker-thornode-${name}-1" "$seed" "$rpc"
  set_ip "docker-thornode-${name}-1" "$rpc"
  set_version "docker-thornode-${name}-1" "$rpc"
done < <(joiner_names)

echo ""
echo "[6.5/10] Restarting bifrosts in bootstrap order (dog first, then others)..."
restart_bifrosts_ordered compose 1

echo ""
echo "[6.6/10] Waiting for all joiners to be Ready before first GG20 churn..."
wait_for_validator_shape "pre-GG20 first churn" 1 "$DKLS_ACTIVE_COUNT" "$DKLS_ACTIVE_COUNT" 180

echo ""
echo "[6.7/10] Lifting HaltChurning so the first GG20 keygen can fire..."
gg20_start_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
gg20_start_epoch=$(date +%s)
mimir HaltChurning 0

echo ""
echo "[7/10] Monitoring initial GG20 churn + keygen..."
echo "  Nodes should move: Whitelisted -> Standby -> Ready -> Active"
echo "  Keygen should produce 1 active DOGE/ECDSA GG20 vault of $DKLS_ACTIVE_COUNT members (check every ${POLL_SECONDS}s)"

gg20_vault=""
for i in $(seq 1 "$INITIAL_KEYGEN_CHECKS"); do
  sleep "$POLL_SECONDS"
  statuses=$(curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '.[].status' 2>/dev/null | sort | uniq -c || echo "error")
  active_vault_shape=$(active_vault_shape)

  echo "  [${i}/${INITIAL_KEYGEN_CHECKS}] nodes: $statuses | active vault shape: ${active_vault_shape:-none}"

  if [ "$active_vault_shape" = "$DKLS_ACTIVE_COUNT" ]; then
    gg20_vault=$(active_vault_pubkey)
    echo ""
    echo "============================================"
    echo "SUCCESS: initial ${DKLS_ACTIVE_COUNT}-member DOGE/GG20 vault created!"
    echo "============================================"
    echo "  GG20 wait-to-vault elapsed=$(($(date +%s) - gg20_start_epoch))s height=$(chain_height)"
    print_keygen_ceremony_timing "GG20" "gg20" "$gg20_vault" "$gg20_start_time"
    append_timing_result "keygen" "gg20" "GG20 initial" "$gg20_vault" "$(($(date +%s) - gg20_start_epoch))" "$(chain_height)" "" "initial GG20 keygen"
    wait_for_checksign_log "GG20" "$gg20_start_time"
    print_keyshare_engines "$gg20_vault"
    break
  fi
done

if [ -z "$gg20_vault" ]; then
  echo ""
  echo "TIMEOUT: initial ${DKLS_ACTIVE_COUNT}-member DOGE/GG20 vault not created."
  dump_chain_state "initial GG20 keygen timeout"
  echo "Check logs:"
  echo "  docker logs docker-thornode-1 2>&1 | grep -i 'keygen\|churn\|rotation'"
  echo "  docker logs docker-bifrost-1 2>&1 | grep -i 'keygen\|doge'"
  exit 1
fi

if [ "${STOP_AFTER_GG20_VAULT:-0}" = "1" ]; then
  mimir_quorum HALTCHURNING 1
  wait_for_validator_shape "post-GG20 stop" "$DKLS_ACTIVE_COUNT" "$DKLS_STANDBY_COUNT" 0 90
  dump_chain_state "GG20 ${DKLS_ACTIVE_COUNT}-node vault ready"
  echo ""
  echo "STOP_AFTER_GG20_VAULT=1 set; stopping before LP/DKLS steps."
  exit 0
fi

mimir_quorum HALTCHURNING 1
wait_for_validator_shape "post-GG20 pre-DKLS" "$DKLS_ACTIVE_COUNT" "$DKLS_STANDBY_COUNT" 1 90
mimir_quorum NumberOfNewNodesPerChurn 1

echo ""
echo "[8/10] Adding DOGE LP funds to GG20 vault, then enabling DKLS + churning..."
gg20_inbound=$(doge_inbound_address)
if [ -z "$gg20_inbound" ] || [ "$gg20_inbound" = "null" ]; then
  echo "ERROR: no DOGE inbound address for GG20 vault"
  exit 1
fi
add_doge_lp_funds "$gg20_inbound"

dkls_start_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
dkls_start_epoch=$(date +%s)
mimir_quorum ChurnRetryInterval "$DKLS_CHURN_RETRY_INTERVAL"
mimir_quorum KeygenRetryInterval "$DKLS_KEYGEN_RETRY_INTERVAL"
mimir_quorum JailTimeKeygen "$DKLS_JAIL_TIME_KEYGEN"
mimir_quorum EnableDKLS 1
mimir_quorum HALTCHURNING 0

echo ""
echo "[9/10] Waiting for DKLS churn-created ECDSA vault and GG20 migration keysign..."
dkls_vault=""
for i in $(seq 1 "$DKLS_KEYGEN_CHECKS"); do
  sleep "$POLL_SECONDS"
  statuses=$(curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '.[].status' 2>/dev/null | sort | uniq -c || echo "error")
  current_shape=$(active_vault_shape)
  current_vault=$(active_vault_pubkey)

  echo "  [${i}/${DKLS_KEYGEN_CHECKS}] nodes: $statuses | active vault shape: ${current_shape:-none} | active vault: ${current_vault:-none}"

  if [ "$current_shape" = "$DKLS_ACTIVE_COUNT" ] && [ -n "$current_vault" ] && [ "$current_vault" != "$gg20_vault" ]; then
    dkls_vault="$current_vault"
    echo ""
    echo "============================================"
    echo "SUCCESS: post-EnableDKLS ${DKLS_ACTIVE_COUNT}-member DOGE/DKLS vault created!"
    echo "============================================"
    echo "  DKLS wait-to-vault elapsed=$(($(date +%s) - dkls_start_epoch))s height=$(chain_height)"
    print_keygen_ceremony_timing "DKLS" "dkls" "$dkls_vault" "$dkls_start_time"
    append_timing_result "keygen" "dkls" "DKLS first" "$dkls_vault" "$(($(date +%s) - dkls_start_epoch))" "$(chain_height)" "" "first DKLS keygen after EnableDKLS"
    wait_for_checksign_log "DKLS" "$dkls_start_time"
    print_keyshare_engines "$dkls_vault"
    mimir_quorum HALTCHURNING 1
    gg20_migration_txid=""
    wait_for_migration_broadcast "GG20" "gg20" "$gg20_vault" "$dkls_start_time" gg20_migration_txid
    append_timing_result "keysign" "gg20" "GG20 migration" "$gg20_vault" "$(($(date +%s) - dkls_start_epoch))" "$(chain_height)" "$gg20_migration_txid" "GG20 retiring vault migration"
    break
  fi
done

if [ -z "$dkls_vault" ]; then
  echo ""
  echo "TIMEOUT: post-EnableDKLS ${DKLS_ACTIVE_COUNT}-member DOGE/DKLS vault not created."
  dump_chain_state "DKLS keygen timeout"
  echo "Check logs:"
  echo "  docker logs docker-thornode-1 2>&1 | grep -i 'keygen\|churn\|rotation'"
  echo "  docker logs docker-bifrost-1 2>&1 | grep -i 'keygen\|dkls\|doge'"
  exit 1
fi

echo ""
echo "[9.5/10] Waiting for GG20 retiring vault to empty before second DKLS churn..."
wait_for_retiring_vault_empty "GG20" "$gg20_vault"
wait_for_validator_shape "pre-second-DKLS" "$DKLS_ACTIVE_COUNT" "$DKLS_STANDBY_COUNT" 1 90

echo ""
echo "[10/10] Waiting for another DKLS churn and DKLS migration keysign..."
dkls2_start_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
dkls2_start_epoch=$(date +%s)
mimir_quorum HALTCHURNING 0

dkls2_vault=""
for i in $(seq 1 "$SECOND_DKLS_KEYGEN_CHECKS"); do
  sleep "$POLL_SECONDS"
  statuses=$(curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '.[].status' 2>/dev/null | sort | uniq -c || echo "error")
  current_shape=$(active_vault_shape)
  current_vault=$(active_vault_pubkey)

  echo "  [${i}/${SECOND_DKLS_KEYGEN_CHECKS}] nodes: $statuses | active vault shape: ${current_shape:-none} | active vault: ${current_vault:-none}"

  if [ "$current_shape" = "$DKLS_ACTIVE_COUNT" ] && [ -n "$current_vault" ] && [ "$current_vault" != "$dkls_vault" ]; then
    dkls2_vault="$current_vault"
    echo ""
    echo "============================================"
    echo "SUCCESS: second DKLS ${DKLS_ACTIVE_COUNT}-member vault created!"
    echo "============================================"
    echo "  DKLS second wait-to-vault elapsed=$(($(date +%s) - dkls2_start_epoch))s height=$(chain_height)"
    print_keygen_ceremony_timing "DKLS second" "dkls" "$dkls2_vault" "$dkls2_start_time"
    append_timing_result "keygen" "dkls" "DKLS second" "$dkls2_vault" "$(($(date +%s) - dkls2_start_epoch))" "$(chain_height)" "" "second DKLS keygen"
    wait_for_checksign_log "DKLS second" "$dkls2_start_time"
    print_keyshare_engines "$dkls2_vault"
    mimir_quorum HALTCHURNING 1
    dkls_migration_txid=""
    wait_for_migration_broadcast "DKLS" "dkls" "$dkls_vault" "$dkls2_start_time" dkls_migration_txid
    append_timing_result "keysign" "dkls" "DKLS migration" "$dkls_vault" "$(($(date +%s) - dkls2_start_epoch))" "$(chain_height)" "$dkls_migration_txid" "DKLS retiring vault migration"
    break
  fi
done

if [ -z "$dkls2_vault" ]; then
  echo ""
  echo "TIMEOUT: second DKLS vault not created after 35 minutes."
  dump_chain_state "second DKLS keygen timeout"
  exit 1
fi

echo ""
echo "============================================"
echo "TIMING RUN COMPLETE"
echo "============================================"
echo "Observed paths:"
echo "  GG20 keygen + check-sign"
echo "  GG20 DOGE migration keysign"
echo "  DKLS keygen + check-sign"
echo "  DKLS DOGE migration keysign"
echo ""
echo "Vault info:"
curl -s localhost:1317/thorchain/vaults/asgard | jq '[.[] | {status, status_since, pub_key, membership_count: (.membership | length), doge: ((.coins // []) | map(select(.asset=="DOGE.DOGE") | .amount) | join(","))}]'
echo ""
echo "Timing rows saved:"
echo "  $RESULTS_CSV"
echo "  $RESULTS_JSONL"
