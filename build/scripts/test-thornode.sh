#!/bin/bash

export CHAIN_ID=thorchain
export SIGNER_NAME=thorchain
export SIGNER_PASSWD=password
export SEED=localhost
export SIGNER_SEED_PHRASE="dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog dog fossil"
export THOR_BLOCK_TIME=2s
export THOR_API_LIMIT_COUNT=100
export THOR_API_LIMIT_DURATION=1s
export CHURN_MIGRATION_ROUNDS=2
export FUND_MIGRATION_INTERVAL=10

export NET=mocknet

./genesis.sh