#!/usr/bin/env bash
set -euo pipefail

die() {
  echo "ERR: $*"
  exit 1
}

./scripts/check-build-env.sh

# Make sure no one is reintroducing a _current.go file.
CURRENT=$(git diff origin/develop --name-only | grep '_current\.go$' || true)
if [ -n "$CURRENT" ]; then
  cat <<EOF
Using the '_current.go' pattern has been deprecated. Failed for introducing:

$CURRENT
EOF
  exit 1
fi

# Check that no .pb.go files were added.
if git ls-files '*.go' | grep -q '.pb.go$'; then
  die "Do not add generated protobuf .pb.go files"
fi

git ls-files '*.go' | grep -v -e '^docs/' | xargs gofumpt -d
if [ -n "$(git ls-files '*.go' | grep -v -e '^docs/' | xargs gofumpt -l)" ]; then
  die "Go formatting errors"
fi
go mod verify

./scripts/lint-handlers.bash

./scripts/lint-managers.bash

./scripts/lint-erc20s.bash
