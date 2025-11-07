#!/bin/sh
set -e
# Resolve library path quietly and run tests without shell xtrace noise
ROOT_LIB=$(realpath ../../../lib)

# Prefer sim variant if present; otherwise use ROOT_LIB (prod)
if [ -d "${ROOT_LIB}/sim" ]; then
  LIB_DIR="${ROOT_LIB}/sim"
else
  LIB_DIR="${ROOT_LIB}"
fi

# Use static library for both macOS and Linux
CGO_LDFLAGS="${LIB_DIR}/libzec.a -lm" CGO_ENABLED=1 go test -v -tags mocknet -count=1 ./zec/...
