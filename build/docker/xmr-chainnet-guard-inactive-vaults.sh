#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/xmr-chainnet-lib.sh"

usage() {
  cat <<'EOF'
Usage: build/docker/xmr-chainnet-guard-inactive-vaults.sh [--host <ssh-target>] [--node <name>|all] [--interval <seconds>] [--once] [--dry-run]

Continuously watches inactive XMR Asgard vaults. If any inactive vault has a
non-zero XMR vault balance, it votes HALTCHURNING=1 from the requested nodes.

Examples:
  build/docker/xmr-chainnet-guard-inactive-vaults.sh --host root@91.99.89.216
  build/docker/xmr-chainnet-guard-inactive-vaults.sh --host root@91.99.89.216 --once
  build/docker/xmr-chainnet-guard-inactive-vaults.sh --node all --interval 5
EOF
}

requested_nodes=()
interval=10
once=0
dry_run=0
while (($# > 0)); do
  case "$1" in
    --host)
      [[ $# -ge 2 ]] || { echo "--host requires a value" >&2; exit 1; }
      export XMR_CHAINNET_HOST="$2"
      shift 2
      ;;
    --node)
      [[ $# -ge 2 ]] || { echo "--node requires a value" >&2; exit 1; }
      requested_nodes+=("$2")
      shift 2
      ;;
    --interval)
      [[ $# -ge 2 ]] || { echo "--interval requires a value" >&2; exit 1; }
      interval="$2"
      shift 2
      ;;
    --once)
      once=1
      shift
      ;;
    --dry-run)
      dry_run=1
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

if ! [[ "$interval" =~ ^[0-9]+$ ]] || ((interval < 1)); then
  echo "--interval must be a positive integer" >&2
  exit 1
fi

xmr_chainnet_require_transport
xmr_chainnet_require jq curl

nodes=()
while IFS= read -r node; do
  [[ -n "$node" ]] && nodes+=("$node")
done < <(xmr_chainnet_expand_requested_nodes "${requested_nodes[@]:-all}")
reference_node="$(xmr_chainnet_reference_node thornode "${nodes[@]}")"

thornode_rpc_for_node() {
  local node="$1"
  if [[ "$node" == "dog" ]]; then
    printf '%s' "tcp://localhost:27147"
  else
    printf 'tcp://thornode-%s:27147' "$node"
  fi
}

vote_halt_churning() {
  local ok=0
  local node container rpc
  for node in "${nodes[@]}"; do
    container="$(xmr_chainnet_thornode_container "$node")"
    rpc="$(thornode_rpc_for_node "$node")"
    if ! xmr_chainnet_container_exists "$container"; then
      echo "skip $node: missing $container" >&2
      continue
    fi
    printf 'vote HALTCHURNING=1 via %s: ' "$node" >&2
    if xmr_chainnet_run docker exec -e RPC="$rpc" "$container" bash -lc \
      'printf "password\npassword\n" | thornode tx thorchain mimir HALTCHURNING 1 --node "$RPC" --from thorchain --keyring-backend file --chain-id thorchain --yes --broadcast-mode sync' >/dev/null; then
      echo "ok" >&2
      ((ok += 1))
    else
      echo "failed" >&2
    fi
  done
  echo "halt votes submitted: $ok/${#nodes[@]}" >&2
}

find_funded_inactive_xmr_vaults() {
  local pubkeys_json vault_json
  pubkeys_json="$(xmr_chainnet_thornode_get "$reference_node" /thorchain/vaults/pubkeys)"

  printf '%s\n' "$pubkeys_json" | jq -r '
    .inactive[]?
    | [.pub_key_frost, .pub_key]
    | @tsv
  ' | while IFS=$'\t' read -r frost secp; do
    [[ -n "${frost:-}${secp:-}" ]] || continue
    vault_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/vault/$frost" 2>/dev/null || xmr_chainnet_thornode_get "$reference_node" "/thorchain/vault/$secp" 2>/dev/null || true)"
    [[ -n "$vault_json" ]] || continue
    printf '%s\n' "$vault_json" | jq -r '
      select((.status // "") == "InactiveVault")
      | ((first(.coins[]? | select((.asset // "") == "XMR.XMR") | .amount) // "0") | tonumber) as $xmr
      | select($xmr > 0)
      | [
          (.pub_key_frost // .pub_key_eddsa // .pub_key),
          ((.addresses[]? | select(.chain == "XMR") | .address) // ""),
          ($xmr | tostring)
        ]
      | @tsv
    '
  done
}

while true; do
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  funded="$(find_funded_inactive_xmr_vaults)"
  if [[ -n "$funded" ]]; then
    echo "$now inactive XMR vault funds detected:"
    {
      printf 'vault\taddress\txmr\n'
      printf '%s\n' "$funded"
    } | xmr_chainnet_tabulate
    if ((dry_run)); then
      echo "dry-run: would vote HALTCHURNING=1"
    else
      vote_halt_churning
    fi
    exit 2
  fi

  echo "$now no funded inactive XMR vaults"
  ((once)) && exit 0
  sleep "$interval"
done
