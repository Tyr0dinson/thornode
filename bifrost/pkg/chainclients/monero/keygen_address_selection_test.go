package monero

import (
	"testing"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"

	"gitlab.com/thorchain/thornode/v3/common"
)

func testVaultPubKeyAndAddress(t *testing.T) (common.PubKey, common.Address) {
	t.Helper()

	pubKey, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	if err != nil {
		t.Fatalf("new pubkey: %v", err)
	}
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
