package monero

import (
	"fmt"
	"sync/atomic"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

var testVaultAddressCounter atomic.Uint64

func testVaultPubKeyAndAddress(t *testing.T) (common.PubKey, common.Address) {
	t.Helper()

	seed := fmt.Sprintf("vault-address:%s:%d", t.Name(), testVaultAddressCounter.Add(1))
	pubKey := testVaultPubKey(t, seed)
	address, err := pubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive monero address: %v", err)
	}
	return pubKey, address
}

func TestExpectedXMRVaultAddressFromKeygenRequest(t *testing.T) {
	t.Parallel()

	pubKey, expected := testVaultPubKeyAndAddress(t)
	got, err := expectedXMRVaultAddressFromKeygenRequest("", pubKey.String())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !got.Equals(expected) {
		t.Fatalf("expected %s, got %s", expected, got)
	}
}

func TestExpectedXMRVaultAddressFromKeygenRequestRejectsMissingEddsa(t *testing.T) {
	t.Parallel()

	pubKey, _ := testVaultPubKeyAndAddress(t)
	_, err := expectedXMRVaultAddressFromKeygenRequest(pubKey.String(), "")
	if err == nil {
		t.Fatal("expected missing eddsa pubkey error")
	}
}
