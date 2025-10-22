#!/bin/sh
set -eu

# This script will be executed inside the container
# Start zcashd with RPC credentials for proper authentication
zcashd \
  -regtest=1 \
  -txindex \
  -nuparams=c8e71055:1 \
  -mineraddress="tmV1zYhR2xisn6VWdCNKHpeD4S7L1U1nPH6" \
  -minetolocalwallet=0 \
  -experimentalfeatures=1 \
  -lightwalletd=1 \
  -rpcuser="mayachain" \
  -rpcpassword="password" \
  -rpcallowip=0.0.0.0/0 \
  -rpcbind=0.0.0.0 \
  -rpcbind="$(hostname)" &

ZC_PID=$!

echo "Waiting for zcashd to start..."
sleep 10

echo "Generating initial blocks..."
INIT_BLOCKS=150
until zcash-cli -regtest -rpcuser=mayachain -rpcpassword=password generate "$INIT_BLOCKS" >/dev/null 2>&1; do
  if ! kill -0 "$ZC_PID" 2>/dev/null; then
    echo "zcashd exited unexpectedly; last 200 log lines (if any):"
    tail -n 200 /srv/zcashd/.zcash/debug.log 2>/dev/null || true
    exit 1
  fi
  echo "Waiting for zcashd RPC to be ready..."
  sleep 5
done

echo "Regtest setup complete!"
echo "Test mineraddress addresses funded:"
echo "  tmV1zYhR2xisn6VWdCNKHpeD4S7L1U1nPH6"
echo "RPC available at localhost:18232"

# Keep the container running by waiting for the background zcashd process
wait
