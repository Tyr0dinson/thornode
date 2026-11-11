//go:build stagenet || mocknet
// +build stagenet mocknet

package frostlib

import "gitlab.com/thorchain/tss/thor-go-frost/gofrost"

// Linked is true in builds that link the go-frost library.
const Linked = true

// ExpectedABIVersion is the C ABI version the Go wrapper of the pinned go-frost
// module was built for. The linked archive must report exactly this version.
const ExpectedABIVersion = gofrost.ExpectedABIVersion

// ABIVersion reports the C ABI version of the linked libgofrost archive.
func ABIVersion() uint32 {
	return gofrost.ABIVersion()
}

// CheckRuntimeCompatibility fails when the linked archive and the Go wrapper
// disagree on the C ABI, which means the archive was not built from the pinned
// module source. Bifrost refuses to start in that case.
func CheckRuntimeCompatibility() error {
	return gofrost.CheckRuntimeCompatibility()
}
