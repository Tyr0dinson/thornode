#
# THORChain
#

#
# Build
#
FROM golang:1.22.2 AS build

ENV GOBIN=/go/bin
ENV GOPATH=/go
ENV CGO_ENABLED=0
ENV GOOS=linux

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TAG=mainnet
ARG COMMIT=unknown

RUN make protob install

#
# Main
#
FROM alpine:3

# Add utilities used by scripts around thornode
# trunk-ignore(hadolint/DL3018)
RUN apk --no-cache add bash curl jq gcc musl-dev openssl-dev python3-dev py3-requests py3-retry py3-pip \
    && pip install --no-cache-dir --break-system-packages web3==7.2.0

# Copy the compiled binaries over.
COPY --from=build /go/bin/thornode /go/bin/bifrost /go/bin/recover-keyshare-backup /usr/bin/

COPY build/scripts /scripts

# default to mainnet
ARG TAG=mainnet
ENV NET=$TAG

# default to fullnode
CMD ["/scripts/fullnode.sh"]
