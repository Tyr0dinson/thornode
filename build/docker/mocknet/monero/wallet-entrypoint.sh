#!/bin/sh

set -eu

WALLET_DIR="${XMR_WALLET_DIR:-/var/lib/monero-wallet}"
WALLET_NAME="${XMR_WALLET_NAME:-mocknet}"
WALLET_PASSWORD="${XMR_WALLET_PASSWORD:-password}"
WALLET_RPC_BIND_IP="${XMR_WALLET_RPC_BIND_IP:-0.0.0.0}"
WALLET_RPC_BIND_PORT="${XMR_WALLET_RPC_BIND_PORT:-18082}"
DAEMON_HOST="${XMR_DAEMON_HOST:-monero-daemon}"
DAEMON_PORT="${XMR_DAEMON_PORT:-18081}"
RPC_URL="http://127.0.0.1:${WALLET_RPC_BIND_PORT}/json_rpc"
DAEMON_RPC_URL="http://${DAEMON_HOST}:${DAEMON_PORT}/json_rpc"

mkdir -p "${WALLET_DIR}"

wait_for_daemon() {
  retries=0
  until curl -sf \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":"0","method":"get_block_count"}' \
    "${DAEMON_RPC_URL}" >/dev/null; do
    retries=$((retries + 1))
    if [ "${retries}" -ge 120 ]; then
      echo "monero daemon did not become ready in time" >&2
      return 1
    fi
    sleep 1
  done
}

wait_for_wallet_rpc() {
  retries=0
  until curl -sf \
    -H "Content-Type: application/json" \
    -d '{"jsonrpc":"2.0","id":"0","method":"get_version"}' \
    "${RPC_URL}" >/dev/null; do
    retries=$((retries + 1))
    if [ "${retries}" -ge 120 ]; then
      echo "monero wallet rpc did not become ready in time" >&2
      return 1
    fi
    sleep 1
  done
}

wallet_request() {
  method="$1"
  params="$2"
  payload=$(printf '{"jsonrpc":"2.0","id":"0","method":"%s","params":%s}' "${method}" "${params}")
  curl -sf -H "Content-Type: application/json" -d "${payload}" "${RPC_URL}"
}

wait_for_daemon

monero-wallet-rpc \
  --non-interactive \
  --confirm-external-bind \
  --rpc-bind-ip "${WALLET_RPC_BIND_IP}" \
  --rpc-bind-port "${WALLET_RPC_BIND_PORT}" \
  --disable-rpc-login \
  --trusted-daemon \
  --daemon-address "${DAEMON_HOST}:${DAEMON_PORT}" \
  --wallet-dir "${WALLET_DIR}" \
  --log-file "${WALLET_DIR}/monero-wallet-rpc.log" &
wallet_pid=$!

shutdown() {
  if kill -0 "${wallet_pid}" 2>/dev/null; then
    kill "${wallet_pid}" 2>/dev/null || true
    wait "${wallet_pid}" 2>/dev/null || true
  fi
}
trap shutdown INT TERM

wait_for_wallet_rpc

wallet_file="${WALLET_DIR}/${WALLET_NAME}"
wallet_keys="${wallet_file}.keys"
if [ -f "${wallet_file}" ] && [ -f "${wallet_keys}" ]; then
  wallet_request "open_wallet" "$(printf '{"filename":"%s","password":"%s"}' "${WALLET_NAME}" "${WALLET_PASSWORD}")" >/dev/null
else
  wallet_request "create_wallet" "$(printf '{"filename":"%s","password":"%s","language":"English"}' "${WALLET_NAME}" "${WALLET_PASSWORD}")" >/dev/null
fi

address_resp="$(wallet_request "get_address" '{}')"
wallet_address="$(printf '%s' "${address_resp}" | jq -r '.result.address // empty')"
if [ -n "${wallet_address}" ]; then
  curl -sf \
    -H "Content-Type: application/json" \
    -d "$(printf '{"jsonrpc":"2.0","id":"0","method":"start_mining","params":{"miner_address":"%s","threads_count":1,"do_background_mining":false,"ignore_battery":true}}' "${wallet_address}")" \
    "${DAEMON_RPC_URL}" >/dev/null || true
fi

wait "${wallet_pid}"
