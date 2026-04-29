#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/xmr-chainnet-lib.sh"

usage() {
  cat <<'EOF'
Usage: build/docker/xmr-chainnet-check-balances.sh [--host <ssh-target>] [--node <name>|all] [--include-retiring]

Checks exact XMR balance reconciliation across three separate views:
onchain balance from signer sidecar scanners, thornode vault accounting, and
pool asset depth.

Amounts are printed in THORChain XMR units (1e8), with onchain atomic balances
converted by /10000.

Examples:
  build/docker/xmr-chainnet-check-balances.sh
  build/docker/xmr-chainnet-check-balances.sh --host root@91.99.89.216
  build/docker/xmr-chainnet-check-balances.sh --node dog
  build/docker/xmr-chainnet-check-balances.sh --node all
  build/docker/xmr-chainnet-check-balances.sh --node all --include-retiring
EOF
}

requested_nodes=()
include_retiring=0
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
    --include-retiring)
      include_retiring=1
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

xmr_chainnet_require_transport
xmr_chainnet_require jq curl column

nodes=()
while IFS= read -r node; do
  [[ -n "$node" ]] && nodes+=("$node")
done < <(xmr_chainnet_expand_requested_nodes "${requested_nodes[@]}")
reference_node="$(xmr_chainnet_reference_node thornode "${nodes[@]}")"

pools_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/pools")"
vaults_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/vaults/asgard")"
nodes_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/nodes")"

