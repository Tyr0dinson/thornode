# Build stage
FROM rust:1.77-slim AS builder

RUN apt-get update && apt-get install -y pkg-config libssl-dev && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY xmr-frost-signer/ .
RUN cargo build --release

# Runtime stage
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y ca-certificates curl && rm -rf /var/lib/apt/lists/*

COPY --from=builder /app/target/release/xmr-frost-signer /usr/local/bin/xmr-frost-signer

RUN useradd -m -s /bin/bash frost
USER frost

EXPOSE 8283

HEALTHCHECK --interval=5s --timeout=5s --retries=30 --start-period=10s \
    CMD curl -sf http://localhost:8283/health || exit 1

ENTRYPOINT ["xmr-frost-signer"]
