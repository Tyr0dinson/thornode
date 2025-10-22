#!/bin/sh
set -e

GO_CONTAINER="zcash-regtest-shielded-e2e"
TS_CONTAINER="zcash-regtest-e2e"

# Check if Node.js e2e container is running and stop it to avoid conflicts
if docker ps | grep -q "$TS_CONTAINER"; then
  echo "Stopping existing Node.js $TS_CONTAINER container..."
  docker stop "$TS_CONTAINER"
  docker rm "$TS_CONTAINER" 2>/dev/null || true
fi

# Check if container is already running and stop it
if docker ps | grep -q "$GO_CONTAINER"; then
  echo "Stopping existing zcash-regtest container..."
  docker stop "$GO_CONTAINER"
fi

# Remove the container if it exists
if docker ps -a | grep -q "$GO_CONTAINER"; then
  echo "Removing existing zcash-regtest container..."
  docker rm "$GO_CONTAINER"
fi

# Start the zcashd container in background
echo "Starting zcashd container..."
docker compose up -d

# Wait for zcashd to be ready
echo "Waiting for zcashd to start in container..."
sleep 15

# Check if zcashd is responding
echo "Testing RPC connection..."
ATTEMPTS=0
MAX_ATTEMPTS=60 # ~5 minutes
while true; do
  if ! docker ps | grep -q "$GO_CONTAINER"; then
    echo "ERROR: Container $GO_CONTAINER is not running. Recent logs:" >&2
    docker logs "$GO_CONTAINER" --tail=200 2>/dev/null || true
    exit 1
  fi
  if docker exec "$GO_CONTAINER" zcash-cli -regtest -rpcuser=mayachain -rpcpassword=password getblockcount >/dev/null 2>&1; then
    echo "zcashd is ready!"
    break
  fi
  ATTEMPTS=$((ATTEMPTS + 1))
  if [ "$ATTEMPTS" -ge "$MAX_ATTEMPTS" ]; then
    echo "ERROR: Timed out waiting for zcashd RPC. Recent logs:" >&2
    docker logs "$GO_CONTAINER" --tail=200 2>/dev/null || true
    exit 1
  fi
  echo "Waiting for zcashd RPC to be ready..."
  sleep 5
done

echo "zcashd regtest container is running and ready!"
echo "RPC available at localhost:18232"
echo ""
echo "Now running integration test..."
echo ""

# Function to cleanup container
cleanup() {
  echo ""
  echo "Stopping $GO_CONTAINER container..."
  docker stop "$GO_CONTAINER" 2>/dev/null || true
  docker rm "$GO_CONTAINER" 2>/dev/null || true
  echo "Container stopped and removed."
}

# Set trap to cleanup on script exit (success or failure)
trap cleanup EXIT

# Run Rust Sapling & Orchard tests:
echo "Starting full Sapling flow test (T->Z->T)..."
ZCASHD_INTEGRATION=1 cargo test --features "shielded e2e" test_full_sapling_integration_with_zcashd -- --ignored --nocapture
echo "Starting full Orchard flow test (T->Z->T)..."
ZCASHD_INTEGRATION=1 cargo test --features "shielded e2e" test_full_orchard_integration_with_zcashd -- --ignored --nocapture
echo "Sapling & Orchard flow tests finished."

echo ""
echo "Now running Go FFI demonstration..."
echo "Building and running Go FFI test..."
ROOT_LIB=$(realpath ../../../lib)
# Use the freshly built E2E lib from target/release alongside RET in root/lib
E2E_LIBDIR=$(realpath ../target/release)
LD_LIBRARY_PATH="${E2E_LIBDIR}:${ROOT_LIB}" \
  CGO_LDFLAGS="-L${E2E_LIBDIR} -L${ROOT_LIB} -Wl,-rpath,${E2E_LIBDIR}:${ROOT_LIB} -lradix_engine_toolkit_uniffi -lzec" \
  go run -v -tags mocknet ./e2e_go_example.go
echo "Go FFI test finished."
