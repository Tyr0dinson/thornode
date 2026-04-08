package monero

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

func expectedXMRVaultAddress(_ common.PubKey, poolPubKeyEddsa common.PubKey) (common.Address, error) {
	vaultPubKey := poolPubKeyEddsa
	if vaultPubKey.IsEmpty() {
		return common.NoAddress, fmt.Errorf("xmr keygen requires a non-empty eddsa vault pubkey")
	}

	expected, err := vaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		return common.NoAddress, fmt.Errorf("fail to derive monero address from vault pubkey %s: %w", vaultPubKey.String(), err)
	}
	if expected.IsEmpty() {
		return common.NoAddress, fmt.Errorf("monero vault pubkey %s derived an empty address", vaultPubKey.String())
	}
	return expected, nil
}

func expectedXMRVaultAddressFromKeygenRequest(vaultPubKey, vaultPubKeyEddsa string) (common.Address, error) {
	parseVaultPubKey := func(value string) (common.PubKey, error) {
		value = strings.TrimSpace(value)
		if value == "" {
			return common.EmptyPubKey, nil
		}
		pubKey, err := common.NewPubKey(value)
		if err != nil {
			return common.EmptyPubKey, fmt.Errorf("invalid vault pubkey %q: %w", value, err)
		}
		return pubKey, nil
	}

	poolPubKey, err := parseVaultPubKey(vaultPubKey)
	if err != nil {
		return common.NoAddress, err
	}
	poolPubKeyEddsa, err := parseVaultPubKey(vaultPubKeyEddsa)
	if err != nil {
		return common.NoAddress, err
	}
	return expectedXMRVaultAddress(poolPubKey, poolPubKeyEddsa)
}

var testVaultAddressCounter atomic.Uint64

func testVaultPubKeyAndAddress(t *testing.T) (common.PubKey, common.Address) {
	t.Helper()

	seed := fmt.Sprintf("vault-address:%s:%d", t.Name(), testVaultAddressCounter.Add(1))
	pubKey := testVaultPubKey(t, seed)
	address, err := pubKey.GetAddress(common.XMRChain)
	if err != nil {
		t.Fatalf("derive monero address: %v", err)
	}
	return pubKey, address
}

func TestExpectedXMRVaultAddressFromKeygenRequest(t *testing.T) {
	t.Parallel()

	pubKey, expected := testVaultPubKeyAndAddress(t)
	got, err := expectedXMRVaultAddressFromKeygenRequest("", pubKey.String())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !got.Equals(expected) {
		t.Fatalf("expected %s, got %s", expected, got)
	}
}

func TestExpectedXMRVaultAddressFromKeygenRequestRejectsMissingEddsa(t *testing.T) {
	t.Parallel()

	pubKey, _ := testVaultPubKeyAndAddress(t)
	_, err := expectedXMRVaultAddressFromKeygenRequest(pubKey.String(), "")
	if err == nil {
		t.Fatal("expected missing eddsa pubkey error")
	}
}
