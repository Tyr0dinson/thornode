package thorclient

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestVaultFrostKeyAlgo(t *testing.T) {
	if got := vaultFrostKeyAlgo(types.GetTaprootTestPubKey()); got != common.SigningAlgoSecp256k1Taproot {
		t.Fatalf("secp256k1 FROST key tagged %s", got)
	}
	if got := vaultFrostKeyAlgo(types.GetRandomPubKey()); got != common.SigningAlgoSecp256k1Taproot {
		t.Fatalf("secp256k1 key tagged %s", got)
	}
	if got := vaultFrostKeyAlgo(types.GetRandomEd25519PubKey()); got != common.SigningAlgoEd25519 {
		t.Fatalf("ed25519 FROST key tagged %s", got)
	}
	if got := vaultFrostKeyAlgo(common.PubKey("garbage")); got != common.SigningAlgoEd25519 {
		t.Fatalf("undecodable key tagged %s", got)
	}
}
