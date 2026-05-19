#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NODE_COUNT="${DKLS_NODE_COUNT:-7}"
RUN_STAMP="${RUN_STAMP:-$(date -u +%Y%m%dT%H%M%SZ)}"
BRANCH="${DKLS_GIT_BRANCH:-feat-dkls}"
GIT_URL="${DKLS_GIT_URL:-https://gitlab.com/thorchain/thornode.git}"
REMOTE_REPO_DIR="${REMOTE_REPO_DIR:-/opt/dkls-thornode}"
NODE_NAME_PREFIX="${DKLS_NODE_NAME_PREFIX:-dkls-thornode}"
OUT_DIR="${DKLS_MULTIHOST_OUT_DIR:-$SCRIPT_DIR/dkls-chainnet-results/multihost-${NODE_COUNT}}"
INVENTORY="$OUT_DIR/inventory.tsv"
NODE_META="$SCRIPT_DIR/dkls-chainnet-generated/nodes-${NODE_COUNT}.tsv"
DOGE_HOST="${DOGE_HOST:-5.223.48.34:18332}"
HCLOUD_ENABLED="${DKLS_HCLOUD_ENABLED:-${HCLOUD:-ON}}"
DO_ENABLED="${DKLS_DO_ENABLED:-${DO:-OFF}}"

HCLOUD_SERVER_LIMIT="${DKLS_HCLOUD_SERVER_LIMIT:-10}"
HCLOUD_PRIMARY_IP_LIMIT="${DKLS_HCLOUD_PRIMARY_IP_LIMIT:-20}"
HCLOUD_RESERVED_SERVER_SLOTS="${DKLS_HCLOUD_RESERVED_SERVER_SLOTS:-0}"
HCLOUD_RESERVED_PRIMARY_IP_SLOTS="${DKLS_HCLOUD_RESERVED_PRIMARY_IP_SLOTS:-0}"
HCLOUD_LOCATION="${DKLS_HCLOUD_LOCATION:-sin}"
HCLOUD_IMAGE="${DKLS_HCLOUD_IMAGE:-ubuntu-24.04}"
DO_REGION="${DKLS_DO_REGION:-sgp1}"
DO_IMAGE="${DKLS_DO_IMAGE:-ubuntu-24-04-x64}"
DO_SSH_KEY="${DKLS_DO_SSH_KEY-}"
RESET_REMOTE_STATE="${DKLS_MULTHOST_RESET_REMOTE_STATE:-${DKLS_MULTIHOST_RESET_REMOTE_STATE:-0}}"

case "$NODE_COUNT" in
7 | 14 | 21 | 28) ;;
*)
  echo "ERROR: DKLS_NODE_COUNT must be one of 7,14,21,28" >&2
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
  build/docker/dkls-chainnet-multihost-deploy.sh [deploy|timing|deploy-timing|destroy|matrix]

Multi-host GG20 -> DKLS timing evaluation. Each THORNode runs on its own
server. Supports 7, 14, 21, and 28 node runs.

Commands:
  deploy         Provision/reuse deterministic servers and start containers.
  timing         Run timing flow against an already deployed inventory.
  deploy-timing  Deploy, then run timing flow.
  deploy-existing-timing
                Start from an existing inventory, then run timing flow.
  destroy        Delete servers from the current inventory.
  matrix         Run deploy-timing for DKLS_MULTIHOST_COUNTS, destroying after each run.

Provider env:
  HCLOUD=ON|OFF
  DO=ON|OFF
  DKLS_HCLOUD_SSH_KEY=dkls-codex
  DKLS_DO_SSH_KEY=<digitalocean ssh key id>

