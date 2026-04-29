#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/xmr-chainnet-lib.sh"

usage() {
  cat <<'EOF'
Usage: build/docker/xmr-chainnet-audit-vault-inventory.sh [--host <ssh-target>] [--node <name>|all]

Audits every XMR vault from /thorchain/vaults/pubkeys against each sidecar's
registered vault metadata, outputs, key-images, txkeys, and outbound inventory.
Exits non-zero if any discrepancy is found.
EOF
}

requested_nodes=()
export XMR_CHAINNET_REMOTE_TIMEOUT="${XMR_CHAINNET_REMOTE_TIMEOUT:-30s}"
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
done < <(xmr_chainnet_expand_requested_nodes "${requested_nodes[@]:-all}")
reference_node="$(xmr_chainnet_reference_node thornode "${nodes[@]}")"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/nodes"

audit_signer_get() {
  local node="$1"
  local auth="$2"
  local path="$3"
  local container
  container="$(xmr_chainnet_signer_container "$node")"
  xmr_chainnet_run docker exec -e AUTH="$auth" -e XPATH="$path" "$container" sh -lc \
    'curl -sf --connect-timeout 2 --max-time 15 -H "Authorization: $AUTH" "http://127.0.0.1:'"$SIGNER_API_PORT"'$XPATH"'
}

xmr_chainnet_thornode_get "$reference_node" /thorchain/vaults/pubkeys > "$work/pubkeys.json"
jq -r '
  (.asgard[]? | ["asgard", .pub_key_frost, .pub_key] | @tsv),
  (.inactive[]? | ["inactive", .pub_key_frost, .pub_key] | @tsv)
' "$work/pubkeys.json" > "$work/pubkeys.tsv"

: > "$work/vaults.ndjson"
while IFS=$'\t' read -r class frost secp; do
  vault_json="$(xmr_chainnet_thornode_get "$reference_node" "/thorchain/vault/$frost" 2>/dev/null || xmr_chainnet_thornode_get "$reference_node" "/thorchain/vault/$secp" 2>/dev/null || true)"
  if [[ -z "$vault_json" ]]; then
    jq -cn --arg class "$class" --arg frost "$frost" --arg secp "$secp" \
      '{pubkey_class:$class,pub_key_frost:$frost,pub_key:$secp,lookup:false}' >> "$work/vaults.ndjson"
  else
    jq -c --arg class "$class" '. + {pubkey_class:$class, lookup:true}' <<<"$vault_json" >> "$work/vaults.ndjson"
  fi
done < "$work/pubkeys.tsv"

