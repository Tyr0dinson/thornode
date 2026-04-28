#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/xmr-chainnet-lib.sh"

usage() {
  cat <<'EOF'
Usage: build/docker/xmr-chainnet-check-pools-vaults.sh [--host <ssh-target>] [--node <name>|all]

Examples:
  build/docker/xmr-chainnet-check-pools-vaults.sh
  build/docker/xmr-chainnet-check-pools-vaults.sh --host root@91.99.89.216
  build/docker/xmr-chainnet-check-pools-vaults.sh --node dog
  build/docker/xmr-chainnet-check-pools-vaults.sh --node all
EOF
}

requested_nodes=()
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

echo "Node summary"
{
  printf 'node\tlast_observed_in\tlast_signed_out\tactive_vault_shape\txmr_rune_depth\txmr_asset_depth\txmr_fee_rate\txmr_inbound_address\n'
  for node in "${nodes[@]}"; do
    if ! xmr_chainnet_container_exists "$(xmr_chainnet_thornode_container "$node")"; then
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$node" "unavailable" "-" "-" "-" "-" "-" "-"
      continue
    fi
    pools_json="$(xmr_chainnet_thornode_get "$node" "/thorchain/pools" 2>/dev/null || true)"
    vaults_json="$(xmr_chainnet_thornode_get "$node" "/thorchain/vaults/asgard" 2>/dev/null || true)"
    lastblock_json="$(xmr_chainnet_thornode_get "$node" "/thorchain/lastblock" 2>/dev/null || true)"
    inbound_json="$(xmr_chainnet_thornode_get "$node" "/thorchain/inbound_addresses" 2>/dev/null || true)"
    if [[ -z "$pools_json" || -z "$vaults_json" || -z "$lastblock_json" || -z "$inbound_json" ]]; then
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$node" "error" "-" "-" "-" "-" "-" "-"
      continue
    fi

    summary="$(jq -nr \
      --argjson pools "$pools_json" \
      --argjson vaults "$vaults_json" \
      --argjson lastblock "$lastblock_json" \
      --argjson inbound "$inbound_json" '
      def xmr_pool: first($pools[]? | select((.asset // "") == "XMR.XMR")) // {};
      def xmr_last: first($lastblock[]? | select((.chain // "") == "XMR")) // {};
      def xmr_inbound: first($inbound[]? | select((.chain // "") == "XMR")) // {};
      [
        ((xmr_last.last_observed_in // 0) | tostring),
        ((xmr_last.last_signed_out // 0) | tostring),
        ([$vaults[]? | select((.status // "") == "ActiveVault") | ((.membership // []) | length)] | sort | join(",")),
        ((xmr_pool.balance_rune // xmr_pool.rune_depth // "0") | tostring),
        ((xmr_pool.balance_asset // xmr_pool.asset_depth // "0") | tostring),
        ((xmr_inbound.observed_fee_rate // 0) | tostring),
        (xmr_inbound.address // "")
      ] | @tsv
    ')"
    printf '%s\t%s\n' "$node" "$summary"
  done
} | xmr_chainnet_tabulate

echo
echo "Pools ($reference_node)"
reference_pools_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/pools" 2>/dev/null || true)"
if [[ -n "$reference_pools_json" ]]; then
  printf '%s\n' "$reference_pools_json" | jq -r '
    .[]
    | [
        (.asset // ""),
        (.status // ""),
        ((.balance_rune // .rune_depth // "0") | tostring),
        ((.balance_asset // .asset_depth // "0") | tostring),
        ((.pending_inbound_rune // "0") | tostring),
        ((.pending_inbound_asset // "0") | tostring)
      ]
    | @tsv
  ' | {
    printf 'asset\tstatus\trune_depth\tasset_depth\tpending_rune\tpending_asset\n'
    cat
  } | xmr_chainnet_tabulate
else
  printf 'asset\tstatus\trune_depth\tasset_depth\tpending_rune\tpending_asset\n'
fi

echo
echo "Vaults ($reference_node)"
reference_vaults_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/vaults/asgard" 2>/dev/null || true)"
if [[ -n "$reference_vaults_json" ]]; then
  printf '%s\n' "$reference_vaults_json" | jq -r '
    .[]
    | select(.chains[]? == "XMR")
    | [
        (.status // ""),
        ((.membership // []) | length),
        (.vault_creation_height_xmr // "0"),
        (((.key_images_xmr // [])) | length),
        (((.tx_keys_xmr // [])) | length),
        (.pub_key_frost // .pub_key_eddsa // .pub_key)
      ]
    | @tsv
  ' | {
    printf 'status\tmembers\tbirthday\tkey_images_xmr\ttx_keys_xmr\tvault\n'
    cat
  } | xmr_chainnet_tabulate
else
  printf 'status\tmembers\tbirthday\tkey_images_xmr\ttx_keys_xmr\tvault\n'
fi