Scale env:
  DKLS_NODE_COUNT=7|14|21|28
  DKLS_MULTIHOST_COUNTS="7 14 21 28"
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
  if [ -n "${DKLS_HCLOUD_AVAILABLE_SLOTS-}" ]; then
    echo "$DKLS_HCLOUD_AVAILABLE_SLOTS"
    return
  fi
  if ! hcloud server list -o json >/tmp/dkls-hcloud-servers.json 2>/dev/null; then
    echo 0
    return
  fi
  if ! hcloud primary-ip list -o json >/tmp/dkls-hcloud-primary-ips.json 2>/dev/null; then
    echo 0
    return
  fi
  local servers primary_ips server_slots ip_slots
  servers=$(json_len_or_zero </tmp/dkls-hcloud-servers.json)
  primary_ips=$(json_len_or_zero </tmp/dkls-hcloud-primary-ips.json)
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
ERROR: DKLS_DO_SSH_KEY is required for DigitalOcean multihost deploys.
Register the local public key once, then pass the numeric key ID:

  doctl compute ssh-key import dkls-codex --public-key-file ~/.ssh/id_ed25519.pub
  DKLS_DO_SSH_KEY=<id> build/docker/dkls-chainnet-multihost-deploy.sh deploy

Do not rely on cloud-init root user_data here. These droplets can force an
expired root-password change before non-interactive SSH, which blocks bootstrap.
EOF
  exit 1
}

require_hcloud_ssh_key() {
  if [ -n "${DKLS_HCLOUD_SSH_KEY-}" ]; then
    return
  fi
  cat >&2 <<'EOF'
ERROR: DKLS_HCLOUD_SSH_KEY is required for Hetzner multihost deploys.

  HCLOUD=ON DO=OFF DKLS_HCLOUD_SSH_KEY=dkls-codex build/docker/dkls-chainnet-multihost-deploy.sh deploy
EOF
  exit 1
}

hcloud_type_for_node() {
  echo "${DKLS_HCLOUD_NODE_TYPE:-cpx32}"
}

do_size_for_node() {
  echo "${DKLS_DO_NODE_SIZE:-s-4vcpu-8gb}"
}

ssh_user_data() {
  local pubkey_file="${DKLS_SSH_PUBLIC_KEY_FILE:-$HOME/.ssh/id_ed25519.pub}"
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
  if hcloud server describe "$name" -o json >/tmp/dkls-hcloud-node.json 2>/dev/null; then
    ip=$(jq -r '.public_net.ipv4.ip // ""' </tmp/dkls-hcloud-node.json)
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
    --ssh-key "$DKLS_HCLOUD_SSH_KEY"
  )
  hcloud "${args[@]}" >/dev/null
  ip=$(hcloud server ip "$name")
  [ -n "$ip" ] || {
    echo "ERROR: created $name but could not resolve IPv4" >&2
    return 1
  }
  printf '%s\t%s\t%s\n' hcloud "$name" "$ip"
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
    '{name:$name, region:$region, size:$size, image:$image, monitoring:true, tags:["dkls-multihost"]}
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
    DKLS_NODE_COUNT="$NODE_COUNT" DKLS_FORCE_BUILD="${DKLS_FORCE_BUILD:-1}" "$SCRIPT_DIR/dkls-chainnet-generate-compose.sh" "$NODE_COUNT"
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

node_index_by_address() {
  awk -F '\t' -v addr="$1" 'NR>1 && $4==addr {print $1; exit}' "$NODE_META"
}

