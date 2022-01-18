#!/bin/sh

set -e

HARDFORK_BLOCK_HEIGHT="${HARDFORK_BLOCK_HEIGHT:--1}"
DATE=$(date +%s)

# backup first
cp -r ~/.thornode/config ~/.thornode/config."$DATE".bak
cp -r ~/.thornode/data ~/.thornode/data."$DATE".bak

# export genesis file
thornode export --height "$HARDFORK_BLOCK_HEIGHT" >thorchain_genesis_export."$DATE".json

# reset the database
thornode unsafe-reset-all

# update chain id
jq '.chain_id="thorchain-v1"' thorchain_genesis_export."$DATE".json >temp.json

# remove all BNB assets
jq 'del(.app_state.bank.balances[].coins[]| select(.denom == "bnb/bnb" or .denom == "bnb/busd-74e" or .denom == "bnb/busd-baf")) |
     del(.app_state.bank.supply[]| select(.denom == "bnb/bnb" or .denom == "bnb/busd-74e" or .denom == "bnb/busd-baf")) |
     del(.app_state.thorchain.liquidity_providers[] | select (.asset == "BNB.BNB" or .asset == "BNB.BUSD-74E" or .asset == "BNB.BUSD-BAF")) |
     del(.app_state.thorchain.vaults[].coins[]|select(.asset == "BNB.BNB" or .asset == "BNB.BUSD-74E" or .asset == "BNB.BUSD-BAF")) |
     del(.app_state.thorchain.pools[] | select(.asset == "BNB.BNB" or .asset == "BNB.BUSD-74E" or .asset == "BNB.BUSD-BAF"))' temp.json > temp1.json
# copied exported genesis file to the config directory
cp temp1.json ~/.thornode/config/genesis.json
