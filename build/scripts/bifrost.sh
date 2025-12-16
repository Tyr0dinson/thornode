#!/bin/bash

set -o pipefail

CHAIN_API="${CHAIN_API:=127.0.0.1:1317}"

. "$(dirname "$0")/core.sh"
"$(dirname "$0")/wait-for-thorchain-api.sh" "$CHAIN_API"

create_thor_user "$SIGNER_NAME" "$SIGNER_PASSWD" "$SIGNER_SEED_PHRASE"

# dynamically set external ip if mocknet and unset
if [ "$NET" = "mocknet" ] && [ -z "$EXTERNAL_IP" ]; then
  EXTERNAL_IP=$(hostname -i)
fi

seed="$HOME/.thornode/address_book.seed"
for host in bifrost bifrost-cat bifrost-fox bifrost-pig; do
  if [ "$(hostname)" == "$host" ]; then
    continue
  fi

  address=$(getent hosts $host | awk '{print $1}')
  if [ -z "$address" ]; then
    continue
  fi

  p2pid=$(curl -s "http://$address:6040/p2pid")
  if [ -z "$p2pid" ]; then
    continue
  fi

  echo "/ip4/$address/tcp/5040/p2p/$p2pid" >>"$seed"
done

if [ -f "$seed" ]; then
  sort -uo "$seed" "$seed"
fi

export SIGNER_NAME SIGNER_PASSWD
exec "$@"
