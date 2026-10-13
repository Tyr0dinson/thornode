package types

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestVaultTaprootLTC(t *testing.T) {
	supported := common.LTCChain.SupportsTaprootVaults()
	vault := NewVault(1, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), []string{common.LTCChain.String(), common.BTCChain.String()}, nil)
	vault.PubKeyFrost = GetTaprootTestPubKey()

	taprootKey, ok := vault.TaprootPubKey(common.LTCChain)
	if ok != supported {
		t.Fatalf("TaprootPubKey ok = %v on %v, want %v", ok, common.CurrentChainNetwork, supported)
	}
	key, err := vault.AlgoPubKey(common.LTCChain)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := vault.GetAddress(common.LTCChain)
	if err != nil {
		t.Fatal(err)
	}
	if supported {
		if !taprootKey.Equals(vault.PubKeyFrost) || !key.Equals(vault.PubKeyFrost) {
			t.Fatalf("LTC key = %s, want the FROST key %s", key, vault.PubKeyFrost)
		}
		if algo := vault.ChainSigningAlgo(common.LTCChain); algo != common.SigningAlgoSecp256k1Taproot {
			t.Fatalf("LTC signing algo = %s", algo)
		}
		want, wantErr := vault.PubKeyFrost.GetAddressForAlgo(common.LTCChain, common.SigningAlgoSecp256k1Taproot)
		if wantErr != nil || !addr.Equals(want) {
			t.Fatalf("LTC address = %s, want taproot %s (%v)", addr, want, wantErr)
		}
		legacy, _ := vault.PubKey.GetAddress(common.LTCChain)
		if addr.Equals(legacy) {
			t.Fatal("LTC address must not be the ECDSA key's address")
		}
	} else {
		if !key.Equals(vault.PubKey) {
			t.Fatalf("LTC key = %s, want the ECDSA key %s on %v", key, vault.PubKey, common.CurrentChainNetwork)
		}
		if algo := vault.ChainSigningAlgo(common.LTCChain); algo != common.SigningAlgoSecp256k1 {
			t.Fatalf("LTC signing algo = %s", algo)
		}
		want, _ := vault.PubKey.GetAddress(common.LTCChain)
		if !addr.Equals(want) {
			t.Fatalf("LTC address = %s, want %s", addr, want)
		}
	}

	// BTC is untouched by the FROST key.
	btcKey, err := vault.AlgoPubKey(common.BTCChain)
	if err != nil || !btcKey.Equals(vault.PubKey) {
		t.Fatalf("BTC key = %s (%v)", btcKey, err)
	}
	if algo := vault.ChainSigningAlgo(common.BTCChain); algo != common.SigningAlgoSecp256k1 {
		t.Fatalf("BTC signing algo = %s", algo)
	}
	btcAddr, err := vault.GetAddress(common.BTCChain)
	want, _ := vault.PubKey.GetAddress(common.BTCChain)
	if err != nil || !btcAddr.Equals(want) {
		t.Fatalf("BTC address = %s (%v), want %s", btcAddr, err, want)
	}

	// An XMR monovault's ed25519 FROST key is never a taproot key.
	xmr := NewVault(1, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), []string{common.XMRChain.String()}, nil)
	xmr.PubKeyFrost = GetRandomEd25519PubKey()
	if _, ok := xmr.TaprootPubKey(common.LTCChain); ok {
		t.Fatal("ed25519 FROST key treated as taproot")
	}
	if algo := xmr.ChainSigningAlgo(common.LTCChain); algo != common.SigningAlgoSecp256k1 {
		t.Fatalf("XMR vault LTC signing algo = %s", algo)
	}
	if xmrKey, err := xmr.AlgoPubKey(common.XMRChain); err != nil || !xmrKey.Equals(xmr.PubKeyFrost) {
		t.Fatalf("XMR key = %s (%v)", xmrKey, err)
	}

	// No FROST key: plain ECDSA vault.
	plain := NewVault(1, VaultStatus_ActiveVault, VaultType_AsgardVault, GetRandomPubKey(), []string{common.LTCChain.String()}, nil)
	if _, ok := plain.TaprootPubKey(common.LTCChain); ok {
		t.Fatal("vault without FROST key treated as taproot")
	}
	if plainKey, err := plain.AlgoPubKey(common.LTCChain); err != nil || !plainKey.Equals(plain.PubKey) {
		t.Fatalf("plain vault LTC key = %s (%v)", plainKey, err)
	}
}
