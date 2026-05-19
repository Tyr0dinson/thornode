#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COUNTS="${DKLS_TIMING_COUNTS:-7 14 21 28}"
CURRENT_BRANCH="$(git -C "$REPO_ROOT" branch --show-current 2>/dev/null || true)"
BRANCH="${DKLS_GIT_BRANCH:-${CURRENT_BRANCH:-feat-dkls}}"
GIT_URL="${DKLS_GIT_URL:-https://gitlab.com/thorchain/thornode.git}"
LOCATION="${DKLS_HCLOUD_LOCATION:-sin}"
IMAGE="${DKLS_HCLOUD_IMAGE:-ubuntu-24.04}"
PROVISION_PROVIDER="${DKLS_PROVISION_PROVIDER:-capacity}"
HCLOUD_SERVER_LIMIT="${DKLS_HCLOUD_SERVER_LIMIT:-10}"
HCLOUD_PRIMARY_IP_LIMIT="${DKLS_HCLOUD_PRIMARY_IP_LIMIT:-20}"
HCLOUD_RESERVED_SERVER_SLOTS="${DKLS_HCLOUD_RESERVED_SERVER_SLOTS:-0}"
HCLOUD_RESERVED_PRIMARY_IP_SLOTS="${DKLS_HCLOUD_RESERVED_PRIMARY_IP_SLOTS:-0}"
DIGITALOCEAN_REGION="${DKLS_DO_REGION:-sgp1}"
DIGITALOCEAN_IMAGE="${DKLS_DO_IMAGE:-ubuntu-24-04-x64}"
DOGE_HOST="${DOGE_HOST:-5.223.48.34:18332}"
RUN_STAMP="${RUN_STAMP:-$(date -u +%Y%m%dT%H%M%SZ)}"
REMOTE_REPO_DIR="${REMOTE_REPO_DIR:-/opt/dkls-thornode}"
HCLOUD_TIMING_SLOTS_REMAINING=0
DO_TIMING_SLOTS_REMAINING=0
CAPACITY_PROVIDER_CHOICE=""

server_type_for_count() {
  case "$1" in
  7) echo "${DKLS_HCLOUD_TYPE_7:-cpx62}" ;;
  14) echo "${DKLS_HCLOUD_TYPE_14:-cpx62}" ;;
  21) echo "${DKLS_HCLOUD_TYPE_21:-cpx62}" ;;
  28) echo "${DKLS_HCLOUD_TYPE_28:-cpx62}" ;;
  *)
    echo "unsupported node count: $1" >&2
    return 1
    ;;
  esac
}

do_size_for_count() {
  case "$1" in
  7) echo "${DKLS_DO_SIZE_7:-s-8vcpu-16gb}" ;;
  14) echo "${DKLS_DO_SIZE_14:-s-16vcpu-32gb}" ;;
  21) echo "${DKLS_DO_SIZE_21:-s-16vcpu-32gb}" ;;
  28) echo "${DKLS_DO_SIZE_28:-s-16vcpu-32gb}" ;;
  *)
    echo "unsupported node count: $1" >&2
    return 1
    ;;
  esac
}

provider_for_count() {
  case "$PROVISION_PROVIDER" in
  hcloud | hetzner) echo "hcloud" ;;
  do | digitalocean | digital-ocean) echo "do" ;;
  capacity) echo "capacity" ;;
  *)
    echo "unsupported DKLS_PROVISION_PROVIDER=$PROVISION_PROVIDER" >&2
    return 1
    ;;
  esac
}

json_len_or_zero() {
  jq 'length' 2>/dev/null || echo 0
}

hcloud_current_server_count() {
  hcloud server list -o json 2>/dev/null | json_len_or_zero
}

hcloud_current_primary_ip_count() {
  hcloud primary-ip list -o json 2>/dev/null | json_len_or_zero
}

hcloud_primary_ips_per_timing_server() {
  if [ "${DKLS_HCLOUD_WITHOUT_IPV6:-1}" = "1" ]; then
    echo 1
  else
    echo 2
  fi
}

hcloud_available_timing_slots() {
  if [ -n "${DKLS_HCLOUD_AVAILABLE_SLOTS-}" ]; then
    echo "$DKLS_HCLOUD_AVAILABLE_SLOTS"
    return
  fi
  local current_servers current_primary_ips server_slots primary_ip_slots ips_per_server
  current_servers=$(hcloud_current_server_count)
  current_primary_ips=$(hcloud_current_primary_ip_count)
  ips_per_server=$(hcloud_primary_ips_per_timing_server)
  server_slots=$((HCLOUD_SERVER_LIMIT - current_servers - HCLOUD_RESERVED_SERVER_SLOTS))
  primary_ip_slots=$(((HCLOUD_PRIMARY_IP_LIMIT - current_primary_ips - HCLOUD_RESERVED_PRIMARY_IP_SLOTS) / ips_per_server))
  [ "$server_slots" -lt 0 ] && server_slots=0
  [ "$primary_ip_slots" -lt 0 ] && primary_ip_slots=0
  if [ "$server_slots" -lt "$primary_ip_slots" ]; then
    echo "$server_slots"
  else
    echo "$primary_ip_slots"
  fi
}

