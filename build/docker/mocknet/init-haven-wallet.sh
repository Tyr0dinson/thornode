#!/bin/sh

set -o pipefail

XHV_HOST="${XHV_HOST:=http://haven:17750}"

# Start the wallet RPC server
haven-wallet-rpc --daemon-address ${XHV_HOST} --rpc-bind-ip 0.0.0.0 --rpc-bind-port 6061 --confirm-external-bind --disable-rpc-login --wallet-dir /wallet
