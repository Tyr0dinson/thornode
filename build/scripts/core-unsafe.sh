#!/bin/bash

set -o pipefail

wait_for_evm_rpc() {
  local chain="$1"
  local rpc="$2"
  local require_accounts="$3"

  echo "Waiting for $chain RPC: $rpc"
  python3 - "$chain" "$rpc" "$require_accounts" <<'PY'
import sys
import time

import requests

chain, rpc, require_accounts = sys.argv[1], sys.argv[2], sys.argv[3] == "1"
deadline = time.time() + 60
last_err = "rpc not ready"
session = requests.Session()

while time.time() < deadline:
    try:
        response = session.post(
            rpc,
            json={"jsonrpc": "2.0", "method": "net_version", "params": [], "id": 1},
            timeout=3,
        )
        response.raise_for_status()
        data = response.json()
        if "result" not in data:
            raise RuntimeError(f"missing net_version result: {data}")

        if require_accounts:
            response = session.post(
                rpc,
                json={"jsonrpc": "2.0", "method": "eth_accounts", "params": [], "id": 1},
                timeout=3,
            )
            response.raise_for_status()
            data = response.json()
            accounts = data.get("result") or []
            if not accounts:
                raise RuntimeError("eth_accounts is empty")

        print(f"{chain} RPC ready", flush=True)
        sys.exit(0)
    except Exception as err:
        last_err = err
        time.sleep(1)

print(f"{chain} RPC not ready after 60s: {last_err}", file=sys.stderr, flush=True)
sys.exit(1)
PY
}

deploy_evm_contracts() {
  # Deploy each EVM chain's contracts in parallel (each subshell waits for its
  # own RPC then deploys, writing only the router address to a per-chain file).
  # The genesis.json edits are applied serially afterwards because they all
  # read/modify the same file. set -e is NOT in effect, so each background job's
  # exit status is checked explicitly (a bare `wait` would mask failures).
  local pids=() chains=()
  for CHAIN in ETH AVAX BASE POL; do
    RPC_VAR="${CHAIN}_HOST"
    RPC_URL="$(eval echo "\$${RPC_VAR}")"
    REQUIRE_ACCOUNTS=0
    if [[ $CHAIN == "ETH" || $CHAIN == "BASE" || $CHAIN == "BSC" ]]; then
      REQUIRE_ACCOUNTS=1
    fi

    (
      wait_for_evm_rpc "$CHAIN" "$RPC_URL" "$REQUIRE_ACCOUNTS" || exit 1

      # deploy contract and get address from output
      echo "Deploying $CHAIN contracts"
      if ! PYTHONUNBUFFERED=1 python3 scripts/evm/evm-tool.py --chain "$CHAIN" --rpc "$RPC_URL" --action deploy >/tmp/evm-tool-$CHAIN.log 2>&1; then
        cat /tmp/evm-tool-$CHAIN.log
        exit 1
      fi
      cat /tmp/evm-tool-$CHAIN.log
      CONTRACT=$(grep </tmp/evm-tool-$CHAIN.log "Router Contract Address" | awk '{print $NF}')
      if [ -z "$CONTRACT" ]; then
        echo "Failed to determine $CHAIN router contract address"
        exit 1
      fi
      echo "$CONTRACT" >"/tmp/evm-router-$CHAIN.addr"
    ) &
    pids+=("$!")
    chains+=("$CHAIN")
  done

  # wait for every deploy; fail loudly if any did
  local rc=0
  for i in "${!pids[@]}"; do
    if ! wait "${pids[$i]}"; then
      echo "EVM contract deploy failed for ${chains[$i]}"
      rc=1
    fi
  done
  [ "$rc" -eq 0 ] || return 1

  # add contract addresses to genesis serially (shared genesis.json)
  for CHAIN in ETH AVAX BASE POL; do
    CONTRACT=$(cat "/tmp/evm-router-$CHAIN.addr" 2>/dev/null)
    if [ -z "$CONTRACT" ]; then
      echo "Failed to determine $CHAIN router contract address"
      return 1
    fi
    echo "$CHAIN Contract Address: $CONTRACT"
    jq --arg CHAIN "$CHAIN" --arg CONTRACT "$CONTRACT" \
      '.app_state.thorchain.chain_contracts += [{"chain": $CHAIN, "router": $CONTRACT}]' \
      ~/.thornode/config/genesis.json >/tmp/genesis-$CHAIN.json
    mv /tmp/genesis-$CHAIN.json ~/.thornode/config/genesis.json
  done
}

