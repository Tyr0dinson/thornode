#!/bin/bash
set -euo pipefail

BINARY="/target/fast-runtime/release/node-subtensor"

echo "Building chain spec..."
# Reduce to single authority so one node can finalize
$BINARY build-spec --chain local --disable-default-bootnode 2>/dev/null |
  jq '.genesis.runtimeGenesis.patch.aura.authorities |= [.[0]] |
      .genesis.runtimeGenesis.patch.grandpa.authorities |= [.[0]]' \
    >/tmp/plain-spec.json

# Fund the master TAO address in genesis
if [ -n "${TAO_MASTER_ADDR-}" ] && [ -n "${TAO_MASTER_AMOUNT-}" ]; then
  echo "Funding $TAO_MASTER_ADDR with $TAO_MASTER_AMOUNT rao in genesis..."
  jq --arg addr "$TAO_MASTER_ADDR" --arg amount "$TAO_MASTER_AMOUNT" \
    '.genesis.runtimeGenesis.patch.balances.balances += [[$addr, ($amount | tonumber)]]' \
    /tmp/plain-spec.json >/tmp/modified-spec.json
else
  cp /tmp/plain-spec.json /tmp/modified-spec.json
fi

echo "Converting to raw chain spec..."
$BINARY build-spec --chain /tmp/modified-spec.json --raw >/tmp/raw-spec.json 2>/dev/null

# Generate node key
$BINARY key generate-node-key --chain=/tmp/raw-spec.json --base-path /tmp/one

echo "Starting single-validator dev localnet..."
$BINARY \
  --base-path /tmp/one \
  --chain=/tmp/raw-spec.json \
  --one \
  --validator \
  --force-authoring \
  --state-pruning archive \
  --blocks-pruning archive \
  --rpc-port 9944 \
  --rpc-cors=all \
  --unsafe-rpc-external
