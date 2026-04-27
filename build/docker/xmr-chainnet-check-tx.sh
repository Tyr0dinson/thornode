#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/xmr-chainnet-lib.sh"

usage() {
  cat <<'EOF'
Usage: build/docker/xmr-chainnet-check-tx.sh --tx <hash> [--host <ssh-target>] [--node <name>|all] [--vault <pubkey>] [--logs-since <window>]

Examples:
  build/docker/xmr-chainnet-check-tx.sh --tx 031ccf...
  build/docker/xmr-chainnet-check-tx.sh --host root@91.99.89.216 --tx 031ccf...
  build/docker/xmr-chainnet-check-tx.sh --tx 031ccf... --node dog
  build/docker/xmr-chainnet-check-tx.sh --tx 031ccf... --node all --logs-since 2h
EOF
}

tx_hash=""
logs_since="30m"
requested_nodes=()
vaults=()
while (($# > 0)); do
  case "$1" in
    --tx)
      [[ $# -ge 2 ]] || { echo "--tx requires a value" >&2; exit 1; }
      tx_hash="$2"
      shift 2
      ;;
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
    --logs-since)
      [[ $# -ge 2 ]] || { echo "--logs-since requires a value" >&2; exit 1; }
      logs_since="$2"
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
xmr_chainnet_require jq curl column grep

[[ -n "$tx_hash" ]] || { echo "--tx is required" >&2; usage >&2; exit 1; }

tx_lower="$(printf '%s' "$tx_hash" | tr '[:upper:]' '[:lower:]')"
tx_upper="$(printf '%s' "$tx_hash" | tr '[:lower:]' '[:upper:]')"
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

if ((${#vaults[@]} == 0)); then
  while IFS= read -r vault; do
    [[ -n "$vault" ]] && vaults+=("$vault")
  done < <(xmr_chainnet_canonical_xmr_vaults "$reference_node" 2>/dev/null || true)
fi

if ((${#vaults[@]} == 0)); then
  while IFS= read -r vault; do
    [[ -n "$vault" ]] && vaults+=("$vault")
  done < <(xmr_chainnet_local_xmr_vaults "${nodes[@]}")
fi

echo "Thornode tx ($reference_node)"
tx_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/tx/$tx_upper" 2>/dev/null || true)"
if [[ -n "$tx_json" ]] && jq -e . >/dev/null 2>&1 <<<"$tx_json"; then
  printf '%s\n' "$tx_json" | jq .
else
  echo "MISSING"
fi

echo
echo "Signer transfers"
for node in "${nodes[@]}"; do
  echo "== $node =="
  mempool_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/transfers?mempool=true" 2>/dev/null || true)"
  mined_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/transfers?height=0" 2>/dev/null || true)"

  echo "mempool:"
  if [[ -n "$mempool_json" ]]; then
    printf '%s\n' "$mempool_json" | jq -c --arg tx "$tx_lower" '
      [
        (.incoming[]? | select((.txid // "") | ascii_downcase == $tx) | . + {direction:"incoming"}),
        (.outgoing[]? | select((.txid // "") | ascii_downcase == $tx) | . + {direction:"outgoing"})
      ] | flatten
    '
  else
    echo "[]"
  fi

  echo "mined:"
  if [[ -n "$mined_json" ]]; then
    printf '%s\n' "$mined_json" | jq -c --arg tx "$tx_lower" '
      [
        (.incoming[]? | select((.txid // "") | ascii_downcase == $tx) | . + {direction:"incoming"}),
        (.outgoing[]? | select((.txid // "") | ascii_downcase == $tx) | . + {direction:"outgoing"})
      ] | flatten
    '
  else
    echo "[]"
  fi
done

echo
echo "Signer outbound inventory"
if ((${#vaults[@]} == 0)); then
  echo "no XMR vaults discovered"
else
  for node in "${nodes[@]}"; do
    echo "== $node =="
    for vault in "${vaults[@]}"; do
      outbound_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/outbounds/$vault" 2>/dev/null || true)"
      printf '%s ' "$(xmr_chainnet_short "$vault" 14 10)"
      if [[ -n "$outbound_json" ]]; then
        printf '%s\n' "$outbound_json" | jq -c --arg tx "$tx_lower" '
          [ .outgoing[]? | select((.tx_hash // "") | ascii_downcase == $tx) ] // []
        '
      else
        echo "[]"
      fi
    done
  done
fi

echo
echo "Tracked outputs"
if ((${#vaults[@]} == 0)); then
  echo "no XMR vaults discovered"
else
  for node in "${nodes[@]}"; do
    echo "== $node =="
    for vault in "${vaults[@]}"; do
      outputs_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/outputs/$vault?view=all&source=store" 2>/dev/null || true)"
      printf '%s ' "$(xmr_chainnet_short "$vault" 14 10)"
      if [[ -n "$outputs_json" ]]; then
        printf '%s\n' "$outputs_json" | jq -c --arg tx "$tx_lower" '
          [ .outputs[]? | select((.tx_hash // "") | ascii_downcase == $tx) ] // []
        '
      else
        echo "[]"
      fi
    done
  done
fi

echo
echo "Bifrost log hits ($logs_since)"
for node in "${nodes[@]}"; do
  container="$(xmr_chainnet_bifrost_container "$node")"
  if ! xmr_chainnet_container_exists "$container"; then
    continue
  fi
  hits="$(xmr_chainnet_bifrost_logs "$node" --since "$logs_since" 2>&1 | grep -i "$tx_lower" || true)"
  if [[ -z "$hits" ]]; then
    continue
  fi
  echo "== $node =="
  printf '%s\n' "$hits" | tail -n 12
done
