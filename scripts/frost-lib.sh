#!/usr/bin/env bash
# Builds the go-frost static library (libgofrost.a) that stagenet and mocknet
# bifrost builds link through cgo, from the exact module version in go.mod:
# `cargo build --release --locked` with the Rust toolchain the module pins in
# its rust-toolchain.toml. No prebuilt archive is ever used.
#
# Usage: scripts/frost-lib.sh <output archive> [cargo target dir]
# The Makefile's frost-lib target runs this and hands the archive to cgo
# through CGO_LDFLAGS (GOFROST_STATIC_LIB); build/docker/Dockerfile runs it in
# its own layer for stagenet and mocknet images.
set -euo pipefail

module=gitlab.com/thorchain/tss/thor-go-frost
out="${1:?usage: $0 <output archive> [cargo target dir]}"
target_dir="${2:-$(dirname "$out")/cargo-target}"

die() {
  echo "frost-lib: $*" >&2
  exit 1
}

command -v cargo >/dev/null ||
  die "cargo not found: install rustup (https://rustup.rs; scripts/install-rust.sh on Linux CI). The go-frost module pins the toolchain version."

# go list prints an empty directory for a module that is not in the module
# cache yet (a cold cache, as in CI); only an explicit download fills it.
mod_dir="$(go list -m -f '{{.Dir}}' "$module")" || die "cannot locate module $module"
if [ -z "$mod_dir" ]; then
  go mod download "$module" || die "cannot download module $module"
  mod_dir="$(go list -m -f '{{.Dir}}' "$module")" || die "cannot locate module $module"
  [ -n "$mod_dir" ] || die "module $module is still not in the module cache after go mod download"
fi
toolchain_file="$mod_dir/rust-toolchain.toml"
want="$(sed -n 's/^channel *= *"\([^"]*\)".*/\1/p' "$toolchain_file")"
[ -n "$want" ] || die "no channel pinned in $toolchain_file"

# With rustup, the toolchain file in the module directory selects the pinned
# version and installs it on first use. Without rustup the installed rustc must
# already be that version: the archive is consensus-adjacent crypto and is only
# built with the toolchain the module was tested with.
if command -v rustup >/dev/null; then
  (cd "$mod_dir" && rustup show >/dev/null)
fi
have="$(cd "$mod_dir" && rustc --version | awk '{print $2}')"
[ "$have" = "$want" ] || die "rustc $have found, go-frost pins $want (rust-toolchain.toml); install it with rustup"

mkdir -p "$(dirname "$out")"
go run "$module/cmd/gofrost-build" -out "$out" -target-dir "$target_dir" >/dev/null
echo "$out"
