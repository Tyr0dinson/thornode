package pubkeymanager

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestTaprootVaultKeyPoolAddress(t *testing.T) {
	if !common.LTCChain.SupportsTaprootVaults() {
		t.Skipf("taproot vaults are not enabled on %v builds", common.CurrentChainNetwork)
	}
	mgr, err := NewPubKeyManager(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	taprootKey := types.GetTaprootTestPubKey()
	ecdsaKey := types.GetRandomPubKey()
	mgr.AddPubKey(taprootKey, true, common.SigningAlgoSecp256k1Taproot)
	mgr.AddPubKey(ecdsaKey, true, common.SigningAlgoSecp256k1)

	taprootAddr, err := taprootKey.GetAddressForAlgo(common.LTCChain, common.SigningAlgoSecp256k1Taproot)
	if err != nil {
		t.Fatal(err)
	}
	ok, cpi := mgr.IsValidPoolAddress(taprootAddr.String(), common.LTCChain)
	if !ok || !cpi.PubKey.Equals(taprootKey) || !cpi.PoolAddress.Equals(taprootAddr) {
		t.Fatalf("taproot vault address not recognised: ok=%v cpi=%+v", ok, cpi)
	}
	if ok, _ := mgr.IsActivePoolAddress(taprootAddr.String(), common.LTCChain); !ok {
		t.Fatal("taproot vault address not recognised through the active-address cache")
	}

	// The taproot key is registered for taproot only, so its unused ECDSA form
	// is not a vault address.
	unused, _ := taprootKey.GetAddress(common.LTCChain)
	if ok, _ := mgr.IsValidPoolAddress(unused.String(), common.LTCChain); ok {
		t.Fatal("ECDSA form of a taproot-only key recognised as a vault address")
	}
	// Taproot keys never produce BTC vault addresses.
	btcForm, _ := taprootKey.GetAddress(common.BTCChain)
	if ok, _ := mgr.IsValidPoolAddress(btcForm.String(), common.BTCChain); ok {
		t.Fatal("taproot key recognised as a BTC vault address")
	}

	// Plain ECDSA vaults keep working alongside.
	legacyAddr, _ := ecdsaKey.GetAddress(common.LTCChain)
	if ok, cpi := mgr.IsValidPoolAddress(legacyAddr.String(), common.LTCChain); !ok || !cpi.PubKey.Equals(ecdsaKey) {
		t.Fatalf("ECDSA vault address not recognised: ok=%v cpi=%+v", ok, cpi)
	}

	if keys := mgr.GetAlgoPubKeys(common.SigningAlgoSecp256k1Taproot, false); len(keys) != 1 || !keys[0].Equals(taprootKey) {
		t.Fatalf("taproot algo keys = %v", keys)
	}
}
