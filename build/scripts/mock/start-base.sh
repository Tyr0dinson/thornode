#!/bin/bash
BLOCK_TIME="${BLOCK_TIME:=5}"

json_rpc() {
  local body="$1"
  exec 3<>/dev/tcp/localhost/8545
  printf "POST / HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s" "${#body}" "$body" >&3
  cat <&3
  exec 3>&-
}

# Start op-reth in dev mode in the background.
op-reth node --dev --dev.block-time "${BLOCK_TIME}s" \
  --http --http.addr 0.0.0.0 --http.port 8545 \
  --http.api "eth,net,web3,txpool,debug" \
  --http.corsdomain "*" \
  -vvv &

RETH_PID=$!

# Wait for the RPC server to be ready.
while ! json_rpc '{"jsonrpc":"2.0","method":"net_version","params":[],"id":0}' 2>/dev/null | grep -q result; do
  sleep 1
done

# op-reth dev mode pre-funds accounts from the Hardhat mnemonic but does not
# return them via eth_accounts (which returns separate random addresses).
# The evm-tool.py expects the admin to be funded. Pre-fund the admin address
# (0x8db97C7cEcE249c2b98bDC0226Cc4C2A57BF52FC) with 900k ETH using a pre-signed
# tx from Hardhat account #0 (nonce=0, chainId=1337).
if ! json_rpc '{"jsonrpc":"2.0","method":"eth_sendRawTransaction","params":["0xf86f80843b9aca00825208948db97c7cece249c2b98bdc0226cc4c2a57bf52fc8abe951906eba2a800000080820a96a0817355ef2841f187dcb2c1900a90f3c1541056a3ffedd2282bb390fb27b16befa01295777af7c7a599a5e03e8501cfa13b0781a0b6032f6590c3051e839975dfee"],"id":1}' | grep -q result; then
  echo "ERROR: failed to fund admin address"
  exit 1
fi

wait $RETH_PID
