package monero

import (
	"crypto/sha256"
	"testing"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"

	"gitlab.com/thorchain/thornode/v3/common"
)

const testSignerStoreSecret = "test-xmr-store-secret"

func testVaultPubKey(t *testing.T, seed string) common.PubKey {
	t.Helper()
	h := sha256.Sum256([]byte(seed))
	pk, err := common.NewPubKeyFromCrypto(cometed25519.PubKey(h[:]))
	if err != nil {
		t.Fatalf("test xmr pubkey: %v", err)
	}
	return pk
}

// testXMRAddress generates a valid checksummed XMR address deterministic to the seed.
// In mocknet the address starts with '4' (primary network byte).
func testXMRAddress(t *testing.T, seed string) string {
	t.Helper()
	pk := testVaultPubKey(t, seed)
	addr, err := pk.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("test xmr address: %v", err)
	}
	return addr.String()
}

func testOutboundXMRDestination() common.Address {
	if common.CurrentChainNetwork == common.MockNet {
		return common.Address(common.XMRChecksummedDummyAddress('8'))
	}
	return common.Address(common.XMRChecksummedDummyAddress('C'))
}
