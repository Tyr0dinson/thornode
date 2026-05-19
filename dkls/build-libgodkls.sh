#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1091
source "$SCRIPT_DIR/SOURCE"

WORK_DIR="${DKLS_BUILD_WORK_DIR:-$SCRIPT_DIR/build}"
SRC_DIR="$WORK_DIR/silent-shard-dkls23-ll"

mkdir -p "$WORK_DIR"

if [ ! -d "$SRC_DIR/.git" ]; then
  git clone "$DKLS_UPSTREAM_REPOSITORY" "$SRC_DIR"
fi

git -C "$SRC_DIR" fetch --tags --force origin "$DKLS_UPSTREAM_REF"
git -C "$SRC_DIR" checkout --detach "$DKLS_UPSTREAM_REF"

echo "Building DKLS upstream source:"
echo "  repository: $DKLS_UPSTREAM_REPOSITORY"
echo "  ref:        $DKLS_UPSTREAM_REF"
echo "  package:    $DKLS_UPSTREAM_PACKAGE"

cargo build --manifest-path "$SRC_DIR/Cargo.toml" --release -p "$DKLS_UPSTREAM_PACKAGE"

cat >&2 <<'MSG'

Built the upstream DKLS Rust library successfully.

This upstream repository does not currently emit the libgodkls C ABI consumed by
dkls/go-wrappers/go-dkls/include/go-dkls.h. A thin Rust FFI crate still needs to
be added before this script can replace the checked-in libgodkls binaries.
MSG
