########################################################################################
# Build
########################################################################################

FROM golang:1.23.8-bookworm AS build

ENV GOBIN=/go/bin
ENV GOPATH=/go
ENV GOOS=linux

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# Copy ZEC static library for build stage
COPY ./lib/libzec.a /app/lib/

COPY . .

ARG TAG=mainnet
ARG COMMIT=unknown

RUN make install

########################################################################################
# Deploy
########################################################################################

FROM debian:bookworm-20250113-slim

RUN apt-get update && \
  apt-get install -y --no-install-recommends \
  ca-certificates \
  curl \
  jq \
  libffi-dev \
  libssl-dev \
  openssl \
  procps \
  python3 \
  python3-pip && \
  rm -rf /var/lib/apt/lists/* && \
  pip3 install --no-cache-dir --break-system-packages requests==2.32.3 web3==7.5.0 retry==0.9.2

# Copy the compiled binaries over.
COPY --from=build /go/bin/thornode /go/bin/bifrost /go/bin/recover-keyshare-backup /usr/bin/
COPY --from=build /go/pkg/mod/github.com/!cosm!wasm/wasmvm/v2@v2.1.2/internal/api/libwasmvm.*.so /usr/lib
# Note: libzec is statically linked, no need to copy to runtime image

COPY build/scripts /scripts

# default to mainnet
ARG TAG=mainnet
ENV NET=$TAG

# default to fullnode
CMD ["/scripts/fullnode.sh"]
