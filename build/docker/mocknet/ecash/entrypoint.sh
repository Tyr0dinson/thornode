#!/bin/sh

set -e

SIGNER_NAME="${SIGNER_NAME:=thorchain}"
SIGNER_PASSWD="${SIGNER_PASSWD:=password}"
MASTER_ADDR="${XEC_MASTER_ADDR:=ecregtest:qpxh73huzlhjfzcccr03zkpd9nd3wsaseg5aean4r2}"
BLOCK_TIME="${BLOCK_TIME:=1}"
RPC_PORT="${RPC_PORT:=18443}"
INIT_BLOCKS="${INIT_BLOCKS:=500}"

bitcoin_cli() {
  bitcoin-cli -regtest -rpcport="$RPC_PORT" -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" "$@"
}

# Disable Avalanche because single-node mocknet has no quorum to finalize
# mempool transactions for inclusion in mined blocks.
bitcoind \
  -regtest \
  -avalanche=0 \
  -server=1 \
  -listen=1 \
  -txindex=1 \
  -rpcport="$RPC_PORT" \
  -rpcuser="$SIGNER_NAME" \
  -rpcpassword="$SIGNER_PASSWD" \
  -rpcallowip=0.0.0.0/0 \
  -rpcbind=127.0.0.1 \
  -rpcbind="$(hostname)" &

# Wait for bitcoind RPC to start before issuing mining commands.
while true; do
  bitcoin_cli getblockchaininfo >/dev/null 2>&1 && break
  sleep 5
done

# Seed the chain with spendable UTXOs.
bitcoin_cli generatetoaddress "$INIT_BLOCKS" "$MASTER_ADDR"

while true; do
  bitcoin_cli generatetoaddress 1 "$MASTER_ADDR"
  sleep "$BLOCK_TIME"
done
