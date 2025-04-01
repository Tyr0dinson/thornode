#!/bin/bash

set -o pipefail

export CHAIN_ID=thorchain
export SIGNER_NAME=thorchain
export SIGNER_PASSWD=password
export SIGNER_SEED_PHRASE="dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"
export CHAIN_API=localhost:1317
export CHAIN_RPC=localhost:26657
export THOR_BLOCK_TIME=2s
export BLOCK_SCANNER_BACKOFF=5s
export BIFROST_METRICS_PPROF_ENABLED="true"
export BIFROST_SIGNER_BACKUP_KEYSHARES="true"
export BIFROST_SIGNER_AUTO_OBSERVE="false"
export BIFROST_SIGNER_KEYGEN_TIMEOUT=30s
export BIFROST_SIGNER_KEYSIGN_TIMEOUT=30s
export BIFROST_SIGNER_SIGNER_DB_PATH="$HOME/data/bifrost/signer_db"
export BIFROST_CHAINS_ADA_BLOCK_SCANNER_DB_PATH="$HOME/data/bifrost/observer"
export ADA_HOST="localhost:3001"
export ADA_N2C_ADDRESS="localhost:7007"

. "$(dirname "$0")/core.sh"
"$(dirname "$0")/wait-for-thorchain-api.sh" "$CHAIN_API"

create_thor_user "$SIGNER_NAME" "$SIGNER_PASSWD" "$SIGNER_SEED_PHRASE"

# dynamically set external ip if mocknet and unset
if [ "$NET" = "mocknet" ] && [ -z "$EXTERNAL_IP" ]; then
  EXTERNAL_IP=$(hostname -i)
fi

bifrost -p