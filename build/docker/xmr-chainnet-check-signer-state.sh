#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/xmr-chainnet-lib.sh"

usage() {
  cat <<'EOF'
Usage: build/docker/xmr-chainnet-check-signer-state.sh [--host <ssh-target>] [--node <name>|all] [--vault <pubkey>]

Examples:
  build/docker/xmr-chainnet-check-signer-state.sh
  build/docker/xmr-chainnet-check-signer-state.sh --host root@91.99.89.216
  build/docker/xmr-chainnet-check-signer-state.sh --node dog
  build/docker/xmr-chainnet-check-signer-state.sh --node dog --node cat
  build/docker/xmr-chainnet-check-signer-state.sh --vault cthorpub1...
EOF
}

requested_nodes=()
vaults=()
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
    --vault)
      [[ $# -ge 2 ]] || { echo "--vault requires a value" >&2; exit 1; }
      vaults+=("$2")
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

if ((${#vaults[@]} == 0)); then
  while IFS= read -r vault; do
    [[ -n "$vault" ]] && vaults+=("$vault")
  done < <(xmr_chainnet_discover_xmr_vaults "$reference_node" "${nodes[@]}")
fi

onchain_vaults_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/vaults/asgard" 2>/dev/null || true)"
active_vaults=()
if [[ -n "$onchain_vaults_json" ]]; then
  while IFS= read -r vault; do
    [[ -n "$vault" ]] && active_vaults+=("$vault")
  done < <(
    printf '%s\n' "$onchain_vaults_json" | jq -r '
      .[]
      | select(.status == "ActiveVault")
      | select(.chains[]? == "XMR")
      | (.pub_key_frost // .pub_key_eddsa // .pub_key)
      | select(length > 0)
    ' | sort -u
  )
fi

echo "On-chain XMR vaults"
if [[ -n "$onchain_vaults_json" ]]; then
  printf '%s\n' "$onchain_vaults_json" | jq -r '
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
    printf 'status\tmembers\tbirthday\tchain_key_images\tchain_tx_keys\tvault\n'
    cat
  } | xmr_chainnet_tabulate
else
  printf 'status\tmembers\tbirthday\tchain_key_images\tchain_tx_keys\tvault\n'
fi

echo
echo "Active vault registration coverage"
{
  printf 'node\tregistered_active\tmissing_active\tmissing_vaults\n'
  for node in "${nodes[@]}"; do
    if ! vaults_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/vaults")"; then
      printf '%s\t-\t-\tunreachable\n' "$node"
      continue
    fi

    local_registered=0
    missing=()
    for vault in "${active_vaults[@]}"; do
      if printf '%s' "$vaults_json" | jq -e --arg vault "$vault" 'first(.vaults[]? | select(.vault_pub_key == $vault)) | type == "object"' >/dev/null; then
        ((local_registered += 1))
      else
        missing+=("$(xmr_chainnet_short "$vault" 14 10)")
      fi
    done

    if ((${#missing[@]} == 0)); then
      printf '%s\t%d\t0\t-\n' "$node" "$local_registered"
    else
      printf '%s\t%d\t%d\t%s\n' "$node" "$local_registered" "${#missing[@]}" "$(IFS=, ; echo "${missing[*]}")"
    fi
  done
} | xmr_chainnet_tabulate

echo
echo "Signer state"
{
  printf 'node\tvault\tsync\tscan_height\trescan\treg_key_images\treg_tx_keys\tstore_total\tstore_unspent\tstore_reserved\tstore_spent\tstore_key_images\tscanner_total\tscanner_key_images\toutbounds\toutbounds_with_tx_keys\toutbounds_mined\toutbounds_mempool\n'
  for node in "${nodes[@]}"; do
    if ! vaults_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/vaults")"; then
      printf '%s\t%s\t%s\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\t-\n' "$node" "ERROR" "unreachable"
      continue
    fi

    for vault in "${vaults[@]}"; do
      state_json="$(printf '%s' "$vaults_json" | jq -c --arg vault "$vault" 'first(.vaults[]? | select(.vault_pub_key == $vault)) // {}')"
      store_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/outputs/$vault?view=all&source=store" 2>/dev/null || printf '%s' '{"outputs":[]}' )"
      scanner_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/outputs/$vault?view=all&source=scanner" 2>/dev/null || printf '%s' '{"outputs":[]}' )"
      outbounds_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/outbounds/$vault" 2>/dev/null || printf '%s' '{"outgoing":[]}' )"

      state_fields="$(printf '%s' "$state_json" | jq -r '[
        (.sync_state // "missing"),
        ((.scan_height // "-") | tostring),
        ((.rescan // false) | tostring),
        ((.registered_key_image_count // 0) | tostring),
        ((.registered_tx_key_count // 0) | tostring)
      ] | @tsv')"
      store_fields="$(printf '%s' "$store_json" | jq -r '[
        (.outputs | length),
        ([.outputs[]? | select((.state // "") == "unspent")] | length),
        ([.outputs[]? | select((.state // "") == "reserved")] | length),
        ([.outputs[]? | select(.spent == true or (.state // "") == "spent")] | length),
        ([.outputs[]? | select((.key_image // "") != "")] | length)
      ] | @tsv')"
      scanner_fields="$(printf '%s' "$scanner_json" | jq -r '[
        (.outputs | length),
        ([.outputs[]? | select((.key_image // "") != "")] | length)
      ] | @tsv')"
      outbound_fields="$(printf '%s' "$outbounds_json" | jq -r '[
        (.outgoing | length),
        ([.outgoing[]? | select((.tx_keys // []) | length > 0)] | length),
        ([.outgoing[]? | select((.status // "") == "observed_mined")] | length),
        ([.outgoing[]? | select((.status // "") == "observed_mempool")] | length)
      ] | @tsv')"

      IFS=$'\t' read -r sync_state scan_height rescan reg_key_images reg_tx_keys <<<"$state_fields"
      IFS=$'\t' read -r store_total store_unspent store_reserved store_spent store_key_images <<<"$store_fields"
      IFS=$'\t' read -r scanner_total scanner_key_images <<<"$scanner_fields"
      IFS=$'\t' read -r outbound_total outbound_with_tx_keys outbound_mined outbound_mempool <<<"$outbound_fields"

      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$node" \
        "$(xmr_chainnet_short "$vault" 14 10)" \
        "$sync_state" \
        "$scan_height" \
        "$rescan" \
        "$reg_key_images" \
        "$reg_tx_keys" \
        "$store_total" \
        "$store_unspent" \
        "$store_reserved" \
        "$store_spent" \
        "$store_key_images" \
        "$scanner_total" \
        "$scanner_key_images" \
        "$outbound_total" \
        "$outbound_with_tx_keys" \
        "$outbound_mined" \
        "$outbound_mempool"
    done
  done
} | xmr_chainnet_tabulate
