#!/bin/sh

set -o pipefail

XHV_DAEMON_ADDRESS="${XHV_DAEMON_ADDRESS:=http://haven:17750}"

# Start the wallet RPC server
haven-wallet-rpc --daemon-address ${XHV_DAEMON_ADDRESS} --rpc-bind-ip 0.0.0.0 --rpc-bind-port 6061 --confirm-external-bind --disable-rpc-login --wallet-dir /wallet
