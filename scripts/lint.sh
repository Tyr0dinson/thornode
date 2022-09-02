#!/usr/bin/env bash
set -euo pipefail

source ./scripts/utils.sh

./scripts/check-build-env.sh

# check docs version
VERSION=$(cat version)
if ! grep "^  version: ${VERSION}" openapi/openapi.yaml; then
  die "docs version (openapi/openapi.yaml) does not match version file ${VERSION}"
fi

# Check that the root `version` file was not downgraded.
CUR_VERSION=$(version "$VERSION")
PREV_VERSION=$(version "$(git show origin/develop:version)")
if [ "$CUR_VERSION" -lt "$PREV_VERSION" ]; then
  die "Cannot downgrade version from ${PREV_VERSION} to ${CUR_VERSION}"
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
