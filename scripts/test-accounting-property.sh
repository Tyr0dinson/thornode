#!/usr/bin/env bash
set -euo pipefail

case "${1:-fast}" in
fast) seeds=4 ;;
campaign) seeds=32 ;;
*)
  echo "usage: $0 [fast|campaign]" >&2
  exit 2
  ;;
esac

cd "$(dirname "$0")/.."
export GOTOOLCHAIN=go1.26.9
export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/thornode-accounting-gocache}"
export GOMAXPROCS=4
export THORNODE_ACCOUNTING_SEEDS="${THORNODE_ACCOUNTING_SEEDS:-$seeds}"

flock -w 600 /tmp/thornode-test-quality-build.lock \
  go test -p 2 ./x/thorchain -check.f AccountingPropertySuite -count=1
