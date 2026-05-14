#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="${XMR_CHAINNET_SCRIPT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
source "$SCRIPT_DIR/xmr-chainnet-lib.sh"

usage() {
  cat <<'EOF'
Usage: build/docker/xmr-chainnet-inventory-check.sh [--host <ssh-target>] [--node <name>|all] [--vault <pubkey>]

Print XMR sidecar inventory: node, vault, outputs, key-images, tx-keys, spendable balance.
EOF
}

requested_nodes=()
vaults=()
remote_host=""
remote_args=()
while (($# > 0)); do
  case "$1" in
    --host)
      [[ $# -ge 2 ]] || { echo "--host requires a value" >&2; exit 1; }
      remote_host="$2"
      shift 2
      ;;
    --node)
      [[ $# -ge 2 ]] || { echo "--node requires a value" >&2; exit 1; }
      requested_nodes+=("$2")
      remote_args+=("--node" "$2")
      shift 2
      ;;
    --vault)
      [[ $# -ge 2 ]] || { echo "--vault requires a value" >&2; exit 1; }
      vaults+=("$2")
      remote_args+=("--vault" "$2")
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

if [[ -n "$remote_host" && -z "${XMR_CHAINNET_NO_REMOTE_EXEC:-}" ]]; then
	remote_root="${XMR_CHAINNET_REMOTE_ROOT:-/root/thornode}"
	printf -v remote_cmd 'cd %q && XMR_CHAINNET_SCRIPT_DIR=build/docker XMR_CHAINNET_NO_REMOTE_EXEC=1 bash -s -- ' "$remote_root"
	if ((${#remote_args[@]} > 0)); then
		printf -v quoted_args '%q ' "${remote_args[@]}"
		remote_cmd+="$quoted_args"
	fi
	ssh -o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=2 "$remote_host" "$remote_cmd" <"$0"
	exit 0
fi

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

{
  printf 'node\tvault\toutputs\tkey_images\ttx_keys\tspendable_balance\n'
  for node in "${nodes[@]}"; do
    vaults_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/vaults" 2>/dev/null || true)"
    if [[ -z "$vaults_json" ]]; then
      printf '%s\tERROR\t-\t-\t-\tunreachable\n' "$node"
      continue
    fi

    for vault in "${vaults[@]}"; do
      state_json="$(printf '%s' "$vaults_json" | jq -c --arg vault "$vault" 'first(.vaults[]? | select(.vault_pub_key == $vault)) // {}')"
      outputs_json="$(xmr_chainnet_signer_get "$node" "/v1/monero/outputs/$vault?view=all&source=store" 2>/dev/null || printf '%s' '{"outputs":[]}')"
      outputs="$(printf '%s' "$outputs_json" | jq -r '[.outputs[]? | select((.amount // 0) > 0)] | length')"
      key_images="$(printf '%s' "$outputs_json" | jq -r '[.outputs[]? | select((.amount // 0) > 0 and ((.key_image // "") != ""))] | length')"
      tx_keys="$(printf '%s' "$state_json" | jq -r '(.registered_tx_key_count // 0) | tostring')"
      scan_height="$(printf '%s' "$state_json" | jq -r '(.scan_height // 0)')"
      spendable="$(printf '%s' "$outputs_json" | jq -r --argjson h "$scan_height" '
        ([.outputs[]?
          | select((.amount // 0) > 0)
          | select((.state // "") == "unspent")
          | select(($h - (.height // 0)) >= 10)
          | (.amount // 0)
        ] | add // 0) / 1000000000000
      ')"

      printf '%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$node" \
        "$(xmr_chainnet_short "$vault" 14 10)" \
        "$outputs" \
        "$key_images" \
        "$tx_keys" \
        "$spendable"
    done
  done
} | xmr_chainnet_tabulate
