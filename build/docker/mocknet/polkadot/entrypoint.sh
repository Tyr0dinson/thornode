#!/bin/bash
set -euo pipefail

BINARY="/usr/bin/polkadot"

echo "Building chain spec..."
$BINARY export-chain-spec --chain dev >/tmp/plain-spec.json 2>/dev/null

if [ -n "${DOT_MASTER_ADDR-}" ] && [ -n "${DOT_MASTER_AMOUNT-}" ]; then
  echo "Funding $DOT_MASTER_ADDR with $DOT_MASTER_AMOUNT planck in genesis..."
  add-master.py /tmp/plain-spec.json "$DOT_MASTER_ADDR" "$DOT_MASTER_AMOUNT"
fi

cp /tmp/plain-spec.json /tmp/modified-spec.json

echo "Converting to raw chain spec..."
$BINARY export-chain-spec --chain /tmp/modified-spec.json --raw >/tmp/raw-spec.json # 2>/dev/null

# Generate node key
$BINARY key generate-node-key --chain=/tmp/raw-spec.json --base-path /tmp/alice # 2>/dev/null

echo "Starting single-validator dev localnet..."
$BINARY \
  --base-path /tmp/alice \
  --chain=/tmp/raw-spec.json \
  --alice \
  --validator \
  --force-authoring \
  --rpc-port 9944 \
  --rpc-cors=all \
  --unsafe-rpc-external \
  --insecure-validator-i-know-what-i-do
