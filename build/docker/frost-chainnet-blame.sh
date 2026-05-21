#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FROST_CHAINNET_LIB_ONLY=1 source "$SCRIPT_DIR/frost-chainnet-singlehost-lib.sh"

usage() {
  cat <<'EOF'
Usage:
  build/docker/frost-chainnet-blame.sh

Single-host FROST blame evaluation. Runs FROST keygen and keysign blame cases
against the local docker-compose chainnet.

Useful env:
  BLAME_STAGE=all|keygen|keygen-existing|keysign-prepare|keysign-funded-active|keysign-existing
  FROST_KEYSIGN_BLAME_VAULT=<vault pubkey>  # required for keysign-existing
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

BLAME_RESULTS_DIR="${BLAME_RESULTS_DIR:-$SCRIPT_DIR/frost-chainnet-results}"
BLAME_RESULTS_JSONL="${BLAME_RESULTS_JSONL:-$BLAME_RESULTS_DIR/frost-blame.jsonl}"
BLAME_CHURN_RETRY_INTERVAL="${BLAME_CHURN_RETRY_INTERVAL:-80}"
BLAME_JAIL_BLOCKS="${BLAME_JAIL_BLOCKS:-120}"
BLAME_POLL_SECONDS="${BLAME_POLL_SECONDS:-10}"
BLAME_KEYGEN_CHECKS="${BLAME_KEYGEN_CHECKS:-90}"
BLAME_KEYSIGN_CHECKS="${BLAME_KEYSIGN_CHECKS:-90}"
BLAME_STAGE="${BLAME_STAGE:-all}"
BLAME_MIN_VAULT_MEMBERS="${BLAME_MIN_VAULT_MEMBERS:-1}"
BLAME_SETTLE_BLOCKS="${BLAME_SETTLE_BLOCKS:-3}"
BLAME_KEYSIGN_MAX_ATTEMPTS="${BLAME_KEYSIGN_MAX_ATTEMPTS:-8}"
BLAME_RUNE_SWAP_AMOUNT="${BLAME_RUNE_SWAP_AMOUNT:-1000000000000}"

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
    ] | .[]?
  ' 2>/dev/null
}

random_released_ready_node_row() {
  released_ready_node_row | sort -R | head -1
}

random_vault_member_row() {
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
  done | sort -R | head -1
}

active_frost_vault_pubkey() {
  local pubkey
  pubkey=$(active_vault_pubkey)
  [ -n "$pubkey" ] || return 1
  if curl -s localhost:1317/thorchain/vaults/asgard | jq -e --arg pubkey "$pubkey" --argjson min "$BLAME_MIN_VAULT_MEMBERS" '
    .[] | select(.status=="ActiveVault")
    | select((if (.pub_key|type)=="object" then .pub_key.value else .pub_key end)==$pubkey)
    | select((.membership|length) >= $min)
  ' >/dev/null; then
    echo "$pubkey"
  fi
}

