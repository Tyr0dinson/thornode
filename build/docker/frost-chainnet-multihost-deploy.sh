#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NODE_COUNT="${FROST_NODE_COUNT:-7}"
RUN_STAMP="${RUN_STAMP:-$(date -u +%Y%m%dT%H%M%SZ)}"
BRANCH="${FROST_GIT_BRANCH:-feat-schnorr}"
GIT_URL="${FROST_GIT_URL:-https://gitlab.com/thorchain/thornode.git}"
REMOTE_REPO_DIR="${REMOTE_REPO_DIR:-/opt/frost-thornode}"
NODE_NAME_PREFIX="${FROST_NODE_NAME_PREFIX:-frost-thornode}"
THORNET="${FROST_THORNET:-mocknet}"
RUNTIME_NET="${FROST_RUNTIME_NET:-mocknet}"
OUT_DIR="${FROST_MULTIHOST_OUT_DIR:-$SCRIPT_DIR/frost-chainnet-results/multihost-${NODE_COUNT}}"
INVENTORY="$OUT_DIR/inventory.tsv"
NODE_META="$SCRIPT_DIR/frost-chainnet-generated/nodes-${NODE_COUNT}-${THORNET}.tsv"
BTC_HOST="${BTC_HOST-}"
BTC_SERVER_NAME="${FROST_BTC_SERVER_NAME:-frost-bitcoin-regtest}"
BTC_MINING_ADDRESS="${BTC_MINING_ADDRESS:-bcrt1qf4l5dlqhaujgkxxqmug4stfvmvt58vx2h44c39}"
HCLOUD_ENABLED="${FROST_HCLOUD_ENABLED:-${HCLOUD:-ON}}"
DO_ENABLED="${FROST_DO_ENABLED:-${DO:-OFF}}"

HCLOUD_SERVER_LIMIT="${FROST_HCLOUD_SERVER_LIMIT:-11}"
HCLOUD_PRIMARY_IP_LIMIT="${FROST_HCLOUD_PRIMARY_IP_LIMIT:-20}"
HCLOUD_RESERVED_SERVER_SLOTS="${FROST_HCLOUD_RESERVED_SERVER_SLOTS:-0}"
HCLOUD_RESERVED_PRIMARY_IP_SLOTS="${FROST_HCLOUD_RESERVED_PRIMARY_IP_SLOTS:-0}"
HCLOUD_LOCATION="${FROST_HCLOUD_LOCATION:-sin}"
HCLOUD_IMAGE="${FROST_HCLOUD_IMAGE:-ubuntu-24.04}"
DO_REGION="${FROST_DO_REGION:-sgp1}"
DO_IMAGE="${FROST_DO_IMAGE:-ubuntu-24-04-x64}"
DO_SSH_KEY="${FROST_DO_SSH_KEY-}"
RESET_REMOTE_STATE="${FROST_MULTHOST_RESET_REMOTE_STATE:-${FROST_MULTIHOST_RESET_REMOTE_STATE:-0}}"

case "$NODE_COUNT" in
7 | 14 | 21 | 28) ;;
*)
  echo "ERROR: FROST_NODE_COUNT must be one of 7,14,21,28" >&2
  exit 1
  ;;
esac

mkdir -p "$OUT_DIR"

log() {
  printf '%s\n' "$*"
}

usage() {
  cat <<'EOF'
Usage:
  build/docker/frost-chainnet-multihost-deploy.sh [deploy|timing|deploy-timing|destroy|matrix]

Multi-host GG20 -> FROST timing evaluation. Each THORNode runs on its own
server. Supports 7, 14, 21, and 28 node runs.

Commands:
  deploy         Provision/reuse deterministic servers and start containers.
  timing         Run timing flow against an already deployed inventory.
  deploy-timing  Deploy, then run timing flow.
  deploy-existing-timing
                Start from an existing inventory, then run timing flow.
  destroy        Delete servers from the current inventory.
  matrix         Run deploy-timing for FROST_MULTIHOST_COUNTS, destroying after each run.

Provider env:
  HCLOUD=ON|OFF
  DO=ON|OFF
  FROST_HCLOUD_SSH_KEY=frost-codex
  FROST_DO_SSH_KEY=<digitalocean ssh key id>

Scale env:
  FROST_NODE_COUNT=7|14|21|28
  FROST_MULTIHOST_COUNTS="7 14 21 28"
EOF
}

enabled() {
  case "${1-}" in
  1 | true | TRUE | yes | YES | on | ON) return 0 ;;
  *) return 1 ;;
  esac
}

node_name() {
  printf '%s-%02d' "$NODE_NAME_PREFIX" "$1"
}

json_len_or_zero() {
  jq 'length' 2>/dev/null || echo 0
}

hcloud_available_slots() {
  if [ -n "${FROST_HCLOUD_AVAILABLE_SLOTS-}" ]; then
    echo "$FROST_HCLOUD_AVAILABLE_SLOTS"
    return
  fi
  if ! hcloud server list -o json >/tmp/frost-hcloud-servers.json 2>/dev/null; then
    echo 0
    return
  fi
  if ! hcloud primary-ip list -o json >/tmp/frost-hcloud-primary-ips.json 2>/dev/null; then
    echo 0
    return
  fi
  local servers primary_ips server_slots ip_slots
  servers=$(json_len_or_zero </tmp/frost-hcloud-servers.json)
  primary_ips=$(json_len_or_zero </tmp/frost-hcloud-primary-ips.json)
  server_slots=$((HCLOUD_SERVER_LIMIT - servers - HCLOUD_RESERVED_SERVER_SLOTS))
  ip_slots=$((HCLOUD_PRIMARY_IP_LIMIT - primary_ips - HCLOUD_RESERVED_PRIMARY_IP_SLOTS))
  [ "$server_slots" -lt 0 ] && server_slots=0
  [ "$ip_slots" -lt 0 ] && ip_slots=0
  if [ "$server_slots" -lt "$ip_slots" ]; then
    echo "$server_slots"
  else
    echo "$ip_slots"
  fi
}

do_api() {
  local method="$1"
  local path="$2"
  local payload="${3-}"
  [ -n "${DIGITALOCEAN_TOKEN-}" ] || {
    echo "ERROR: DIGITALOCEAN_TOKEN is required" >&2
    return 1
  }
  if [ -n "$payload" ]; then
    curl -fsS -X "$method" \
      -H "Authorization: Bearer $DIGITALOCEAN_TOKEN" \
      -H "Content-Type: application/json" \
      -d "$payload" \
      "https://api.digitalocean.com/v2$path"
  else
    curl -fsS -X "$method" \
      -H "Authorization: Bearer $DIGITALOCEAN_TOKEN" \
      "https://api.digitalocean.com/v2$path"
  fi
}

do_available_slots() {
  [ -n "${DIGITALOCEAN_TOKEN-}" ] || {
    echo 0
    return
  }
  local limit current
  limit=$(do_api GET /account | jq -r '.account.droplet_limit // 0')
  current=$(do_api GET '/droplets?per_page=1' | jq -r '.meta.total // 0')
  if [ "$limit" -gt "$current" ]; then
    echo $((limit - current))
  else
    echo 0
  fi
}

require_do_ssh_key() {
  if [ -n "$DO_SSH_KEY" ]; then
    return
  fi
  cat >&2 <<'EOF'
ERROR: FROST_DO_SSH_KEY is required for DigitalOcean multihost deploys.
Register the local public key once, then pass the numeric key ID:

  doctl compute ssh-key import frost-codex --public-key-file ~/.ssh/id_ed25519.pub
  FROST_DO_SSH_KEY=<id> build/docker/frost-chainnet-multihost-deploy.sh deploy

Do not rely on cloud-init root user_data here. These droplets can force an
expired root-password change before non-interactive SSH, which blocks bootstrap.
EOF
  exit 1
}

require_hcloud_ssh_key() {
  if [ -n "${FROST_HCLOUD_SSH_KEY-}" ]; then
    return
  fi
  cat >&2 <<'EOF'
ERROR: FROST_HCLOUD_SSH_KEY is required for Hetzner multihost deploys.

  HCLOUD=ON DO=OFF FROST_HCLOUD_SSH_KEY=frost-codex build/docker/frost-chainnet-multihost-deploy.sh deploy
EOF
  exit 1
}

hcloud_type_for_node() {
  echo "${FROST_HCLOUD_NODE_TYPE:-cpx32}"
}

do_size_for_node() {
  echo "${FROST_DO_NODE_SIZE:-s-4vcpu-8gb}"
}

ssh_user_data() {
  local pubkey_file="${FROST_SSH_PUBLIC_KEY_FILE:-$HOME/.ssh/id_ed25519.pub}"
  [ -f "$pubkey_file" ] || {
    echo "ERROR: missing SSH public key $pubkey_file" >&2
    return 1
  }
  python3 - "$pubkey_file" <<'PY'
import sys
key = open(sys.argv[1], encoding="utf-8").read().strip()
print(f"""#cloud-config
disable_root: false
ssh_pwauth: false
users:
  - name: root
    shell: /bin/bash
    ssh_authorized_keys:
      - {key}
""")
PY
}