do_available_droplet_slots() {
  [ -n "${DIGITALOCEAN_TOKEN-}" ] || {
    echo 0
    return
  }
  [ "${DKLS_SKIP_DO_CAPACITY:-0}" != "1" ] || {
    echo "${DKLS_DO_AVAILABLE_SLOTS:-0}"
    return
  }
  local account droplet_limit current_droplets
  account=$(do_api GET /account 2>/dev/null || true)
  droplet_limit=$(echo "$account" | jq -r '.account.droplet_limit // 0' 2>/dev/null || echo 0)
  current_droplets=$(do_api GET "/droplets?per_page=1" 2>/dev/null | jq -r '.meta.total // 0' 2>/dev/null || echo 0)
  if [ "$droplet_limit" -gt "$current_droplets" ]; then
    echo $((droplet_limit - current_droplets))
  else
    echo 0
  fi
}

choose_capacity_provider() {
  if [ "$HCLOUD_TIMING_SLOTS_REMAINING" -gt 0 ]; then
    HCLOUD_TIMING_SLOTS_REMAINING=$((HCLOUD_TIMING_SLOTS_REMAINING - 1))
    CAPACITY_PROVIDER_CHOICE="hcloud"
    return
  fi
  if [ "$DO_TIMING_SLOTS_REMAINING" -gt 0 ]; then
    DO_TIMING_SLOTS_REMAINING=$((DO_TIMING_SLOTS_REMAINING - 1))
    CAPACITY_PROVIDER_CHOICE="do"
    return
  fi
  echo "ERROR: no remote provisioning capacity available in Hetzner or DigitalOcean" >&2
  echo "       Hetzner available=$(hcloud_available_timing_slots), DigitalOcean available=$(do_available_droplet_slots)" >&2
  return 1
}

print_capacity() {
  local do_slots="unknown"
  if [ -n "${DIGITALOCEAN_TOKEN-}" ]; then
    do_slots=$(do_available_droplet_slots)
  fi
  if [ -n "${DKLS_HCLOUD_AVAILABLE_SLOTS-}" ]; then
    echo "  capacity: hcloud_timing_slots=$DKLS_HCLOUD_AVAILABLE_SLOTS override do_droplet_slots=$do_slots"
    return
  fi
  echo "  capacity: hcloud_servers=$(hcloud_current_server_count)/$HCLOUD_SERVER_LIMIT hcloud_primary_ips=$(hcloud_current_primary_ip_count)/$HCLOUD_PRIMARY_IP_LIMIT hcloud_timing_slots=$(hcloud_available_timing_slots) do_droplet_slots=$do_slots"
}

init_capacity_plan() {
  HCLOUD_TIMING_SLOTS_REMAINING=$(hcloud_available_timing_slots)
  DO_TIMING_SLOTS_REMAINING=$(do_available_droplet_slots)
  print_capacity
}

run_local_count() {
  local count="$1"
  local results_dir="$SCRIPT_DIR/dkls-chainnet-results/timing-${count}-${RUN_STAMP}"
  local compose_env_file="${COMPOSE_ENV_FILE:-$REPO_ROOT/.env}"
  local compose_env_args=()
  if [ ! -f "$compose_env_file" ] && [ -f "$SCRIPT_DIR/.env" ]; then
    compose_env_file="$SCRIPT_DIR/.env"
  fi
  if [ -f "$compose_env_file" ]; then
    compose_env_args=(--env-file "$compose_env_file")
  fi
  local compose_cmd=(docker compose)
  if [ "${#compose_env_args[@]}" -gt 0 ]; then
    compose_cmd+=("${compose_env_args[@]}")
  fi
  compose_cmd+=(-f "$SCRIPT_DIR/docker-compose.dkls-chainnet-timing.yml")

  echo "============================================"
  echo "DKLS timing local run: node_count=$count GG20->DKLS"
  echo "============================================"

  DKLS_NODE_COUNT="$count" DKLS_FORCE_BUILD="${DKLS_FORCE_BUILD:-1}" "$SCRIPT_DIR/dkls-chainnet-generate-compose.sh" "$count"

  "${compose_cmd[@]}" down -v --remove-orphans || true
  DOGE_HOST="$DOGE_HOST" "${compose_cmd[@]}" up -d

  if DKLS_NODE_COUNT="$count" \
    NODE_META_FILE="$SCRIPT_DIR/dkls-chainnet-generated/nodes-${count}.tsv" \
    COMPOSE_FILE="docker-compose.dkls-chainnet-timing.yml" \
    DOGE_HOST="$DOGE_HOST" \
    RESULTS_DIR="$results_dir" \
    "$SCRIPT_DIR/dkls-chainnet-timing-run.sh"; then
    echo "  node_count=$count complete; results=$results_dir"
    "${compose_cmd[@]}" down -v --remove-orphans || true
  else
    local rc=$?
    echo "  node_count=$count failed; preserving containers for debugging"
    return "$rc"
  fi
}

