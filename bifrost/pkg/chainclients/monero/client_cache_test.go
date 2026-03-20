package monero

import (
	"strings"
	"testing"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestTxCacheVaultUsesEddsaVaultPubKey(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKeyEddsa: common.PubKey("thorpub1eddsa"),
	}

	key := txCacheVault(tx, common.XMRChain)
	if !strings.Contains(key, "thorpub1eddsa") {
		t.Fatalf("expected cache key to include eddsa vault pubkey, got %s", key)
	}
}

func TestTxVaultPubKeyPrefersEddsa(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      common.PubKey("thorpub1secp"),
		VaultPubKeyEddsa: common.PubKey("thorpub1eddsa"),
	}

	got := txVaultPubKey(tx)
	if !got.Equals(tx.VaultPubKeyEddsa) {
		t.Fatalf("expected eddsa vault pubkey, got %s", got.String())
	}
}

func TestTxVaultPubKeyRequiresEddsa(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Chain:       common.XMRChain,
		VaultPubKey: common.PubKey("thorpub1secp"),
	}

	got := txVaultPubKey(tx)
	if !got.IsEmpty() {
		t.Fatalf("expected empty vault pubkey when eddsa key is missing, got %s", got.String())
	}
}