# A successful sync broadcast only means CheckTx accepted the transaction. Wait
# for successful execution before signing the next transaction with this account.
mocknet_tx() {
  local node="tcp://${PEER}:${PORT_RPC:-26657}"
  local response code txhash attempt poll committed
  for ((attempt = 1; attempt <= 10; attempt++)); do
    if ! response=$(printf "%s\n" "$SIGNER_PASSWD" | timeout 30s thornode tx thorchain "$@" \
      --node "$node" --from "$SIGNER_NAME" --keyring-backend=file --chain-id "$CHAIN_ID" \
      --yes --broadcast-mode sync --output json); then
      echo "Mocknet $1 broadcast failed" >&2
      return 1
    fi
    if ! code=$(jq -er '.code | numbers' <<<"$response"); then
      echo "Mocknet $1 returned an invalid broadcast response" >&2
      return 1
    fi

    if [ "$code" = "0" ]; then
      if ! txhash=$(jq -er '.txhash | strings | select(test("^[[:xdigit:]]{64}$"))' <<<"$response"); then
        echo "Mocknet $1 broadcast returned no valid transaction hash" >&2
        return 1
      fi
      committed=false
      for ((poll = 1; poll <= 30; poll++)); do
        if response=$(timeout 5s thornode query tx "$txhash" --node "$node" --output json 2>/dev/null) &&
          jq -e --arg hash "$txhash" '.txhash == $hash and ((.height | tonumber) > 0) and (.code | type == "number")' <<<"$response" >/dev/null 2>&1; then
          code=$(jq -r '.code' <<<"$response")
          committed=true
          break
        fi
        sleep 2
      done
      if [ "$committed" != true ]; then
        # The outcome is unknown. In particular, never submit another bond just
        # because the accepted transaction could not be found in the index yet.
        echo "Mocknet $1 transaction $txhash was not confirmed after 30 queries" >&2
        return 1
      fi
      if [ "$code" = "0" ]; then
        echo "Mocknet $1 transaction $txhash committed successfully"
        return 0
      fi
    fi

    # Only a known sequence rejection is safe to retry automatically. Each
    # invocation signs again using the account sequence from the same peer.
    if [ "$code" != "32" ] || ! jq -e '.codespace == "sdk"' <<<"$response" >/dev/null; then
      echo "Mocknet $1 transaction rejected: $(jq -r '.raw_log' <<<"$response") (code $code)" >&2
      return 1
    fi
    echo "Mocknet $1 account sequence mismatch (attempt $attempt/10)" >&2
    sleep 2
  done
  echo "Mocknet $1 failed after 10 sequence retries" >&2
  return 1
}

# Publish a complete state with a same-directory rename. A failed write must
# never allow registration to proceed without its restart guard.
write_mocknet_registration_state() {
  local state_file="$1"
  local state="$2"
  local temporary_file
  mkdir -p "$(dirname "$state_file")" || return 1
  temporary_file=$(mktemp "${state_file}.XXXXXX") || return 1
  if ! printf '%s\n' "$state" >"$temporary_file" || ! mv -f "$temporary_file" "$state_file"; then
    rm -f "$temporary_file"
    echo "Failed to persist mocknet registration state: $state" >&2
    return 1
  fi
}

init_mocknet() {
  NODE_ADDRESS=$(echo "$SIGNER_PASSWD" | thornode keys show "$SIGNER_NAME" -a --keyring-backend file)

  if [ "$PEER" = "none" ]; then
    echo "Missing PEER"
    exit 1
  fi

  # wait for peer
  until curl -s "$PEER:$PORT_RPC" 1>/dev/null 2>&1; do
    echo "Waiting for peer: $PEER:$PORT_RPC"
    sleep 3
  done

  # send bond
  mocknet_tx deposit 100000000000000 RUNE "bond:$NODE_ADDRESS" || return 1

  NODE_PUB_KEY=$(echo "$SIGNER_PASSWD" | thornode keys show thorchain --pubkey --keyring-backend=file | thornode pubkey)
  VALIDATOR=$(thornode tendermint show-validator | thornode pubkey --bech cons)

  # set node keys
  mocknet_tx set-node-keys "$NODE_PUB_KEY" "$NODE_PUB_KEY_ED25519" "$VALIDATOR" || return 1

  # add IP address
  NODE_IP_ADDRESS=${EXTERNAL_IP:=$(curl -s http://whatismyip.akamai.com)}
  mocknet_tx set-ip-address "$NODE_IP_ADDRESS" || return 1

  # set node version
  mocknet_tx set-version || return 1
}

# set external ip to localhost in mocknet
if [ "$NET" = "mocknet" ]; then
  EXTERNAL_IP="$(hostname -i)"
  export EXTERNAL_IP
fi
