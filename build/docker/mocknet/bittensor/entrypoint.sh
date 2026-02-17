#!/bin/bash
set -euo pipefail

BINARY="/target/fast-runtime/release/node-subtensor"

echo "Building chain spec..."
$BINARY build-spec --chain local > /tmp/plain-spec.json 2>/dev/null

# Fund the master TAO address in genesis
if [ -n "${TAO_MASTER_ADDR:-}" ] && [ -n "${TAO_MASTER_AMOUNT:-}" ]; then
  echo "Funding $TAO_MASTER_ADDR with $TAO_MASTER_AMOUNT rao in genesis..."
  jq --arg addr "$TAO_MASTER_ADDR" --arg amount "$TAO_MASTER_AMOUNT" \
    '.genesis.runtimeGenesis.patch.balances.balances += [[$addr, ($amount | tonumber)]]' \
    /tmp/plain-spec.json > /tmp/modified-spec.json
else
  cp /tmp/plain-spec.json /tmp/modified-spec.json
fi

echo "Converting to raw chain spec..."
$BINARY build-spec --chain /tmp/modified-spec.json --raw > /tmp/raw-spec.json 2>/dev/null

# Generate node keys
$BINARY key generate-node-key --chain=/tmp/raw-spec.json --base-path /tmp/one 2>/dev/null
$BINARY key generate-node-key --chain=/tmp/raw-spec.json --base-path /tmp/two 2>/dev/null

echo "Starting two-validator localnet..."
trap 'pkill -P $$' EXIT SIGINT SIGTERM

(
  $BINARY \
    --base-path /tmp/one \
    --chain=/tmp/raw-spec.json \
    --one \
    --port 30334 \
    --rpc-port 9944 \
    --validator \
    --rpc-cors=all \
    --allow-private-ipv4 \
    --discover-local \
    --unsafe-rpc-external \
    --unsafe-force-node-key-generation 2>&1 &

  $BINARY \
    --base-path /tmp/two \
    --chain=/tmp/raw-spec.json \
    --two \
    --port 30335 \
    --rpc-port 9945 \
    --validator \
    --rpc-cors=all \
    --allow-private-ipv4 \
    --discover-local \
    --unsafe-rpc-external \
    --unsafe-force-node-key-generation 2>&1

  wait
)
