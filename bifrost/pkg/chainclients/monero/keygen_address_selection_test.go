package monero

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

func expectedXMRVaultAddress(vaultPubKeyFrost common.PubKey) (common.Address, error) {
	vaultPubKey := vaultPubKeyFrost
	if vaultPubKey.IsEmpty() {
		return common.NoAddress, fmt.Errorf("xmr keygen requires a non-empty frost vault pubkey")
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

func expectedXMRVaultAddressFromKeygenRequest(vaultPubKeyFrost string) (common.Address, error) {
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

	poolPubKeyFrost, err := parseVaultPubKey(vaultPubKeyFrost)
	if err != nil {
		return common.NoAddress, err
	}
	return expectedXMRVaultAddress(poolPubKeyFrost)
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
	got, err := expectedXMRVaultAddressFromKeygenRequest(pubKey.String())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !got.Equals(expected) {
		t.Fatalf("expected %s, got %s", expected, got)
	}
}

func TestExpectedXMRVaultAddressFromKeygenRequestRejectsMissingFrost(t *testing.T) {
	t.Parallel()

	_, err := expectedXMRVaultAddressFromKeygenRequest("")
	if err == nil {
		t.Fatal("expected missing frost pubkey error")
	}
}
