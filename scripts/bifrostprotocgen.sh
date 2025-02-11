#!/usr/bin/env bash

set -euo pipefail

# Delete any existing protobuf generated files.
find . -name "*.pb.go" -delete

which protoc &>/dev/null || (apt-get update && apt-get install -y --no-install-recommends protobuf-compiler)

# Generate proto files for go-tss.
echo "Generating proto files for go-tss"
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2
pushd bifrost/tss/go-tss
protoc --go_out=module=gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss:. ./messages/*.proto
popd

# Generate proto files for Solana geyser
echo "Generating proto files for Solana geyers"
apt-get update && apt-get install -y --no-install-recommends libprotobuf-dev
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
pushd bifrost/pkg/chainclients/solana/geyser
protoc \
    --experimental_allow_proto3_optional \
    --go_out=module=gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/geyser:. \
	--go-grpc_out=./proto \
	--go-grpc_opt=paths=source_relative \
    ./*.proto
popd