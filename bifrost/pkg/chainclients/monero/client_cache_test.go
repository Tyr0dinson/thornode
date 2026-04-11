package monero

import (
	"strings"
	"testing"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestTxCacheVaultUsesFrostVaultPubKey(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKeyFrost: common.PubKey("thorpub1frost"),
	}

	key := txCacheVault(tx, common.XMRChain)
	if !strings.Contains(key, "thorpub1frost") {
		t.Fatalf("expected cache key to include frost vault pubkey, got %s", key)
	}
}

func TestTxVaultPubKeyUsesFrost(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Chain:            common.XMRChain,
		VaultPubKey:      common.PubKey("thorpub1secp"),
		VaultPubKeyEddsa: common.PubKey("thorpub1eddsa"),
		VaultPubKeyFrost: common.PubKey("thorpub1frost"),
	}

	got := txVaultPubKey(tx)
	if !got.Equals(tx.VaultPubKeyFrost) {
		t.Fatalf("expected frost vault pubkey, got %s", got.String())
	}
}

func TestTxVaultPubKeyRequiresFrost(t *testing.T) {
	t.Parallel()

	tx := stypes.TxOutItem{
		Chain:       common.XMRChain,
		VaultPubKey: common.PubKey("thorpub1secp"),
	}

	got := txVaultPubKey(tx)
	if !got.IsEmpty() {
		t.Fatalf("expected empty vault pubkey when frost key is missing, got %s", got.String())
	}
}
