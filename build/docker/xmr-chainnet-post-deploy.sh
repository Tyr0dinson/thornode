#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE="docker-compose.xmr-chainnet.yml"
COMPOSE_FILE_PATH="$SCRIPT_DIR/$COMPOSE_FILE"
COMPOSE_ENV_FILE="$REPO_ROOT/.env"
CHAIN_ID="thorchain"
KEYRING="file"
PASSWORD="password"
XMR_DAEMON_URL="${XMR_DAEMON_URL:-}"
DOG_RPC="tcp://localhost:27147"
CAT_RPC="tcp://thornode-cat:27147"
FOX_RPC="tcp://thornode-fox:27147"
GOAT_RPC="tcp://thornode-goat:27147"
WHALE_RPC="tcp://thornode-whale:27147"
WOLF_RPC="tcp://thornode-wolf:27147"
EAGLE_RPC="tcp://thornode-eagle:27147"

DOG_NODE_ADDR="cthor1zf3gsk7edzwl9syyefvfhle37cjtql35layy8z"
CAT_SEED="cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat cat crawl"
FOX_SEED="fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox fox filter"
GOAT_SEED="goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat goat answer"
WHALE_SEED="whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale whale away"
WOLF_SEED="wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf wolf analyst"
EAGLE_SEED="eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle eagle bitter"

tx() {
  local container="$1"; shift
  docker exec "$container" bash -c "printf '${PASSWORD}\n${PASSWORD}\n' | thornode tx thorchain $* --from thorchain --keyring-backend $KEYRING --chain-id $CHAIN_ID --yes" 2>&1
}

compose() {
  docker compose --env-file "$COMPOSE_ENV_FILE" -f "$COMPOSE_FILE_PATH" "$@"
}

joiner_names() {
  printf '%s\n' cat fox goat whale wolf eagle
}

