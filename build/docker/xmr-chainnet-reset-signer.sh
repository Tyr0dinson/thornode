#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/xmr-chainnet-lib.sh"

usage() {
  cat <<'EOF'
Usage: build/docker/xmr-chainnet-reset-signer.sh [--node <name>|all] [--vault <pubkey>] [--local-vaults] [--dry-run]

Examples:
  build/docker/xmr-chainnet-reset-signer.sh --node goat
  build/docker/xmr-chainnet-reset-signer.sh --node dog --node cat
  build/docker/xmr-chainnet-reset-signer.sh --node goat --vault cthorpub1...
  build/docker/xmr-chainnet-reset-signer.sh --node goat --local-vaults
EOF
}

xmr_chainnet_require docker jq curl

requested_nodes=()
requested_vaults=()
use_local_vaults=false
dry_run=false
while (($# > 0)); do
  case "$1" in
    --node)
      [[ $# -ge 2 ]] || { echo "--node requires a value" >&2; exit 1; }
      requested_nodes+=("$2")
      shift 2
      ;;
    --vault)
      [[ $# -ge 2 ]] || { echo "--vault requires a value" >&2; exit 1; }
      requested_vaults+=("$2")
      shift 2
      ;;
    --local-vaults)
      use_local_vaults=true
      shift
      ;;
    --dry-run)
      dry_run=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

nodes=()
if ((${#requested_nodes[@]} == 0)); then
  while IFS= read -r node; do
    [[ -n "$node" ]] && nodes+=("$node")
  done < <(xmr_chainnet_expand_nodes)
else
  while IFS= read -r node; do
    [[ -n "$node" ]] && nodes+=("$node")
  done < <(xmr_chainnet_expand_nodes "${requested_nodes[@]}")
fi
reference_node="$(xmr_chainnet_reference_node thornode "${nodes[@]}")"

canonical_requested_vaults=()
if ((${#requested_vaults[@]} > 0)); then
  for requested in "${requested_vaults[@]}"; do
    vault_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/vault/$requested" 2>/dev/null || true)"
    canonical="$(printf '%s' "$vault_json" | jq -r '(.pub_key_frost // .pub_key_eddsa // .pub_key // empty)' 2>/dev/null || true)"
    if [[ -n "$canonical" ]]; then
      canonical_requested_vaults+=("$canonical")
    else
      canonical_requested_vaults+=("$requested")
    fi
  done
fi

if ((${#canonical_requested_vaults[@]} > 0)); then
  mapfile -t default_vaults < <(printf '%s\n' "${canonical_requested_vaults[@]}" | awk '!seen[$0]++')
else
  mapfile -t default_vaults < <(xmr_chainnet_canonical_xmr_vaults "$reference_node")
fi

lookup_birthday() {
  local node="$1"
  local vault="$2"
  local local_state_json="$3"
  local birthday=""
  local vault_json=""

  vault_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/vault/$vault" 2>/dev/null || true)"
  birthday="$(printf '%s' "$vault_json" | jq -r '(.vault_creation_height_xmr // 0)' 2>/dev/null || true)"
  if [[ -z "$birthday" || "$birthday" == "0" || "$birthday" == "null" ]]; then
    birthday="$(printf '%s' "$local_state_json" | jq -r --arg vault "$vault" '
      first(.vaults[]? | select(.vault_pub_key == $vault) | (.birthday // 0)) // 0
    ' 2>/dev/null || true)"
  fi
  if [[ -z "$birthday" || "$birthday" == "0" || "$birthday" == "null" ]]; then
    echo "unable to determine birthday for $vault on $node" >&2
    return 1
  fi
  printf '%s\n' "$birthday"
}

for node in "${nodes[@]}"; do
  local_state_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/vaults" 2>/dev/null || printf '%s' '{"vaults":[]}')"

  node_vaults=()
  if $use_local_vaults; then
    while IFS= read -r vault; do
      [[ -n "$vault" ]] && node_vaults+=("$vault")
    done < <(printf '%s' "$local_state_json" | jq -r '.vaults[]? | .vault_pub_key')
  else
    node_vaults=("${default_vaults[@]}")
  fi

  if ((${#node_vaults[@]} == 0)); then
    echo "== $node =="
    echo "no target vaults"
    echo
    continue
  fi

  echo "== $node =="
  for vault in "${node_vaults[@]}"; do
    birthday="$(lookup_birthday "$node" "$vault" "$local_state_json")" || continue
    printf '%s birthday=%s' "$(xmr_chainnet_short "$vault" 14 10)" "$birthday"
    if $dry_run; then
      printf ' dry-run\n'
      continue
    fi
    payload="$(jq -cn --arg vault "$vault" '{vault_pub_key:$vault}')"
    if response="$(xmr_chainnet_signer_post_json "$node" "/v1/monero/scanner/reset" "$payload" 2>/dev/null)"; then
      printf ' reset=%s\n' "$(printf '%s' "$response" | jq -c '.')"
    else
      printf ' reset=ERROR\n'
    fi
  done
  echo
done
