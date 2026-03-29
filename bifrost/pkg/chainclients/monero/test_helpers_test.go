package monero

import (
	"crypto/sha256"
	"testing"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"

	"gitlab.com/thorchain/thornode/v3/common"
)

const testSignerStoreSecret = "test-xmr-store-secret" // #nosec G101 -- test fixture only

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
	return common.Address(common.XMRChecksummedDummyAddress('C'))
}
