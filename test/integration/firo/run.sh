#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../../.."

: "${FIRO_SOURCE:?The test image must provide the FIRO source framework}"
: "${FIROD:?The test image must provide firod}"
: "${FIROCLI:?The test image must provide firo-cli}"
test -f "$FIRO_SOURCE/qa/rpc-tests/test_framework/test_framework.py"
test -x "$FIROD"
test -x "$FIROCLI"
"$FIROD" --version
"$FIROCLI" --version
go version
python3 -c 'import os, sys; sys.path.insert(0, os.environ["FIRO_SOURCE"] + "/qa/rpc-tests"); from test_framework.test_framework import EvoZnodeTestFramework'

test_dir=$(mktemp -d -t firo-integration.XXXXXX)
finish() {
  status=$?
  if [ "$status" -ne 0 ]; then
    echo "FIRO integration failed; last 100 lines of each test log:" >&2
    find "$test_dir" -type f -name '*.log' -exec tail -n 100 -v {} + >&2 || true
  fi
  rm -rf "$test_dir"
  exit "$status"
}
trap finish EXIT

echo "Running FIRO ChainLocks integration"
python3 test/integration/firo/chainlocks.py --srcdir="$(dirname "$FIROD")" --tmpdir="$test_dir/chainlocks"
echo "Running FIRO InstantSend integration"
python3 test/integration/firo/instantsend.py --srcdir="$(dirname "$FIROD")" --tmpdir="$test_dir/instantsend"
echo "Running FIRO mocknet recovery integration"
FIRO_TEST_DIR="$test_dir/mocknet" python3 test/integration/firo/mocknet.py