wait_for_blame_vault_ready() {
  local label="$1" checks="${2:-80}" vault size
  for i in $(seq 1 "$checks"); do
    vault=$(active_frost_vault_pubkey || true)
    if [ -n "$vault" ]; then
      size=$(curl -s localhost:1317/thorchain/vaults/asgard | jq -r --arg vault "$vault" '
        [.[] | select((if (.pub_key|type)=="object" then .pub_key.value else .pub_key end)==$vault) | (.membership|length)] | .[0] // 0
      ')
      echo "  $label vault=$vault members=$size"
      return 0
    fi
    sleep "$BLAME_POLL_SECONDS"
    echo "  [$i/$checks] waiting for $label active_shape=$(active_vault_shape)"
  done
  dump_chain_state "$label vault not ready"
  return 1
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
  export FROST_ABORT_MODE=
  export FROST_ABORT_PHASE=
  export FROST_ABORT_LOCAL_PUBKEY=
  export FROST_ABORT_AFTER_OUTPUTS=
  export FROST_ABORT_AFTER_INPUTS=
  clear_frost_abort_config
  restart_bifrosts_ordered compose "$clear_books"
}

clear_frost_abort_config() {
  local name container
  for name in $(node_names); do
    container=$(bifrost_container_for_name "$name")
    docker exec "$container" sh -lc 'rm -f /var/data/bifrost/frost-abort.env' 2>/dev/null || true
  done
}

wait_blocks() {
  local blocks="$1"
  local label="${2:-settle}"
  local start target current
  start=$(chain_height)
  target=$((start + blocks))
  echo "  waiting $blocks blocks for $label: start=$start target=$target"
  while :; do
    current=$(chain_height)
    [ "${current:-0}" -ge "$target" ] && return 0
    sleep "$FAST_POLL_SECONDS"
  done
}

settle_blame_case() {
  local label="$1"
  mimir_quorum HALTCHURNING 1
  clear_frost_abort_config
  restart_bifrosts_clean
  wait_blocks "$BLAME_SETTLE_BLOCKS" "$label"
}

set_frost_abort_config() {
  local phase="$1" mode="$2" target_pubkey="$3" after_outputs="${4:-1}" after_inputs="${5-}"
  local name container
  for name in $(node_names); do
    container=$(bifrost_container_for_name "$name")
    docker exec -i "$container" sh -lc "cat >/var/data/bifrost/frost-abort.env" 2>/dev/null <<EOF || true
BIFROST_FROST_ABORT_MODE=$mode
BIFROST_FROST_ABORT_PHASE=$phase
BIFROST_FROST_ABORT_LOCAL_PUBKEY=$target_pubkey
BIFROST_FROST_ABORT_AFTER_OUTPUTS=$after_outputs
BIFROST_FROST_ABORT_AFTER_INPUTS=$after_inputs
EOF
  done
}

active_bifrost_names() {
  for name in $(node_names); do
    local addr status
    addr=$(node_address_for_name "$name" 2>/dev/null || true)
    [ -n "$addr" ] || continue
    status=$(curl -s localhost:1317/thorchain/nodes | jq -r --arg addr "$addr" '.[] | select(.node_address==$addr) | .status // ""')
    if [ "$status" = "Active" ]; then
      echo "$name"
    fi
  done
}

wait_for_active_bifrost_p2p() {
  local max_checks="${1:-60}" min_peers="${2:-5}"
  local name container peer_count ok names
  names=$(active_bifrost_names | tr '\n' ' ')
  echo "  waiting for active bifrost P2P peers >= $min_peers: ${names:-unknown}"
  for i in $(seq 1 "$max_checks"); do
    ok=1
    for name in $names; do
      container=$(bifrost_container_for_name "$name")
      peer_count=$(bifrost_peer_count "$container")
      printf "    [%s/%s] %s peers=%s\n" "$i" "$max_checks" "$name" "${peer_count:-0}"
      [ "${peer_count:-0}" -ge "$min_peers" ] || ok=0
    done
    [ "$ok" -eq 1 ] && return 0
    sleep 5
  done
  return 1
}

restart_bifrosts_with_abort() {
  local phase="$1" mode="$2" target_pubkey="$3" after_outputs="${4:-1}" after_inputs="${5-}"
  export FROST_ABORT_MODE="$mode"
  export FROST_ABORT_PHASE="$phase"
  export FROST_ABORT_LOCAL_PUBKEY="$target_pubkey"
  export FROST_ABORT_AFTER_OUTPUTS="$after_outputs"
  export FROST_ABORT_AFTER_INPUTS="$after_inputs"
  set_frost_abort_config "$phase" "$mode" "$target_pubkey" "$after_outputs" "$after_inputs"
  wait_for_active_bifrost_p2p 60 5
}

wait_for_onchain_keygen_blame() {
  local since="$1" target_pubkey="$2" target_addr="$3" case_name="$4" base_slash="$5" base_release="$6"
  local logs txid node_json slash release jail_reason reason

  for i in $(seq 1 "$BLAME_KEYGEN_CHECKS"); do
    sleep "$BLAME_POLL_SECONDS"
    echo "  [$i/$BLAME_KEYGEN_CHECKS] height=$(chain_height)"
    logs=$(bifrost_logs_since "$since" | grep -Ei 'keygen blame|sent keygen tx|Received a TxHash|failed tx|FROST.*abort|corrupt|malformed|auth|identifiable|sender-skew|missing participant' | tail -120 || true)
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
      record_blame_result keygen "$case_name" "$FROST_ABORT_MODE" "$target_pubkey" "$target_addr" pass "${txid-}" "base_slash=$base_slash slash=$slash base_release=$base_release release=$release jail=$jail_reason reason=${reason:-unknown}"
      return 0
    fi
  done

  record_blame_result keygen "$case_name" "$FROST_ABORT_MODE" "$target_pubkey" "$target_addr" fail "${txid-}" "base_slash=$base_slash slash=${slash:-unknown} base_release=$base_release release=${release:-unknown} no new on-chain slash or jail observed"
  return 1
}

wait_for_onchain_keygen_blame_for_ready_target() {
  local since="$1" target_pubkey="$2" target_addr="$3" case_name="$4" base_slash="$5" base_release="$6"
  wait_for_onchain_keygen_blame "$since" "$target_pubkey" "$target_addr" "$case_name" "$base_slash" "$base_release"
}

wait_for_onchain_keysign_blame() {
  local since="$1" target_pubkey="$2" target_addr="$3" case_name="$4" vault_pubkey="$5" base_slash="$6" base_release="$7"
  local logs txid node_json slash release jail_reason reason

  for i in $(seq 1 "$BLAME_KEYSIGN_CHECKS"); do
    sleep "$BLAME_POLL_SECONDS"
    echo "  [$i/$BLAME_KEYSIGN_CHECKS] height=$(chain_height)"
    logs=$(bifrost_logs_since "$since" | grep -Ei 'keysign failure|post keysign failure|Received a TxHash|failed tx|FROST.*abort|corrupt|malformed|auth|identifiable|sender-skew|result' | tail -120 || true)
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
      record_blame_result keysign "$case_name" "$FROST_ABORT_MODE" "$target_pubkey" "$target_addr" pass "${txid-}" "vault=$vault_pubkey base_slash=$base_slash slash=$slash base_release=$base_release release=$release jail=$jail_reason reason=${reason:-unknown}"
      return 0
    fi
  done

  record_blame_result keysign "$case_name" "$FROST_ABORT_MODE" "$target_pubkey" "$target_addr" fail "${txid-}" "vault=$vault_pubkey base_slash=$base_slash slash=${slash:-unknown} base_release=$base_release release=${release:-unknown} no new on-chain slash or jail observed"
  return 1
}

wait_for_onchain_keysign_blame_any() {
  local since="$1" case_name="$2" vault_pubkey="$3" baseline_file="$4"
  local logs txid pubkey addr base_slash base_release node_json slash release jail_reason reason

  for i in $(seq 1 "$BLAME_KEYSIGN_CHECKS"); do
    sleep "$BLAME_POLL_SECONDS"
    echo "  [$i/$BLAME_KEYSIGN_CHECKS] height=$(chain_height)"
    logs=$(bifrost_logs_since "$since" | grep -Ei 'keysign failure|post keysign failure|Received a TxHash|failed tx|FROST.*abort|identifiable|sender-skew|result' | tail -120 || true)
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
        record_blame_result keysign "$case_name" "$FROST_ABORT_MODE" "$pubkey" "$addr" pass "${txid-}" "vault=$vault_pubkey base_slash=$base_slash slash=$slash base_release=$base_release release=$release jail=$jail_reason reason=${reason:-unknown}"
        return 0
      fi
    done <"$baseline_file"
  done

  record_blame_result keysign "$case_name" "$FROST_ABORT_MODE" "" "" fail "${txid-}" "vault=$vault_pubkey no member slash/release changed"
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
    if [ "${release:-0}" = "0" ] || [ "$current" -ge "$release" ]; then
      return 0
    fi
    sleep "$BLAME_POLL_SECONDS"
  done
  return 1
}

ensure_btc_network_fee_ready() {
  local fee_rate inbound last_observed btc_tip txid self_addr
  echo "  waiting for BTC network fee/inbound readiness"
  for i in $(seq 1 60); do
    fee_rate=$(curl -s localhost:1317/thorchain/inbound_addresses 2>/dev/null | jq -r '.[] | select(.chain=="BTC") | .observed_fee_rate' 2>/dev/null)
    inbound=$(btc_inbound_address)
    last_observed=$(curl -s localhost:1317/thorchain/lastblock 2>/dev/null | jq -r '.[] | select(.chain=="BTC") | .last_observed_in' 2>/dev/null)
    if [ -n "$inbound" ] && [ "$inbound" != "null" ] && [ -n "$fee_rate" ] && [ "$fee_rate" != "0" ] && [ "$fee_rate" != "null" ] && [ -n "$last_observed" ] && [ "$last_observed" != "0" ] && [ "$last_observed" != "null" ]; then
      echo "  BTC fee=$fee_rate observed=$last_observed inbound=$inbound OK"
      return 0
    fi
    if [ $((i % 6)) -eq 0 ]; then
      btc_tip=$(btc_rpc getblockchaininfo | jq -r '.blocks // 0' 2>/dev/null || echo 0)
      echo "  BTC fee wait [$i/60] inbound=${inbound:-none} fee=${fee_rate:-none} observed=${last_observed:-none} btc_tip=$btc_tip"
      ensure_btc_wallet_funded
      self_addr=$(btc_rpc getnewaddress)
      btc_rpc settxfee "[0.001]" >/dev/null || true
      txid=$(btc_rpc sendtoaddress "[\"$self_addr\",0.001]")
      echo "  BTC self-fee tx=$txid"
      btc_rpc generatetoaddress "[2,\"$BTC_MINING_ADDRESS\"]" >/dev/null
    fi
    sleep 10
  done
  echo "  WARN: BTC fee was not observed; continuing with current chain state"
}

trigger_rune_to_btc_swap() {
  local btc_addr memo amount txhash
  btc_addr=$(btc_rpc getnewaddress)
  memo="=:BTC.BTC:$btc_addr"
  amount="$BLAME_RUNE_SWAP_AMOUNT"
  echo "  triggering RUNE->BTC swap amount=$amount memo=$memo"
  txhash=$(tx docker-thornode-1 "deposit $amount THOR.RUNE '$memo' --node $DOG_RPC --gas auto" | tee /dev/stderr | sed -n 's/txhash: //p' | tail -1 || true)
  echo "  RUNE->BTC swap tx=${txhash:-unknown}"
  sleep "$FAST_POLL_SECONDS"
}

bootstrap_straight_to_frost() {
  echo "[0/8] Preflight"
  preflight

  echo "[1/8] Starting cluster and waiting for thornodes"
  compose up -d
  wait_for_health docker-thornode-1 120
  while read -r name; do
    wait_for_health "docker-thornode-${name}-1" 120
  done < <(joiner_names)

  echo "[2/8] One-node bootstrap Mimirs"
  mimir HaltChurning 1
  mimir P2PGateDisabled 1
  mimir NumberOfNewNodesPerChurn 1
  mimir EnableFrostBTC 1
  mimir ChurnInterval "$BLAME_CHURN_RETRY_INTERVAL"
  mimir ChurnRetryInterval "$BLAME_CHURN_RETRY_INTERVAL"
  mimir DesiredValidatorSet 6
  mimir AsgardSize 6
  mimir BadValidatorRedline 1000000
  mimir MaxNodeToChurnOutForLowVersion 0
  mimir ChurnMigrateRounds 1
  mimir SigningTransactionPeriod 30
  mimir MinimumNodesForYggdrasil 1
  mimir JailTimeKeygen "$BLAME_JAIL_BLOCKS"
  mimir JailTimeKeysign "$BLAME_JAIL_BLOCKS"

  echo "[3/8] Waiting for BTC observations and network fee"
  ensure_btc_network_fee_ready

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
  restart_bifrosts_clean 0

  echo "[7/8] Waiting for ready joiners"
  wait_for_validator_shape "pre-blame ready set" 1 6 6 120

  echo "[8/8] Halting before keygen blame matrix"
  mimir_quorum HALTCHURNING 1
  dump_chain_state "FROST blame bootstrap ready"
}

run_keygen_blame_case() {
  local case_name="$1" mode="$2" after_outputs="${3:-1}" after_inputs="${4-}"
  local target_pubkey target_addr base_slash base_release start row

  echo ""
  echo "============================================"
  echo "KEYGEN BLAME CASE: $case_name mode=$mode"
  echo "============================================"

  mimir_quorum HALTCHURNING 1
  clear_frost_abort_config
  local current_vault
  current_vault=$(active_vault_pubkey)
  row=$(random_vault_member_row "$current_vault" || true)
  if [ -z "$row" ] || [ "$row" = "null" ]; then
    dump_chain_state "missing active vault member for $case_name"
    return 1
  fi
  target_pubkey=$(printf '%s' "$row" | cut -f1)
  target_addr=$(printf '%s' "$row" | cut -f2)
  base_slash=$(printf '%s' "$row" | cut -f3)
  base_release=$(printf '%s' "$row" | cut -f4)
  echo "  target active vault member pubkey=$target_pubkey addr=$target_addr base_slash=$base_slash base_release=$base_release"

  restart_bifrosts_with_abort keygen "$mode" "$target_pubkey" "$after_outputs" "$after_inputs"

  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mimir_quorum HALTCHURNING 0
  wait_for_onchain_keygen_blame_for_ready_target "$start" "$target_pubkey" "$target_addr" "$case_name" "$base_slash" "$base_release"
  settle_blame_case "after keygen $case_name"
  wait_for_target_released "$target_addr"
}

run_keygen_blame_matrix() {
  local start_case="${BLAME_KEYGEN_START_CASE:-default_join}" started=0
  if [ "$start_case" = "default_join" ] || [ "$started" = "1" ]; then
    started=1
    run_keygen_blame_case default_join join 1 ""
  fi
  if [ "$start_case" = "default_exit" ] || [ "$started" = "1" ]; then
    started=1
    run_keygen_blame_case default_exit exit 1 ""
  fi
  if [ "$start_case" = "corrupt" ] || [ "$started" = "1" ]; then
    started=1
    run_keygen_blame_case corrupt corrupt 1 ""
  fi
  if [ "$start_case" = "malformed" ] || [ "$started" = "1" ]; then
    started=1
    run_keygen_blame_case malformed malformed 1 ""
  fi
  if [ "$start_case" = "auth" ] || [ "$started" = "1" ]; then
    started=1
    run_keygen_blame_case auth auth 1 ""
  fi
  if [ "$start_case" = "native_error" ] || [ "$started" = "1" ]; then
    run_keygen_blame_case native_error error 1 ""
  fi
}

prepare_keysign_blame() {
  echo ""
  echo "Preparing keysign blame stage"
  mimir_quorum HALTCHURNING 1
  restart_bifrosts_clean
  local old_vault funded_vault signing_vault next_vault inbound start

  old_vault=$(active_frost_vault_pubkey)
  [ -n "$old_vault" ] || {
    echo "ERROR: no active FROST vault before successful churn"
    return 1
  }
  echo "  clean churn from FROST vault=$old_vault"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mimir_quorum HALTCHURNING 0
  funded_vault=""
  for i in $(seq 1 "$SECOND_FROST_KEYGEN_CHECKS"); do
    sleep "$POLL_SECONDS"
    current_vault=$(active_vault_pubkey)
    current_shape=$(active_vault_shape)
    echo "  clean churn [$i/$SECOND_FROST_KEYGEN_CHECKS] active_shape=${current_shape:-none} vault=${current_vault:-none}"
    if [ "$current_shape" = "6" ] && [ -n "$current_vault" ] && [ "$current_vault" != "$old_vault" ]; then
      funded_vault="$current_vault"
      print_keygen_ceremony_timing "FROST clean" "frost" "$funded_vault" "$start"
      wait_for_checksign_log "FROST clean" "$start"
      break
    fi
  done
  [ -n "$funded_vault" ] || {
    dump_chain_state "clean FROST churn timeout"
    return 1
  }

  mimir_quorum HALTCHURNING 1
  wait_for_retiring_vault_empty "pre-blame FROST" "$old_vault"

  inbound=$(btc_inbound_address)
  if [ -z "$inbound" ] || [ "$inbound" = "null" ]; then
    echo "ERROR: no BTC inbound address"
    return 1
  fi
  echo "  funding active FROST vault=$funded_vault"
  add_btc_lp_funds "$inbound"

  signing_vault="$funded_vault"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mimir_quorum HALTCHURNING 0
  next_vault=""
  for i in $(seq 1 "$SECOND_FROST_KEYGEN_CHECKS"); do
    sleep "$POLL_SECONDS"
    current_vault=$(active_vault_pubkey)
    current_shape=$(active_vault_shape)
    echo "  keysign-stage churn [$i/$SECOND_FROST_KEYGEN_CHECKS] active_shape=${current_shape:-none} vault=${current_vault:-none}"
    if [ "$current_shape" = "6" ] && [ -n "$current_vault" ] && [ "$current_vault" != "$signing_vault" ]; then
      next_vault="$current_vault"
      print_keygen_ceremony_timing "FROST keysign-stage" "frost" "$next_vault" "$start"
      wait_for_checksign_log "FROST keysign-stage" "$start"
      break
    fi
  done
  [ -n "$next_vault" ] || {
    dump_chain_state "keysign-stage FROST churn timeout"
    return 1
  }

  mimir_quorum HALTCHURNING 1
  FROST_KEYSIGN_BLAME_VAULT="$signing_vault"
  echo "  retiring funded FROST vault for keysign blame=$FROST_KEYSIGN_BLAME_VAULT"
  dump_chain_state "ready for FROST keysign blame"
}

create_clean_frost_vault_for_keysign() {
  local old_vault current_vault current_shape start
  old_vault=$(active_vault_pubkey || true)
  echo "Creating clean FROST vault for keysign blame; old_vault=${old_vault:-none}"
  mimir_quorum HALTCHURNING 1
  mimir_quorum NumberOfNewNodesPerChurn 5
  clear_frost_abort_config
  restart_bifrosts_clean
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mimir_quorum HALTCHURNING 0
  for i in $(seq 1 "$SECOND_FROST_KEYGEN_CHECKS"); do
    sleep "$POLL_SECONDS"
    current_vault=$(active_vault_pubkey)
    current_shape=$(active_vault_shape)
    echo "  clean FROST churn [$i/$SECOND_FROST_KEYGEN_CHECKS] active_shape=${current_shape:-none} vault=${current_vault:-none}"
    if [ -n "$current_vault" ] && [ "$current_vault" != "$old_vault" ]; then
      FROST_KEYSIGN_BLAME_VAULT="$current_vault"
      print_keygen_ceremony_timing "FROST clean" "frost" "$current_vault" "$start"
      wait_for_checksign_log "FROST clean" "$start"
      print_keyshare_engines "$current_vault"
      mimir_quorum HALTCHURNING 1
      return 0
    fi
  done
  dump_chain_state "clean FROST vault timeout"
  return 1
}

prepare_keysign_blame_current_flow() {
  local inbound vault
  create_clean_frost_vault_for_keysign
  vault="${FROST_KEYSIGN_BLAME_VAULT-}"
  [ -n "$vault" ] || {
    echo "ERROR: no FROST vault for keysign blame"
    return 1
  }
  mimir_quorum HALTCHURNING 1
  inbound=$(btc_inbound_address)
  if [ -z "$inbound" ] || [ "$inbound" = "null" ]; then
    echo "ERROR: no BTC inbound address"
    return 1
  fi
  echo "  funding active FROST vault=$vault inbound=$inbound"
  add_btc_lp_funds "$inbound"
  wait_blocks "$BLAME_SETTLE_BLOCKS" "after BTC LP funding"
  dump_chain_state "ready for FROST keysign blame"
}

prepare_keysign_blame_from_funded_active() {
  echo ""
  echo "Preparing keysign blame stage from funded active vault"
  local signing_vault next_vault start current_vault current_shape

  signing_vault=$(active_frost_vault_pubkey)
  [ -n "$signing_vault" ] || {
    echo "ERROR: no active funded FROST vault"
    return 1
  }
  echo "  signing vault=$signing_vault"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  mimir_quorum HALTCHURNING 0
  next_vault=""
  for i in $(seq 1 "$SECOND_FROST_KEYGEN_CHECKS"); do
    sleep "$POLL_SECONDS"
    current_vault=$(active_vault_pubkey)
    current_shape=$(active_vault_shape)
    echo "  keysign-stage churn [$i/$SECOND_FROST_KEYGEN_CHECKS] active_shape=${current_shape:-none} vault=${current_vault:-none}"
    if [ "$current_shape" = "6" ] && [ -n "$current_vault" ] && [ "$current_vault" != "$signing_vault" ]; then
      next_vault="$current_vault"
      print_keygen_ceremony_timing "FROST keysign-stage" "frost" "$next_vault" "$start"
      wait_for_checksign_log "FROST keysign-stage" "$start"
      break
    fi
  done
  [ -n "$next_vault" ] || {
    dump_chain_state "keysign-stage FROST churn timeout"
    return 1
  }

  mimir_quorum HALTCHURNING 1
  FROST_KEYSIGN_BLAME_VAULT="$signing_vault"
  echo "  retiring funded FROST vault for keysign blame=$FROST_KEYSIGN_BLAME_VAULT"
  dump_chain_state "ready for FROST keysign blame"
}

run_keysign_blame_case() {
  local case_name="$1" mode="$2" vault_pubkey="$3" used_pubkeys="$4" after_outputs="${5:-1}" after_inputs="${6-}"
  local row target_pubkey target_addr base_slash base_release start attempt baseline_file

  echo ""
  echo "============================================"
  echo "KEYSIGN BLAME CASE: $case_name mode=$mode"
  echo "============================================"

  for attempt in $(seq 1 "$BLAME_KEYSIGN_MAX_ATTEMPTS"); do
    echo "  keysign attempt $attempt/$BLAME_KEYSIGN_MAX_ATTEMPTS"
    mimir_quorum HALTCHURNING 1
    clear_frost_abort_config

    if [ "$case_name" = "default_exit" ]; then
      baseline_file=$(mktemp)
      vault_member_baseline "$vault_pubkey" >"$baseline_file"
      echo "  target=default leader vault=$vault_pubkey"
      restart_bifrosts_with_abort keysign "$mode" "" "$after_outputs" "$after_inputs"
      start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
      trigger_rune_to_btc_swap
      if wait_for_onchain_keysign_blame_any "$start" "$case_name" "$vault_pubkey" "$baseline_file"; then
        rm -f "$baseline_file"
        settle_blame_case "after keysign $case_name"
        return 0
      fi
      rm -f "$baseline_file"
    else
      row=$(random_vault_member_row "$vault_pubkey" "$used_pubkeys" || true)
      if { [ -z "$row" ] || [ "$row" = "null" ]; } && [ "${FROST_KEYSIGN_ALLOW_TARGET_REUSE:-1}" = "1" ]; then
        echo "  no unused released signer remains; reusing a released vault signer"
        row=$(random_vault_member_row "$vault_pubkey" "${FROST_KEYSIGN_HARD_EXCLUDED_PUBKEYS-}" || true)
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
      trigger_rune_to_btc_swap
      if wait_for_onchain_keysign_blame "$start" "$target_pubkey" "$target_addr" "$case_name" "$vault_pubkey" "$base_slash" "$base_release"; then
        settle_blame_case "after keysign $case_name"
        wait_for_target_released "$target_addr"
        return 0
      fi
    fi

    echo "  no keysign blame landed for $case_name attempt=$attempt; clearing hooks and retrying"
    settle_blame_case "after missed keysign $case_name"
  done

  echo "ERROR: keysign blame case $case_name did not land after $BLAME_KEYSIGN_MAX_ATTEMPTS attempts"
  return 1
}

run_keysign_blame_matrix() {
  local vault_pubkey="$1"
  local used="${FROST_KEYSIGN_USED_PUBKEYS-}"
  local start_case="${FROST_KEYSIGN_START_CASE:-default_exit}"
  local started=0
  local dog_pubkey

  dog_pubkey=$(curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r --arg addr "$DOG_NODE_ADDR" '.[] | select(.node_address==$addr) | .pub_key_set.secp256k1 // ""' 2>/dev/null || true)
  if [ -n "$dog_pubkey" ] && [ "$dog_pubkey" != "null" ]; then
    used="${used:+$used,}$dog_pubkey"
    export FROST_KEYSIGN_HARD_EXCLUDED_PUBKEYS="${FROST_KEYSIGN_HARD_EXCLUDED_PUBKEYS:+$FROST_KEYSIGN_HARD_EXCLUDED_PUBKEYS,}$dog_pubkey"
  fi

  if [ "$start_case" = "default_exit" ] && [ "${FROST_KEYSIGN_SKIP_DEFAULT_EXIT:-0}" != "1" ]; then
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
      vault_pubkey="${FROST_KEYSIGN_BLAME_VAULT-}"
      [ -n "$vault_pubkey" ] || {
        echo "ERROR: FROST_KEYSIGN_BLAME_VAULT was not set after keysign preparation"
        return 1
      }
      used="${FROST_KEYSIGN_USED_PUBKEYS-}"
      if [ -n "$dog_pubkey" ] && [ "$dog_pubkey" != "null" ]; then
        used="${used:+$used,}$dog_pubkey"
      fi
    fi
    run_keysign_blame_case native_error error "$vault_pubkey" "$used" 1 ""
  fi

  restart_bifrosts_clean
}

echo "============================================"
echo "FROST/BTC Chainnet Blame Run"
echo "============================================"
echo "This script bootstraps directly to FROST, validates keygen blame cases, churns/funds/churns, then validates keysign blame cases."
echo "Results: $BLAME_RESULTS_JSONL"
echo "Stage: $BLAME_STAGE"

if [ "$BLAME_STAGE" = "keygen-existing" ]; then
  mimir_quorum HALTCHURNING 1
  wait_for_blame_vault_ready "existing pre-blame FROST" 60
  dump_chain_state "existing FROST blame bootstrap ready"
  run_keygen_blame_matrix
  echo ""
  echo "============================================"
  echo "FROST KEYGEN BLAME MATRIX COMPLETE"
  echo "============================================"
  echo "Results: $BLAME_RESULTS_JSONL"
  exit 0
fi

if [ "$BLAME_STAGE" = "keysign-existing" ]; then
  [ -n "${FROST_KEYSIGN_BLAME_VAULT-}" ] || {
    echo "ERROR: FROST_KEYSIGN_BLAME_VAULT is required for BLAME_STAGE=keysign-existing"
    exit 1
  }
  run_keysign_blame_matrix "$FROST_KEYSIGN_BLAME_VAULT"
  echo ""
  echo "============================================"
  echo "FROST KEYSIGN BLAME MATRIX COMPLETE"
  echo "============================================"
  echo "Results: $BLAME_RESULTS_JSONL"
  exit 0
fi

if [ "$BLAME_STAGE" = "keysign-prepare" ]; then
  prepare_keysign_blame_current_flow
  [ -n "${FROST_KEYSIGN_BLAME_VAULT-}" ] || {
    echo "ERROR: FROST_KEYSIGN_BLAME_VAULT was not set after keysign preparation"
    exit 1
  }
  run_keysign_blame_matrix "$FROST_KEYSIGN_BLAME_VAULT"
  echo ""
  echo "============================================"
  echo "FROST KEYSIGN BLAME MATRIX COMPLETE"
  echo "============================================"
  echo "Results: $BLAME_RESULTS_JSONL"
  exit 0
fi

if [ "$BLAME_STAGE" = "keysign-funded-active" ]; then
  prepare_keysign_blame_current_flow
  [ -n "${FROST_KEYSIGN_BLAME_VAULT-}" ] || {
    echo "ERROR: FROST_KEYSIGN_BLAME_VAULT was not set after keysign preparation"
    exit 1
  }
  run_keysign_blame_matrix "$FROST_KEYSIGN_BLAME_VAULT"
  echo ""
  echo "============================================"
  echo "FROST KEYSIGN BLAME MATRIX COMPLETE"
  echo "============================================"
  echo "Results: $BLAME_RESULTS_JSONL"
  exit 0
fi

bootstrap_straight_to_frost
run_keygen_blame_matrix
if [ "$BLAME_STAGE" = "keygen" ]; then
  echo ""
  echo "============================================"
  echo "FROST KEYGEN BLAME MATRIX COMPLETE"
  echo "============================================"
  echo "Results: $BLAME_RESULTS_JSONL"
  exit 0
fi
prepare_keysign_blame_current_flow
[ -n "${FROST_KEYSIGN_BLAME_VAULT-}" ] || {
  echo "ERROR: FROST_KEYSIGN_BLAME_VAULT was not set after keysign preparation"
  exit 1
}
run_keysign_blame_matrix "$FROST_KEYSIGN_BLAME_VAULT"

echo ""
echo "============================================"
echo "FROST KEYGEN + KEYSIGN BLAME MATRICES COMPLETE"
echo "============================================"
echo "Results: $BLAME_RESULTS_JSONL"