remote_script() {
  local count="$1"
  local git_url_quoted
  printf -v git_url_quoted '%q' "$GIT_URL"
  cat <<REMOTE
set -euo pipefail
git_url=$git_url_quoted
export DEBIAN_FRONTEND=noninteractive
if command -v cloud-init >/dev/null 2>&1; then
  cloud-init status --wait >/dev/null
fi
if ! command -v docker >/dev/null 2>&1; then
  until apt-get update; do sleep 5; done
  until apt-get install -y ca-certificates curl git jq make python3 xxd; do sleep 5; done
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc
  . /etc/os-release
  echo "deb [arch=\$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \${VERSION_CODENAME} stable" > /etc/apt/sources.list.d/docker.list
  until apt-get update; do sleep 5; done
  until apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin; do sleep 5; done
fi
mkdir -p "$REMOTE_REPO_DIR"
if [ ! -d "$REMOTE_REPO_DIR/.git" ]; then
  git clone --branch "$BRANCH" "\$git_url" "$REMOTE_REPO_DIR"
fi
cd "$REMOTE_REPO_DIR"
git fetch origin "$BRANCH"
git reset --hard "origin/$BRANCH"
cat > build/docker/.env <<ENV
DOGE_HOST=$DOGE_HOST
DOGE_RPC_USER=\${DOGE_RPC_USER:-thorchain}
DOGE_RPC_PASSWORD=\${DOGE_RPC_PASSWORD:-password}
ENV
cd build/docker
DKLS_TIMING_COUNTS="$count" DOGE_HOST="$DOGE_HOST" RUN_STAMP="$RUN_STAMP" ./dkls-chainnet-singlehost-hcloud-lib.sh --local
tar -C dkls-chainnet-results -czf "/tmp/dkls-timing-${count}-${RUN_STAMP}.tgz" "timing-${count}-${RUN_STAMP}"
REMOTE
}

hcloud_provision_and_run_count() {
  local count="$1"
  local server_type
  server_type=$(server_type_for_count "$count")
  local name="dkls-timing-${count}-${RUN_STAMP}"

  echo "============================================"
  echo "DKLS timing provision: node_count=$count type=$server_type location=$LOCATION"
  echo "============================================"

  local create_args=(
    server create
    --name "$name"
    --type "$server_type"
    --image "$IMAGE"
    --location "$LOCATION"
    --start-after-create
  )
  # IPv6 is not needed for the timing host and consumes a Hetzner Primary IP slot.
  if [ "${DKLS_HCLOUD_WITHOUT_IPV6:-1}" = "1" ]; then
    create_args+=(--without-ipv6)
  fi
  if [ -n "${DKLS_HCLOUD_SSH_KEY-}" ]; then
    create_args+=(--ssh-key "$DKLS_HCLOUD_SSH_KEY")
  fi
  hcloud "${create_args[@]}" >/dev/null

  local ip
  ip=$(hcloud server ip "$name")
  echo "  server=$name ip=$ip"
  ssh-keygen -R "$ip" >/dev/null 2>&1 || true
  until ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "root@$ip" true 2>/dev/null; do
    sleep 5
  done
  remote_script "$count" | ssh -o StrictHostKeyChecking=no "root@$ip" bash -s
  mkdir -p "$SCRIPT_DIR/dkls-chainnet-results"
  scp -o StrictHostKeyChecking=no "root@$ip:/tmp/dkls-timing-${count}-${RUN_STAMP}.tgz" "$SCRIPT_DIR/dkls-chainnet-results/" >/dev/null
  tar -C "$SCRIPT_DIR/dkls-chainnet-results" -xzf "$SCRIPT_DIR/dkls-chainnet-results/dkls-timing-${count}-${RUN_STAMP}.tgz"
  if [ "${DKLS_HCLOUD_KEEP_SERVERS:-0}" != "1" ]; then
    hcloud server delete "$name" >/dev/null
  fi
}

