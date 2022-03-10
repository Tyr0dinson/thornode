#!/bin/sh

set -o pipefail

HAVEN_ADDRESS="hvta8RELT6TbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNGc7f7"

# Start mining on the daemon
start_mining () {
  curl http://haven:27750/start_mining -d "{\"do_background_mining\":false, \"ignore_battery\":true, \"miner_address\":\"${HAVEN_ADDRESS}\", \"threads_count\":1}" -H 'Content-Type: application/json'
}
# until start_mining; do
#   echo "Wait for daemon to be ready..."
#   sleep 1
# done

# Start the wallet RPC server
haven-wallet-rpc --daemon-address http://haven:17750 --rpc-bind-ip 0.0.0.0 --rpc-bind-port 6061 --confirm-external-bind --disable-rpc-login --wallet-dir /wallet
