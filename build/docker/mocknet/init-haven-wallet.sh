#!/bin/sh

set -o pipefail

# Start the wallet RPC server
haven-wallet-rpc --daemon-address http://haven:17750 --rpc-bind-ip 0.0.0.0 --rpc-bind-port 6061 --confirm-external-bind --disable-rpc-login --wallet-dir /wallet
