#!/bin/bash

# Wait for the validator to be ready

echo "Airdrop SOL to master account"

sleep 10

# set local url
solana config set --url localhost

# airdrop to master account
solana airdrop 10000 AAH1XhfycX678et7Pjvwp1DMWnHpSP92wpJwV19MXHtX