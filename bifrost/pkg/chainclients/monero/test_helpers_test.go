package monero

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"

	"gitlab.com/thorchain/thornode/v3/common"
)

func testVaultPubKey(t *testing.T, seed string) common.PubKey {
	t.Helper()
	h := sha256.Sum256([]byte(seed))
	pk, err := common.NewPubKeyFromCrypto(cometed25519.PubKey(h[:]))
	if err != nil {
		t.Fatalf("test xmr pubkey: %v", err)
	}
	return pk
}

func testOutboundXMRDestination() common.Address {
	if common.CurrentChainNetwork == common.MockNet {
		return common.Address(common.XMRChecksummedDummyAddress('8'))
	}
	return common.Address(common.XMRChecksummedDummyAddress('4'))
}

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

// expectedXMRVaultAddress is the pure helper behind expectedXMRVaultAddressFromKeygenRequest.
func expectedXMRVaultAddress(vaultPubKeyFrost common.PubKey) (common.Address, error) {
	if vaultPubKeyFrost.IsEmpty() {
		return common.NoAddress, fmt.Errorf("xmr keygen requires a non-empty frost vault pubkey")
	}
	expected, err := vaultPubKeyFrost.GetAddress(common.XMRChain)
	if err != nil {
		return common.NoAddress, fmt.Errorf("fail to derive monero address from vault pubkey %s: %w", vaultPubKeyFrost.String(), err)
	}
	if expected.IsEmpty() {
		return common.NoAddress, fmt.Errorf("monero vault pubkey %s derived an empty address", vaultPubKeyFrost.String())
	}
	return expected, nil
}

// expectedXMRVaultAddressFromKeygenRequest is exercised by client_keygen_test.go and
// scanner_test.go to verify that the keygen handshake computes the same address the
// chain expects from a vault pubkey.
func expectedXMRVaultAddressFromKeygenRequest(vaultPubKeyFrost string) (common.Address, error) {
	value := strings.TrimSpace(vaultPubKeyFrost)
	if value == "" {
		return expectedXMRVaultAddress(common.EmptyPubKey)
	}
	pubKey, err := common.NewPubKey(value)
	if err != nil {
		return common.NoAddress, fmt.Errorf("invalid vault pubkey %q: %w", value, err)
	}
	return expectedXMRVaultAddress(pubKey)
}