create_hcloud_node() {
  local index="$1"
  local name ip
  name=$(node_name "$index")
  if hcloud server describe "$name" -o json >/tmp/frost-hcloud-node.json 2>/dev/null; then
    ip=$(jq -r '.public_net.ipv4.ip // ""' </tmp/frost-hcloud-node.json)
    [ -n "$ip" ] || {
      echo "ERROR: existing $name has no IPv4" >&2
      return 1
    }
    printf '%s\t%s\t%s\n' hcloud "$name" "$ip"
    return
  fi
  local args=(
    server create
    --name "$name"
    --type "$(hcloud_type_for_node)"
    --image "$HCLOUD_IMAGE"
    --location "$HCLOUD_LOCATION"
    --without-ipv6
    --start-after-create
    --ssh-key "$FROST_HCLOUD_SSH_KEY"
  )
  hcloud "${args[@]}" >/dev/null
  ip=$(hcloud server ip "$name")
  [ -n "$ip" ] || {
    echo "ERROR: created $name but could not resolve IPv4" >&2
    return 1
  }
  printf '%s\t%s\t%s\n' hcloud "$name" "$ip"
}

create_hcloud_btc_node() {
  local name="$BTC_SERVER_NAME"
  local ip
  if hcloud server describe "$name" -o json >/tmp/frost-hcloud-btc.json 2>/dev/null; then
    jq -r '.public_net.ipv4.ip // ""' </tmp/frost-hcloud-btc.json
    return
  fi
  hcloud server create \
    --name "$name" \
    --type "${FROST_HCLOUD_BTC_NODE_TYPE:-cax11}" \
    --image "$HCLOUD_IMAGE" \
    --location "$HCLOUD_LOCATION" \
    --without-ipv6 \
    --start-after-create \
    --ssh-key "$FROST_HCLOUD_SSH_KEY" >/dev/null
  ip=$(hcloud server ip "$name")
  [ -n "$ip" ] || {
    echo "ERROR: created $name but could not resolve IPv4" >&2
    return 1
  }
  printf '%s\n' "$ip"
}

