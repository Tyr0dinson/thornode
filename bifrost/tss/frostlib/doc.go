// Package frostlib is where bifrost links the go-frost library
// (gitlab.com/thorchain/tss/thor-go-frost), the ZF FROST Taproot engine that
// signs for LTC vaults on stagenet.
//
// Only stagenet and mocknet builds link it. Every file that imports the gofrost
// package carries `//go:build stagenet || mocknet`; mainnet and chainnet builds
// compile the stubs in runtime_stub.go instead, so those binaries do not
// contain the library at all (`make frost-lib-check-mainnet` verifies this).
//
// The library is a static archive (libgofrost.a) built from the module source
// by scripts/frost-lib.sh and handed to cgo through CGO_LDFLAGS; see the
// frost-lib target in the Makefile.
package frostlib
