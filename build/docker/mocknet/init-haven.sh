#!/bin/sh

set -o pipefail

MASTER_ADDR="${HVX_MASTER_ADDR:=hvxy6ABzHQFbfQNrDj6kbDUbk79eaEpbpTLxPjZPZcLLVnBX7KGAiep4cgPMp1hUtHAWrnAFq1o26Q8qvDf6cbmc7jtFNfXHMg}"
BLOCK_TIME=${BLOCK_TIME:=1}

# Start the daemon
havend --regtest --offline --fixed-difficulty 1 --non-interactive --rpc-bind-ip 0.0.0.0 --confirm-external-bind &


# mine a new block every BLOCK_TIME
while true
do
	curl http://127.0.0.1:17750/json_rpc -d \
	'{"jsonrpc":"2.0","id":"0","method":"generateblocks","params":{
	"amount_of_blocks":1,
	"wallet_address":"'"${MASTER_ADDR}"'",
	"starting_nonce": 0}' \
	-H 'Content-Type: application/json'
	sleep $BLOCK_TIME
done