ssh_raw() {
  local ip="$1"
  shift
  for _ in $(seq 1 20); do
    if ssh -n -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ConnectTimeout=10 "root@$ip" "$@"; then
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
    if ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ConnectTimeout=10 "root@$ip" "$@"; then
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
        TAG: chainnet
    image: thornode:dkls-chainnet
    restart: unless-stopped
    hostname: thornode
    environment:
      NET: chainnet
      CHAIN_ID: thorchain
      SIGNER_NAME: thorchain
      SIGNER_PASSWD: password
      SIGNER_SEED_PHRASE: "$seed"
      THORNODE_GRPC_ENABLE: "true"
      THOR_COSMOS_EBIFROST_ADDRESS: "0.0.0.0:50051"
      THOR_BLOCK_TIME: "6s"
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
      THOR_CHAINNET_CHURN_RETRY_INTERVAL: "${DKLS_CHAINNET_CHURN_RETRY_INTERVAL:-180}"
      THOR_CHAINNET_KEYGEN_RETRY_INTERVAL: "${DKLS_CHAINNET_KEYGEN_RETRY_INTERVAL:-120}"
      THOR_CHAINNET_JAIL_TIME_KEYGEN: "${DKLS_CHAINNET_JAIL_TIME_KEYGEN:-181}"
      THOR_CHAINNET_JAIL_TIME_KEYSIGN: "${DKLS_CHAINNET_JAIL_TIME_KEYSIGN:-181}"
      THOR_CHAINNET_SIGNING_TRANSACTION_PERIOD: "30"
      EXTRA_FUND_ACCOUNT_COUNT: "$((NODE_COUNT - 1))"
      FAUCET: "$(node_field 1 address)"
$(fund_env | sed 's/^/      /')
      BIFROST_CHAINS_DOGE_DISABLED: "false"
      BIFROST_CHAINS_BTC_DISABLED: "true"
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
      BIFROST_CHAINS_SUI_DISABLED: "true"
      BIFROST_CHAINS_ADA_DISABLED: "true"
    ports:
      - "27146:27146"
      - "27147:27147"
      - "1317:1317"
      - "50051:50051"
      - "9090:9090"
    volumes:
      - "../scripts:/docker/scripts"
      - thornode:/root/.thornode
    entrypoint: /docker/scripts/${role}.sh
    command: ["thornode", "start"]
  bifrost:
    image: thornode:dkls-chainnet
    restart: unless-stopped
    depends_on:
      - thornode
    environment:
      NET: chainnet
      CHAIN_ID: thorchain
      SIGNER_NAME: thorchain
      SIGNER_PASSWD: password
      SIGNER_SEED_PHRASE: "$seed"
      CHAIN_API: thornode:1317
      CHAIN_RPC: thornode:27147
      BIFROST_THORCHAIN_CHAIN_EBIFROST: thornode:50051
      THOR_SEED_NODES_ENDPOINT: ""
      THOR_BLOCK_TIME: "6s"
      BLOCK_SCANNER_BACKOFF: "5s"
      BIFROST_METRICS_PPROF_ENABLED: "true"
      BIFROST_SIGNER_BACKUP_KEYSHARES: "false"
      BIFROST_SIGNER_RESCHEDULE_BUFFER_BLOCKS: "20"
      BIFROST_SIGNER_AUTO_OBSERVE: "false"
      BIFROST_SIGNER_KEYGEN_TIMEOUT: "${DKLS_BIFROST_KEYGEN_TIMEOUT:-300s}"
      BIFROST_SIGNER_KEYSIGN_TIMEOUT: "${DKLS_BIFROST_KEYSIGN_TIMEOUT:-300s}"
      BIFROST_SIGNER_PARTY_TIMEOUT: "55s"
      THOR_CHAINNET_CHURN_RETRY_INTERVAL: "${DKLS_CHAINNET_CHURN_RETRY_INTERVAL:-180}"
      THOR_CHAINNET_KEYGEN_RETRY_INTERVAL: "${DKLS_CHAINNET_KEYGEN_RETRY_INTERVAL:-120}"
      THOR_CHAINNET_JAIL_TIME_KEYGEN: "${DKLS_CHAINNET_JAIL_TIME_KEYGEN:-181}"
      THOR_CHAINNET_JAIL_TIME_KEYSIGN: "${DKLS_CHAINNET_JAIL_TIME_KEYSIGN:-181}"
      BIFROST_TSS_EXTERNAL_IP: "$public_ip"
      BIFROST_TSS_BOOTSTRAP_PEERS: "$seeds"
      PEER: "$seeds"
      LD_LIBRARY_PATH: /usr/lib:/usr/local/lib
      DOGE_HOST: "$DOGE_HOST"
      BIFROST_CHAINS_DOGE_CHAIN_NETWORK: mocknet
      BIFROST_CHAINS_DOGE_BLOCK_SCANNER_START_BLOCK_HEIGHT: "0"
      BIFROST_CHAINS_DOGE_BLOCK_SCANNER_MAX_HEALTHY_LAG: "${DKLS_DOGE_MAX_HEALTHY_LAG:-8h}"
      BIFROST_CHAINS_DOGE_DISABLED: "false"
      BIFROST_CHAINS_BTC_DISABLED: "true"
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
  thornode:
  bifrost:
YAML
}

