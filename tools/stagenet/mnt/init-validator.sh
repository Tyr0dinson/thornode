#!/bin/sh

# set version
printf "%s\n%s\n" "password" "password" | thornode tx thorchain set-version --from thorchain --keyring-backend file --chain-id "$CHAIN_ID" --node http://localhost:27147 --yes

# Resolve the signer seed phrase: `thornode ed25519` now derives the ed25519
# pubkey from the mnemonic on stdin (it no longer reads the keyring password).
# Prefer the file mount so the secret stays off the process args/env.
if [ -n "$SIGNER_SEED_PHRASE_FILE" ] && [ -r "$SIGNER_SEED_PHRASE_FILE" ]; then
  SIGNER_SEED_PHRASE="$(cat "$SIGNER_SEED_PHRASE_FILE")"
fi
if [ -z "$SIGNER_SEED_PHRASE" ]; then
  echo "SIGNER_SEED_PHRASE (or SIGNER_SEED_PHRASE_FILE) must be set to derive the ed25519 node key" >&2
  exit 1
fi

# set node keys
NODE_PUB_KEY=$(echo "password" | thornode keys show thorchain --pubkey --keyring-backend file | thornode pubkey)
NODE_PUB_KEY_ED25519=$(printf "%s\n" "$SIGNER_SEED_PHRASE" | thornode ed25519)
VALIDATOR=$(thornode tendermint show-validator | thornode pubkey --bech cons)
printf "%s\n%s\n" "password" "password" | thornode tx thorchain set-node-keys "$NODE_PUB_KEY" "$NODE_PUB_KEY_ED25519" "$VALIDATOR" --from thorchain --keyring-backend file --chain-id "$CHAIN_ID" --node http://localhost:27147 --yes

# set node ip
printf "%s\n%s\n" "password" "password" | thornode tx thorchain set-ip-address "$(hostname -i)" --from thorchain --keyring-backend file --chain-id "$CHAIN_ID" --node http://localhost:27147 --yes
