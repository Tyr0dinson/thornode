#!/bin/sh

set -o pipefail

# Start the daemon
havend --testnet --offline --fixed-difficulty 10 --non-interactive --rpc-bind-ip 0.0.0.0 --confirm-external-bind
