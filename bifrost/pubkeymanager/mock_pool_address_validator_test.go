package pubkeymanager

import (
	"testing"

	"github.com/btcsuite/btcutil/bech32"

	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestNormalizeMockPubKeyCov(t *testing.T) {
	// invalid bech32 is returned untouched
	if got := normalizeMockPubKey("not-bech32"); got != "not-bech32" {
		t.Fatalf("got %q", got)
	}

	// already in the build's prefix: untouched
	if got := normalizeMockPubKey(MockPubkey); got != MockPubkey {
		t.Fatalf("got %q", got)
	}

	// different prefix gets re-encoded with the build's account pubkey prefix
	hrp, data, err := bech32.DecodeNoLimit(MockPubkey)
	if err != nil {
		t.Fatal(err)
	}
	if hrp != cmd.Bech32PrefixAccPub {
		t.Fatalf("unexpected hrp %s", hrp)
	}
	other, err := bech32.Encode("otherpub", data)
	if err != nil {
		t.Fatal(err)
	}
	if got := normalizeMockPubKey(other); got != MockPubkey {
		t.Fatalf("expected %q got %q", MockPubkey, got)
	}
}

func TestMockPoolAddressValidatorCov(t *testing.T) {
	cosmos.GetConfig().SetBech32PrefixForAccount(cmd.Bech32PrefixAccAddr, cmd.Bech32PrefixAccPub)
	m := NewMockPoolAddressValidator()

	if m.GetPubKeys() != nil {
		t.Fatal("expected nil pubkeys")
	}
	if m.GetAlgoPubKeys(common.SigningAlgoSecp256k1, true) != nil {
		t.Fatal("expected nil algo pubkeys")
	}

	signKeys := m.GetSignPubKeys()
	if len(signKeys) != 1 || signKeys[0].String() != MockPubkey {
		t.Fatalf("unexpected sign pubkeys %v", signKeys)
	}
	if !m.HasPubKey(signKeys[0]) {
		t.Fatal("expected HasPubKey true for mock pubkey")
	}
	if m.HasPubKey(common.EmptyPubKey) {
		t.Fatal("expected HasPubKey false for empty pubkey")
	}
	if !m.GetNodePubKey(common.SigningAlgoSecp256k1).IsEmpty() {
		t.Fatal("expected empty node pubkey")
	}

	// mutators are no-ops
	m.AddPubKey(signKeys[0], true, common.SigningAlgoSecp256k1)
	m.AddNodePubKey(signKeys[0], common.SigningAlgoSecp256k1)
	m.RemovePubKey(signKeys[0])
	m.RegisterCallback(nil)
	if m.GetPubKeys() != nil || !m.HasPubKey(signKeys[0]) {
		t.Fatal("mutators must not change state")
	}

	if err := m.Start(); err == nil || err.Error() != "kaboom" {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Stop(); err == nil || err.Error() != "kaboom" {
		t.Fatalf("Stop: %v", err)
	}

	ok, info := m.IsValidPoolAddress("addr", common.BTCChain)
	if ok || info != common.EmptyChainPoolInfo {
		t.Fatal("IsValidPoolAddress must be false/empty")
	}
	ok, info = m.IsActivePoolAddress("addr", common.BTCChain)
	if ok || info != common.EmptyChainPoolInfo {
		t.Fatal("IsActivePoolAddress must be false/empty")
	}
	if m.GetContracts(common.ETHChain, true) != nil {
		t.Fatal("expected nil contracts")
	}
	if m.GetContract(common.ETHChain, signKeys[0]) != common.NoAddress {
		t.Fatal("expected NoAddress")
	}
}
