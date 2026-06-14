#!/bin/sh

set -e

SIGNER_NAME="${SIGNER_NAME:=thorchain}"
SIGNER_PASSWD="${SIGNER_PASSWD:=password}"
MASTER_ADDR="${XEC_MASTER_ADDR:=ecregtest:qzfc77h794v2scmrmsj7sjreuzmy2q9p8shq0ahtvr}"
BLOCK_TIME="${BLOCK_TIME:=1}"
RPC_PORT="${RPC_PORT:=18443}"
INIT_BLOCKS="${INIT_BLOCKS:=500}"
WALLET_NAME="${WALLET_NAME:=default}"

bitcoin_cli() {
  bitcoin-cli -regtest -rpcport="$RPC_PORT" -rpcuser="$SIGNER_NAME" -rpcpassword="$SIGNER_PASSWD" "$@"
}

bitcoin_wallet_cli() {
  bitcoin_cli -rpcwallet="$WALLET_NAME" "$@"
}

bitcoind \
  -regtest \
  -server=1 \
  -listen=1 \
  -txindex=1 \
  -rpcport="$RPC_PORT" \
  -rpcuser="$SIGNER_NAME" \
  -rpcpassword="$SIGNER_PASSWD" \
  -rpcallowip=0.0.0.0/0 \
  -rpcbind=127.0.0.1 \
  -rpcbind="$(hostname)" &

# Wait for bitcoind RPC to start before issuing wallet or mining commands.
while true; do
  bitcoin_cli getblockchaininfo >/dev/null 2>&1 && break
  sleep 5
done

wallets="$(bitcoin_cli listwallets)"
case "$wallets" in
  *\""$WALLET_NAME"\"*)
    echo "Wallet $WALLET_NAME already loaded"
    ;;
  *)
    echo "Initializing wallet $WALLET_NAME"
    if bitcoin_cli createwallet "$WALLET_NAME"; then
      echo "Wallet $WALLET_NAME created"
    else
      echo "Wallet $WALLET_NAME may already exist; attempting to load it"
      bitcoin_cli loadwallet "$WALLET_NAME"
    fi
    ;;
esac

wallets="$(bitcoin_cli listwallets)"
case "$wallets" in
  *\""$WALLET_NAME"\"*) ;;
  *)
    echo "Wallet $WALLET_NAME is not loaded" >&2
    exit 1
    ;;
esac

# Seed the chain with spendable UTXOs.
bitcoin_wallet_cli generatetoaddress "$INIT_BLOCKS" "$MASTER_ADDR"

while true; do
  bitcoin_wallet_cli generatetoaddress 1 "$MASTER_ADDR"
  sleep "$BLOCK_TIME"
done
