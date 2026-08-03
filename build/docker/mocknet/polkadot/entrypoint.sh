#!/bin/bash
set -euo pipefail

BINARY="/usr/local/bin/polkadot-omni-node"

# Plain spec was generated at image build from the production Asset Hub
# Polkadot runtime; only the genesis balances patch is per-container.
cp /usr/local/lib/plain-spec.json /tmp/spec.json

if [ -n "${DOT_MASTER_ADDR-}" ] && [ -n "${DOT_MASTER_AMOUNT-}" ]; then
  echo "Funding $DOT_MASTER_ADDR with $DOT_MASTER_AMOUNT planck in genesis..."
  add-master.py /tmp/spec.json "$DOT_MASTER_ADDR" "$DOT_MASTER_AMOUNT"
fi

# --dev-block-time runs a standalone parachain node with manual seal (no relay
# chain needed); each block is finalized instantly, matching the scanner's
# max_reorg_rescan_blocks: 0 assumption. First boot spends a few minutes
# building genesis state from the plain spec — the compose healthcheck
# start_period accounts for this.
echo "Starting single-node Asset Hub dev localnet (manual seal)..."
exec $BINARY \
  --chain /tmp/spec.json \
  --dev-block-time 1000 \
  --base-path /tmp/asset-hub \
  --state-pruning archive \
  --blocks-pruning archive \
  --rpc-port 9944 \
  --rpc-cors=all \
  --unsafe-rpc-external
