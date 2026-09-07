#!/bin/bash

set -o pipefail

CHAIN_API="${CHAIN_API:=127.0.0.1:1317}"

. "$(dirname "$0")/core.sh"

resolve_signer_seed_phrase

"$(dirname "$0")/wait-for-thorchain-api.sh" "$CHAIN_API"

wait_for_tss_bootstrap_peer() {
  local peers="$1"
  local peer=""
  local peer_id=""

  if [ -z "$peers" ]; then
    return 0
  fi

  echo "Waiting for bifrost bootstrap peer(s): $peers"
  until [ -n "$peer_id" ]; do
    IFS=',' read -r -a peer_list <<<"$peers"
    for peer in "${peer_list[@]}"; do
      peer_id=$(curl -fsS --max-time 2 "http://$peer:6040/p2pid" 2>/dev/null || true)
      if [ -n "$peer_id" ]; then
        break
      fi
    done
    sleep 1
  done

  echo "Bootstrap peer ready: $peer ($peer_id)"
}

wait_for_tss_bootstrap_peer "${PEER-}"

create_thor_user "$SIGNER_NAME" "$SIGNER_PASSWD" "$SIGNER_SEED_PHRASE"

# dynamically set external ip if mocknet and unset
if [ "$NET" = "mocknet" ] && [ -z "$EXTERNAL_IP" ]; then
  EXTERNAL_IP=$(hostname -i)
fi

export SIGNER_NAME SIGNER_PASSWD
exec "$@"