create_do_node() {
  local index="$1"
  local name payload response id ip
  name=$(node_name "$index")
  response=$(do_api GET '/droplets?per_page=200')
  id=$(echo "$response" | jq -r --arg name "$name" '.droplets[]? | select(.name==$name) | .id' | head -1)
  if [ -n "$id" ]; then
    ip=$(echo "$response" | jq -r --arg name "$name" '.droplets[]? | select(.name==$name) | [.networks.v4[]? | select(.type=="public") | .ip_address][0] // ""' | head -1)
    if [ -n "$ip" ]; then
      printf '%s\t%s\t%s\t%s\n' "do" "$name" "$ip" "$id"
      return
    fi
  fi
  payload=$(jq -n \
    --arg name "$name" \
    --arg region "$DO_REGION" \
    --arg size "$(do_size_for_node)" \
    --arg image "$DO_IMAGE" \
    --arg ssh_key "$DO_SSH_KEY" \
    --arg user_data "$(ssh_user_data)" \
    '{name:$name, region:$region, size:$size, image:$image, monitoring:true, tags:["frost-multihost"]}
    + (if $ssh_key != "" then {ssh_keys: [$ssh_key]} else {user_data:$user_data} end)')
  response=$(do_api POST /droplets "$payload")
  id=$(echo "$response" | jq -r '.droplet.id')
  for _ in $(seq 1 120); do
    ip=$(do_api GET "/droplets/$id" | jq -r '.droplet.networks.v4[]? | select(.type=="public") | .ip_address' | head -1)
    [ -n "$ip" ] && break
    sleep 5
  done
  [ -n "${ip-}" ] || {
    echo "ERROR: timed out waiting for $name IPv4" >&2
    return 1
  }
  printf '%s\t%s\t%s\t%s\n' "do" "$name" "$ip" "$id"
}

do_node_exists() {
  local name
  name=$(node_name "$1")
  [ -n "${DIGITALOCEAN_TOKEN-}" ] || return 1
  do_api GET '/droplets?per_page=200' |
    jq -e --arg name "$name" '.droplets[]? | select(.name==$name)' >/dev/null
}

ensure_node_meta() {
  if [ ! -f "$NODE_META" ]; then
    FROST_NODE_COUNT="$NODE_COUNT" FROST_THORNET="$THORNET" FROST_FORCE_BUILD="${FROST_FORCE_BUILD:-1}" "$SCRIPT_DIR/frost-chainnet-generate-compose.sh" "$NODE_COUNT"
  fi
}

plan_inventory() {
  local hcloud_slots do_slots index provider name ip id provider_line
  hcloud_slots=0
  do_slots=0
  if enabled "$HCLOUD_ENABLED"; then
    require_hcloud_ssh_key
    hcloud_slots=$(hcloud_available_slots)
  fi
  if enabled "$DO_ENABLED"; then
    require_do_ssh_key
    do_slots=$(do_available_slots)
  fi
  if ! enabled "$HCLOUD_ENABLED" && ! enabled "$DO_ENABLED"; then
    echo "ERROR: enable at least one provider with HCLOUD=ON or DO=ON" >&2
    exit 1
  fi
  log "providers: hcloud=$HCLOUD_ENABLED digitalocean=$DO_ENABLED"
  log "capacity: hcloud_slots=$hcloud_slots do_slots=$do_slots requested=$NODE_COUNT"

  printf 'index\tprovider\tname\tip\tprovider_id\n' >"$INVENTORY"
  for index in $(seq 1 "$NODE_COUNT"); do
    provider_line=""
    if enabled "$HCLOUD_ENABLED" && [ "$hcloud_slots" -gt 0 ]; then
      provider_line=$(create_hcloud_node "$index")
      hcloud_slots=$((hcloud_slots - 1))
    elif enabled "$HCLOUD_ENABLED" && hcloud server describe "$(node_name "$index")" >/dev/null 2>&1; then
      provider_line=$(create_hcloud_node "$index")
    elif enabled "$DO_ENABLED" && do_node_exists "$index"; then
      provider_line=$(create_do_node "$index")
    elif enabled "$DO_ENABLED" && [ "$do_slots" -gt 0 ]; then
      provider_line=$(create_do_node "$index")
      do_slots=$((do_slots - 1))
    fi
    if [ -z "$provider_line" ]; then
      echo "ERROR: no enabled provider capacity for node $index ($(node_name "$index"))" >&2
      exit 1
    fi
    provider=$(echo "$provider_line" | awk -F '\t' '{print $1}')
    name=$(echo "$provider_line" | awk -F '\t' '{print $2}')
    ip=$(echo "$provider_line" | awk -F '\t' '{print $3}')
    id=$(echo "$provider_line" | awk -F '\t' '{print $4}')
    printf '%s\t%s\t%s\t%s\t%s\n' "$index" "$provider" "$name" "$ip" "${id-}" | tee -a "$INVENTORY"
  done
}

node_field() {
  local index="$1"
  local field="$2"
  awk -F '\t' -v idx="$index" -v field="$field" '
    NR==1 {
      for (i=1; i<=NF; i++) col[$i]=i
      next
    }
    $1==idx { print $col[field] }
  ' "$NODE_META"
}

inventory_ip() {
  awk -F '\t' -v idx="$1" 'NR>1 && $1==idx {print $4}' "$INVENTORY"
}

prior_ips() {
  local index="$1"
  awk -F '\t' -v idx="$index" 'NR>1 && $1<idx {print $4}' "$INVENTORY" | tail -3 | paste -sd, -
}

other_ips() {
  local index="$1"
  awk -F '\t' -v idx="$index" 'NR>1 && $1!=idx {print $4}' "$INVENTORY" | paste -sd, -
}

node_index_by_address() {
  awk -F '\t' -v addr="$1" 'NR>1 && $4==addr {print $1; exit}' "$NODE_META"
}

ssh_raw() {
  local ip="$1"
  shift
  for _ in $(seq 1 20); do
    if ssh -n -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR -o ConnectTimeout=10 "root@$ip" "$@"; then
      return 0
    fi
    sleep 5
  done
  return 1
}

ssh_raw_stdin() {
  local ip="$1"
  shift
  for _ in $(seq 1 20); do
    if ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR -o ConnectTimeout=10 "root@$ip" "$@"; then
      return 0
    fi
    sleep 5
  done
  return 1
}

fund_env() {
  awk -F '\t' 'NR>2 {printf "EXTRA_FUND_ACCOUNT_%d: \"%s\"\n", NR-2, $4}' "$NODE_META"
}

compose_for_node() {
  local index="$1"
  local seed="$2"
  local public_ip="$3"
  local genesis_ip="$4"
  local seeds="$5"
  local tss_peers="$6"
  local tss_wait_peers="$7"
  local role="validator"
  [ "$index" = "1" ] && role="genesis"
  local active_count=$((NODE_COUNT - 1))
  local new_nodes=$((active_count - 1))

  cat <<YAML
services:
  thornode:
    build:
      context: ../../
      dockerfile: ./build/docker/Dockerfile
      args:
        TAG: "$THORNET"
    image: thornode:frost-chainnet
    restart: unless-stopped
    hostname: thornode
    environment:
      NET: "$RUNTIME_NET"
      CHAIN_ID: thorchain
      SIGNER_NAME: thorchain
      SIGNER_PASSWD: password
      SIGNER_SEED_PHRASE: "$seed"
      THORNODE_GRPC_ENABLE: "true"
      THOR_COSMOS_EBIFROST_ADDRESS: "0.0.0.0:50051"
      THOR_BLOCK_TIME: "6s"
      SKIP_EVM_DEPLOY: "true"
      PORT_RPC: "27147"
      PORT_API: "1317"
      EXTERNAL_IP: "$public_ip"
      SEED: thornode
      SEEDS: "$seeds"
      PEER: "$genesis_ip"
      AUTO_BOND: "true"
      THOR_SEED_NODES_ENDPOINT: ""
      THOR_CHAINNET_DESIRED_VALIDATOR_SET: "$active_count"
      THOR_CHAINNET_ASGARD_SIZE: "$active_count"
      THOR_CHAINNET_NUMBER_OF_NEW_NODES_PER_CHURN: "$new_nodes"
      THOR_CHAINNET_CHURN_RETRY_INTERVAL: "${FROST_CHAINNET_CHURN_RETRY_INTERVAL:-10}"
      THOR_CHAINNET_KEYGEN_RETRY_INTERVAL: "${FROST_CHAINNET_KEYGEN_RETRY_INTERVAL:-10}"
      THOR_CHAINNET_JAIL_TIME_KEYGEN: "${FROST_CHAINNET_CONFIG_JAIL_TIME_KEYGEN:-120}"
      THOR_CHAINNET_JAIL_TIME_KEYSIGN: "${FROST_CHAINNET_CONFIG_JAIL_TIME_KEYSIGN:-120}"
      JAIL_TIME_KEYGEN: "${FROST_CHAINNET_CONFIG_JAIL_TIME_KEYGEN:-120}"
      JAIL_TIME_KEYSIGN: "${FROST_CHAINNET_CONFIG_JAIL_TIME_KEYSIGN:-120}"
      THOR_CHAINNET_SIGNING_TRANSACTION_PERIOD: "30"
      EXTRA_FUND_ACCOUNT_COUNT: "$((NODE_COUNT - 1))"
      FAUCET: "$(node_field 1 address)"
$(fund_env | sed 's/^/      /')
      BIFROST_CHAINS_BTC_DISABLED: "false"
      BIFROST_CHAINS_DOGE_DISABLED: "true"
      BIFROST_CHAINS_BCH_DISABLED: "true"
      BIFROST_CHAINS_LTC_DISABLED: "true"
      BIFROST_CHAINS_ETH_DISABLED: "true"
      BIFROST_CHAINS_AVAX_DISABLED: "true"
      BIFROST_CHAINS_BSC_DISABLED: "true"
      BIFROST_CHAINS_BASE_DISABLED: "true"
      BIFROST_CHAINS_GAIA_DISABLED: "true"
      BIFROST_CHAINS_NOBLE_DISABLED: "true"
      BIFROST_CHAINS_SOL_DISABLED: "true"
      BIFROST_CHAINS_XRP_DISABLED: "true"
      BIFROST_CHAINS_TRON_DISABLED: "true"
      BIFROST_CHAINS_POL_DISABLED: "true"
      BIFROST_CHAINS_ZEC_DISABLED: "true"
      BIFROST_CHAINS_XMR_DISABLED: "true"
      BIFROST_CHAINS_SUI_DISABLED: "true"
      BIFROST_CHAINS_ADA_DISABLED: "true"
    ports:
      - "26656:26656"
      - "26657:26657"
      - "27146:27146"
      - "27147:26657"
      - "1317:1317"
      - "50051:50051"
      - "9090:9090"
    volumes:
      - "../scripts:/docker/scripts"
      - thornode:/root/.thornode
    entrypoint: /docker/scripts/${role}.sh
    command: ["thornode", "start"]
  bifrost:
    image: thornode:frost-chainnet
    restart: unless-stopped
    depends_on:
      - thornode
    environment:
      NET: "$RUNTIME_NET"
      CHAIN_ID: thorchain
      SIGNER_NAME: thorchain
      SIGNER_PASSWD: password
      SIGNER_SEED_PHRASE: "$seed"
      CHAIN_API: thornode:1317
      CHAIN_RPC: thornode:26657
      BIFROST_THORCHAIN_CHAIN_EBIFROST: thornode:50051
      THOR_SEED_NODES_ENDPOINT: ""
      THOR_BLOCK_TIME: "6s"
      SKIP_EVM_DEPLOY: "true"
      BLOCK_SCANNER_BACKOFF: "5s"
      BIFROST_METRICS_PPROF_ENABLED: "true"
      BIFROST_SIGNER_BACKUP_KEYSHARES: "false"
      BIFROST_SIGNER_RESCHEDULE_BUFFER_BLOCKS: "20"
      BIFROST_SIGNER_AUTO_OBSERVE: "false"
      BIFROST_SIGNER_KEYGEN_TIMEOUT: "${FROST_BIFROST_KEYGEN_TIMEOUT:-60s}"
      BIFROST_SIGNER_KEYSIGN_TIMEOUT: "${FROST_BIFROST_KEYSIGN_TIMEOUT:-60s}"
      BIFROST_SIGNER_PARTY_TIMEOUT: "55s"
      THOR_CHAINNET_CHURN_RETRY_INTERVAL: "${FROST_CHAINNET_CHURN_RETRY_INTERVAL:-10}"
      THOR_CHAINNET_KEYGEN_RETRY_INTERVAL: "${FROST_CHAINNET_KEYGEN_RETRY_INTERVAL:-10}"
      THOR_CHAINNET_JAIL_TIME_KEYGEN: "${FROST_CHAINNET_CONFIG_JAIL_TIME_KEYGEN:-120}"
      THOR_CHAINNET_JAIL_TIME_KEYSIGN: "${FROST_CHAINNET_CONFIG_JAIL_TIME_KEYSIGN:-120}"
      JAIL_TIME_KEYGEN: "${FROST_CHAINNET_CONFIG_JAIL_TIME_KEYGEN:-120}"
      JAIL_TIME_KEYSIGN: "${FROST_CHAINNET_CONFIG_JAIL_TIME_KEYSIGN:-120}"
      BIFROST_TSS_EXTERNAL_IP: "$public_ip"
      BIFROST_TSS_BOOTSTRAP_PEERS: "$tss_peers"
      PEER: "$tss_wait_peers"
      LD_LIBRARY_PATH: /usr/lib:/usr/local/lib
      BTC_HOST: "$BTC_HOST"
      BIFROST_CHAINS_BTC_CHAIN_NETWORK: mocknet
      BIFROST_CHAINS_BTC_BLOCK_SCANNER_START_BLOCK_HEIGHT: "0"
      BIFROST_CHAINS_BTC_BLOCK_SCANNER_MAX_HEALTHY_LAG: "${FROST_BTC_MAX_HEALTHY_LAG:-8h}"
      BIFROST_CHAINS_BTC_DISABLED: "false"
      BIFROST_CHAINS_DOGE_DISABLED: "true"
      BIFROST_CHAINS_BCH_DISABLED: "true"
      BIFROST_CHAINS_LTC_DISABLED: "true"
      BIFROST_CHAINS_ETH_DISABLED: "true"
      BIFROST_CHAINS_AVAX_DISABLED: "true"
      BIFROST_CHAINS_BSC_DISABLED: "true"
      BIFROST_CHAINS_BASE_DISABLED: "true"
      BIFROST_CHAINS_GAIA_DISABLED: "true"
      BIFROST_CHAINS_NOBLE_DISABLED: "true"
      BIFROST_CHAINS_SOL_DISABLED: "true"
      BIFROST_CHAINS_XRP_DISABLED: "true"
      BIFROST_CHAINS_TRON_DISABLED: "true"
      BIFROST_CHAINS_POL_DISABLED: "true"
      BIFROST_CHAINS_ZEC_DISABLED: "true"
      BIFROST_CHAINS_XMR_DISABLED: "true"
      BIFROST_CHAINS_SUI_DISABLED: "true"
      BIFROST_CHAINS_ADA_DISABLED: "true"
    ports:
      - "5040:5040"
      - "6040:6040"
    volumes:
      - "../scripts:/docker/scripts"
      - bifrost:/var/data/bifrost
      - thornode:/root/.thornode
    entrypoint: /docker/scripts/bifrost.sh
    command: ["bifrost", "-p"]
volumes:
  thornode: {}
  bifrost: {}
YAML
}

remote_prepare() {
  local ip="$1"
  ssh-keygen -R "$ip" >/dev/null 2>&1 || true
  until ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR -o ConnectTimeout=5 "root@$ip" true 2>/dev/null; do
    sleep 5
  done
  ssh_raw_stdin "$ip" "bash -s" <<REMOTE
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if command -v cloud-init >/dev/null 2>&1; then cloud-init status --wait >/dev/null || true; fi
if ! command -v docker >/dev/null 2>&1; then
  until apt-get update; do sleep 5; done
  until apt-get install -y ca-certificates curl git jq make python3 xxd; do sleep 5; done
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  . /etc/os-release
  echo "deb [arch=\$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \${VERSION_CODENAME} stable" >/etc/apt/sources.list.d/docker.list
  until apt-get update; do sleep 5; done
  until apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin; do sleep 5; done
fi
mkdir -p "$REMOTE_REPO_DIR"
if [ -e "$REMOTE_REPO_DIR" ] && [ ! -d "$REMOTE_REPO_DIR/.git" ]; then
  rm -rf "$REMOTE_REPO_DIR"
  mkdir -p "$REMOTE_REPO_DIR"
fi
if [ ! -d "$REMOTE_REPO_DIR/.git" ]; then
  git clone --branch "$BRANCH" "$GIT_URL" "$REMOTE_REPO_DIR"
fi
cd "$REMOTE_REPO_DIR"
git fetch origin "$BRANCH"
git reset --hard "origin/$BRANCH"
REMOTE
}

remote_start_bitcoin() {
  local ip="$1"
  local wallet_name="${BTC_WALLET_NAME:-thornode-frost}"
  remote_prepare "$ip"
  ssh_raw_stdin "$ip" "bash -s" <<REMOTE
set -euo pipefail
mkdir -p /opt/bitcoin-regtest
cat >/opt/bitcoin-regtest/docker-compose.yml <<'YAML'
services:
  bitcoin:
    image: registry.gitlab.com/thorchain/devops/node-launcher:bitcoin-daemon-26.2
    restart: unless-stopped
    user: "0:0"
    environment:
      BLOCK_TIME: "1"
      BTC_MASTER_ADDR: "$BTC_MINING_ADDRESS"
    ports:
      - "18443:18443"
      - "18444:18444"
    entrypoint: "/scripts/entrypoint-mock.sh"
    volumes:
      - bitcoin:/root/.bitcoin
volumes:
  bitcoin: {}
YAML
cd /opt/bitcoin-regtest
if [ "${FROST_RESET_BTC_STATE:-0}" = "1" ]; then
  docker compose down -v --remove-orphans || true
fi
docker compose up -d
until docker exec bitcoin-regtest-bitcoin-1 bitcoin-cli -regtest -rpcuser="${BTC_RPC_USER:-thorchain}" -rpcpassword="${BTC_RPC_PASSWORD:-password}" getblockchaininfo >/dev/null 2>&1; do
  sleep 2
done
if ! docker exec bitcoin-regtest-bitcoin-1 bitcoin-cli -regtest -rpcuser="${BTC_RPC_USER:-thorchain}" -rpcpassword="${BTC_RPC_PASSWORD:-password}" listwallets | grep -F "\"${wallet_name}\"" >/dev/null; then
  if ! docker exec bitcoin-regtest-bitcoin-1 bitcoin-cli -regtest -rpcuser="${BTC_RPC_USER:-thorchain}" -rpcpassword="${BTC_RPC_PASSWORD:-password}" loadwallet "${wallet_name}" >/dev/null 2>&1; then
    docker exec bitcoin-regtest-bitcoin-1 bitcoin-cli -regtest -rpcuser="${BTC_RPC_USER:-thorchain}" -rpcpassword="${BTC_RPC_PASSWORD:-password}" createwallet "${wallet_name}" false false "" false true >/dev/null
  fi
fi
balance=\$(docker exec bitcoin-regtest-bitcoin-1 bitcoin-cli -regtest -rpcuser="${BTC_RPC_USER:-thorchain}" -rpcpassword="${BTC_RPC_PASSWORD:-password}" -rpcwallet="${wallet_name}" getbalance)
if awk "BEGIN { exit !(\$balance < 2) }"; then
  mine_addr=\$(docker exec bitcoin-regtest-bitcoin-1 bitcoin-cli -regtest -rpcuser="${BTC_RPC_USER:-thorchain}" -rpcpassword="${BTC_RPC_PASSWORD:-password}" -rpcwallet="${wallet_name}" getnewaddress)
  docker exec bitcoin-regtest-bitcoin-1 bitcoin-cli -regtest -rpcuser="${BTC_RPC_USER:-thorchain}" -rpcpassword="${BTC_RPC_PASSWORD:-password}" generatetoaddress 101 "\$mine_addr" >/dev/null
fi
REMOTE
  BTC_HOST="$ip:18443/wallet/${BTC_WALLET_NAME:-thornode-frost}"
  export BTC_HOST
  log "bitcoin regtest: $BTC_HOST"
}

remote_write_compose() {
  local index="$1"
  local ip="$2"
  local seed public_ip genesis_ip seeds tss_peers tss_wait_peers
  seed=$(node_field "$index" seed)
  public_ip=$(inventory_ip "$index")
  genesis_ip=$(inventory_ip 1)
  tss_peers=$(other_ips "$index")
  tss_wait_peers="$genesis_ip"
  [ "$index" = "1" ] && tss_wait_peers=""
  seeds=$(prior_ips "$index")
  if [ "$index" = "1" ]; then
    seeds=""
  elif [ -n "$seeds" ]; then
    seeds="$genesis_ip,$seeds"
  else
    seeds="$genesis_ip"
  fi
  if [ "$RESET_REMOTE_STATE" = "1" ]; then
    ssh_raw "$ip" "cd $REMOTE_REPO_DIR/build/docker 2>/dev/null && docker compose -f docker-compose.frost-chainnet-multihost.yml down -v --remove-orphans || true"
  fi
  ssh_raw "$ip" "mkdir -p $REMOTE_REPO_DIR/build/docker"
  compose_for_node "$index" "$seed" "$public_ip" "$genesis_ip" "$seeds" "$tss_peers" "$tss_wait_peers" | ssh_raw_stdin "$ip" "cat > $REMOTE_REPO_DIR/build/docker/docker-compose.frost-chainnet-multihost.yml"
}

remote_start_node() {
  local index="$1"
  local ip="$2"
  ssh_raw "$ip" "cd $REMOTE_REPO_DIR/build/docker && docker compose -f docker-compose.frost-chainnet-multihost.yml up -d --build"
  wait_remote_node "$index" "$ip"
}

wait_remote_node() {
  local index="$1"
  local ip="$2"
  for _ in $(seq 1 60); do
    if ssh_raw "$ip" "docker inspect docker-thornode-1 >/dev/null 2>&1 && docker inspect docker-bifrost-1 >/dev/null 2>&1"; then
      return 0
    fi
    sleep 5
  done
  log "ERROR: node $index containers not ready on $ip"
  ssh_raw "$ip" "cd $REMOTE_REPO_DIR/build/docker && docker compose -f docker-compose.frost-chainnet-multihost.yml ps || true; docker ps -a || true; docker logs --tail 120 docker-thornode-1 2>&1 || true"
  return 1
}

api() {
  curl -fsS "http://$(inventory_ip 1):1317$1"
}

chain_height() {
  api /thorchain/block | jq -r '.header.height // 0'
}

active_node_count() {
  api /thorchain/nodes | jq -r '[.[] | select(.status=="Active")] | length'
}

standby_ready_count() {
  api /thorchain/nodes | jq -r '[.[] | select(.status=="Ready" or .status=="Standby")] | length'
}

active_vault_shape() {
  api /thorchain/vaults/asgard | jq -r '[.[] | select(.status=="ActiveVault") | (.membership | length)] | sort | join(",")'
}

active_vault_pubkey() {
  local active_count=$((NODE_COUNT - 1))
  api /thorchain/vaults/asgard | jq -r --argjson active "$active_count" '[.[] | select(.status=="ActiveVault") | select((.membership | length)==$active) | (.pub_key | if type=="object" then .value else . end)] | .[0] // ""'
}

btc_inbound_address() {
  api /thorchain/inbound_addresses | jq -r '.[] | select(.chain=="BTC") | .address'
}

ssh_node() {
  local index="$1"
  shift
  ssh_raw "$(inventory_ip "$index")" "$@"
}

ssh_node_once() {
  local index="$1"
  shift
  ssh -n -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR -o ConnectTimeout=10 "root@$(inventory_ip "$index")" "$@"
}

remote_tx() {
  local index="$1"
  shift
  ssh_node_once "$index" "timeout 60 docker exec docker-thornode-1 bash -lc 'printf \"password\\n\" | thornode tx thorchain $* --from thorchain --keyring-backend file --chain-id thorchain --yes --node tcp://$(inventory_ip 1):27147'" 2>&1 || true
}

remote_node_address() {
  local index="$1"
  ssh_node "$index" "docker exec docker-thornode-1 bash -lc 'printf \"password\\n\" | thornode keys show thorchain --keyring-backend file -a'"
}

remote_set_node_keys() {
  local index="$1"
  local seed
  seed=$(node_field "$index" seed)
  ssh_node_once "$index" "timeout 60 docker exec -e SIGNER_SEED_PHRASE='$seed' docker-thornode-1 bash -lc '
    NODE_PUB_KEY=\$(echo password | thornode keys show thorchain --pubkey --keyring-backend file | thornode pubkey)
    NODE_PUB_KEY_ED25519=\$(thornode ed25519)
    VALIDATOR=\$(thornode tendermint show-validator | thornode pubkey --bech cons)
    printf \"password\\n\" | thornode tx thorchain set-node-keys \"\$NODE_PUB_KEY\" \"\$NODE_PUB_KEY_ED25519\" \"\$VALIDATOR\" --from thorchain --keyring-backend file --chain-id thorchain --node tcp://$(inventory_ip 1):27147 --yes --gas auto
  '" 2>&1 | grep -E 'code:|txhash:' || true
}

remote_restart_bifrosts() {
  local index
  for index in $(seq 1 "$NODE_COUNT"); do
    log "restart bifrost node $index"
    ssh_node "$index" "cd $REMOTE_REPO_DIR/build/docker && docker compose -f docker-compose.frost-chainnet-multihost.yml restart bifrost" >/dev/null
    sleep 5
  done
}

wait_after_bifrost_restart() {
  local hold="${FROST_TSS_SYNC_HOLD:-180}"
  [ "$hold" -gt 0 ] || return 0
  log "wait ${hold}s for TSS/libp2p sync after bifrost restart"
  sleep "$hold"
}

single_mimir() {
  local key="$1"
  local value="$2"
  log "mimir $key=$value"
  remote_tx 1 "mimir $key $value" | grep -E 'code:|txhash:' || true
  sleep 7
}

quorum_mimir() {
  local key="$1"
  local value="$2"
  local active threshold votes index addr
  active=$(active_node_count)
  threshold=$(((active * 2) / 3 + 1))
  [ "$threshold" -lt 1 ] && threshold=1
  votes=0
  log "mimir quorum $key=$value active=$active threshold=$threshold"
  for index in $(seq 1 "$NODE_COUNT"); do
    addr=$(node_field "$index" address)
    if api /thorchain/nodes | jq -e --arg addr "$addr" '.[] | select(.node_address==$addr and .status=="Active")' >/dev/null; then
      (
        log "mimir vote $key=$value node=$index"
        remote_tx "$index" "mimir $key $value" | grep -E 'code:|txhash:' || true
      ) &
      votes=$((votes + 1))
      [ "$votes" -ge "$threshold" ] && break
    fi
  done
  wait
  sleep 7
}

wait_shape() {
  local label="$1"
  local active_target="$2"
  local standby_target="$3"
  local max_checks="${4:-120}"
  local i active standby vault_shape
  for i in $(seq 1 "$max_checks"); do
    active=$(active_node_count)
    standby=$(standby_ready_count)
    vault_shape=$(active_vault_shape)
    log "[$i/$max_checks] $label active=$active standby_ready=$standby vault_shape=${vault_shape:-none} height=$(chain_height)"
    if [ "$active" = "$active_target" ] && [ "$standby" = "$standby_target" ]; then
      return 0
    fi
    sleep 10
  done
  return 1
}

wait_vault_shape() {
  local label="$1"
  local vault_target="$2"
  local max_checks="${3:-120}"
  local i active standby vault_shape
  for i in $(seq 1 "$max_checks"); do
    active=$(active_node_count)
    standby=$(standby_ready_count)
    vault_shape=$(active_vault_shape)
    log "[$i/$max_checks] $label active=$active standby_ready=$standby vault_shape=${vault_shape:-none} height=$(chain_height)"
    if [ "$vault_shape" = "$vault_target" ]; then
      return 0
    fi
    sleep 10
  done
  return 1
}

btc_rpc() {
  local method="$1"
  local params="${2:-[]}"
  local response error
  response=$(curl -fsS --user "${BTC_RPC_USER:-thorchain}:${BTC_RPC_PASSWORD:-password}" \
    --data-binary "{\"jsonrpc\":\"1.0\",\"id\":\"chainnet\",\"method\":\"$method\",\"params\":$params}" \
    -H 'content-type:text/plain;' "http://$BTC_HOST/")
  error=$(jq -r '.error // empty' <<<"$response")
  if [ -n "$error" ] && [ "$error" != "null" ]; then
    echo "BTC RPC $method failed: $error" >&2
    return 1
  fi
  jq -r '.result' <<<"$response"
}

seed_btc_network_fee() {
  local i addr
  log "seed BTC fee-bearing regtest blocks"
  btc_rpc settxfee "[0.001]" >/dev/null || true
  for i in $(seq 1 "${FROST_BTC_FEE_SEED_BLOCKS:-12}"); do
    addr=$(btc_rpc getnewaddress)
    btc_rpc sendtoaddress "[\"$addr\",0.001]" >/dev/null
    btc_rpc generatetoaddress "[1,\"$BTC_MINING_ADDRESS\"]" >/dev/null
  done
}

wait_btc_network_fee() {
  local max_checks="${1:-120}"
  local i gas_rate outbound_fee
  for i in $(seq 1 "$max_checks"); do
    gas_rate=$(api /thorchain/inbound_addresses | jq -r '.[] | select(.chain=="BTC") | .gas_rate // "0"' | head -1)
    outbound_fee=$(api /thorchain/inbound_addresses | jq -r '.[] | select(.chain=="BTC") | .outbound_fee // "0"' | head -1)
    log "[$i/$max_checks] BTC network fee gas_rate=${gas_rate:-0} outbound_fee=${outbound_fee:-0}"
    if [ "${gas_rate:-0}" != "0" ] && [ "${outbound_fee:-0}" != "0" ]; then
      return 0
    fi
    sleep 10
  done
  return 1
}

add_btc_lp() {
  local inbound="$1"
  local bitcoin_inbound
  local memo
  local memo_hex raw funded signed txid
  memo="ADD:BTC.BTC:$(node_field 1 address)"
  memo_hex=$(printf "%s" "$memo" | xxd -p -c 256)
  bitcoin_inbound=$(btc_daemon_address "$inbound")
  log "fund BTC LP inbound=$inbound daemon_inbound=$bitcoin_inbound memo=$memo" >&2
  raw=$(btc_rpc createrawtransaction "[[],{\"$bitcoin_inbound\":1,\"data\":\"$memo_hex\"}]")
  funded=$(btc_rpc fundrawtransaction "[\"$raw\",{\"feeRate\":0.001}]" | jq -r '.hex')
  signed=$(btc_rpc signrawtransactionwithwallet "[\"$funded\"]" | jq -r '.hex')
  txid=$(btc_rpc sendrawtransaction "[\"$signed\"]")
  btc_rpc generatetoaddress "[6,\"$BTC_MINING_ADDRESS\"]" >/dev/null
  log "BTC LP tx=$txid" >&2
  printf '%s\n' "$txid"
}

btc_daemon_address() {
  local addr="$1"
  if [[ $addr != bc1* ]]; then
    printf '%s\n' "$addr"
    return 0
  fi
  python3 - "$addr" <<'PY'
import sys

CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
BECH32M_CONST = 0x2bc830a3
BECH32_CONST = 1

def hrp_expand(hrp):
    return [ord(x) >> 5 for x in hrp] + [0] + [ord(x) & 31 for x in hrp]

def polymod(values):
    chk = 1
    for v in values:
        top = chk >> 25
        chk = (chk & 0x1ffffff) << 5 ^ v
        for i, g in enumerate([0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3]):
            if (top >> i) & 1:
                chk ^= g
    return chk

def decode(bech):
    bech = bech.lower()
    pos = bech.rfind("1")
    if pos < 1:
        raise SystemExit("invalid bech32 address")
    hrp = bech[:pos]
    data = [CHARSET.find(c) for c in bech[pos + 1:]]
    if any(v < 0 for v in data):
        raise SystemExit("invalid bech32 charset")
    spec = BECH32M_CONST if polymod(hrp_expand(hrp) + data) == BECH32M_CONST else BECH32_CONST
    return data[:-6], spec

def encode(hrp, data, spec):
    values = hrp_expand(hrp) + data
    pm = polymod(values + [0, 0, 0, 0, 0, 0]) ^ spec
    checksum = [(pm >> 5 * (5 - i)) & 31 for i in range(6)]
    return hrp + "1" + "".join(CHARSET[d] for d in data + checksum)

data, spec = decode(sys.argv[1])
print(encode("bcrt", data, spec))
PY
}

top_up_active_bonds() {
  local amount="${1:-5000000000000}"
  local addr index
  log "top up active bonds amount=$amount"
  api /thorchain/nodes | jq -r '.[] | select(.status=="Active") | .node_address' | while read -r addr; do
    [ -n "$addr" ] || continue
    index=$(node_index_by_address "$addr")
    [ -n "$index" ] || continue
    [ "$index" = "1" ] && continue
    remote_tx "$index" "deposit $amount THOR.RUNE BOND:$addr --gas auto" | grep -E 'code:|txhash:' || true
    sleep 8
  done
}

active_vault_btc_amount() {
  local vault="$1"
  api /thorchain/vaults/asgard |
    jq -r --arg vault "$vault" '
      [.[] | select(.status=="ActiveVault") | select((.pub_key | if type=="object" then .value else . end)==$vault) |
        ((.coins // []) | map(select(.asset=="BTC.BTC") | .amount | tonumber) | add // 0)] | .[0] // 0'
}

retiring_vault_btc_amount() {
  local vault="$1"
  api /thorchain/vaults/asgard |
    jq -r --arg vault "$vault" '
      [.[] | select(.status=="RetiringVault") | select((.pub_key | if type=="object" then .value else . end)==$vault) |
        ((.coins // []) | map(select(.asset=="BTC.BTC") | .amount | tonumber) | add // 0)] | .[0] // 0'
}

wait_btc_lp_observed() {
  local vault="$1" txid="$2" max_checks="${3:-180}"
  local i amount signers upper_txid
  upper_txid=$(tr '[:lower:]' '[:upper:]' <<<"$txid")
  for i in $(seq 1 "$max_checks"); do
    amount=$(active_vault_btc_amount "$vault")
    signers=$(api "/thorchain/tx/details/$upper_txid" 2>/dev/null | jq -r '(.txs[0].signers // []) | length' 2>/dev/null || echo 0)
    log "[$i/$max_checks] BTC LP observed amount=${amount:-0} signers=${signers:-0} vault=$vault"
    [ "${amount:-0}" != "0" ] && return 0
    sleep 10
  done
  return 1
}

wait_btc_scanners_healthy() {
  local max_checks="${1:-120}"
  local i unhealthy index
  for i in $(seq 1 "$max_checks"); do
    unhealthy=0
    for index in $(seq 1 "$NODE_COUNT"); do
      if api /thorchain/nodes | jq -e --arg addr "$(node_field "$index" address)" '.[] | select(.node_address==$addr and .status=="Active")' >/dev/null; then
        if ! ssh_node_once "$index" "docker logs --tail 160 docker-bifrost-1 2>&1 | grep 'scan block' | grep 'chain=.\{0,8\}BTC' | tail -1 | grep -Eq 'healthy=.{0,16}true'"; then
          unhealthy=$((unhealthy + 1))
        fi
      fi
    done
    log "[$i/$max_checks] BTC active scanner unhealthy=$unhealthy"
    [ "$unhealthy" = 0 ] && return 0
    sleep 10
  done
  return 1
}

wait_retiring_vault_empty() {
  local vault="$1" label="$2" max_checks="${3:-180}"
  local i amount
  for i in $(seq 1 "$max_checks"); do
    amount=$(retiring_vault_btc_amount "$vault")
    log "[$i/$max_checks] $label retiring BTC amount=${amount:-0}"
    [ "${amount:-0}" = "0" ] && return 0
    sleep 10
  done
  return 1
}

wait_migration_broadcast() {
  local since="$1" vault="$2" logs line txid
  logs="$(mktemp)"
  for _ in $(seq 1 180); do
    collect_bifrost_logs "$since"
    cp "$OUT_DIR/bifrost.log" "$logs"
    line=$(grep -i "broadcasted tx to chain" "$logs" | grep -Ei 'memo=.?MIGRATE|"memo":"MIGRATE' | grep -F "$vault" | sort | tail -1 || true)
    [ -z "$line" ] && line=$(grep -i "broadcasted tx to chain" "$logs" | grep -Ei 'memo=.?MIGRATE|"memo":"MIGRATE' | sort | tail -1 || true)
    if [ -n "$line" ]; then
      txid=$(sed -n 's/.*txid=\([^ ]*\).*/\1/p; s/.*"txid":"\([^"]*\)".*/\1/p' <<<"$line" | head -1)
      rm -f "$logs"
      printf '%s\n' "$txid"
      return 0
    fi
    sleep 10
  done
  rm -f "$logs"
  return 0
}

collect_bifrost_logs() {
  local since="$1"
  local index
  : >"$OUT_DIR/bifrost.log"
  for index in $(seq 1 "$NODE_COUNT"); do
    ssh_node "$index" "docker logs --timestamps --since '$since' docker-bifrost-1 2>&1" | sed "s/^/node${index} /" >>"$OUT_DIR/bifrost.log" || true
  done
}

record_tss_timing() {
  local phase="$1"
  local engine="$2"
  local label="$3"
  local since="$4"
  local pool_pubkey="${5-}"
  collect_bifrost_logs "$since"
  python3 - "$OUT_DIR/bifrost.log" "$OUT_DIR/tss-timings.csv" "$RUN_STAMP" "$phase" "$engine" "$label" "$pool_pubkey" <<'PY'
import csv, json, re, sys
log_path, csv_path, run_id, phase, engine, label, pool_pubkey = sys.argv[1:]
engine_label = "FROST" if engine == "frost" else engine.upper()
start_pat = f"{engine_label} {phase} ceremony started"
stop_pat = f"{engine_label} {phase} ceremony party produced"
rows = []
for line in open(log_path, errors="ignore"):
    if start_pat not in line and stop_pat not in line:
        continue
    ts = ""
    msg_id = ""
    try:
        obj = json.loads(line[line.index("{"):])
        ts = obj.get("time", "")
        msg_id = obj.get("msg_id", "")
        if pool_pubkey and phase != "keygen" and obj.get("pool_pub_key", "") not in ("", pool_pubkey):
            continue
    except Exception:
        m = re.search(r'^node\d+\s+(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z)', line)
        if m: ts = m.group(1)
        m = re.search(r'time":"([^"]+)"', line)
        if m: ts = m.group(1)
        m = re.search(r'msg_id=([^ ]+)|"msg_id":"([^"]+)"', line)
        if m: msg_id = m.group(1) or m.group(2)
        if pool_pubkey and phase != "keygen" and pool_pubkey not in line:
            continue
    if ts and msg_id:
        rows.append((ts, msg_id, "start" if start_pat in line else "stop", line.strip()))
starts = {}
best = None
for ts, msg_id, kind, line in sorted(rows):
    if kind == "start":
        starts.setdefault(msg_id, (ts, line))
    elif msg_id in starts:
        best = (msg_id, starts[msg_id][0], ts, starts[msg_id][1], line)
        break
if not best:
    print(f"WARN: no {engine} {phase} timing found")
    sys.exit(0)
from datetime import datetime
def parse(t):
    t = t.replace("Z","+00:00")
    return datetime.fromisoformat(re.sub(r'\.(\d{6})\d+([+-]\d\d:\d\d)$', r'.\1\2', t))
msg_id, start, stop, start_line, stop_line = best
elapsed = int((parse(stop)-parse(start)).total_seconds() * 1000)
exists = False
try:
    exists = bool(open(csv_path).read(1))
except FileNotFoundError:
    pass
fields = ["run_id","recorded_at","phase","engine","label","msg_id","tss_elapsed_ms","start_ts","stop_ts","notes"]
with open(csv_path, "a", newline="") as f:
    w = csv.DictWriter(f, fieldnames=fields)
    if not exists:
        w.writeheader()
    w.writerow({"run_id":run_id,"recorded_at":datetime.utcnow().replace(microsecond=0).isoformat()+"Z","phase":phase,"engine":engine,"label":label,"msg_id":msg_id,"tss_elapsed_ms":elapsed,"start_ts":start,"stop_ts":stop,"notes":"multihost"})
print(f"{label}: {elapsed}ms msg_id={msg_id}")
PY
}

collect_keyshare_sizes() {
  local out="$OUT_DIR/keyshare-sizes.csv"
  local index
  printf 'run_id,node,path,size_bytes,signing_engine\n' >"$out"
  for index in $(seq 1 "$NODE_COUNT"); do
    ssh_node "$index" "docker exec docker-bifrost-1 sh -lc 'for f in /var/data/bifrost/localstate-*.json; do [ -f \"\$f\" ] || continue; size=\$(wc -c < \"\$f\"); engine=\$(jq -r \".signing_engine // \\\"gg20\\\"\" \"\$f\"); printf \"%s,%s,%s\\n\" \"\$f\" \"\$size\" \"\$engine\"; done'" 2>/dev/null |
      awk -F, -v run="$RUN_STAMP" -v node="$index" '{print run "," node "," $1 "," $2 "," $3}' >>"$out" || true
  done
}

write_final_report() {
  local report="$OUT_DIR/report.md"
  {
    echo "# FROST BTC 7-node timing run"
    echo
    echo "- run: $RUN_STAMP"
    echo "- nodes: $NODE_COUNT"
    echo "- active vault target: $((NODE_COUNT - 1))"
    echo "- branch: $BRANCH"
    echo "- btc regtest: $BTC_HOST"
    echo
    echo "## TSS timings"
    echo
    if [ -f "$OUT_DIR/tss-timings.csv" ]; then
      python3 - "$OUT_DIR/tss-timings.csv" <<'PY'
import csv, sys
rows=list(csv.DictReader(open(sys.argv[1])))
print("| phase | engine | label | elapsed_ms | msg_id |")
print("|---|---:|---|---:|---|")
for r in rows:
    print(f"| {r.get('phase','')} | {r.get('engine','')} | {r.get('label','')} | {r.get('tss_elapsed_ms','')} | {r.get('msg_id','')} |")
PY
    fi
    echo
    echo "## Keyshare sizes"
    echo
    if [ -f "$OUT_DIR/keyshare-sizes.csv" ]; then
      python3 - "$OUT_DIR/keyshare-sizes.csv" <<'PY'
import csv, sys
rows=list(csv.DictReader(open(sys.argv[1])))
print("| node | engine | size_bytes | path |")
print("|---:|---|---:|---|")
for r in rows:
    print(f"| {r.get('node','')} | {r.get('signing_engine','')} | {r.get('size_bytes','')} | {r.get('path','')} |")
PY
    fi
  } >"$report"
}

wait_for_vault_change() {
  local label="$1"
  local old_vault="$2"
  local max_checks="${3:-120}"
  local i shape vault active_count
  active_count=$((NODE_COUNT - 1))
  for i in $(seq 1 "$max_checks"); do
    shape=$(active_vault_shape)
    vault=$(active_vault_pubkey)
    log "[$i/$max_checks] $label shape=${shape:-none} vault=${vault:-none} height=$(chain_height)" >&2
    if [ "$shape" = "$active_count" ] && [ -n "$vault" ] && [ "$vault" != "$old_vault" ]; then
      printf '%s\n' "$vault"
      return 0
    fi
    sleep 15
  done
  return 1
}

multihost_timing() {
  [ -f "$INVENTORY" ] || {
    echo "ERROR: missing inventory $INVENTORY" >&2
    exit 1
  }
  local active_count=$((NODE_COUNT - 1))
  local bootstrap_new=$((active_count - 1))
  local index addr gg20_vault frost1_vault frost2_vault inbound start btc_txid gg20_migration_txid frost_migration_txid bootstrap_bond joiner_fee_fund
  bootstrap_bond="${FROST_CHAINNET_BOOTSTRAP_BOND:-20010000000000}"
  joiner_fee_fund="${FROST_CHAINNET_JOINER_FEE_FUND:-1000000000}"
  mkdir -p "$OUT_DIR"
  log "timing output: $OUT_DIR/tss-timings.csv"

  log "milestone: genesis health"
  until curl -fsS --max-time 3 "http://$(inventory_ip 1):27147/health" >/dev/null; do sleep 5; done
  single_mimir HaltChurning 1
  single_mimir P2PGateDisabled 1
  single_mimir DesiredValidatorSet "$active_count"
  single_mimir AsgardSize "$active_count"
  single_mimir NumberOfNewNodesPerChurn "$bootstrap_new"
  single_mimir ChurnInterval "${FROST_CHAINNET_CHURN_INTERVAL:-10}"
  single_mimir ChurnRetryInterval "${FROST_CHAINNET_CHURN_RETRY_INTERVAL:-10}"
  single_mimir KeygenRetryInterval "${FROST_CHAINNET_KEYGEN_RETRY_INTERVAL:-10}"
  single_mimir DisableXMRMonovaultKeygen 1
  single_mimir JailTimeKeygen "${FROST_CHAINNET_JAIL_TIME_KEYGEN:-11}"
  single_mimir JailTimeKeysign "${FROST_CHAINNET_JAIL_TIME_KEYSIGN:-11}"
  single_mimir ChurnMigrateRounds "${FROST_CHAINNET_CHURN_MIGRATE_ROUNDS:-2}"
  single_mimir FundMigrationInterval "${FROST_CHAINNET_FUND_MIGRATION_INTERVAL:-30}"
  single_mimir SigningTransactionPeriod "${FROST_CHAINNET_SIGNING_TRANSACTION_PERIOD:-30}"
  single_mimir BadValidatorRedline 0

  log "milestone: register joiners"
  for index in $(seq 2 "$NODE_COUNT"); do
    log "set node keys/ip/version node $index"
    addr=$(remote_node_address "$index")
    remote_tx 1 "send $addr ${joiner_fee_fund}rune --gas auto" | grep -E 'code:|txhash:' || true
    sleep 8
    remote_tx 1 "deposit $bootstrap_bond THOR.RUNE BOND:$addr --gas auto" | grep -E 'code:|txhash:' || true
    sleep 8
    remote_set_node_keys "$index"
    sleep 8
    remote_tx "$index" "set-ip-address $(inventory_ip "$index") --gas auto" | grep -E 'code:|txhash:' || true
    sleep 8
    remote_tx "$index" "set-version --gas auto" | grep -E 'code:|txhash:' || true
    sleep 8
  done
  remote_tx 1 "deposit $bootstrap_bond THOR.RUNE BOND:$(node_field 1 address) --gas auto" | grep -E 'code:|txhash:' || true
  remote_restart_bifrosts
  wait_after_bifrost_restart

  log "milestone: pre-GG20 ready"
  wait_shape "pre-GG20-ready" 1 "$active_count" 120
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  single_mimir HaltChurning 0
  gg20_vault=$(wait_for_vault_change "GG20 vault" "" 120)
  log "GG20 vault=$gg20_vault"
  quorum_mimir HaltChurning 1
  record_tss_timing keygen gg20 "GG20 initial" "$start" "$gg20_vault"
  if [ "${FROST_STOP_AFTER_GG20_KEYGEN:-0}" = "1" ]; then
    quorum_mimir HaltChurning 1
    log "stopping after GG20 keygen as requested: $OUT_DIR/tss-timings.csv"
    return
  fi
  wait_vault_shape "post-GG20" "$active_count" 120
  top_up_active_bonds 5000000000000
  quorum_mimir DesiredValidatorSet "$active_count"
  quorum_mimir AsgardSize "$active_count"
  quorum_mimir NumberOfNewNodesPerChurn 1
  quorum_mimir MaxMissingBlockChurnOut 1
  quorum_mimir MaxNodeToChurnOutForLowVersion 1
  quorum_mimir BadValidatorRedline 0

  seed_btc_network_fee
  wait_btc_network_fee 120
  wait_btc_scanners_healthy 10 || log "WARN: BTC scanner health did not fully settle before LP funding"
  inbound=$(btc_inbound_address)
  btc_txid=$(add_btc_lp "$inbound")
  wait_btc_lp_observed "$gg20_vault" "$btc_txid" 240
  wait_btc_scanners_healthy 10 || log "WARN: BTC scanner health did not fully settle before FROST churn"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  quorum_mimir EnableFrostBTC 1
  quorum_mimir DisableXMRMonovaultKeygen 1
  quorum_mimir HaltChurning 0
  frost1_vault=$(wait_for_vault_change "FROST1 vault" "$gg20_vault" 160)
  log "FROST1 vault=$frost1_vault"
  record_tss_timing keygen frost "FROST first" "$start" "$frost1_vault"
  if [ "${FROST_STOP_AFTER_FROST_KEYGEN:-0}" = "1" ]; then
    quorum_mimir HaltChurning 1
    log "stopping after FROST keygen as requested: $OUT_DIR/tss-timings.csv"
    return
  fi
  gg20_migration_txid=$(wait_migration_broadcast "$start" "$gg20_vault" || true)
  log "GG20 migration tx=${gg20_migration_txid:-none}"
  record_tss_timing keysign gg20 "GG20 migration" "$start" "$gg20_vault"
  if [ "${FROST_STOP_AFTER_GG20_KEYSIGN:-0}" = "1" ]; then
    quorum_mimir HaltChurning 1
    log "stopping after GG20 keysign as requested: $OUT_DIR/tss-timings.csv"
    return
  fi
  wait_retiring_vault_empty "$gg20_vault" "GG20 migration" 240

  quorum_mimir HaltChurning 1
  wait_vault_shape "pre-FROST2" "$active_count" 160
  wait_btc_scanners_healthy 10 || log "WARN: BTC scanner health did not fully settle before FROST2 churn"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  quorum_mimir HaltChurning 0
  frost2_vault=$(wait_for_vault_change "FROST2 vault" "$frost1_vault" 160)
  log "FROST2 vault=$frost2_vault"
  record_tss_timing keygen frost "FROST second" "$start" "$frost2_vault"
  frost_migration_txid=$(wait_migration_broadcast "$start" "$frost1_vault" || true)
  log "FROST migration tx=${frost_migration_txid:-none}"
  record_tss_timing keysign frost "FROST migration" "$start" "$frost1_vault"
  wait_retiring_vault_empty "$frost1_vault" "FROST migration" 240
  quorum_mimir HaltChurning 1
  collect_keyshare_sizes
  write_final_report
  log "timing complete: $OUT_DIR/tss-timings.csv"
}

deploy() {
  ensure_node_meta
  if [ -z "$BTC_HOST" ]; then
    require_hcloud_ssh_key
    btc_ip=$(create_hcloud_btc_node)
    remote_start_bitcoin "$btc_ip"
  fi
  plan_inventory
  log "inventory: $INVENTORY"

  while IFS=$'\t' read -r index provider name ip provider_id; do
    [ "$index" = "index" ] && continue
    log "prepare node $index $provider $ip"
    remote_prepare "$ip" &
  done <"$INVENTORY"
  wait

  while IFS=$'\t' read -r index provider name ip provider_id; do
    [ "$index" = "index" ] && continue
    remote_write_compose "$index" "$ip"
  done <"$INVENTORY"

  log "start genesis $(inventory_ip 1)"
  remote_start_node 1 "$(inventory_ip 1)"
  until curl -fsS --max-time 3 "http://$(inventory_ip 1):27147/health" >/dev/null 2>&1; do
    sleep 5
  done

  while IFS=$'\t' read -r index provider name ip provider_id; do
    [ "$index" = "index" ] && continue
    [ "$index" = "1" ] && continue
    log "start validator node $index $ip"
    remote_start_node "$index" "$ip" &
  done <"$INVENTORY"
  wait
  log "multihost deploy started"
  log "genesis API: http://$(inventory_ip 1):1317"
  log "genesis RPC: http://$(inventory_ip 1):27147"
}

deploy_existing_inventory() {
  ensure_node_meta
  if [ -z "$BTC_HOST" ]; then
    require_hcloud_ssh_key
    btc_ip=$(create_hcloud_btc_node)
    remote_start_bitcoin "$btc_ip"
  fi
  [ -f "$INVENTORY" ] || {
    echo "ERROR: missing inventory $INVENTORY" >&2
    exit 1
  }
  log "reuse inventory: $INVENTORY"

  while IFS=$'\t' read -r index provider name ip provider_id; do
    [ "$index" = "index" ] && continue
    log "prepare node $index $provider $ip"
    remote_prepare "$ip" &
  done <"$INVENTORY"
  wait

  while IFS=$'\t' read -r index provider name ip provider_id; do
    [ "$index" = "index" ] && continue
    remote_write_compose "$index" "$ip"
  done <"$INVENTORY"

  log "start genesis $(inventory_ip 1)"
  remote_start_node 1 "$(inventory_ip 1)"
  until curl -fsS --max-time 3 "http://$(inventory_ip 1):27147/health" >/dev/null 2>&1; do
    sleep 5
  done

  while IFS=$'\t' read -r index provider name ip provider_id; do
    [ "$index" = "index" ] && continue
    [ "$index" = "1" ] && continue
    log "start validator node $index $ip"
    remote_start_node "$index" "$ip" &
  done <"$INVENTORY"
  wait
  log "multihost deploy started"
  log "genesis API: http://$(inventory_ip 1):1317"
  log "genesis RPC: http://$(inventory_ip 1):27147"
}

destroy() {
  [ -f "$INVENTORY" ] || {
    echo "ERROR: missing inventory $INVENTORY" >&2
    exit 1
  }
  while IFS=$'\t' read -r index provider name ip provider_id; do
    [ "$index" = "index" ] && continue
    case "$provider" in
    hcloud) hcloud server delete "$name" >/dev/null || true ;;
    do) do_api DELETE "/droplets/$provider_id" >/dev/null || true ;;
    esac
    log "deleted node $index $provider $name"
  done <"$INVENTORY"
  if [ "${FROST_DESTROY_BTC:-0}" = "1" ] && hcloud server describe "$BTC_SERVER_NAME" >/dev/null 2>&1; then
    hcloud server delete "$BTC_SERVER_NAME" >/dev/null || true
    log "deleted bitcoin regtest $BTC_SERVER_NAME"
  fi
}

matrix() {
  local count
  for count in ${FROST_MULTIHOST_COUNTS:-7 14 21 28}; do
    case "$count" in
    7 | 14 | 21 | 28) ;;
    *)
      echo "ERROR: unsupported matrix count $count" >&2
      exit 1
      ;;
    esac
    log "============================================"
    log "FROST multihost timing matrix count=$count"
    log "============================================"
    FROST_NODE_COUNT="$count" FROST_DESTROY_BTC=0 "$0" destroy || true
    FROST_NODE_COUNT="$count" FROST_MULTIHOST_RESET_REMOTE_STATE=1 "$0" deploy-timing
    FROST_NODE_COUNT="$count" FROST_DESTROY_BTC=0 "$0" destroy || true
  done
}

resume_after_gg20() {
  [ -f "$INVENTORY" ] || {
    echo "ERROR: missing inventory $INVENTORY" >&2
    exit 1
  }
  local active_count=$((NODE_COUNT - 1))
  local gg20_vault frost1_vault frost2_vault start gg20_migration_txid frost_migration_txid
  mkdir -p "$OUT_DIR"
  gg20_vault=$(active_vault_pubkey)
  [ -n "$gg20_vault" ] || {
    echo "ERROR: no active GG20 vault found" >&2
    exit 1
  }
  log "resume after GG20 vault=$gg20_vault"
  wait_vault_shape "post-GG20-resume" "$active_count" 60
  wait_btc_scanners_healthy 10 || log "WARN: BTC scanner health did not fully settle before FROST churn"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  quorum_mimir EnableFrostBTC 1
  quorum_mimir DisableXMRMonovaultKeygen 1
  quorum_mimir HaltChurning 0
  frost1_vault=$(wait_for_vault_change "FROST1 vault" "$gg20_vault" 160)
  log "FROST1 vault=$frost1_vault"
  record_tss_timing keygen frost "FROST first" "$start" "$frost1_vault"
  if [ "${FROST_STOP_AFTER_FROST_KEYGEN:-0}" = "1" ]; then
    quorum_mimir HaltChurning 1
    log "stopping after FROST keygen as requested: $OUT_DIR/tss-timings.csv"
    return
  fi
  gg20_migration_txid=$(wait_migration_broadcast "$start" "$gg20_vault" || true)
  record_tss_timing keysign gg20 "GG20 migration" "$start" "$gg20_vault"
  wait_retiring_vault_empty "$gg20_vault" "GG20 migration" 240

  quorum_mimir HaltChurning 1
  wait_vault_shape "pre-FROST2" "$active_count" 160
  wait_btc_scanners_healthy 10 || log "WARN: BTC scanner health did not fully settle before FROST2 churn"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  quorum_mimir HaltChurning 0
  frost2_vault=$(wait_for_vault_change "FROST2 vault" "$frost1_vault" 160)
  log "FROST2 vault=$frost2_vault"
  record_tss_timing keygen frost "FROST second" "$start" "$frost2_vault"
  frost_migration_txid=$(wait_migration_broadcast "$start" "$frost1_vault" || true)
  record_tss_timing keysign frost "FROST migration" "$start" "$frost1_vault"
  wait_retiring_vault_empty "$frost1_vault" "FROST migration" 240
  quorum_mimir HaltChurning 1
  collect_keyshare_sizes
  write_final_report
  log "timing complete: $OUT_DIR/tss-timings.csv"
}

resume_after_frost1() {
  [ -f "$INVENTORY" ] || {
    echo "ERROR: missing inventory $INVENTORY" >&2
    exit 1
  }
  local active_count=$((NODE_COUNT - 1))
  local gg20_vault frost1_vault frost2_vault start frost_migration_txid
  mkdir -p "$OUT_DIR"
  gg20_vault=$(api /thorchain/vaults/asgard | jq -r '[.[] | select(.status=="RetiringVault") | (.pub_key | if type=="object" then .value else . end)] | .[0] // ""')
  frost1_vault=$(active_vault_pubkey)
  [ -n "$gg20_vault" ] || {
    echo "ERROR: no retiring GG20 vault found" >&2
    exit 1
  }
  [ -n "$frost1_vault" ] || {
    echo "ERROR: no active FROST1 vault found" >&2
    exit 1
  }
  log "resume after FROST1 gg20=$gg20_vault frost1=$frost1_vault"
  record_tss_timing keygen frost "FROST first" "$(date -u -v-30M +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%SZ)" "$frost1_vault"
  record_tss_timing keysign gg20 "GG20 migration" "$(date -u -v-30M +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%SZ)" "$gg20_vault"
  wait_retiring_vault_empty "$gg20_vault" "GG20 migration" 240

  quorum_mimir HaltChurning 1
  wait_vault_shape "pre-FROST2" "$active_count" 160
  wait_btc_scanners_healthy 10 || log "WARN: BTC scanner health did not fully settle before FROST2 churn"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  quorum_mimir HaltChurning 0
  frost2_vault=$(wait_for_vault_change "FROST2 vault" "$frost1_vault" 160)
  log "FROST2 vault=$frost2_vault"
  record_tss_timing keygen frost "FROST second" "$start" "$frost2_vault"
  frost_migration_txid=$(wait_migration_broadcast "$start" "$frost1_vault" || true)
  record_tss_timing keysign frost "FROST migration" "$start" "$frost1_vault"
  wait_retiring_vault_empty "$frost1_vault" "FROST migration" 240
  quorum_mimir HaltChurning 1
  collect_keyshare_sizes
  write_final_report
  log "timing complete: $OUT_DIR/tss-timings.csv"
}

case "${1:-deploy}" in
-h | --help | help) usage ;;
deploy) deploy ;;
timing) multihost_timing ;;
deploy-timing)
  deploy
  multihost_timing
  ;;
deploy-existing-timing)
  deploy_existing_inventory
  multihost_timing
  ;;
destroy) destroy ;;
matrix) matrix ;;
resume-after-gg20) resume_after_gg20 ;;
resume-after-frost1) resume_after_frost1 ;;
*)
  usage >&2
  exit 1
  ;;
esac
