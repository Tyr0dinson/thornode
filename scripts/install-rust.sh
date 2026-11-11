#!/bin/sh
# Installs rustup, pinned by version and checksum, with no default toolchain:
# the Rust toolchain that builds the go-frost static library is the one the
# module pins in its rust-toolchain.toml, which rustup installs on first use
# (scripts/frost-lib.sh). Used by build/docker/Dockerfile for stagenet and
# mocknet images and by CI jobs that link bifrost natively. Linux only; on a
# workstation install rustup yourself (https://rustup.rs). Idempotent: an
# existing $CARGO_HOME/bin/rustup is kept. Needs curl and sha256sum.
set -eu

rustup_version=1.28.2
CARGO_HOME="${CARGO_HOME:-$HOME/.cargo}"
RUSTUP_HOME="${RUSTUP_HOME:-$HOME/.rustup}"
export CARGO_HOME RUSTUP_HOME

if [ -x "$CARGO_HOME/bin/rustup" ]; then
  echo "rustup already installed: $("$CARGO_HOME/bin/rustup" --version 2>/dev/null)"
  exit 0
fi

# rustup-init checksums as published at
# https://static.rust-lang.org/rustup/archive/<version>/<target>/rustup-init.sha256
case "$(uname -s)-$(uname -m)" in
Linux-x86_64)
  target=x86_64-unknown-linux-gnu
  sha256=20a06e644b0d9bd2fbdbfd52d42540bdde820ea7df86e92e533c073da0cdd43c
  ;;
Linux-aarch64)
  target=aarch64-unknown-linux-gnu
  sha256=e3853c5a252fca15252d07cb23a1bdd9377a8c6f3efa01531109281ae47f841c
  ;;
*)
  echo "install-rust.sh: unsupported platform $(uname -s) $(uname -m); install rustup from https://rustup.rs" >&2
  exit 1
  ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location \
  "https://static.rust-lang.org/rustup/archive/$rustup_version/$target/rustup-init" \
  --output "$tmp/rustup-init"
echo "$sha256  $tmp/rustup-init" | sha256sum --check --strict - >/dev/null
chmod 0755 "$tmp/rustup-init"
"$tmp/rustup-init" -y --no-modify-path --profile minimal --default-toolchain none
echo "installed $("$CARGO_HOME/bin/rustup" --version 2>/dev/null)"
