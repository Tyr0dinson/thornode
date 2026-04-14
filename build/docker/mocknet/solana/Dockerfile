FROM rust:1.80-bookworm AS build

ARG AGAVE_TAG=v2.3.8

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN set -eux; \
  required_pkgs=( \
    'git=1:2.39.5-0+deb12u3' \
    'clang=1:14.0-55.7~deb12u1' \
    'llvm=1:14.0-55.7~deb12u1' \
    'libclang-dev=1:14.0-55.7~deb12u1' \
    'pkg-config=1.8.1-1' \
    'libssl-dev=3.0.18-1~deb12u2' \
    'protobuf-compiler=3.21.12-3' \
    'cmake=3.25.1-1' \
    'libudev-dev=252.39-1~deb12u1' \
  ); \
  validate_pkg() { \
    local spec="$1"; \
    apt-cache show "$spec" >/dev/null 2>&1; \
  }; \
  sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
  for attempt in 1 2 3 4 5 6 7 8 9 10; do \
    rm -rf /var/lib/apt/lists/*; \
    apt-get -o Acquire::Retries=5 update || true; \
    missing=0; \
    for pkg in "${required_pkgs[@]}"; do \
      validate_pkg "$pkg" || missing=1; \
    done; \
    [ "$missing" -eq 0 ] && break; \
    [ "$attempt" -lt 10 ] || { echo "apt metadata incomplete after retries" >&2; exit 1; }; \
    sleep 5; \
  done; \
  apt-get install -y --no-install-recommends \
    git=1:2.39.5-0+deb12u3 \
    clang=1:14.0-55.7~deb12u1 \
    llvm=1:14.0-55.7~deb12u1 \
    libclang-dev=1:14.0-55.7~deb12u1 \
    pkg-config=1.8.1-1 \
    libssl-dev=3.0.18-1~deb12u2 \
    protobuf-compiler=3.21.12-3 \
    cmake=3.25.1-1 \
    libudev-dev=252.39-1~deb12u1; \
  rm -rf /var/lib/apt/lists/*

WORKDIR /src
RUN git clone --depth=1 --branch="$AGAVE_TAG" https://github.com/anza-xyz/agave.git . && \
  cargo build --release \
  --bin solana-test-validator \
  --bin solana-genesis \
  --bin solana \
  --bin solana-keygen


FROM debian:bookworm-slim AS runtime

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN set -eux; \
  required_pkgs=( \
    'ca-certificates=20230311+deb12u1' \
    'libssl3=3.0.18-1~deb12u2' \
    'libudev1=252.39-1~deb12u1' \
  ); \
  validate_pkg() { \
    local spec="$1"; \
    apt-cache show "$spec" >/dev/null 2>&1; \
  }; \
  refresh_metadata() { \
    rm -rf /var/lib/apt/lists/*; \
    apt-get -o Acquire::Retries=5 update || true; \
  }; \
  if [ ! -s /etc/ssl/certs/ca-certificates.crt ]; then \
    for attempt in 1 2 3 4 5 6 7 8 9 10; do \
      refresh_metadata; \
      validate_pkg 'ca-certificates=20230311+deb12u1' && break; \
      [ "$attempt" -lt 10 ] || { echo "ca-certificates metadata incomplete after retries" >&2; exit 1; }; \
      sleep 5; \
    done; \
    apt-get install -y --no-install-recommends ca-certificates=20230311+deb12u1; \
    rm -rf /var/lib/apt/lists/*; \
  fi; \
  sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
  for attempt in 1 2 3 4 5 6 7 8 9 10; do \
    refresh_metadata; \
    missing=0; \
    for pkg in "${required_pkgs[@]}"; do \
      validate_pkg "$pkg" || missing=1; \
    done; \
    [ "$missing" -eq 0 ] && break; \
    [ "$attempt" -lt 10 ] || { echo "apt metadata incomplete after retries" >&2; exit 1; }; \
    sleep 5; \
  done; \
  apt-get install -y --no-install-recommends \
    ca-certificates=20230311+deb12u1 \
    libssl3=3.0.18-1~deb12u2 \
    libudev1=252.39-1~deb12u1; \
  rm -rf /var/lib/apt/lists/*

COPY --from=build /src/target/release/solana-test-validator /usr/local/bin/
COPY --from=build /src/target/release/solana-genesis /usr/local/bin/
COPY --from=build /src/target/release/solana /usr/local/bin/
COPY --from=build /src/target/release/solana-keygen /usr/local/bin/