pool_asset_depth="$(
  printf '%s\n' "$pools_json" | jq -r '
    first(.[]? | select((.asset // "") == "XMR.XMR") | ((.balance_asset // .asset_depth // "0") | tonumber)) // 0
  '
)"
pending_inbound_asset="$(
  printf '%s\n' "$pools_json" | jq -r '
    first(.[]? | select((.asset // "") == "XMR.XMR") | ((.pending_inbound_asset // "0") | tonumber)) // 0
  '
)"

vault_rows="$(
  printf '%s\n' "$vaults_json" | jq -r --argjson include_retiring "$include_retiring" '
    .[]
    | select(.chains[]? == "XMR")
    | select((.status // "") == "ActiveVault" or ($include_retiring == 1 and (.status // "") == "RetiringVault"))
    | [
        (.status // ""),
        (.pub_key_frost // .pub_key_eddsa // .pub_key),
        (.coins[]? | select((.asset // "") == "XMR.XMR") | ((.amount // "0") | tonumber))
      ]
    | select(.[1] != null and .[1] != "" and .[2] != null)
    | @tsv
  '
)"

if [[ -z "$vault_rows" ]]; then
  echo "no XMR vaults found on reference node: $reference_node" >&2
  exit 1
fi
vault_row_array=()
while IFS= read -r vault_row; do
  [[ -n "$vault_row" ]] && vault_row_array+=("$vault_row")
done < <(printf '%s\n' "$vault_rows")

vault_sum="$(
  printf '%s\n' "$vault_rows" | awk -F $'\t' '$1 == "ActiveVault" { s += $3 } END { printf "%.0f", s }'
)"
selected_vault_sum="$(
  printf '%s\n' "$vault_rows" | awk -F $'\t' '{ s += $3 } END { printf "%.0f", s }'
)"

per_vault_file="$(mktemp)"
totals_file="$(mktemp)"
trap 'rm -f "$per_vault_file" "$totals_file"' EXIT

printf 'node\tstatus\tvault_address\tvault\tonchain\tonchain_minus_vault\tmatch_vault\n' >"$per_vault_file"
printf 'node\tnode_status\tonchain\tvault\tpool\tonchain_minus_vault\tonchain_minus_pool\tvault_minus_pool\tmatch_vault\tmatch_pool\n' >"$totals_file"

for node in "${nodes[@]}"; do
  node_status="unknown"
  if xmr_chainnet_container_exists "$(xmr_chainnet_thornode_container "$node")"; then
    node_address="$(
      xmr_chainnet_run docker exec "$(xmr_chainnet_thornode_container "$node")" sh -lc \
        'printf "%s\n" "$SIGNER_PASSWD" | thornode keys show "$SIGNER_NAME" -a --keyring-backend file' 2>/dev/null || true
    )"
    if [[ -n "$node_address" ]]; then
      node_status="$(
        printf '%s\n' "$nodes_json" | jq -r --arg node_address "$node_address" '
          first(.[]? | select(.node_address == $node_address) | .status) // "unknown"
        '
      )"
    fi
    if [[ "$node_status" == "unknown" ]]; then
      node_ip="$(
        xmr_chainnet_run docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' \
          "$(xmr_chainnet_thornode_container "$node")" 2>/dev/null || true
      )"
      if [[ -n "$node_ip" ]]; then
        node_status="$(
          printf '%s\n' "$nodes_json" | jq -r --arg node_ip "$node_ip" '
            first(.[]? | select(.ip_address == $node_ip) | .status) // "unknown"
          '
        )"
      fi
    fi
  fi

  if ! xmr_chainnet_container_exists "$(xmr_chainnet_signer_container "$node")"; then
    for vault_row in "${vault_row_array[@]}"; do
      IFS=$'\t' read -r status vault thornode_balance <<<"$vault_row"
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$node" "$status" "$(xmr_chainnet_short "$vault" 10 6)" "$thornode_balance" "unavailable" "-" "no" >>"$per_vault_file"
    done
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$node" "$node_status" "unavailable" "$vault_sum" "$pool_asset_depth" "-" "-" "$((vault_sum - pool_asset_depth))" "no" "no" >>"$totals_file"
    continue
  fi

  onchain_total=0
  node_had_error=0
  for vault_row in "${vault_row_array[@]}"; do
    IFS=$'\t' read -r status vault thornode_balance <<<"$vault_row"
    balance_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/balance/$vault" 2>/dev/null || true)"
    if [[ -z "$balance_json" ]]; then
      node_had_error=1
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$node" "$status" "$(xmr_chainnet_short "$vault" 10 6)" "$thornode_balance" "error" "-" "no" >>"$per_vault_file"
      continue
    fi

    sidecar_atomic="$(printf '%s\n' "$balance_json" | jq -r '(.balance // 0) | tonumber' 2>/dev/null || printf 'error')"
    if [[ "$sidecar_atomic" == "error" ]]; then
      node_had_error=1
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$node" "$status" "$(xmr_chainnet_short "$vault" 10 6)" "$thornode_balance" "error" "-" "no" >>"$per_vault_file"
      continue
    fi

    sidecar_balance="$((sidecar_atomic / 10000))"
    onchain_total="$((onchain_total + sidecar_balance))"
    diff="$((sidecar_balance - thornode_balance))"
    match="no"
    [[ "$diff" -eq 0 ]] && match="yes"
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
      "$node" "$status" "$(xmr_chainnet_short "$vault" 10 6)" "$thornode_balance" "$sidecar_balance" "$diff" "$match" >>"$per_vault_file"
  done

  onchain_minus_vault="$((onchain_total - vault_sum))"
  onchain_minus_pool="$((onchain_total - pool_asset_depth))"
  vault_minus_pool="$((vault_sum - pool_asset_depth))"
  match_vault="no"
  [[ "$onchain_minus_vault" -eq 0 && "$node_had_error" -eq 0 ]] && match_vault="yes"
  match_pool="no"
  [[ "$onchain_minus_pool" -eq 0 && "$node_had_error" -eq 0 ]] && match_pool="yes"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$node" "$node_status" "$onchain_total" "$vault_sum" "$pool_asset_depth" "$onchain_minus_vault" "$onchain_minus_pool" "$vault_minus_pool" "$match_vault" "$match_pool" >>"$totals_file"
done

echo "Current summary ($reference_node)"
xmr_chainnet_tabulate <"$totals_file"

echo
printf 'pending_inbound_asset\t%s\n' "$pending_inbound_asset" | xmr_chainnet_tabulate
if ((include_retiring)); then
  printf 'vault_selected\t%s\nvault_selected_minus_pool\t%s\n' \
    "$selected_vault_sum" "$((selected_vault_sum - pool_asset_depth))" | xmr_chainnet_tabulate
fi

echo
echo "Per-vault balances"
xmr_chainnet_tabulate <"$per_vault_file"
