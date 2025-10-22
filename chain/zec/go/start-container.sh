#!/bin/sh
set -eu

# Check if container is already running and stop it
if docker ps | grep -q zcash-regtest-shielded-e2e; then
  echo "Stopping existing zcash-regtest container..."
  docker stop zcash-regtest-shielded-e2e
fi

# Remove the container if it exists
if docker ps -a | grep -q zcash-regtest-shielded-e2e; then
  echo "Removing existing zcash-regtest container..."
  docker rm zcash-regtest-shielded-e2e
fi

# Start the zcashd container in background
echo "Starting zcashd container..."
docker compose up -d

# Wait for zcashd to be ready
echo "Waiting for zcashd to start in container..."
sleep 15

# Check if zcashd is responding
echo "Testing RPC connection..."
while true; do
  if docker exec zcash-regtest-shielded-e2e zcash-cli -regtest getblockcount 2>/dev/null; then
    echo "zcashd is ready!"
    break
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
  echo "Stopping zcash-regtest-shielded-e2e container..."
  docker stop zcash-regtest-shielded-e2e 2>/dev/null || true
  docker rm zcash-regtest-shielded-e2e 2>/dev/null || true
  echo "Container stopped and removed."
}

# Set trap to cleanup on script exit (success or failure)
trap cleanup EXIT

# Run the integration test
./test-shielded-flow.sh
