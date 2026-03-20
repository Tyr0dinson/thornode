package monero

import (
	"crypto/sha256"
	"testing"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"

	"gitlab.com/thorchain/thornode/v3/common"
)

const testSignerStoreSecret = "test-xmr-store-secret"

// testXMRAddress generates a valid checksummed XMR address deterministic to the seed.
// In mocknet the address starts with '4' (primary network byte).
func testXMRAddress(t *testing.T, seed string) string {
	t.Helper()
	h := sha256.Sum256([]byte(seed))
	pk, err := common.NewPubKeyFromCrypto(cometed25519.PubKey(h[:]))
	if err != nil {
		t.Fatalf("test xmr pubkey: %v", err)
	}
	addr, err := pk.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("test xmr address: %v", err)
	}
	return addr.String()
}
