# Match the base Debian image to https://github.com/anza-xyz/agave/blob/master/sdk/docker-solana/Dockerfile
FROM debian:bookworm AS builder

# hadolint ignore=DL3008,DL4006
RUN apt-get update && \
    apt-get install -y build-essential git curl openssl libssl-dev pkg-config

# set up Rust
RUN curl --proto '=https' --tlsv1.3 https://sh.rustup.rs -sSf | bash -s -- -y

ENV PATH="/root/.cargo/bin:${PATH}"

# build from source
ARG COMMITSH="94f09a2091c481cd5874d3af319069563453c5d5"
ARG TAG="v6.0.0+solana.2.2.4"
WORKDIR /build
RUN git clone --depth 1 -b "$TAG" https://github.com/rpcpool/yellowstone-grpc . && \
    git checkout $COMMITSH && \
    cargo build --release

# Use an official Solana release image as a parent image
FROM anzaxyz/agave:v2.2.8

# Set the working directory
WORKDIR /mocknet

# Copy the current directory contents into the container at /mocknet
COPY . /mocknet

# Copy the build geyser plugin
COPY --from=builder /build/target/release/libyellowstone_grpc_geyser.so /usr/share/libyellowstone_grpc_geyser.so

# Ensure the entrypoint script is executable
RUN chmod +x /mocknet/entrypoint.sh

# Expose the RPC and WebSocket ports
EXPOSE 8899
EXPOSE 8900
EXPOSE 10000