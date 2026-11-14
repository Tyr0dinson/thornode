GOTOOLCHAIN := go1.26.10
export GOTOOLCHAIN
CA_BUNDLE ?= $(firstword $(wildcard /etc/ssl/certs/ca-certificates.crt /etc/ssl/cert.pem))

.PHONY: test vet vuln binary image benchmark
test:
	go test -race -count=1 -timeout=120s ./...

vet:
	go vet ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

binary:
	mkdir -p build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w' -o build/bifrost-rpc-router .
	test -n "$(CA_BUNDLE)"
	cp "$(CA_BUNDLE)" build/ca-certificates.crt

benchmark:
	go test -run='^$$' -bench=BenchmarkTwoLayerLoopback -benchtime=2s -count=3

# Deterministic OCI archive (binary + CA bundle only); needs no container builder.
# Writes proxy-image.tar next to build_oci.py and prints the manifest digest.
IMAGE_NAME ?= localhost/bifrost-rpc-router:dev
image: binary
	python3 -B build_oci.py build $(IMAGE_NAME)
