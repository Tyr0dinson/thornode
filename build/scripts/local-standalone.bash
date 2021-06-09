#!/bin/bash
# This script is designed to run a standalone thornode locally (non-containerized)

set -o pipefail

. "$(dirname "$0")/core.sh"

SIGNER_NAME="${SIGNER_NAME:=thorchain}"
SIGNER_PASSWD="${SIGNER_PASSWD:=password}"

create_thor_user "$SIGNER_NAME" "$SIGNER_PASSWD" "$SIGNER_SEED_PHRASE"

if [ ! -f ~/.thornode/config/priv_validator_key.json ]; then
  init_chain
  # remove the original generate genesis file , as below will init chain again
  rm -rf ~/.thornode/config/genesis.json
fi

VALIDATOR=$(thornode tendermint show-validator)
echo $VALIDATOR
NODE_ADDRESS=$(echo "$SIGNER_PASSWD" | thornode keys show thorchain -a --keyring-backend file)
NODE_PUB_KEY=$(echo "$SIGNER_PASSWD" | thornode keys show thorchain -p --keyring-backend file)
VERSION=$(fetch_version)
NODE_IP_ADDRESS="127.0.0.1"

echo "Setting THORNode as standalone"
if [ ! -f ~/.thornode/config/genesis.json ]; then
  init_chain

  add_node_account $NODE_ADDRESS $VALIDATOR $NODE_PUB_KEY $VERSION $NODE_ADDRESS $NODE_PUB_KEY $NODE_IP_ADDRESS

  # disable default bank transfer, and opt to use our own custom one
  disable_bank_send

  echo "Genesis content"
  cat ~/.thornode/config/genesis.json
  thornode validate-genesis --trace
fi

printf "%s\n%s\n" "$SIGNER_NAME" "$SIGNER_PASSWD" | thornode start --log_level debug --log_format plain --rpc.laddr "tcp://0.0.0.0:26657"
