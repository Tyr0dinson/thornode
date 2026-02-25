#!/bin/bash

export PATH=$PATH:/root/bin
export CARDANO_CLI=/root/bin/cardano-cli
export CARDANO_NODE=/root/bin/cardano-node
export CARDANO_NODE_SOCKET_PATH=/root/mocknet/node.socket
export CARDANO_NODE_NETWORK_ID=42

MASTER=60b318969256969713685eed5cab48df77e67405613325c9ffb065e78f

if [ ! -d /root/mocknet ]; then
	cardano-testnet create-env \
		--num-pool-nodes 1 \
		--num-dreps 1 \
		--nodeLoggingFormat text \
		--output mocknet

	jq --arg MASTER "$MASTER" '.initialFunds[$MASTER] = 20000000000000' < mocknet/shelley-genesis.json > /tmp/shelley-genesis.json
	mv  /tmp/shelley-genesis.json mocknet/shelley-genesis.json

	sed -ri "s/(\"Trace[^\"]+\":) true/\\1 false/" mocknet/configuration.yaml
	sed -ri "s/ScJson/ScText/" mocknet/configuration.yaml
fi 

nohup socat TCP-LISTEN:3002,fork UNIX-CONNECT:$CARDANO_NODE_SOCKET_PATH &

cardano-node run \
--config mocknet/configuration.yaml \
--topology mocknet/node-data/node1/topology.json \
--database-path mocknet/node-data/node1/db \
--socket-path $CARDANO_NODE_SOCKET_PATH \
--host-addr 0.0.0.0 \
--port 3001 \
--shelley-kes-key mocknet/pools-keys/pool1/kes.skey \
--shelley-vrf-key mocknet/pools-keys/pool1/vrf.skey \
--shelley-operational-certificate mocknet/pools-keys/pool1/opcert.cert &

NODE_PID=$!

while [ ! -S $CARDANO_NODE_SOCKET_PATH ]; do
  echo "waiting for socket"
  sleep 1
done

socat TCP-LISTEN:3002,fork,keepalive,keepidle=120,keepintvl=10,keepcnt=6 UNIX-CONNECT:$CARDANO_NODE_SOCKET_PATH &

SOCAT_PID=$!

wait $NODE_PID $SOCAT_PID