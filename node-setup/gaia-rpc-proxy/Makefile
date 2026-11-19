GOTOOLCHAIN := go1.26.10
export GOTOOLCHAIN

.PHONY: test binary
test:
	go test -race -count=1 ./...
	go vet ./...
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

binary:
	mkdir -p build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w' -o build/gaia-rpc-proxy .
