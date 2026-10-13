package thorchain

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestTxOutSourceAddressTaproot(t *testing.T) {
	supported := common.LTCChain.SupportsTaprootVaults()
	ecdsaKey := types.GetRandomPubKey()
	frostKey := types.GetTaprootTestPubKey()
	coin := common.NewCoin(common.LTCAsset, cosmos.NewUint(1_000_000))

	item := types.TxOutItem{Chain: common.LTCChain, ToAddress: types.GetRandomLTCAddress(), VaultPubKey: ecdsaKey, VaultPubKeyFrost: frostKey, Coin: coin}
	addr, err := txOutSourceAddress(item)
	if err != nil {
		t.Fatal(err)
	}
	if supported {
		if !txOutUsesTaprootVault(item) || txOutSigningAlgo(item) != common.SigningAlgoSecp256k1Taproot {
			t.Fatal("LTC item with a secp256k1 FROST key should use the taproot vault")
		}
		src, srcErr := txOutSourcePubKey(item)
		if srcErr != nil || !src.Equals(frostKey) {
			t.Fatalf("source pubkey = %s (%v), want FROST key", src, srcErr)
		}
		want, _ := frostKey.GetAddressForAlgo(common.LTCChain, common.SigningAlgoSecp256k1Taproot)
		if !addr.Equals(want) {
			t.Fatalf("source address = %s, want taproot %s", addr, want)
		}
	} else {
		if txOutUsesTaprootVault(item) || txOutSigningAlgo(item) != common.SigningAlgoSecp256k1 {
			t.Fatalf("taproot vault used on %v build", common.CurrentChainNetwork)
		}
		want, _ := ecdsaKey.GetAddress(common.LTCChain)
		if !addr.Equals(want) {
			t.Fatalf("source address = %s, want ECDSA %s", addr, want)
		}
	}

	// Without a FROST key the ECDSA vault address is used.
	plain := item
	plain.VaultPubKeyFrost = common.EmptyPubKey
	addr, err = txOutSourceAddress(plain)
	want, _ := ecdsaKey.GetAddress(common.LTCChain)
	if err != nil || !addr.Equals(want) || txOutUsesTaprootVault(plain) {
		t.Fatalf("plain source address = %s (%v), want %s", addr, err, want)
	}

	// An ed25519 FROST key on an LTC item is not a taproot vault.
	odd := item
	odd.VaultPubKeyFrost = types.GetRandomEd25519PubKey()
	if txOutUsesTaprootVault(odd) {
		t.Fatal("ed25519 FROST key treated as a taproot vault")
	}

	// BTC never uses the FROST key.
	btc := item
	btc.Chain = common.BTCChain
	btc.Coin = common.NewCoin(common.BTCAsset, cosmos.NewUint(1))
	addr, err = txOutSourceAddress(btc)
	want, _ = ecdsaKey.GetAddress(common.BTCChain)
	if err != nil || !addr.Equals(want) || txOutSigningAlgo(btc) != common.SigningAlgoSecp256k1 {
		t.Fatalf("BTC source address = %s (%v), want %s", addr, err, want)
	}

	// XMR keeps resolving through its ed25519 FROST key.
	xmr := types.TxOutItem{Chain: common.XMRChain, VaultPubKey: ecdsaKey, VaultPubKeyFrost: types.GetRandomEd25519PubKey()}
	src, err := txOutSourcePubKey(xmr)
	if err != nil || !src.Equals(xmr.VaultPubKeyFrost) || txOutSigningAlgo(xmr) != common.SigningAlgoEd25519 {
		t.Fatalf("XMR source pubkey = %s (%v)", src, err)
	}
}
