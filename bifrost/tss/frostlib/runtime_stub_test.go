//go:build !stagenet && !mocknet
// +build !stagenet,!mocknet

package frostlib

import "testing"

func TestLibraryNotLinked(t *testing.T) {
	if Linked {
		t.Fatal("mainnet and chainnet builds must not link go-frost")
	}
	if ABIVersion() != 0 || ExpectedABIVersion != 0 {
		t.Fatalf("unlinked build reports ABI %d (expected %d)", ABIVersion(), ExpectedABIVersion)
	}
	if err := CheckRuntimeCompatibility(); err != nil {
		t.Fatalf("unlinked build has nothing to check, got %v", err)
	}
}
