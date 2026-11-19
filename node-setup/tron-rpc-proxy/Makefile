GOTOOLCHAIN := go1.26.10
export GOTOOLCHAIN
IMAGE ?= tron-rpc-proxy:dev
CA_BUNDLE ?= /etc/ssl/certs/ca-certificates.crt

.PHONY: test binary image
test:
	go test -race -count=1 ./...
	go vet ./...
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

binary:
	mkdir -p build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w' -o build/tron-rpc-proxy .

image: binary
	cp "$(CA_BUNDLE)" build/ca-certificates.crt
	docker build --platform linux/amd64 -t "$(IMAGE)" .
