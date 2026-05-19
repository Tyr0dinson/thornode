#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DKLS_CHAINNET_LIB_ONLY=1 source "$SCRIPT_DIR/dkls-chainnet-singlehost-lib.sh"

usage() {
  cat <<'EOF'
Usage:
  build/docker/dkls-chainnet-blame.sh

Single-host DKLS blame evaluation. Runs DKLS keygen and keysign blame cases
against the local docker-compose chainnet.

Useful env:
  BLAME_STAGE=all|keygen|keysign-prepare|keysign-funded-active|keysign-existing
  DKLS_KEYSIGN_BLAME_VAULT=<vault pubkey>  # required for keysign-existing
EOF
}

case "${1:-run}" in
run | "") ;;
-h | --help | help)
  usage
  exit 0
  ;;
*)
  usage >&2
  exit 1
  ;;
esac

BLAME_RESULTS_DIR="${BLAME_RESULTS_DIR:-$SCRIPT_DIR/dkls-chainnet-results}"
BLAME_RESULTS_JSONL="${BLAME_RESULTS_JSONL:-$BLAME_RESULTS_DIR/dkls-blame.jsonl}"
BLAME_CHURN_RETRY_INTERVAL="${BLAME_CHURN_RETRY_INTERVAL:-40}"
BLAME_JAIL_BLOCKS="${BLAME_JAIL_BLOCKS:-20}"
BLAME_POLL_SECONDS="${BLAME_POLL_SECONDS:-10}"
BLAME_KEYGEN_CHECKS="${BLAME_KEYGEN_CHECKS:-90}"
BLAME_KEYSIGN_CHECKS="${BLAME_KEYSIGN_CHECKS:-90}"
BLAME_STAGE="${BLAME_STAGE:-all}"

json_escape() {
  python3 -c 'import json,sys; print(json.dumps(sys.stdin.read().strip()))'
}

record_blame_result() {
  local phase="$1" case_name="$2" mode="$3" target_pubkey="$4" target_addr="$5" status="$6" txid="$7" notes="$8"
  mkdir -p "$BLAME_RESULTS_DIR"
  python3 - "$BLAME_RESULTS_JSONL" "$phase" "$case_name" "$mode" "$target_pubkey" "$target_addr" "$status" "$txid" "$notes" "$(chain_height)" <<'PY'
import json
import sys
path, phase, case_name, mode, target_pubkey, target_addr, status, txid, notes, height = sys.argv[1:]
row = {
    "recorded_at": __import__("datetime").datetime.utcnow().replace(microsecond=0).isoformat() + "Z",
    "phase": phase,
    "case": case_name,
    "mode": mode,
    "target_pubkey": target_pubkey,
    "target_address": target_addr,
    "status": status,
    "txid": txid,
    "notes": notes,
    "height": height,
}
with open(path, "a") as f:
    f.write(json.dumps(row, separators=(",", ":")) + "\n")
PY
}

node_addr_for_pubkey() {
  local pubkey="$1"
  curl -s localhost:1317/thorchain/nodes | jq -r --arg pubkey "$pubkey" '.[] | select(.pub_key_set.secp256k1==$pubkey) | .node_address'
}

ready_node_secp_pubkey() {
  curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '
    [.[] | select(.status=="Ready" or (.status=="Standby" and .preflight_status.status=="Ready")) | .pub_key_set.secp256k1] | .[0] // ""
  ' 2>/dev/null
}

