//go:build stagenet || mocknet
// +build stagenet mocknet

package frostlib

import "testing"

// The C ABI of the pinned go-frost tag. Bump it together with the module
// version in go.mod, never one without the other.
const pinnedABIVersion uint32 = 10

func TestLinkedLibraryMatchesWrapperABI(t *testing.T) {
	if !Linked {
		t.Fatal("stagenet and mocknet builds must link go-frost")
	}
	if ExpectedABIVersion != pinnedABIVersion {
		t.Fatalf("go-frost wrapper expects ABI %d, this build pins %d: re-pin them together", ExpectedABIVersion, pinnedABIVersion)
	}
	if got := ABIVersion(); got != ExpectedABIVersion {
		t.Fatalf("linked libgofrost reports ABI %d, wrapper expects %d", got, ExpectedABIVersion)
	}
	if err := CheckRuntimeCompatibility(); err != nil {
		t.Fatal(err)
	}
}
