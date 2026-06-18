########################################################################################
# Build
########################################################################################

ARG DOCKER_MIRROR=
FROM ${DOCKER_MIRROR}golang:1.26.4-bookworm AS build

ENV GOBIN=/go/bin
ENV GOPATH=/go
ENV GOOS=linux

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TAG=mainnet
RUN go install -tags $TAG ./tools/events

########################################################################################
# Deploy
########################################################################################

FROM debian:bookworm-slim

COPY --from=build /go/bin/events /usr/bin/
COPY --from=build /go/pkg/mod/github.com/!cosm!wasm/wasmvm/v2@v2.2.1/internal/api/libwasmvm.*.so /usr/lib

RUN apt-get update && \
  apt-get install -y --no-install-recommends curl ca-certificates && \
  rm -rf /var/lib/apt/lists/*

CMD ["events"]