do_api() {
  local method="$1"
  local path="$2"
  local payload="${3-}"
  [ -n "${DIGITALOCEAN_TOKEN-}" ] || {
    echo "ERROR: DIGITALOCEAN_TOKEN is required for DigitalOcean provisioning" >&2
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

do_user_data() {
  local pubkey_file="${DKLS_DO_SSH_PUBLIC_KEY_FILE:-$HOME/.ssh/id_ed25519.pub}"
  if [ -n "${DKLS_DO_SSH_KEY-}" ]; then
    printf ''
    return
  fi
  [ -f "$pubkey_file" ] || {
    echo "ERROR: set DKLS_DO_SSH_KEY or provide $pubkey_file" >&2
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

do_droplet_ip() {
  local droplet_id="$1"
  do_api GET "/droplets/$droplet_id" | jq -r '.droplet.networks.v4[]? | select(.type=="public") | .ip_address' | head -1
}

do_provision_and_run_count() {
  local count="$1"
  local size
  size=$(do_size_for_count "$count")
  local name="dkls-timing-${count}-${RUN_STAMP}"

  echo "============================================"
  echo "DKLS timing provision: node_count=$count provider=digitalocean size=$size region=$DIGITALOCEAN_REGION"
  echo "============================================"

  local account droplet_limit current_droplets
  account=$(do_api GET /account)
  droplet_limit=$(echo "$account" | jq -r '.account.droplet_limit // "unknown"')
  current_droplets=$(do_api GET "/droplets?per_page=1" | jq -r '.meta.total // "unknown"')
  echo "  digitalocean droplet_limit=$droplet_limit current_droplets=$current_droplets"

  local ssh_key user_data payload create_response droplet_id ip
  ssh_key="${DKLS_DO_SSH_KEY-}"
  user_data="$(do_user_data)"
  payload=$(jq -n \
    --arg name "$name" \
    --arg region "$DIGITALOCEAN_REGION" \
    --arg size "$size" \
    --arg image "$DIGITALOCEAN_IMAGE" \
    --arg ssh_key "$ssh_key" \
    --arg user_data "$user_data" \
    '{
      name: $name,
      region: $region,
      size: $size,
      image: $image,
      monitoring: true,
      tags: ["dkls-timing"]
    }
    + (if $ssh_key != "" then {ssh_keys: [$ssh_key]} else {user_data: $user_data} end)')
  create_response=$(do_api POST /droplets "$payload")
  droplet_id=$(echo "$create_response" | jq -r '.droplet.id')
  echo "  droplet=$name id=$droplet_id"

  for _ in $(seq 1 120); do
    ip=$(do_droplet_ip "$droplet_id")
    [ -n "$ip" ] && break
    sleep 5
  done
  [ -n "${ip-}" ] || {
    echo "ERROR: timed out waiting for DigitalOcean public IPv4" >&2
    return 1
  }
  echo "  droplet=$name ip=$ip"

  ssh-keygen -R "$ip" >/dev/null 2>&1 || true
  until ssh -o StrictHostKeyChecking=no -o ConnectTimeout=5 "root@$ip" true 2>/dev/null; do
    sleep 5
  done
  remote_script "$count" | ssh -o StrictHostKeyChecking=no "root@$ip" bash -s
  mkdir -p "$SCRIPT_DIR/dkls-chainnet-results"
  scp -o StrictHostKeyChecking=no "root@$ip:/tmp/dkls-timing-${count}-${RUN_STAMP}.tgz" "$SCRIPT_DIR/dkls-chainnet-results/" >/dev/null
  tar -C "$SCRIPT_DIR/dkls-chainnet-results" -xzf "$SCRIPT_DIR/dkls-chainnet-results/dkls-timing-${count}-${RUN_STAMP}.tgz"
  if [ "${DKLS_DO_KEEP_DROPLETS:-0}" != "1" ]; then
    do_api DELETE "/droplets/$droplet_id" >/dev/null
  fi
}

provision_and_run_count() {
  local count="$1"
  local provider="${2:-$(provider_for_count "$count")}"
  case "$provider" in
  hcloud) hcloud_provision_and_run_count "$count" ;;
  do) do_provision_and_run_count "$count" ;;
  capacity)
    choose_capacity_provider
    provision_and_run_count "$count" "$CAPACITY_PROVIDER_CHOICE"
    ;;
  esac
}

if [ "${1-}" = "--local" ]; then
  for count in $COUNTS; do
    run_local_count "$count"
  done
  exit 0
fi

if [ "${1-}" = "--capacity-plan" ]; then
  init_capacity_plan
  for count in $COUNTS; do
    choose_capacity_provider
    echo "  plan: node_count=$count provider=$CAPACITY_PROVIDER_CHOICE"
  done
  exit 0
fi

if [ "${DKLS_PROVISION_HCLOUD:-0}" = "1" ] || [ "${DKLS_PROVISION_REMOTE:-0}" = "1" ]; then
  if [ "$PROVISION_PROVIDER" = "capacity" ]; then
    init_capacity_plan
  fi
  for count in $COUNTS; do
    provision_and_run_count "$count"
  done
else
  for count in $COUNTS; do
    run_local_count "$count"
  done
fi
