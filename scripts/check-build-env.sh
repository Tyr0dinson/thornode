#!/bin/bash
# Check that the build environment is set up properly for this repo.
set -euo pipefail

source ./scripts/utils.sh

# Check Go version.
GO_VER=$(go version | grep -Eo 'go[0-9.]+' | sed -e s/go//)
MIN_VER="1.17.0"

# shellcheck disable=SC2046
if [ $(version "$GO_VER") -lt $(version "$MIN_VER") ]; then
  cat <<EOF
Error: Detected Go version $GO_VER - this repository requires Go $MIN_VER as a minimum.
Please update Go and try again.
EOF
  exit 1
fi