released_ready_node_row() {
  local height
  height=$(chain_height)
  curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r --argjson height "$height" '
    [
      .[]
      | select(.status=="Ready" or (.status=="Standby" and .preflight_status.status=="Ready"))
      | select((.jail.release_height // 0) <= $height)
      | [.pub_key_set.secp256k1, .node_address, (.slash_points // 0), (.jail.release_height // 0)]
      | @tsv
    ] | .[0] // ""
  ' 2>/dev/null
}

active_dkls_vault_pubkey() {
  local pubkey
  pubkey=$(active_vault_pubkey)
  [ -n "$pubkey" ] || return 1
  if curl -s localhost:1317/thorchain/vaults/asgard | jq -e --arg pubkey "$pubkey" '.[] | select(.status=="ActiveVault") | select(.pub_key==$pubkey) | select((.membership|length)==6)' >/dev/null; then
    echo "$pubkey"
  fi
}

asgard_vault_exists() {
  local pubkey="$1"
  [ -n "$pubkey" ] || return 1
  curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -e --arg vault "$pubkey" '
    any(.[]; ((.pub_key | if type=="object" then .value else . end)==$vault))
  ' >/dev/null
}

vault_member_row() {
  local vault_pubkey="$1"
  local excluded="${2-}"
  local height
  height=$(chain_height)
  curl -s localhost:1317/thorchain/vaults/asgard | jq -r --arg vault "$vault_pubkey" '
    .[] | select((if (.pub_key|type)=="object" then .pub_key.value else .pub_key end)==$vault) | .membership[]?
  ' | while read -r member; do
    [ -n "$member" ] || continue
    case ",$excluded," in
    *",$member,"*) continue ;;
    esac
    curl -s localhost:1317/thorchain/nodes | jq -r --arg pubkey "$member" --argjson height "$height" '
      .[]
      | select(.pub_key_set.secp256k1==$pubkey)
      | select((.jail.release_height // 0) <= $height)
      | [.pub_key_set.secp256k1, .node_address, (.slash_points // 0), (.jail.release_height // 0)]
      | @tsv
    '
  done | head -1
}

vault_member_baseline() {
  local vault_pubkey="$1"
  curl -s localhost:1317/thorchain/vaults/asgard | jq -r --arg vault "$vault_pubkey" '
    .[] | select((if (.pub_key|type)=="object" then .pub_key.value else .pub_key end)==$vault) | .membership[]?
  ' | while read -r member; do
    [ -n "$member" ] || continue
    curl -s localhost:1317/thorchain/nodes | jq -r --arg pubkey "$member" '
      .[]
      | select(.pub_key_set.secp256k1==$pubkey)
      | [.pub_key_set.secp256k1, .node_address, (.slash_points // 0), (.jail.release_height // 0)]
      | @tsv
    '
  done
}

restart_bifrosts_clean() {
  local clear_books="${1:-0}"
  export DKLS_ABORT_MODE=
  export DKLS_ABORT_PHASE=
  export DKLS_ABORT_LOCAL_PUBKEY=
  export DKLS_ABORT_AFTER_OUTPUTS=
  export DKLS_ABORT_AFTER_INPUTS=
  clear_dkls_abort_config
  restart_bifrosts_ordered compose "$clear_books"
}

clear_dkls_abort_config() {
  local name container
  for name in $(node_names); do
    container=$(bifrost_container_for_name "$name")
    docker exec "$container" sh -lc 'rm -f /var/data/bifrost/dkls-abort.env' 2>/dev/null || true
  done
}

set_dkls_abort_config() {
  local phase="$1" mode="$2" target_pubkey="$3" after_outputs="${4:-1}" after_inputs="${5-}"
  local name container
  for name in $(node_names); do
    container=$(bifrost_container_for_name "$name")
    docker exec -i "$container" sh -lc "cat >/var/data/bifrost/dkls-abort.env" <<EOF
BIFROST_DKLS_ABORT_MODE=$mode
BIFROST_DKLS_ABORT_PHASE=$phase
BIFROST_DKLS_ABORT_LOCAL_PUBKEY=$target_pubkey
BIFROST_DKLS_ABORT_AFTER_OUTPUTS=$after_outputs
BIFROST_DKLS_ABORT_AFTER_INPUTS=$after_inputs
EOF
  done
}

restart_bifrosts_with_abort() {
  local phase="$1" mode="$2" target_pubkey="$3" after_outputs="${4:-1}" after_inputs="${5-}"
  export DKLS_ABORT_MODE="$mode"
  export DKLS_ABORT_PHASE="$phase"
  export DKLS_ABORT_LOCAL_PUBKEY="$target_pubkey"
  export DKLS_ABORT_AFTER_OUTPUTS="$after_outputs"
  export DKLS_ABORT_AFTER_INPUTS="$after_inputs"
  set_dkls_abort_config "$phase" "$mode" "$target_pubkey" "$after_outputs" "$after_inputs"
  wait_for_bifrost_p2p 60 5
}

wait_for_onchain_keygen_blame() {
  local since="$1" target_pubkey="$2" target_addr="$3" case_name="$4" base_slash="$5" base_release="$6"
  local logs txid node_json slash release jail_reason reason

  for i in $(seq 1 "$BLAME_KEYGEN_CHECKS"); do
    sleep "$BLAME_POLL_SECONDS"
    echo "  [$i/$BLAME_KEYGEN_CHECKS] height=$(chain_height)"
    logs=$(bifrost_logs_since "$since" | grep -Ei 'keygen blame|sent keygen tx|Received a TxHash|failed tx|DKLS.*abort|corrupt|malformed|auth|identifiable|sender-skew|missing participant' | tail -120 || true)
    [ -n "$logs" ] && printf '%s\n' "$logs"
    txid=$(printf '%s\n' "$logs" | sed -n 's/.*txid=\([A-F0-9]\{64\}\).*/\1/p; s/.*TxHash of \([A-F0-9]\{64\}\).*/\1/p' | tail -1)
    reason=$(docker logs --since "$since" docker-thornode-1 2>&1 | grep -E 'tss keygen results blame' | grep "$target_addr" | tail -1 | sed -n 's/.*reason=\("[^"]*"\).*/\1/p' || true)
    node_json=$(curl -s localhost:1317/thorchain/nodes | jq -r --arg addr "$target_addr" '.[] | select(.node_address==$addr) | {status,slash_points,jail}')
    slash=$(printf '%s\n' "$node_json" | jq -r '.slash_points // 0')
    release=$(printf '%s\n' "$node_json" | jq -r '.jail.release_height // 0')
    jail_reason=$(printf '%s\n' "$node_json" | jq -r '.jail.reason // ""')
    echo "  target state: slash=$slash/$base_slash release=$release/$base_release jail=${jail_reason:-none} txid=${txid:-none}"
    if [ "${slash:-0}" -gt "$base_slash" ] || [ "${release:-0}" -gt "$base_release" ]; then
      echo "  on-chain blame landed for $case_name: addr=$target_addr slash=$slash jail_reason=${jail_reason:-none} txid=${txid:-unknown}"
      record_blame_result keygen "$case_name" "$DKLS_ABORT_MODE" "$target_pubkey" "$target_addr" pass "${txid-}" "base_slash=$base_slash slash=$slash base_release=$base_release release=$release jail=$jail_reason reason=${reason:-unknown}"
      return 0
    fi
  done

  record_blame_result keygen "$case_name" "$DKLS_ABORT_MODE" "$target_pubkey" "$target_addr" fail "${txid-}" "base_slash=$base_slash slash=${slash:-unknown} base_release=$base_release release=${release:-unknown} no new on-chain slash or jail observed"
  return 1
}

wait_for_onchain_keysign_blame() {
  local since="$1" target_pubkey="$2" target_addr="$3" case_name="$4" vault_pubkey="$5" base_slash="$6" base_release="$7"
  local logs txid node_json slash release jail_reason reason

  for i in $(seq 1 "$BLAME_KEYSIGN_CHECKS"); do
    sleep "$BLAME_POLL_SECONDS"
    echo "  [$i/$BLAME_KEYSIGN_CHECKS] height=$(chain_height)"
    logs=$(bifrost_logs_since "$since" | grep -Ei 'keysign failure|post keysign failure|Received a TxHash|failed tx|DKLS.*abort|corrupt|malformed|auth|identifiable|sender-skew|result' | tail -120 || true)
    [ -n "$logs" ] && printf '%s\n' "$logs"
    txid=$(printf '%s\n' "$logs" | sed -n 's/.*tx_id":"\([A-F0-9]\{64\}\)".*/\1/p; s/.*tx_id=\([A-F0-9]\{64\}\).*/\1/p; s/.*TxHash of \([A-F0-9]\{64\}\).*/\1/p' | tail -1)
    reason=$(docker logs --since "$since" docker-thornode-1 2>&1 | grep -E 'tss keysign failure' | grep "$target_pubkey" | tail -1 | sed -n 's/.*reason=\("[^"]*"\).*/\1/p' || true)
    node_json=$(curl -s localhost:1317/thorchain/nodes | jq -r --arg addr "$target_addr" '.[] | select(.node_address==$addr) | {status,slash_points,jail}')
    slash=$(printf '%s\n' "$node_json" | jq -r '.slash_points // 0')
    release=$(printf '%s\n' "$node_json" | jq -r '.jail.release_height // 0')
    jail_reason=$(printf '%s\n' "$node_json" | jq -r '.jail.reason // ""')
    echo "  target state: slash=$slash/$base_slash release=$release/$base_release jail=${jail_reason:-none} txid=${txid:-none}"
    if [ "${slash:-0}" -gt "$base_slash" ] || [ "${release:-0}" -gt "$base_release" ]; then
      echo "  on-chain keysign blame landed for $case_name: addr=$target_addr slash=$slash jail_reason=${jail_reason:-none} txid=${txid:-unknown}"
      record_blame_result keysign "$case_name" "$DKLS_ABORT_MODE" "$target_pubkey" "$target_addr" pass "${txid-}" "vault=$vault_pubkey base_slash=$base_slash slash=$slash base_release=$base_release release=$release jail=$jail_reason reason=${reason:-unknown}"
      return 0
    fi
  done

  record_blame_result keysign "$case_name" "$DKLS_ABORT_MODE" "$target_pubkey" "$target_addr" fail "${txid-}" "vault=$vault_pubkey base_slash=$base_slash slash=${slash:-unknown} base_release=$base_release release=${release:-unknown} no new on-chain slash or jail observed"
  return 1
}

wait_for_onchain_keysign_blame_any() {
  local since="$1" case_name="$2" vault_pubkey="$3" baseline_file="$4"
  local logs txid pubkey addr base_slash base_release node_json slash release jail_reason reason

  for i in $(seq 1 "$BLAME_KEYSIGN_CHECKS"); do
    sleep "$BLAME_POLL_SECONDS"
    echo "  [$i/$BLAME_KEYSIGN_CHECKS] height=$(chain_height)"
    logs=$(bifrost_logs_since "$since" | grep -Ei 'keysign failure|post keysign failure|Received a TxHash|failed tx|DKLS.*abort|identifiable|sender-skew|result' | tail -120 || true)
    [ -n "$logs" ] && printf '%s\n' "$logs"
    txid=$(printf '%s\n' "$logs" | sed -n 's/.*tx_id":"\([A-F0-9]\{64\}\)".*/\1/p; s/.*tx_id=\([A-F0-9]\{64\}\).*/\1/p; s/.*TxHash of \([A-F0-9]\{64\}\).*/\1/p' | tail -1)
    while IFS=$'\t' read -r pubkey addr base_slash base_release; do
      [ -n "$pubkey" ] || continue
      node_json=$(curl -s localhost:1317/thorchain/nodes | jq -r --arg addr "$addr" '.[] | select(.node_address==$addr) | {status,slash_points,jail}')
      slash=$(printf '%s\n' "$node_json" | jq -r '.slash_points // 0')
      release=$(printf '%s\n' "$node_json" | jq -r '.jail.release_height // 0')
      jail_reason=$(printf '%s\n' "$node_json" | jq -r '.jail.reason // ""')
      if [ "${slash:-0}" -gt "$base_slash" ] || [ "${release:-0}" -gt "$base_release" ]; then
        reason=$(docker logs --since "$since" docker-thornode-1 2>&1 | grep -E 'tss keysign failure' | grep "$pubkey" | tail -1 | sed -n 's/.*reason=\("[^"]*"\).*/\1/p' || true)
        echo "  on-chain keysign blame landed for $case_name: addr=$addr slash=$slash jail_reason=${jail_reason:-none} txid=${txid:-unknown}"
        LAST_KEYSIGN_BLAMED_PUBKEY="$pubkey"
        record_blame_result keysign "$case_name" "$DKLS_ABORT_MODE" "$pubkey" "$addr" pass "${txid-}" "vault=$vault_pubkey base_slash=$base_slash slash=$slash base_release=$base_release release=$release jail=$jail_reason reason=${reason:-unknown}"
        return 0
      fi
    done <"$baseline_file"
  done

  record_blame_result keysign "$case_name" "$DKLS_ABORT_MODE" "" "" fail "${txid-}" "vault=$vault_pubkey no member slash/release changed"
  return 1
}

wait_for_target_released() {
  local target_addr="$1"
  local status release current
  for i in $(seq 1 80); do
    current=$(chain_height)
    status=$(curl -s localhost:1317/thorchain/nodes | jq -r --arg addr "$target_addr" '.[] | select(.node_address==$addr) | .status')
    release=$(curl -s localhost:1317/thorchain/nodes | jq -r --arg addr "$target_addr" '.[] | select(.node_address==$addr) | .jail.release_height // 0')
    echo "  release wait [$i/80] addr=$target_addr status=$status height=$current release=$release"
    if [ "$status" = "Ready" ] || [ "$status" = "Standby" ]; then
      if [ "${release:-0}" = "0" ] || [ "$current" -ge "$release" ]; then
        return 0
      fi
    fi
    sleep "$BLAME_POLL_SECONDS"
  done
  return 1
}

bootstrap_straight_to_dkls() {
  echo "[0/8] Preflight"
  preflight

  echo "[1/8] Waiting for thornodes"
  wait_for_health docker-thornode-1 120
  while read -r name; do
    wait_for_health "docker-thornode-${name}-1" 120
  done < <(joiner_names)

  echo "[2/8] One-node bootstrap Mimirs"
  mimir HaltChurning 1
  mimir P2PGateDisabled 1
  mimir NumberOfNewNodesPerChurn 5
  mimir EnableDKLS 1
  mimir ChurnRetryInterval "$BLAME_CHURN_RETRY_INTERVAL"
  mimir JailTimeKeygen "$BLAME_JAIL_BLOCKS"
  mimir JailTimeKeysign "$BLAME_JAIL_BLOCKS"

  echo "[3/8] Waiting for DOGE observations"
  for i in $(seq 1 60); do
    fee_rate=$(curl -s localhost:1317/thorchain/inbound_addresses 2>/dev/null | jq -r '.[] | select(.chain=="DOGE") | .observed_fee_rate' 2>/dev/null)
    inbound=$(curl -s localhost:1317/thorchain/inbound_addresses 2>/dev/null | jq -r '.[] | select(.chain=="DOGE") | .address' 2>/dev/null)
    last_observed=$(curl -s localhost:1317/thorchain/lastblock 2>/dev/null | jq -r '.[] | select(.chain=="DOGE") | .last_observed_in' 2>/dev/null)
    if [ -n "$fee_rate" ] && [ "$fee_rate" != "0" ] && [ "$fee_rate" != "null" ] && [ -n "$last_observed" ] && [ "$last_observed" != "0" ] && [ "$last_observed" != "null" ]; then
      echo "  DOGE fee=$fee_rate chain_tip=$last_observed OK"
      break
    fi
    if [ $((i % 6)) -eq 0 ]; then
      doge_tip=$(doge_rpc getblockchaininfo | jq -r '.blocks // 0' 2>/dev/null || echo 0)
      echo "  DOGE wait [$i/60] inbound=${inbound:-none} fee=${fee_rate:-none} observed=${last_observed:-none} doge_tip=$doge_tip"
      if [ -n "$inbound" ] && [ "$inbound" != "null" ] && [ "${doge_tip:-0}" -gt 0 ]; then
        echo "  DOGE inbound and scanner are available; continuing before fee observation"
        break
      fi
    fi
    sleep 10
  done

  echo "[4/8] Bonding joiners and dog"
  get_node_addr() {
    local container="$1"
    docker exec "$container" bash -c "printf '${PASSWORD}\n' | thornode keys show thorchain --keyring-backend $KEYRING -a"
  }
  for name in $(joiner_names); do
    addr=$(get_node_addr "docker-thornode-${name}-1")
    echo "  bonding 70T RUNE to $name $addr"
    tx docker-thornode-1 "deposit 70000000000000 THOR.RUNE BOND:$addr --node $DOG_RPC --gas auto" | grep -E "code:|txhash:" || true
    sleep 7
  done
  tx docker-thornode-1 "deposit 70000000000000 THOR.RUNE BOND:$DOG_NODE_ADDR --node $DOG_RPC --gas auto" | grep -E "code:|txhash:" || true
  sleep 12

  echo "[5/8] Setting node keys/IP/version"
  while read -r name; do
    seed=$(seed_for_name "$name")
    rpc=$(rpc_for_name "$name")
    set_node_keys "docker-thornode-${name}-1" "$seed" "$rpc"
    sleep 7
    set_ip "docker-thornode-${name}-1" "$rpc"
    sleep 7
    set_version "docker-thornode-${name}-1" "$rpc"
    sleep 7
  done < <(joiner_names)

  echo "[6/8] Restarting Bifrosts"
  restart_bifrosts_clean 1

  echo "[7/8] Waiting for ready joiners, then first DKLS churn"
  dkls_vault=""
  if [ "$(active_node_count)" = "6" ] && [ "$(standby_node_count)" = "1" ] && [ "$(ready_node_count)" -ge "1" ]; then
    dkls_vault=$(active_dkls_vault_pubkey)
    if [ -n "$dkls_vault" ]; then
      echo "  existing 6-member DKLS vault found; skipping bootstrap churn"
      print_keyshare_engines "$dkls_vault"
    fi
  fi

  if [ -z "$dkls_vault" ]; then
    wait_for_validator_shape "pre-DKLS first churn" 1 6 6 120
    dkls_start_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    mimir HaltChurning 0

    for i in $(seq 1 "$DKLS_KEYGEN_CHECKS"); do
      sleep "$POLL_SECONDS"
      current_shape=$(active_vault_shape)
      current_vault=$(active_vault_pubkey)
      echo "  [$i/$DKLS_KEYGEN_CHECKS] active_shape=${current_shape:-none} vault=${current_vault:-none}"
      if [ "$current_shape" = "6" ] && [ -n "$current_vault" ]; then
        dkls_vault="$current_vault"
        print_keygen_ceremony_timing "DKLS initial" "dkls" "$dkls_vault" "$dkls_start_time"
        wait_for_checksign_log "DKLS initial" "$dkls_start_time"
        print_keyshare_engines "$dkls_vault"
        break
      fi
    done
  fi
  [ -n "$dkls_vault" ] || {
    dump_chain_state "initial DKLS keygen timeout"
    exit 1
  }

  echo "[8/8] Halting before keygen blame matrix"
  mimir_quorum HALTCHURNING 1
  wait_for_validator_shape "pre-blame DKLS vault" 6 1 1 60
  dump_chain_state "DKLS blame bootstrap ready"
}

run_keygen_blame_case() {
  local case_name="$1" mode="$2" after_outputs="${3:-1}" after_inputs="${4-}"
  local target_pubkey target_addr base_slash base_release start row

  echo ""
  echo "============================================"
  echo "KEYGEN BLAME CASE: $case_name mode=$mode"
  echo "============================================"

  wait_for_validator_shape "before $case_name" 6 1 1 80
  row=$(released_ready_node_row || true)
  if [ -z "$row" ] || [ "$row" = "null" ]; then
    dump_chain_state "missing ready standby for $case_name"
    return 1
  fi
  target_pubkey=$(printf '%s' "$row" | cut -f1)
  target_addr=$(printf '%s' "$row" | cut -f2)
  base_slash=$(printf '%s' "$row" | cut -f3)
  base_release=$(printf '%s' "$row" | cut -f4)
  echo "  target pubkey=$target_pubkey addr=$target_addr base_slash=$base_slash base_release=$base_release"

  mimir_quorum HALTCHURNING 1
  restart_bifrosts_with_abort keygen "$mode" "$target_pubkey" "$after_outputs" "$after_inputs"

  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mimir_quorum HALTCHURNING 0
  wait_for_onchain_keygen_blame "$start" "$target_pubkey" "$target_addr" "$case_name" "$base_slash" "$base_release"
  mimir_quorum HALTCHURNING 1
  restart_bifrosts_clean
  wait_for_target_released "$target_addr"
}

run_keygen_blame_matrix() {
  run_keygen_blame_case default_join join 1 ""
  run_keygen_blame_case default_exit exit 1 ""
  run_keygen_blame_case corrupt corrupt 1 ""
  run_keygen_blame_case malformed malformed 1 ""
  run_keygen_blame_case auth auth 1 ""
  run_keygen_blame_case native_error error 1 ""
}

prepare_keysign_blame() {
  echo ""
  echo "Preparing keysign blame stage"
  mimir_quorum HALTCHURNING 1
  restart_bifrosts_clean
  local old_vault funded_vault signing_vault next_vault inbound start

  old_vault=$(active_dkls_vault_pubkey)
  [ -n "$old_vault" ] || {
    echo "ERROR: no active DKLS vault before successful churn"
    return 1
  }
  echo "  clean churn from DKLS vault=$old_vault"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mimir_quorum HALTCHURNING 0
  funded_vault=""
  for i in $(seq 1 "$SECOND_DKLS_KEYGEN_CHECKS"); do
    sleep "$POLL_SECONDS"
    current_vault=$(active_vault_pubkey)
    current_shape=$(active_vault_shape)
    echo "  clean churn [$i/$SECOND_DKLS_KEYGEN_CHECKS] active_shape=${current_shape:-none} vault=${current_vault:-none}"
    if [ "$current_shape" = "6" ] && [ -n "$current_vault" ] && [ "$current_vault" != "$old_vault" ]; then
      funded_vault="$current_vault"
      print_keygen_ceremony_timing "DKLS clean" "dkls" "$funded_vault" "$start"
      wait_for_checksign_log "DKLS clean" "$start"
      break
    fi
  done
  [ -n "$funded_vault" ] || {
    dump_chain_state "clean DKLS churn timeout"
    return 1
  }

  mimir_quorum HALTCHURNING 1
  wait_for_retiring_vault_empty "pre-blame DKLS" "$old_vault"

  inbound=$(doge_inbound_address)
  if [ -z "$inbound" ] || [ "$inbound" = "null" ]; then
    echo "ERROR: no DOGE inbound address"
    return 1
  fi
  echo "  funding active DKLS vault=$funded_vault"
  add_doge_lp_funds "$inbound"

  signing_vault="$funded_vault"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mimir_quorum HALTCHURNING 0
  next_vault=""
  for i in $(seq 1 "$SECOND_DKLS_KEYGEN_CHECKS"); do
    sleep "$POLL_SECONDS"
    current_vault=$(active_vault_pubkey)
    current_shape=$(active_vault_shape)
    echo "  keysign-stage churn [$i/$SECOND_DKLS_KEYGEN_CHECKS] active_shape=${current_shape:-none} vault=${current_vault:-none}"
    if [ "$current_shape" = "6" ] && [ -n "$current_vault" ] && [ "$current_vault" != "$signing_vault" ]; then
      next_vault="$current_vault"
      print_keygen_ceremony_timing "DKLS keysign-stage" "dkls" "$next_vault" "$start"
      wait_for_checksign_log "DKLS keysign-stage" "$start"
      break
    fi
  done
  [ -n "$next_vault" ] || {
    dump_chain_state "keysign-stage DKLS churn timeout"
    return 1
  }

  mimir_quorum HALTCHURNING 1
  DKLS_KEYSIGN_BLAME_VAULT="$signing_vault"
  echo "  retiring funded DKLS vault for keysign blame=$DKLS_KEYSIGN_BLAME_VAULT"
  dump_chain_state "ready for DKLS keysign blame"
}

prepare_keysign_blame_from_funded_active() {
  echo ""
  echo "Preparing keysign blame stage from funded active vault"
  local signing_vault next_vault start current_vault current_shape

  signing_vault=$(active_dkls_vault_pubkey)
  [ -n "$signing_vault" ] || {
    echo "ERROR: no active funded DKLS vault"
    return 1
  }
  echo "  signing vault=$signing_vault"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mimir_quorum HALTCHURNING 0
  next_vault=""
  for i in $(seq 1 "$SECOND_DKLS_KEYGEN_CHECKS"); do
    sleep "$POLL_SECONDS"
    current_vault=$(active_vault_pubkey)
    current_shape=$(active_vault_shape)
    echo "  keysign-stage churn [$i/$SECOND_DKLS_KEYGEN_CHECKS] active_shape=${current_shape:-none} vault=${current_vault:-none}"
    if [ "$current_shape" = "6" ] && [ -n "$current_vault" ] && [ "$current_vault" != "$signing_vault" ]; then
      next_vault="$current_vault"
      print_keygen_ceremony_timing "DKLS keysign-stage" "dkls" "$next_vault" "$start"
      wait_for_checksign_log "DKLS keysign-stage" "$start"
      break
    fi
  done
  [ -n "$next_vault" ] || {
    dump_chain_state "keysign-stage DKLS churn timeout"
    return 1
  }

  mimir_quorum HALTCHURNING 1
  DKLS_KEYSIGN_BLAME_VAULT="$signing_vault"
  echo "  retiring funded DKLS vault for keysign blame=$DKLS_KEYSIGN_BLAME_VAULT"
  dump_chain_state "ready for DKLS keysign blame"
}

run_keysign_blame_case() {
  local case_name="$1" mode="$2" vault_pubkey="$3" used_pubkeys="$4" after_outputs="${5:-1}" after_inputs="${6-}"
  local row target_pubkey target_addr base_slash base_release start

  echo ""
  echo "============================================"
  echo "KEYSIGN BLAME CASE: $case_name mode=$mode"
  echo "============================================"

  if [ "$case_name" = "default_exit" ]; then
    local baseline_file
    baseline_file=$(mktemp)
    vault_member_baseline "$vault_pubkey" >"$baseline_file"
    echo "  target=default leader vault=$vault_pubkey"
    restart_bifrosts_with_abort keysign "$mode" "" "$after_outputs" "$after_inputs"
    start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    wait_for_onchain_keysign_blame_any "$start" "$case_name" "$vault_pubkey" "$baseline_file"
    rm -f "$baseline_file"
    return 0
  fi

  row=$(vault_member_row "$vault_pubkey" "$used_pubkeys" || true)
  if { [ -z "$row" ] || [ "$row" = "null" ]; } && [ "${DKLS_KEYSIGN_ALLOW_TARGET_REUSE:-1}" = "1" ]; then
    echo "  no unused released signer remains; reusing a released vault signer"
    row=$(vault_member_row "$vault_pubkey" "${DKLS_KEYSIGN_HARD_EXCLUDED_PUBKEYS-}" || true)
  fi
  if [ -z "$row" ] || [ "$row" = "null" ]; then
    dump_chain_state "missing released vault member for $case_name"
    return 1
  fi
  target_pubkey=$(printf '%s' "$row" | cut -f1)
  target_addr=$(printf '%s' "$row" | cut -f2)
  base_slash=$(printf '%s' "$row" | cut -f3)
  base_release=$(printf '%s' "$row" | cut -f4)
  echo "  target pubkey=$target_pubkey addr=$target_addr base_slash=$base_slash base_release=$base_release vault=$vault_pubkey"
  LAST_KEYSIGN_BLAMED_PUBKEY="$target_pubkey"

  restart_bifrosts_with_abort keysign "$mode" "$target_pubkey" "$after_outputs" "$after_inputs"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  wait_for_onchain_keysign_blame "$start" "$target_pubkey" "$target_addr" "$case_name" "$vault_pubkey" "$base_slash" "$base_release"
}

run_keysign_blame_matrix() {
  local vault_pubkey="$1"
  local used="${DKLS_KEYSIGN_USED_PUBKEYS-}"
  local start_case="${DKLS_KEYSIGN_START_CASE:-default_exit}"
  local started=0
  local dog_pubkey

  dog_pubkey=$(curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r --arg addr "$DOG_NODE_ADDR" '.[] | select(.node_address==$addr) | .pub_key_set.secp256k1 // ""' 2>/dev/null || true)
  if [ -n "$dog_pubkey" ] && [ "$dog_pubkey" != "null" ]; then
    used="${used:+$used,}$dog_pubkey"
    export DKLS_KEYSIGN_HARD_EXCLUDED_PUBKEYS="${DKLS_KEYSIGN_HARD_EXCLUDED_PUBKEYS:+$DKLS_KEYSIGN_HARD_EXCLUDED_PUBKEYS,}$dog_pubkey"
  fi

  if [ "$start_case" = "default_exit" ] && [ "${DKLS_KEYSIGN_SKIP_DEFAULT_EXIT:-0}" != "1" ]; then
    started=1
    run_keysign_blame_case default_exit exit "$vault_pubkey" "$used" 1 ""
    used="${used:+$used,}$LAST_KEYSIGN_BLAMED_PUBKEY"
  fi
  if [ "$start_case" = "corrupt" ] || [ "$started" = "1" ]; then
    started=1
    run_keysign_blame_case corrupt corrupt "$vault_pubkey" "$used" 1 ""
    used="${used:+$used,}$LAST_KEYSIGN_BLAMED_PUBKEY"
  fi
  if [ "$start_case" = "malformed" ] || [ "$started" = "1" ]; then
    started=1
    run_keysign_blame_case malformed malformed "$vault_pubkey" "$used" 1 ""
    used="${used:+$used,}$LAST_KEYSIGN_BLAMED_PUBKEY"
  fi
  if [ "$start_case" = "auth" ] || [ "$started" = "1" ]; then
    started=1
    run_keysign_blame_case auth auth "$vault_pubkey" "$used" 1 ""
    used="${used:+$used,}$LAST_KEYSIGN_BLAMED_PUBKEY"
  fi
  if [ "$start_case" = "result" ] || [ "$started" = "1" ]; then
    started=1
    run_keysign_blame_case result result "$vault_pubkey" "$used" 1 ""
    used="${used:+$used,}$LAST_KEYSIGN_BLAMED_PUBKEY"
  fi
  if [ "$start_case" = "native_error" ] || [ "$started" = "1" ]; then
    if ! asgard_vault_exists "$vault_pubkey"; then
      echo "  keysign blame vault $vault_pubkey no longer exists; preparing a fresh funded retiring vault for native_error"
      prepare_keysign_blame_from_funded_active
      vault_pubkey="$DKLS_KEYSIGN_BLAME_VAULT"
      used="${DKLS_KEYSIGN_USED_PUBKEYS-}"
      if [ -n "$dog_pubkey" ] && [ "$dog_pubkey" != "null" ]; then
        used="${used:+$used,}$dog_pubkey"
      fi
    fi
    run_keysign_blame_case native_error error "$vault_pubkey" "$used" 1 ""
  fi

  restart_bifrosts_clean
  wait_for_retiring_vault_empty "DKLS keysign-blame" "$vault_pubkey"
}

echo "============================================"
echo "DKLS/DOGE Chainnet Blame Run"
echo "============================================"
echo "This script bootstraps directly to DKLS, validates keygen blame cases, churns/funds/churns, then validates keysign blame cases."
echo "Results: $BLAME_RESULTS_JSONL"
echo "Stage: $BLAME_STAGE"

if [ "$BLAME_STAGE" = "keysign-existing" ]; then
  [ -n "${DKLS_KEYSIGN_BLAME_VAULT-}" ] || {
    echo "ERROR: DKLS_KEYSIGN_BLAME_VAULT is required for BLAME_STAGE=keysign-existing"
    exit 1
  }
  run_keysign_blame_matrix "$DKLS_KEYSIGN_BLAME_VAULT"
  echo ""
  echo "============================================"
  echo "DKLS KEYSIGN BLAME MATRIX COMPLETE"
  echo "============================================"
  echo "Results: $BLAME_RESULTS_JSONL"
  exit 0
fi

if [ "$BLAME_STAGE" = "keysign-prepare" ]; then
  prepare_keysign_blame
  [ -n "${DKLS_KEYSIGN_BLAME_VAULT-}" ] || {
    echo "ERROR: DKLS_KEYSIGN_BLAME_VAULT was not set after keysign preparation"
    exit 1
  }
  run_keysign_blame_matrix "$DKLS_KEYSIGN_BLAME_VAULT"
  echo ""
  echo "============================================"
  echo "DKLS KEYSIGN BLAME MATRIX COMPLETE"
  echo "============================================"
  echo "Results: $BLAME_RESULTS_JSONL"
  exit 0
fi

if [ "$BLAME_STAGE" = "keysign-funded-active" ]; then
  prepare_keysign_blame_from_funded_active
  [ -n "${DKLS_KEYSIGN_BLAME_VAULT-}" ] || {
    echo "ERROR: DKLS_KEYSIGN_BLAME_VAULT was not set after keysign preparation"
    exit 1
  }
  run_keysign_blame_matrix "$DKLS_KEYSIGN_BLAME_VAULT"
  echo ""
  echo "============================================"
  echo "DKLS KEYSIGN BLAME MATRIX COMPLETE"
  echo "============================================"
  echo "Results: $BLAME_RESULTS_JSONL"
  exit 0
fi

bootstrap_straight_to_dkls
run_keygen_blame_matrix
if [ "$BLAME_STAGE" = "keygen" ]; then
  echo ""
  echo "============================================"
  echo "DKLS KEYGEN BLAME MATRIX COMPLETE"
  echo "============================================"
  echo "Results: $BLAME_RESULTS_JSONL"
  exit 0
fi
prepare_keysign_blame
[ -n "${DKLS_KEYSIGN_BLAME_VAULT-}" ] || {
  echo "ERROR: DKLS_KEYSIGN_BLAME_VAULT was not set after keysign preparation"
  exit 1
}
run_keysign_blame_matrix "$DKLS_KEYSIGN_BLAME_VAULT"

echo ""
echo "============================================"
echo "DKLS KEYGEN + KEYSIGN BLAME MATRICES COMPLETE"
echo "============================================"
echo "Results: $BLAME_RESULTS_JSONL"