rpc_for_name() {
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

mimir() {
  local key="$1"; local val="$2"
  echo "  mimir $key = $val"
  tx docker-thornode-1 "mimir $key $val --node $DOG_RPC" | grep -E "code:|txhash:" || true
  sleep 7
}

# Vote for a mimir on all active nodes to reach quorum
mimir_quorum() {
  local key="$1"; local val="$2"
  echo "  mimir(quorum) $key = $val"
  for name in dog $(joiner_names); do
    if [ "$name" = "dog" ]; then
      container="docker-thornode-1"
    else
      container="docker-thornode-${name}-1"
    fi
    rpc=$(rpc_for_name "$name")
    tx "$container" "mimir $key $val --node $rpc" | grep -E "code:|txhash:" || true
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

require_external_full_monerod() {
  if [ -z "$XMR_DAEMON_URL" ]; then
    echo "ERROR: XMR_DAEMON_URL is not set."
    echo "Export the external full Monero RPC URL before running this script."
    exit 1
  fi

  echo "  external full Monero RPC: $XMR_DAEMON_URL"
  if ! curl -sf -H "Content-Type: application/json" \
      -d '{"jsonrpc":"2.0","id":"0","method":"get_block_count"}' \
      "${XMR_DAEMON_URL%/}/json_rpc" >/dev/null; then
    echo "ERROR: cannot reach external full Monero RPC at $XMR_DAEMON_URL"
    exit 1
  fi
}

install_host_reset_tool() {
  echo "  installing xmr-reset-scanner onto host from thornode:xmr-chainnet"
  "$SCRIPT_DIR/xmr-install-host-tools.sh" --image thornode:xmr-chainnet xmr-reset-scanner
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
    printf "'"$PASSWORD"'\n'"$PASSWORD"'\n" | thornode tx thorchain set-node-keys "$NODE_PUB_KEY" "$NODE_PUB_KEY_ED25519" "$VALIDATOR" --from thorchain --keyring-backend '"$KEYRING"' --chain-id '"$CHAIN_ID"' --node '"$rpc"' --yes
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

echo "============================================"
echo "XMR Chainnet Post-Deploy"
echo "============================================"
echo ""
echo "NOTE: This script assumes a fresh deploy. If P2P deadlock recurs"
echo "      (DOG bifrost stuck dialing CAT/FOX 172.x), stop bifrosts and"
echo "      'rm /root/.thornode/address_book.seed' inside each thornode"
echo "      container, then restart bifrosts in DOG -> CAT/FOX order."
echo "      XMR services require XMR_DAEMON_URL to point at the external"
echo "      full Monero RPC before compose or this script is run."

echo ""
echo "[prep] Installing host helper tools..."
install_host_reset_tool

echo ""
echo "[0/8] Verifying external full Monero RPC..."
require_external_full_monerod

echo ""
echo "[1/8] Waiting for thornode health..."
wait_for_health docker-thornode-1 120

echo ""
echo "[2/8] Setting initial mimirs..."
# Most constants (ChurnInterval, JailTime, EmissionCurve, etc.) are baked into
# constants_chainnet.go. Only runtime-toggled values need mimir here.
# P2PGateDisabled must be a mimir (not just a constant) because bifrost reads
# it via GetMimir API, not from /thorchain/constants.
mimir HaltChurning 1
mimir DollarsPerRune 100000000
mimir P2PGateDisabled 1
mimir DesiredValidatorSet 6
mimir AsgardSize 3
mimir NumberOfNewNodesPerChurn 5

echo ""
echo "[3/8] Waiting for dog bifrost to report XMR fee + chain tip (sole active node = 1 attestation quorum)..."
# Fresh XMR bring-up can sit at fee=0 / tip=0 for several minutes even when
# bifrost and the signer are healthy. When dog starts scanning at current tip,
# thornode will usually not publish observed_fee_rate / last_observed_in until
# the first Monero block after bifrost startup lands and is observed.
for i in $(seq 1 60); do
  fee_rate=$(curl -s localhost:1317/thorchain/inbound_addresses 2>/dev/null | jq -r '.[] | select(.chain=="XMR") | .observed_fee_rate' 2>/dev/null)
  last_observed=$(curl -s localhost:1317/thorchain/lastblock 2>/dev/null | jq -r '.[] | select(.chain=="XMR") | .last_observed_in' 2>/dev/null)
  if [ -n "$fee_rate" ] && [ "$fee_rate" != "0" ] && [ "$fee_rate" != "null" ] && [ -n "$last_observed" ] && [ "$last_observed" != "0" ] && [ "$last_observed" != "null" ]; then
    echo "  XMR fee=$fee_rate chain_tip=$last_observed OK (${i}x10s)"
    break
  fi
  echo -n "  [$i] fee=$fee_rate tip=$last_observed"
  echo " waiting..."
  sleep 10
done

echo ""
echo "[4/8] Waiting for joiner thornodes ($(joiner_label))..."
while read -r name; do
  wait_for_health "docker-thornode-${name}-1" 120
done < <(joiner_names)

echo ""
echo "[5/8] Setting node keys, IPs, versions for joiners ($(joiner_label))..."
while read -r name; do
  seed=$(seed_for_name "$name")
  rpc=$(rpc_for_name "$name")
  set_node_keys "docker-thornode-${name}-1" "$seed" "$rpc"
  set_ip "docker-thornode-${name}-1" "$rpc"
  set_version "docker-thornode-${name}-1" "$rpc"
  sleep 7
done < <(joiner_names)

echo ""
echo "[6/8] Topping up bond for dog and joiners ($(joiner_label))..."
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
  sleep 7
done
echo "  bonding 70T RUNE to dog"
for addr in "$DOG_NODE_ADDR"; do
  tx docker-thornode-1 "deposit 70000000000000 THOR.RUNE BOND:$addr --node $DOG_RPC --gas auto" | grep -E "code:|txhash:" || true
  sleep 7
done

echo ""
echo "[6.5/8] Restarting bifrosts in bootstrap order (dog first, then others)..."
"$SCRIPT_DIR/xmr-chainnet-restart-bifrosts.sh" restart

echo ""
echo "[6.6/8] Lifting HaltChurning so the first keygen can fire..."
mimir HaltChurning 0

echo ""
echo "[7/8] Monitoring for churn + keygen..."
echo "  Nodes should move: Whitelisted -> Standby -> Ready -> Active"
echo "  Keygen should produce 2 active XMR vaults of 3 members each (check every 30s, timeout 10min)"

for i in $(seq 1 20); do
  sleep 30
  statuses=$(curl -s localhost:1317/thorchain/nodes 2>/dev/null | jq -r '.[].status' 2>/dev/null | sort | uniq -c || echo "error")
  active_vault_shape=$(curl -s localhost:1317/thorchain/vaults/asgard 2>/dev/null | jq -r '[.[] | select(.status=="ActiveVault") | (.membership | length)] | sort | join(",")' 2>/dev/null || echo "")

  echo "  [${i}/20] nodes: $statuses | active vault shape: ${active_vault_shape:-none}"

  if [ "$active_vault_shape" = "3,3" ]; then
    echo ""
    echo "============================================"
    echo "SUCCESS: 2 active 3-member vaults created!"
    echo "============================================"

    echo ""
    echo "Applying post-keygen mimirs..."
    echo "  Halting churning after initial 2-vault setup"
    mimir_quorum HALTCHURNING 1
    # Steady-state: DesiredValidatorSet=6 with 7 total nodes leaves 1 standby.
    # AsgardSize=3 keeps the network at 2x 2-of-3 XMR vaults.
    # NumberOfNewNodesPerChurn=1 keeps a single-node rotation cadence.
    mimir_quorum NUMBEROFNEWNODESPERCHURN 1

    echo ""
    echo "Vault info:"
    curl -s localhost:1317/thorchain/vaults/asgard | jq '[.[] | select(.status=="ActiveVault") | {status, block_height, chains, membership_count: (.membership | length)}]'

    echo ""
    echo "XMR inbound address:"
    curl -s localhost:1317/thorchain/inbound_addresses | jq '.[] | select(.chain=="XMR")'

    echo ""
    echo "REMINDER: Export keyshares now!"
    echo "  mkdir -p ~/frost-keyshares/xmr-chainnet/\$(date +%Y%m%d)"
    for n in dog $(joiner_names); do
      svc="docker-xmr-frost-signer-1"; [ "$n" = dog ] || svc="docker-xmr-frost-signer-${n}-1"
      echo "  ssh root@91.99.89.216 'docker cp ${svc}:/var/lib/xmr-frost - | gzip' > ~/frost-keyshares/xmr-chainnet/\$(date +%Y%m%d)/${n}.tar.gz"
    done
    exit 0
  fi
done

echo ""
echo "TIMEOUT: 3-member vault not created after 10 minutes."
echo "Check logs:"
echo "  docker logs docker-thornode-1 2>&1 | grep -i 'keygen\|churn\|rotation'"
echo "  docker logs docker-bifrost-1 2>&1 | grep -i 'keygen\|dkg'"
echo "  docker logs docker-xmr-frost-signer-1 2>&1 | grep -i 'dkg\|group_key'"
exit 1
