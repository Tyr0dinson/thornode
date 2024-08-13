#!/bin/sh
set -e

echo "Waiting for THORChain API..."

until curl -s "$1/thorchain/ping" >/dev/null; do
  sleep 1
done

echo "THORChain API ready"
