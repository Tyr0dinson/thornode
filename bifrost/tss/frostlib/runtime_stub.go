//go:build !stagenet && !mocknet
// +build !stagenet,!mocknet

package frostlib

// Linked is false: mainnet and chainnet builds do not contain the go-frost
// library.
const Linked = false

// ExpectedABIVersion is zero when the library is not linked.
const ExpectedABIVersion uint32 = 0

// ABIVersion reports zero when the library is not linked.
func ABIVersion() uint32 {
	return 0
}

// CheckRuntimeCompatibility has nothing to check when the library is not
// linked.
func CheckRuntimeCompatibility() error {
	return nil
}
