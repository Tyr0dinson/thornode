#!/bin/bash

# Start test validator
solana-test-validator --ledger /tmp/test-ledger --reset --log --limit-ledger-size --url http://localhost:8899 &

# Wait for the validator to be ready
sleep 10

# Check if the validator is running
until solana cluster-version; do
  echo "Waiting for the validator to be fully up..."
  sleep 5
done

# Set local URL for Solana CLI
solana config set --url http://localhost:$RPC_PORT

# Generate a new keypair. This wallet isn't used, but needed to enable the airdrop command.
TEMP_KEYPAIR="/tmp/solana-keypair.json"
if [ ! -f $TEMP_KEYPAIR ]; then
  solana-keygen new --outfile $TEMP_KEYPAIR --no-passphrase
fi

# Set the generated keypair as the default wallet (CLI needs this to interact with the network)
solana config set --keypair $TEMP_KEYPAIR

# Airdrop to master account
solana airdrop 10000 AAH1XhfycX678et7Pjvwp1DMWnHpSP92wpJwV19MXHtX
