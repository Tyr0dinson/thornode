#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if command -v xmr-reset-scanner >/dev/null 2>&1; then
  exec xmr-reset-scanner "$@"
fi

exec go run "$REPO_ROOT/tools/xmr-reset-scanner" "$@"