jq -s '
  [.[] | {
    class: .pubkey_class,
    vault: (.pub_key_frost // .pub_key_eddsa // .pub_key),
    status: (.status // ""),
    members: ((.membership // []) | length),
    birthday: ((.vault_creation_height_xmr // "0") | tonumber),
    address: ((.addresses[]? | select(.chain == "XMR") | .address) // ""),
    xmr_amount: ((.coins[]? | select(.asset == "XMR.XMR") | .amount) // "0"),
    key_images: ((.key_images_xmr // []) | map({
      tx_hash: (.tx_hash | ascii_downcase),
      output_index: (.output_index // 0),
      key_image: (.key_image | ascii_downcase)
    })),
    tx_keys: ((.tx_keys_xmr // []) | map({
      tx_hash: (.tx_hash | ascii_downcase),
      tx_key_count: ((.tx_keys // []) | length)
    }))
  }]
' "$work/vaults.ndjson" > "$work/vaults.json"
jq -r '.[].vault' "$work/vaults.json" > "$work/vaults.txt"

node_auths=()
for node in "${nodes[@]}"; do
  node_auths+=("$(xmr_chainnet_signer_auth "$(xmr_chainnet_signer_container "$node")")")
done

for i in "${!nodes[@]}"; do
  node="${nodes[$i]}"
  auth="${node_auths[$i]}"
  mkdir -p "$work/nodes/$node"
  audit_signer_get "$node" "$auth" /v1/monero/vaults > "$work/nodes/$node/vaults.json" 2>/dev/null || echo '{"vaults":[]}' > "$work/nodes/$node/vaults.json"
  while IFS= read -r vault; do
    safe="$(printf '%s' "$vault" | tr -c 'A-Za-z0-9_' '_')"
    audit_signer_get "$node" "$auth" "/v1/monero/outputs/$vault?view=all&source=store" > "$work/nodes/$node/outputs_$safe.json" 2>/dev/null || echo '{"outputs":[]}' > "$work/nodes/$node/outputs_$safe.json"
    audit_signer_get "$node" "$auth" "/v1/monero/outbounds/$vault" > "$work/nodes/$node/outbounds_$safe.json" 2>/dev/null || echo '{"outgoing":[]}' > "$work/nodes/$node/outbounds_$safe.json"
  done < "$work/vaults.txt"
done

echo "THOR XMR vault inventory"
jq -r '
  .[] | [
    .class, .status, (.members | tostring), (.birthday | tostring),
    .xmr_amount, ((.key_images | length) | tostring), ((.tx_keys | length) | tostring),
    (if .address == "" then "-" else .address end), .vault
  ] | @tsv
' "$work/vaults.json" | {
  printf 'class\tstatus\tmembers\tbirthday\txmr_amount\tkey_images\ttxkeys\taddress\tvault\n'
  cat
} | xmr_chainnet_tabulate

echo
echo "Sidecar aggregate"
{
  printf 'class\tstatus\tvault\tnodes_registered\tnodes_addr_match\tnodes_birthday_match\treg_ki_set\tthor_ki\treg_txk_set\tthor_txk\tstore_total_set\tstore_ki_set\tstore_spent_set\tstore_unspent_set\toutbound_set\toutbound_txk_set\n'
  while IFS= read -r vault; do
    thor="$(jq -c --arg vault "$vault" '.[] | select(.vault == $vault)' "$work/vaults.json")"
    safe="$(printf '%s' "$vault" | tr -c 'A-Za-z0-9_' '_')"
    : > "$work/vals"
    for node in "${nodes[@]}"; do
      jq -nr \
        --argjson thor "$thor" \
        --argjson side "$(jq -c --arg vault "$vault" 'first(.vaults[]? | select(.vault_pub_key == $vault)) // {}' "$work/nodes/$node/vaults.json")" \
        --slurpfile outputs "$work/nodes/$node/outputs_$safe.json" \
        --slurpfile outbounds "$work/nodes/$node/outbounds_$safe.json" '
        def outs: ($outputs[0].outputs // []);
        def obs: ($outbounds[0].outgoing // []);
        [
          (($side | length) > 0),
          (if ($side | length) > 0 then (($side.address // "") == ($thor.address // "")) else false end),
          (if ($side | length) > 0 then (($side.birthday // 0) == ($thor.birthday // 0)) else false end),
          (($side.registered_key_image_count // 0) | tostring),
          (($side.registered_tx_key_count // 0) | tostring),
          (outs | length | tostring),
          ([outs[]? | select((.key_image // "") != "")] | length | tostring),
          ([outs[]? | select(.state == "spent" or .spent == true)] | length | tostring),
          ([outs[]? | select(.state == "unspent")] | length | tostring),
          (obs | length | tostring),
          ([obs[]? | select((.tx_keys // []) | length > 0)] | length | tostring)
        ] | @tsv
      ' >> "$work/vals"
    done
    jq -r \
      --arg vault "$vault" \
      --arg nodes_registered "$(awk -F'\t' '$1 == "true" { c++ } END { print c + 0 }' "$work/vals")" \
      --arg addr_match "$(awk -F'\t' '$2 == "true" { c++ } END { print c + 0 }' "$work/vals")" \
      --arg birthday_match "$(awk -F'\t' '$3 == "true" { c++ } END { print c + 0 }' "$work/vals")" \
      --arg reg_ki "$(cut -f4 "$work/vals" | sort -nu | paste -sd, -)" \
      --arg reg_txk "$(cut -f5 "$work/vals" | sort -nu | paste -sd, -)" \
      --arg store_total "$(cut -f6 "$work/vals" | sort -nu | paste -sd, -)" \
      --arg store_ki "$(cut -f7 "$work/vals" | sort -nu | paste -sd, -)" \
      --arg store_spent "$(cut -f8 "$work/vals" | sort -nu | paste -sd, -)" \
      --arg store_unspent "$(cut -f9 "$work/vals" | sort -nu | paste -sd, -)" \
      --arg outbound "$(cut -f10 "$work/vals" | sort -nu | paste -sd, -)" \
      --arg outbound_txk "$(cut -f11 "$work/vals" | sort -nu | paste -sd, -)" \
      '.[] | select(.vault == $vault) | [
        .class, .status, .vault, $nodes_registered, $addr_match, $birthday_match,
        $reg_ki, ((.key_images | length) | tostring), $reg_txk, ((.tx_keys | length) | tostring),
        $store_total, $store_ki, $store_spent, $store_unspent, $outbound, $outbound_txk
      ] | @tsv' "$work/vaults.json"
  done < "$work/vaults.txt"
} | xmr_chainnet_tabulate

: > "$work/issues.txt"
jq -r '
  [.[] as $vault | $vault.key_images[]? | {vault:$vault.vault, ref:(.tx_hash + ":" + ((.output_index // 0) | tostring)), key_image:.key_image}]
  | group_by(.ref)[] | select(length > 1)
  | "THOR duplicate keyimage ref " + .[0].ref + " vaults=" + (map(.vault) | unique | join(","))
' "$work/vaults.json" >> "$work/issues.txt"
jq -r '
  [.[] as $vault | $vault.tx_keys[]? | {vault:$vault.vault, tx:.tx_hash}]
  | group_by(.tx)[] | select(length > 1)
  | "THOR duplicate txkey " + .[0].tx + " vaults=" + (map(.vault) | unique | join(","))
' "$work/vaults.json" >> "$work/issues.txt"

for node in "${nodes[@]}"; do
  while IFS= read -r vault; do
    thor="$(jq -c --arg vault "$vault" '.[] | select(.vault == $vault)' "$work/vaults.json")"
    safe="$(printf '%s' "$vault" | tr -c 'A-Za-z0-9_' '_')"
    side="$(jq -c --arg vault "$vault" 'first(.vaults[]? | select(.vault_pub_key == $vault)) // {}' "$work/nodes/$node/vaults.json")"
    jq -nr \
      --arg node "$node" \
      --arg vault "$vault" \
      --argjson thor "$thor" \
      --argjson side "$side" \
      --slurpfile outputs "$work/nodes/$node/outputs_$safe.json" \
      --slurpfile outbounds "$work/nodes/$node/outbounds_$safe.json" '
      def ref($x): (($x.tx_hash | ascii_downcase) + ":" + (($x.output_index // 0) | tostring));
      def outs: ($outputs[0].outputs // []);
      def obs: ($outbounds[0].outgoing // []);
      def thor_kirefs: [$thor.key_images[]? | .tx_hash + ":" + ((.output_index // 0) | tostring)] | unique;
      def store_kirefs: [outs[]? | select((.key_image // "") != "") | ref(.)] | unique;
      def thor_txrefs: [$thor.tx_keys[]? | .tx_hash] | unique;
      def outbound_txrefs: [obs[]? | select((.tx_keys // []) | length > 0) | (.tx_hash | ascii_downcase)] | unique;
      [
        (if ($side | length) == 0 then "missing sidecar vault" else empty end),
        (if ($side | length) > 0 and (($side.address // "") != ($thor.address // "")) then "address mismatch" else empty end),
        (if ($side | length) > 0 and (($side.birthday // 0) != ($thor.birthday // 0)) then "birthday mismatch sidecar=" + (($side.birthday // 0) | tostring) + " thor=" + (($thor.birthday // 0) | tostring) else empty end),
        (if ($side | length) > 0 and (($side.registered_key_image_count // 0) != ($thor.key_images | length)) then "registered keyimage count mismatch sidecar=" + (($side.registered_key_image_count // 0) | tostring) + " thor=" + (($thor.key_images | length) | tostring) else empty end),
        (if ($side | length) > 0 and (($side.registered_tx_key_count // 0) != ($thor.tx_keys | length)) then "registered txkey count mismatch sidecar=" + (($side.registered_tx_key_count // 0) | tostring) + " thor=" + (($thor.tx_keys | length) | tostring) else empty end),
        ((thor_kirefs - store_kirefs)[]? | "THOR keyimage missing from store ref=" + .),
        ((store_kirefs - thor_kirefs)[]? | "store keyimage not on THOR ref=" + .),
        ((thor_txrefs - outbound_txrefs)[]? | "THOR txkey missing from outbounds tx=" + .),
        ((outbound_txrefs - thor_txrefs)[]? | "outbound txkey not on THOR tx=" + .),
        ([outs[]? | ref(.)] | group_by(.)[] | select(length > 1) | "duplicate store output ref=" + .[0]),
        ([obs[]? | (.tx_hash | ascii_downcase)] | group_by(.)[] | select(length > 1) | "duplicate outbound tx=" + .[0]),
        (obs[]? | select(((.memo // "") | ascii_downcase | test("migrate|consolidate")) or (.has_owned_change == true)) | select(((.tx_keys // []) | length) == 0 or (.key_images_ready != true)) | "edge outbound incomplete tx=" + (.tx_hash | ascii_downcase) + " memo=" + (.memo // ""))
      ][] | select(length > 0) | $node + "\t" + $vault + "\t" + .
    ' >> "$work/issues.txt"
  done < "$work/vaults.txt"
done

echo
echo "Discrepancies"
if [[ -s "$work/issues.txt" ]]; then
  awk '{ print "ISSUE\t" $0 }' "$work/issues.txt" | xmr_chainnet_tabulate
  echo
  printf 'SUMMARY nodes=%s vaults=%s issues=%s\n' "${#nodes[@]}" "$(wc -l < "$work/vaults.txt" | tr -d ' ')" "$(grep -cve '^$' "$work/issues.txt")"
  exit 1
fi

echo "No discrepancies found across all /thorchain/vaults/pubkeys XMR vaults."
printf 'SUMMARY nodes=%s vaults=%s issues=0\n' "${#nodes[@]}" "$(wc -l < "$work/vaults.txt" | tr -d ' ')"