remote_prepare() {
  local ip="$1"
  ssh-keygen -R "$ip" >/dev/null 2>&1 || true
  until ssh -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ConnectTimeout=5 "root@$ip" true 2>/dev/null; do
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
python3 - <<'PY'
from pathlib import Path

path = Path("x/thorchain/manager_validator_current.go")
text = path.read_text()
old = """\tnewNode, err := vm.k.GetMimir(ctx, constants.NumberOfNewNodesPerChurn.String())
\tif err != nil || newNode <= 0 {
\t\tnewNode = 1
\t}
"""
new = """\tnewNode := vm.k.GetConfigInt64(ctx, constants.NumberOfNewNodesPerChurn)
\tif newNode <= 0 {
\t\tnewNode = 1
\t}
"""
if old in text:
    path.write_text(text.replace(old, new))
PY
REMOTE
}

remote_write_compose() {
  local index="$1"
  local ip="$2"
  local seed public_ip genesis_ip seeds
  seed=$(node_field "$index" seed)
  public_ip=$(inventory_ip "$index")
  genesis_ip=$(inventory_ip 1)
  seeds=$(prior_ips "$index")
  if [ "$index" = "1" ]; then
    seeds=""
  elif [ -n "$seeds" ]; then
    seeds="$genesis_ip,$seeds"
  else
    seeds="$genesis_ip"
  fi
  if [ "$RESET_REMOTE_STATE" = "1" ]; then
    ssh_raw "$ip" "cd $REMOTE_REPO_DIR/build/docker 2>/dev/null && docker compose -f docker-compose.dkls-chainnet-multihost.yml down -v --remove-orphans || true"
  fi
  ssh_raw "$ip" "mkdir -p $REMOTE_REPO_DIR/build/docker"
  compose_for_node "$index" "$seed" "$public_ip" "$genesis_ip" "$seeds" | ssh_raw_stdin "$ip" "cat > $REMOTE_REPO_DIR/build/docker/docker-compose.dkls-chainnet-multihost.yml"
}

