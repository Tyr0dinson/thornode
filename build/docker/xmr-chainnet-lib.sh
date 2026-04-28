#!/usr/bin/env bash
set -euo pipefail

THORNODE_API_PORT="${THORNODE_API_PORT:-1317}"
SIGNER_API_PORT="${SIGNER_API_PORT:-8283}"

xmr_chainnet_require() {
  local missing=()
  local cmd
  for cmd in "$@"; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      missing+=("$cmd")
    fi
  done
  if ((${#missing[@]} > 0)); then
    echo "missing required commands: ${missing[*]}" >&2
    exit 1
  fi
}

xmr_chainnet_require_transport() {
  if [[ -n "${XMR_CHAINNET_HOST:-}" ]]; then
    xmr_chainnet_require ssh
  else
    xmr_chainnet_require docker
  fi
}

xmr_chainnet_run() {
  if [[ -n "${XMR_CHAINNET_HOST:-}" ]]; then
    local remote_cmd
    printf -v remote_cmd '%q ' "$@"
    ssh -o BatchMode=yes "${XMR_CHAINNET_HOST}" "$remote_cmd"
  else
    "$@"
  fi
}

xmr_chainnet_list_nodes() {
  if [[ -n "${XMR_CHAINNET_NODES:-}" ]]; then
    local configured="${XMR_CHAINNET_NODES//,/ }"
    local node
    for node in $configured; do
      [[ -n "$node" ]] && printf '%s\n' "$node"
    done | awk '!seen[$0]++'
    return 0
  fi

  local discovered=()
  local name node
  while IFS= read -r name; do
    case "$name" in
      docker-thornode-1|docker-bifrost-1|docker-xmr-frost-signer-1)
        discovered+=("dog")
        ;;
      docker-thornode-*-1)
        node="${name#docker-thornode-}"
        node="${node%-1}"
        discovered+=("$node")
        ;;
      docker-bifrost-*-1)
        node="${name#docker-bifrost-}"
        node="${node%-1}"
        discovered+=("$node")
        ;;
      docker-xmr-frost-signer-*-1)
        node="${name#docker-xmr-frost-signer-}"
        node="${node%-1}"
        discovered+=("$node")
        ;;
    esac
  done < <(xmr_chainnet_run docker ps -a --format '{{.Names}}')

  if ((${#discovered[@]} == 0)); then
    echo "no chainnet nodes discovered from docker containers; set XMR_CHAINNET_NODES if needed" >&2
    exit 1
  fi

  local saw_dog=0
  for node in "${discovered[@]}"; do
    if [[ "$node" == "dog" ]]; then
      saw_dog=1
      break
    fi
  done

  if ((saw_dog)); then
    printf '%s\n' dog
  fi
  printf '%s\n' "${discovered[@]}" | awk '$0 != "dog" && !seen[$0]++'
}

xmr_chainnet_is_valid_node() {
  local candidate="${1:-}"
  local node
  while IFS= read -r node; do
    if [[ "$node" == "$candidate" ]]; then
      return 0
    fi
  done < <(xmr_chainnet_list_nodes)
  return 1
}

xmr_chainnet_expand_nodes() {
  if (($# == 0)); then
    xmr_chainnet_list_nodes
    return 0
  fi

  local requested=()
  local node
  for node in "$@"; do
    if [[ "$node" == "all" ]]; then
      xmr_chainnet_list_nodes
      return 0
    fi
    if ! xmr_chainnet_is_valid_node "$node"; then
      echo "unknown node: $node" >&2
      exit 1
    fi
    requested+=("$node")
  done

  printf '%s\n' "${requested[@]}" | awk '!seen[$0]++'
}

xmr_chainnet_expand_requested_nodes() {
  if (($# == 0)); then
    xmr_chainnet_expand_nodes
  else
    xmr_chainnet_expand_nodes "$@"
  fi
}

xmr_chainnet_signer_container() {
  local node="$1"
  if [[ "$node" == "dog" ]]; then
    echo "docker-xmr-frost-signer-1"
  else
    echo "docker-xmr-frost-signer-${node}-1"
  fi
}

xmr_chainnet_bifrost_container() {
  local node="$1"
  if [[ "$node" == "dog" ]]; then
    echo "docker-bifrost-1"
  else
    echo "docker-bifrost-${node}-1"
  fi
}

xmr_chainnet_monerod_container() {
  echo "docker-monero-daemon-1"
}

xmr_chainnet_thornode_container() {
  local node="$1"
  if [[ "$node" == "dog" ]]; then
    echo "docker-thornode-1"
  else
    echo "docker-thornode-${node}-1"
  fi
}

xmr_chainnet_container_exists() {
  local container="$1"
  xmr_chainnet_run docker container inspect "$container" >/dev/null 2>&1
}

xmr_chainnet_reference_node() {
  local service="$1"
  shift || true

  local node container
  for node in "$@"; do
    case "$service" in
      signer) container="$(xmr_chainnet_signer_container "$node")" ;;
      bifrost) container="$(xmr_chainnet_bifrost_container "$node")" ;;
      thornode) container="$(xmr_chainnet_thornode_container "$node")" ;;
      *) echo "unknown service: $service" >&2; exit 1 ;;
    esac
    if xmr_chainnet_container_exists "$container"; then
      printf '%s\n' "$node"
      return 0
    fi
  done

  while IFS= read -r node; do
    case "$service" in
      signer) container="$(xmr_chainnet_signer_container "$node")" ;;
      bifrost) container="$(xmr_chainnet_bifrost_container "$node")" ;;
      thornode) container="$(xmr_chainnet_thornode_container "$node")" ;;
    esac
    if xmr_chainnet_container_exists "$container"; then
      printf '%s\n' "$node"
      return 0
    fi
  done < <(xmr_chainnet_list_nodes)

  echo "no $service container found for the current cluster" >&2
  exit 1
}

