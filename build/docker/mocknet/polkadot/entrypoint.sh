#!/bin/bash
set -euo pipefail

SHARED=/shared
PARA_SPEC=$SHARED/para-spec.json
RELAY_SPEC=$SHARED/relay-spec.json
PARA_ID=1000

# Allow host overrides for local tests.
ALICE_HOST=${ALICE_HOST:-polkadot-relay-alice}
BOB_HOST=${BOB_HOST:-polkadot-relay-bob}
COLLATOR_HOST=${COLLATOR_HOST:-polkadot}

# Use separate ports for each collator's embedded relay node.
P2P_PORT=30333
RPC_PORT=9944
EMBEDDED_RELAY_P2P_PORT=30334
EMBEDDED_RELAY_RPC_PORT=9945

node_key_of() { echo "$SHARED/node-key-$1"; }
peer_id_of() { cat "$SHARED/peer-id-$1"; }

# Build a libp2p bootnode address.
bootnode_at() { echo "/dns4/$1/tcp/$P2P_PORT/p2p/$(peer_id_of "$2")"; }

# Create stable node keys and peer IDs.
generate_node_keys() {
  for node in relay-alice relay-bob collator-alice collator-bob; do
    local key
    key=$(node_key_of "$node")
    if [ ! -f "$key" ]; then
      echo "Generating node key for $node..."
      polkadot key generate-node-key --file "$key" 2>/dev/null
      chmod 600 "$key"
    fi
    polkadot key inspect-node-key --file "$key" >"$SHARED/peer-id-$node"
  done
}

# Generate node keys and chain specs.
run_genesis() {
  generate_node_keys

  # Replace each completed spec atomically.
  cp /usr/local/lib/plain-spec.json "$PARA_SPEC.tmp"
  if [ -n "${DOT_MASTER_ADDR-}" ] && [ -n "${DOT_MASTER_AMOUNT-}" ]; then
    echo "Funding $DOT_MASTER_ADDR with $DOT_MASTER_AMOUNT planck in genesis..."
    add-master.py "$PARA_SPEC.tmp" "$DOT_MASTER_ADDR" "$DOT_MASTER_AMOUNT"
  fi
  mv "$PARA_SPEC.tmp" "$PARA_SPEC"

  echo "Exporting parachain genesis head and validation code..."
  polkadot-omni-node export-genesis-head --chain "$PARA_SPEC" >"$SHARED/para-head.hex"
  polkadot-omni-node export-genesis-wasm --chain "$PARA_SPEC" >"$SHARED/para-wasm.hex"

  echo "Building relay chain spec with parachain $PARA_ID registered at genesis..."
  polkadot build-spec --chain rococo-local --disable-default-bootnode >"$SHARED/relay-plain.json"
  register-para.py "$SHARED/relay-plain.json" "$PARA_ID" \
    "$SHARED/para-head.hex" "$SHARED/para-wasm.hex"
  polkadot build-spec --chain "$SHARED/relay-plain.json" --raw --disable-default-bootnode \
    >"$RELAY_SPEC.tmp"
  mv "$RELAY_SPEC.tmp" "$RELAY_SPEC"

  echo "Chain specs ready."
}

# Start an Alice or Bob relay validator.
run_relay() {
  local name bootnodes address
  name=${RELAY_NAME:?RELAY_NAME must be set to alice or bob}

  bootnodes=()
  if [ "$name" != alice ]; then
    address=$(bootnode_at "$ALICE_HOST" relay-alice)
    echo "Joining relay chain through alice at $address"
    bootnodes=(--bootnodes "$address")
  fi

  echo "Starting relay chain validator $name..."

  # Disable PVF sandboxing for the trusted local runtime.
  exec polkadot \
    --chain "$RELAY_SPEC" \
    "--$name" \
    --validator \
    --insecure-validator-i-know-what-i-do \
    --node-key-file "$(node_key_of "relay-$name")" \
    --base-path /tmp/relay \
    --port "$P2P_PORT" \
    --rpc-port "$RPC_PORT" \
    --rpc-cors=all \
    --unsafe-rpc-external \
    "${bootnodes[@]}"
}

# Start an Asset Hub collator backed by the local relay chain.
run_collator() {
  local name bootnodes address
  name=${COLLATOR_NAME:?COLLATOR_NAME must be set to alice or bob}

  bootnodes=()
  if [ "$name" != alice ]; then
    address=$(bootnode_at "$COLLATOR_HOST" collator-alice)
    echo "Joining parachain network through the first collator at $address"
    bootnodes=(--bootnodes "$address")
  fi

  echo "Starting Asset Hub collator $name against the local relay chain..."

  # Asset Hub requires slot-based authoring for async backing.
  # Force authoring because the local collator has no sync peer.
  # Read relay state from both validators over RPC.
  exec polkadot-omni-node \
    --chain "$PARA_SPEC" \
    --collator \
    "--$name" \
    --authoring slot-based \
    --force-authoring \
    --node-key-file "$(node_key_of "collator-$name")" \
    --relay-chain-rpc-urls "ws://$ALICE_HOST:$RPC_PORT" "ws://$BOB_HOST:$RPC_PORT" \
    --base-path /tmp/asset-hub \
    --state-pruning archive \
    --blocks-pruning archive \
    --port "$P2P_PORT" \
    --rpc-port "$RPC_PORT" \
    --rpc-cors=all \
    --unsafe-rpc-external \
    "${bootnodes[@]}" \
    -- \
    --chain "$RELAY_SPEC" \
    --port "$EMBEDDED_RELAY_P2P_PORT" \
    --rpc-port "$EMBEDDED_RELAY_RPC_PORT" \
    --bootnodes "$(bootnode_at "$ALICE_HOST" relay-alice)"
}

case "${ROLE:?ROLE must be set to genesis, relay or collator}" in
  genesis) run_genesis ;;
  relay) run_relay ;;
  collator) run_collator ;;
  *)
    echo "Unknown ROLE '$ROLE'; expected genesis, relay or collator." >&2
    exit 1
    ;;
esac
