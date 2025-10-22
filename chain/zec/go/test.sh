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

if [ "$(uname)" = "Darwin" ]; then
  DYLD_LIBRARY_PATH="${LIB_DIR}:${ROOT_LIB}" \
    CGO_LDFLAGS="-L${LIB_DIR} -L${ROOT_LIB} -Wl,-rpath,${LIB_DIR} -Wl,-rpath,${ROOT_LIB} -lradix_engine_toolkit_uniffi -lzec" \
    go test -v -tags mocknet -count=1 ./zec/...
else
  LD_LIBRARY_PATH="${LIB_DIR}:${ROOT_LIB}" \
    CGO_LDFLAGS="-L${LIB_DIR} -L${ROOT_LIB} -Wl,-rpath,${LIB_DIR}:${ROOT_LIB} -lradix_engine_toolkit_uniffi -lzec" \
    go test -v -tags mocknet -count=1 ./zec/...
fi