xmr_chainnet_signer_auth() {
  local container="$1"
  xmr_chainnet_run docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$container" | awk -F= '
    /^XMR_FROST_AUTH_BEARER=/ { sub(/^XMR_FROST_AUTH_BEARER=/, "", $0); print; found=1; exit }
    END {
      if (!found) {
        exit 1
      }
    }
  ' 2>/dev/null || xmr_chainnet_run docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$container" | awk -F= '
    /^SIGNER_SEED_PHRASE=/ { sub(/^SIGNER_SEED_PHRASE=/, "", $0); print; exit }
  '
}

xmr_chainnet_signer_get() {
  local node="$1"
  local path="$2"
  local container
  container="$(xmr_chainnet_signer_container "$node")"
  local auth
  auth="$(xmr_chainnet_signer_auth "$container")"
  xmr_chainnet_run docker exec -e AUTH="$auth" -e XPATH="$path" "$container" sh -lc \
    'curl -sf -H "Authorization: $AUTH" "http://127.0.0.1:'"$SIGNER_API_PORT"'$XPATH"'
}

xmr_chainnet_signer_post_json() {
  local node="$1"
  local path="$2"
  local payload="$3"
  local container
  container="$(xmr_chainnet_signer_container "$node")"
  local auth
  auth="$(xmr_chainnet_signer_auth "$container")"
  xmr_chainnet_run docker exec -e AUTH="$auth" -e XPATH="$path" -e JSON_PAYLOAD="$payload" "$container" sh -lc \
    'curl -sf -H "Authorization: $AUTH" -H "Content-Type: application/json" -X POST -d "$JSON_PAYLOAD" "http://127.0.0.1:'"$SIGNER_API_PORT"'$XPATH"'
}

xmr_chainnet_thornode_get() {
  local node="$1"
  local path="$2"
  local container
  container="$(xmr_chainnet_thornode_container "$node")"
  xmr_chainnet_run docker exec -e XPATH="$path" "$container" sh -lc \
    'curl -sf "http://127.0.0.1:'"$THORNODE_API_PORT"'$XPATH"'
}

xmr_chainnet_bifrost_logs() {
  local node="$1"
  shift
  local container
  container="$(xmr_chainnet_bifrost_container "$node")"
  xmr_chainnet_run docker logs "$@" "$container"
}

xmr_chainnet_monerod_post_json() {
  local path="$1"
  local payload="$2"
  local container
  container="$(xmr_chainnet_monerod_container)"
  xmr_chainnet_run docker exec -e XPATH="$path" -e JSON_PAYLOAD="$payload" "$container" sh -lc \
    'curl -sf -H "Content-Type: application/json" -X POST -d "$JSON_PAYLOAD" "http://127.0.0.1:18081$XPATH"'
}

xmr_chainnet_short() {
  local value="${1:-}"
  local prefix="${2:-12}"
  local suffix="${3:-8}"
  if ((${#value} <= prefix + suffix + 3)); then
    printf '%s' "$value"
  else
    printf '%s...%s' "${value:0:prefix}" "${value: -suffix}"
  fi
}

xmr_chainnet_canonical_xmr_vaults() {
  local node="${1:-dog}"
  xmr_chainnet_thornode_get "$node" "/thorchain/vaults/asgard" | jq -r '
    .[]
    | select(.chains[]? == "XMR")
    | select((.vault_creation_height_xmr // "0") != "0")
    | (.pub_key_frost // .pub_key_eddsa // .pub_key)
    | select(length > 0)
  ' | sort -u
}

xmr_chainnet_local_xmr_vaults() {
  local nodes=("$@")
  local node vaults_json
  for node in "${nodes[@]}"; do
    vaults_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/vaults" 2>/dev/null || true)"
    [[ -n "$vaults_json" ]] || continue
    printf '%s\n' "$vaults_json" | jq -r '
      .vaults[]?
      | .vault_pub_key
      | select(length > 0)
    '
  done | sort -u
}

xmr_chainnet_discover_xmr_vaults() {
  local reference_node="${1:-}"
  shift || true

  local found=0
  local vault
  while IFS= read -r vault; do
    [[ -z "$vault" ]] && continue
    found=1
    printf '%s\n' "$vault"
  done < <(xmr_chainnet_canonical_xmr_vaults "$reference_node" 2>/dev/null || true)

  if ((found == 0)); then
    xmr_chainnet_local_xmr_vaults "$@"
  fi
}

xmr_chainnet_tabulate() {
  column -ts $'\t'
}