remote_start_node() {
  local index="$1"
  local ip="$2"
  ssh_raw "$ip" "cd $REMOTE_REPO_DIR/build/docker && docker compose -f docker-compose.dkls-chainnet-multihost.yml up -d --build"
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
  ssh_raw "$ip" "cd $REMOTE_REPO_DIR/build/docker && docker compose -f docker-compose.dkls-chainnet-multihost.yml ps || true; docker ps -a || true; docker logs --tail 120 docker-thornode-1 2>&1 || true"
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

doge_inbound_address() {
  api /thorchain/inbound_addresses | jq -r '.[] | select(.chain=="DOGE") | .address'
}

ssh_node() {
  local index="$1"
  shift
  ssh_raw "$(inventory_ip "$index")" "$@"
}

ssh_node_once() {
  local index="$1"
  shift
  ssh -n -o UserKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o ConnectTimeout=10 "root@$(inventory_ip "$index")" "$@"
}

remote_tx() {
  local index="$1"
  shift
  ssh_node_once "$index" "timeout 60 docker exec docker-thornode-1 bash -lc 'printf \"password\\n\" | thornode tx thorchain $* --from thorchain --keyring-backend file --chain-id thorchain --yes --node tcp://localhost:27147'" 2>&1 || true
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
    printf \"password\\n\" | thornode tx thorchain set-node-keys \"\$NODE_PUB_KEY\" \"\$NODE_PUB_KEY_ED25519\" \"\$VALIDATOR\" --from thorchain --keyring-backend file --chain-id thorchain --node tcp://localhost:27147 --yes --gas auto
  '" 2>&1 | grep -E 'code:|txhash:' || true
}

remote_restart_bifrosts() {
  local index
  for index in $(seq 1 "$NODE_COUNT"); do
    log "restart bifrost node $index"
    ssh_node "$index" "cd $REMOTE_REPO_DIR/build/docker && docker compose -f docker-compose.dkls-chainnet-multihost.yml restart bifrost" >/dev/null
    sleep 5
  done
}

wait_after_bifrost_restart() {
  local hold="${DKLS_TSS_SYNC_HOLD:-180}"
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

doge_rpc() {
  local method="$1"
  local params="${2:-[]}"
  curl -fsS --user "${DOGE_RPC_USER:-thorchain}:${DOGE_RPC_PASSWORD:-password}" \
    --data-binary "{\"jsonrpc\":\"1.0\",\"id\":\"chainnet\",\"method\":\"$method\",\"params\":$params}" \
    -H 'content-type:text/plain;' "http://$DOGE_HOST/" | jq -r '.result'
}

add_doge_lp() {
  local inbound="$1"
  local memo
  local memo_hex raw funded signed txid
  memo="ADD:DOGE.DOGE:$(node_field 1 address)"
  memo_hex=$(printf "%s" "$memo" | xxd -p -c 256)
  log "fund DOGE LP inbound=$inbound memo=$memo"
  raw=$(doge_rpc createrawtransaction "[[],{\"$inbound\":10000,\"data\":\"$memo_hex\"}]")
  funded=$(doge_rpc fundrawtransaction "[\"$raw\"]" | jq -r '.hex')
  signed=$(doge_rpc signrawtransaction "[\"$funded\"]" | jq -r '.hex')
  txid=$(doge_rpc sendrawtransaction "[\"$signed\"]")
  doge_rpc generatetoaddress '[6,"mnaioCtEGdw6bd6rWJ13Mbre1kN5rPa2Mo"]' >/dev/null
  log "DOGE LP tx=$txid"
  printf '%s\n' "$txid"
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

active_vault_doge_amount() {
  local vault="$1"
  api /thorchain/vaults/asgard |
    jq -r --arg vault "$vault" '
      [.[] | select(.status=="ActiveVault") | select((.pub_key | if type=="object" then .value else . end)==$vault) |
        ((.coins // []) | map(select(.asset=="DOGE.DOGE") | .amount | tonumber) | add // 0)] | .[0] // 0'
}

retiring_vault_doge_amount() {
  local vault="$1"
  api /thorchain/vaults/asgard |
    jq -r --arg vault "$vault" '
      [.[] | select(.status=="RetiringVault") | select((.pub_key | if type=="object" then .value else . end)==$vault) |
        ((.coins // []) | map(select(.asset=="DOGE.DOGE") | .amount | tonumber) | add // 0)] | .[0] // 0'
}

wait_doge_lp_observed() {
  local vault="$1" txid="$2" max_checks="${3:-180}"
  local i amount signers upper_txid
  upper_txid=$(tr '[:lower:]' '[:upper:]' <<<"$txid")
  for i in $(seq 1 "$max_checks"); do
    amount=$(active_vault_doge_amount "$vault")
    signers=$(api "/thorchain/tx/details/$upper_txid" 2>/dev/null | jq -r '(.txs[0].signers // []) | length' 2>/dev/null || echo 0)
    log "[$i/$max_checks] DOGE LP observed amount=${amount:-0} signers=${signers:-0} vault=$vault"
    [ "${amount:-0}" != "0" ] && return 0
    sleep 10
  done
  return 1
}

wait_doge_scanners_healthy() {
  local max_checks="${1:-120}"
  local i unhealthy index
  for i in $(seq 1 "$max_checks"); do
    unhealthy=0
    for index in $(seq 1 "$NODE_COUNT"); do
      if api /thorchain/nodes | jq -e --arg addr "$(node_field "$index" address)" '.[] | select(.node_address==$addr and .status=="Active")' >/dev/null; then
        if ! ssh_node_once "$index" "docker logs --tail 160 docker-bifrost-1 2>&1 | grep 'scan block' | grep 'chain=.\{0,8\}DOGE' | tail -1 | grep -Eq 'healthy=.{0,16}true'"; then
          unhealthy=$((unhealthy + 1))
        fi
      fi
    done
    log "[$i/$max_checks] DOGE active scanner unhealthy=$unhealthy"
    [ "$unhealthy" = 0 ] && return 0
    sleep 10
  done
  return 1
}

wait_retiring_vault_empty() {
  local vault="$1" label="$2" max_checks="${3:-180}"
  local i amount
  for i in $(seq 1 "$max_checks"); do
    amount=$(retiring_vault_doge_amount "$vault")
    log "[$i/$max_checks] $label retiring DOGE amount=${amount:-0}"
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
start_pat = f"{engine.upper()} {phase} ceremony leader started"
stop_pat = f"{engine.upper()} {phase} ceremony party produced"
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
  local index addr gg20_vault dkls1_vault dkls2_vault inbound start doge_txid gg20_migration_txid dkls_migration_txid
  mkdir -p "$OUT_DIR"
  log "timing output: $OUT_DIR/tss-timings.csv"

  log "milestone: genesis health"
  until curl -fsS --max-time 3 "http://$(inventory_ip 1):27147/health" >/dev/null; do sleep 5; done
  single_mimir HaltChurning 1
  single_mimir P2PGateDisabled 1
  single_mimir DesiredValidatorSet "$active_count"
  single_mimir AsgardSize "$active_count"
  single_mimir NumberOfNewNodesPerChurn "$bootstrap_new"
  single_mimir ChurnRetryInterval "${DKLS_CHAINNET_CHURN_RETRY_INTERVAL:-180}"
  single_mimir KeygenRetryInterval "${DKLS_CHAINNET_KEYGEN_RETRY_INTERVAL:-120}"
  single_mimir JailTimeKeygen "${DKLS_CHAINNET_JAIL_TIME_KEYGEN:-181}"
  single_mimir JailTimeKeysign "${DKLS_CHAINNET_JAIL_TIME_KEYSIGN:-181}"
  single_mimir BadValidatorRedline 0

  log "milestone: register joiners"
  for index in $(seq 2 "$NODE_COUNT"); do
    log "set node keys/ip/version node $index"
    addr=$(remote_node_address "$index")
    remote_tx "$index" "deposit 70000000000000 THOR.RUNE BOND:$addr --gas auto" | grep -E 'code:|txhash:' || true
    sleep 8
    remote_set_node_keys "$index"
    sleep 8
    remote_tx "$index" "set-ip-address $(inventory_ip "$index") --gas auto" | grep -E 'code:|txhash:' || true
    sleep 8
    remote_tx "$index" "set-version --gas auto" | grep -E 'code:|txhash:' || true
    sleep 8
  done
  remote_tx 1 "deposit 70000000000000 THOR.RUNE BOND:$(node_field 1 address) --gas auto" | grep -E 'code:|txhash:' || true
  remote_restart_bifrosts
  wait_after_bifrost_restart

  log "milestone: pre-GG20 shape"
  wait_shape "pre-GG20" 1 "$active_count" 120
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  single_mimir HaltChurning 0
  gg20_vault=$(wait_for_vault_change "GG20 vault" "" 120)
  log "GG20 vault=$gg20_vault"
  quorum_mimir HaltChurning 1
  record_tss_timing keygen gg20 "GG20 initial" "$start" "$gg20_vault"
  if [ "${DKLS_STOP_AFTER_GG20_KEYGEN:-0}" = "1" ]; then
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

  inbound=$(doge_inbound_address)
  doge_txid=$(add_doge_lp "$inbound")
  wait_doge_lp_observed "$gg20_vault" "$doge_txid" 240
  wait_doge_scanners_healthy 10 || log "WARN: DOGE scanner health did not fully settle before DKLS churn"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  quorum_mimir EnableDKLS 1
  quorum_mimir HaltChurning 0
  dkls1_vault=$(wait_for_vault_change "DKLS1 vault" "$gg20_vault" 160)
  log "DKLS1 vault=$dkls1_vault"
  record_tss_timing keygen dkls "DKLS first" "$start" "$dkls1_vault"
  if [ "${DKLS_STOP_AFTER_DKLS_KEYGEN:-0}" = "1" ]; then
    quorum_mimir HaltChurning 1
    log "stopping after DKLS keygen as requested: $OUT_DIR/tss-timings.csv"
    return
  fi
  gg20_migration_txid=$(wait_migration_broadcast "$start" "$gg20_vault" || true)
  log "GG20 migration tx=${gg20_migration_txid:-none}"
  record_tss_timing keysign gg20 "GG20 migration" "$start" "$gg20_vault"
  if [ "${DKLS_STOP_AFTER_GG20_KEYSIGN:-0}" = "1" ]; then
    quorum_mimir HaltChurning 1
    log "stopping after GG20 keysign as requested: $OUT_DIR/tss-timings.csv"
    return
  fi
  wait_retiring_vault_empty "$gg20_vault" "GG20 migration" 240

  quorum_mimir HaltChurning 1
  wait_vault_shape "pre-DKLS2" "$active_count" 160
  wait_doge_scanners_healthy 10 || log "WARN: DOGE scanner health did not fully settle before DKLS2 churn"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  quorum_mimir HaltChurning 0
  dkls2_vault=$(wait_for_vault_change "DKLS2 vault" "$dkls1_vault" 160)
  log "DKLS2 vault=$dkls2_vault"
  record_tss_timing keygen dkls "DKLS second" "$start" "$dkls2_vault"
  dkls_migration_txid=$(wait_migration_broadcast "$start" "$dkls1_vault" || true)
  log "DKLS migration tx=${dkls_migration_txid:-none}"
  record_tss_timing keysign dkls "DKLS migration" "$start" "$dkls1_vault"
  wait_retiring_vault_empty "$dkls1_vault" "DKLS migration" 240
  quorum_mimir HaltChurning 1
  log "timing complete: $OUT_DIR/tss-timings.csv"
}

deploy() {
  ensure_node_meta
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
}

matrix() {
  local count
  for count in ${DKLS_MULTIHOST_COUNTS:-7 14 21 28}; do
    case "$count" in
    7 | 14 | 21 | 28) ;;
    *)
      echo "ERROR: unsupported matrix count $count" >&2
      exit 1
      ;;
    esac
    log "============================================"
    log "DKLS multihost timing matrix count=$count"
    log "============================================"
    DKLS_NODE_COUNT="$count" "$0" destroy || true
    DKLS_NODE_COUNT="$count" DKLS_MULTIHOST_RESET_REMOTE_STATE=1 "$0" deploy-timing
    DKLS_NODE_COUNT="$count" "$0" destroy || true
  done
}

resume_after_gg20() {
  [ -f "$INVENTORY" ] || {
    echo "ERROR: missing inventory $INVENTORY" >&2
    exit 1
  }
  local active_count=$((NODE_COUNT - 1))
  local gg20_vault dkls1_vault dkls2_vault start gg20_migration_txid dkls_migration_txid
  mkdir -p "$OUT_DIR"
  gg20_vault=$(active_vault_pubkey)
  [ -n "$gg20_vault" ] || {
    echo "ERROR: no active GG20 vault found" >&2
    exit 1
  }
  log "resume after GG20 vault=$gg20_vault"
  wait_vault_shape "post-GG20-resume" "$active_count" 60
  wait_doge_scanners_healthy 10 || log "WARN: DOGE scanner health did not fully settle before DKLS churn"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  quorum_mimir EnableDKLS 1
  quorum_mimir HaltChurning 0
  dkls1_vault=$(wait_for_vault_change "DKLS1 vault" "$gg20_vault" 160)
  log "DKLS1 vault=$dkls1_vault"
  record_tss_timing keygen dkls "DKLS first" "$start" "$dkls1_vault"
  if [ "${DKLS_STOP_AFTER_DKLS_KEYGEN:-0}" = "1" ]; then
    quorum_mimir HaltChurning 1
    log "stopping after DKLS keygen as requested: $OUT_DIR/tss-timings.csv"
    return
  fi
  gg20_migration_txid=$(wait_migration_broadcast "$start" "$gg20_vault" || true)
  record_tss_timing keysign gg20 "GG20 migration" "$start" "$gg20_vault"
  wait_retiring_vault_empty "$gg20_vault" "GG20 migration" 240

  quorum_mimir HaltChurning 1
  wait_vault_shape "pre-DKLS2" "$active_count" 160
  wait_doge_scanners_healthy 10 || log "WARN: DOGE scanner health did not fully settle before DKLS2 churn"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  quorum_mimir HaltChurning 0
  dkls2_vault=$(wait_for_vault_change "DKLS2 vault" "$dkls1_vault" 160)
  log "DKLS2 vault=$dkls2_vault"
  record_tss_timing keygen dkls "DKLS second" "$start" "$dkls2_vault"
  dkls_migration_txid=$(wait_migration_broadcast "$start" "$dkls1_vault" || true)
  record_tss_timing keysign dkls "DKLS migration" "$start" "$dkls1_vault"
  wait_retiring_vault_empty "$dkls1_vault" "DKLS migration" 240
  quorum_mimir HaltChurning 1
  log "timing complete: $OUT_DIR/tss-timings.csv"
}

resume_after_dkls1() {
  [ -f "$INVENTORY" ] || {
    echo "ERROR: missing inventory $INVENTORY" >&2
    exit 1
  }
  local active_count=$((NODE_COUNT - 1))
  local gg20_vault dkls1_vault dkls2_vault start dkls_migration_txid
  mkdir -p "$OUT_DIR"
  gg20_vault=$(api /thorchain/vaults/asgard | jq -r '[.[] | select(.status=="RetiringVault") | (.pub_key | if type=="object" then .value else . end)] | .[0] // ""')
  dkls1_vault=$(active_vault_pubkey)
  [ -n "$gg20_vault" ] || {
    echo "ERROR: no retiring GG20 vault found" >&2
    exit 1
  }
  [ -n "$dkls1_vault" ] || {
    echo "ERROR: no active DKLS1 vault found" >&2
    exit 1
  }
  log "resume after DKLS1 gg20=$gg20_vault dkls1=$dkls1_vault"
  record_tss_timing keygen dkls "DKLS first" "$(date -u -v-30M +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%SZ)" "$dkls1_vault"
  record_tss_timing keysign gg20 "GG20 migration" "$(date -u -v-30M +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%SZ)" "$gg20_vault"
  wait_retiring_vault_empty "$gg20_vault" "GG20 migration" 240

  quorum_mimir HaltChurning 1
  wait_vault_shape "pre-DKLS2" "$active_count" 160
  wait_doge_scanners_healthy 10 || log "WARN: DOGE scanner health did not fully settle before DKLS2 churn"
  start=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  quorum_mimir HaltChurning 0
  dkls2_vault=$(wait_for_vault_change "DKLS2 vault" "$dkls1_vault" 160)
  log "DKLS2 vault=$dkls2_vault"
  record_tss_timing keygen dkls "DKLS second" "$start" "$dkls2_vault"
  dkls_migration_txid=$(wait_migration_broadcast "$start" "$dkls1_vault" || true)
  record_tss_timing keysign dkls "DKLS migration" "$start" "$dkls1_vault"
  wait_retiring_vault_empty "$dkls1_vault" "DKLS migration" 240
  quorum_mimir HaltChurning 1
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
resume-after-dkls1) resume_after_dkls1 ;;
*)
  usage >&2
  exit 1
  ;;
esac
