# syntax=docker/dockerfile:1.4

# Build stage
FROM rust:1.85-slim AS builder

# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends pkg-config libssl-dev && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY bifrost/pkg/chainclients/monero/xmr-frost-signer/ .
RUN cargo build --release

# Runtime stage
FROM ubuntu:24.04

# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/target/release/xmr-frost-signer /usr/local/bin/xmr-frost-signer
COPY build/docker/xmr-frost-signer/entrypoint.sh /usr/local/bin/xmr-frost-entrypoint

RUN useradd -m -s /bin/bash frost \
  && chmod 755 /usr/local/bin/xmr-frost-entrypoint

EXPOSE 8283 8284

HEALTHCHECK --interval=5s --timeout=5s --retries=30 --start-period=10s \
    CMD curl -sf http://localhost:8283/health || exit 1

ENTRYPOINT ["xmr-frost-entrypoint"]
