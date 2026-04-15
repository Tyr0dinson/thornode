#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COMPOSE_FILE_PATH="$SCRIPT_DIR/docker-compose.xmr-chainnet.yml"
COMPOSE_ENV_FILE="$REPO_ROOT/.env"
MODE="${1:-recreate}"

compose() {
  docker compose --env-file "$COMPOSE_ENV_FILE" -f "$COMPOSE_FILE_PATH" "$@"
}

joiner_names() {
  printf '%s\n' cat fox goat whale wolf eagle
}

compose_bifrost_action() {
  case "$MODE" in
    recreate)
      compose up -d --force-recreate "$@"
      ;;
    restart)
      compose restart "$@"
      ;;
    *)
      echo "ERROR: unknown mode '$MODE' (expected 'recreate' or 'restart')" >&2
      exit 1
      ;;
  esac
}

echo "Restarting bifrosts in bootstrap order (mode=$MODE)"
echo "  Clearing stale address_book.seed from all thornode volumes..."
for name in dog $(joiner_names); do
  if [ "$name" = "dog" ]; then
    docker exec docker-thornode-1 rm -f /root/.thornode/address_book.seed 2>/dev/null || true
  else
    docker exec "docker-thornode-${name}-1" rm -f /root/.thornode/address_book.seed 2>/dev/null || true
  fi
done

echo "  Recreating dog bifrost..."
compose_bifrost_action bifrost

echo -n "  Waiting for dog /p2pid"
dog_ready=false
for i in $(seq 1 30); do
  if docker exec docker-bifrost-1 curl -sf http://127.0.0.1:6040/p2pid >/dev/null 2>&1; then
    dog_ready=true
    echo " OK (${i}x2s)"
    break
  fi
  echo -n "."
  sleep 2
done
if [ "$dog_ready" != true ]; then
  echo " TIMEOUT"
  exit 1
fi

other_bifrosts=()
for name in $(joiner_names); do
  other_bifrosts+=("bifrost-${name}")
done

echo "  Recreating joiner bifrosts (${other_bifrosts[*]})..."
compose_bifrost_action "${other_bifrosts[@]}"

echo "  Waiting for all bifrosts /p2pid..."
BIFROSTS=(docker-bifrost-1)
for name in $(joiner_names); do
  BIFROSTS+=("docker-bifrost-${name}-1")
done
for attempt in $(seq 1 60); do
  all_ok=true
  status_line=""
  for bf in "${BIFROSTS[@]}"; do
    pid=$(docker exec "$bf" curl -sf http://127.0.0.1:6040/p2pid 2>/dev/null || echo "FAIL")
    if [ "$pid" = "FAIL" ]; then
      all_ok=false
      status_line="$status_line ${bf##*-bifrost-}=FAIL"
    else
      status_line="$status_line ${bf##*-bifrost-}=OK"
    fi
  done
  if $all_ok; then
    echo "  All ${#BIFROSTS[@]} bifrosts p2p OK (${attempt}x5s)"
    exit 0
  fi
  echo "  [$attempt/60]$status_line"
  sleep 5
done

echo "TIMEOUT: not all bifrosts have p2p after 5 min."
exit 1
