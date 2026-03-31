package monero

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestLoadVaultAddressMapRestoresPersistedMapping(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)
	store := newTestVaultAddressStore(t)
	if err := store.Set(vaultPubKey.String(), vaultAddress.String()); err != nil {
		t.Fatalf("persist mapping: %v", err)
	}

	c := &Client{
		chain:             common.XMRChain,
		vaultAddressStore: store,
		vaultAddressMap:   make(map[string]common.Address),
	}

	if err := c.loadVaultAddressMap(); err != nil {
		t.Fatalf("load vault address map: %v", err)
	}
	mapped, ok := c.getMappedVaultAddress(vaultPubKey)
	if !ok {
		t.Fatalf("expected restored mapping for %s", vaultPubKey)
	}
	if !mapped.Equals(vaultAddress) {
		t.Fatalf("expected restored address %s, got %s", vaultAddress, mapped)
	}
}

func TestLoadVaultAddressMapIgnoresMismatchedPersistedMapping(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	_, wrongAddress := testVaultPubKeyAndAddress(t)
	store := newTestVaultAddressStore(t)
	if err := store.Set(vaultPubKey.String(), wrongAddress.String()); err != nil {
		t.Fatalf("persist mismatched mapping: %v", err)
	}

	c := &Client{
		chain:             common.XMRChain,
		vaultAddressStore: store,
		vaultAddressMap:   make(map[string]common.Address),
	}

	if err := c.loadVaultAddressMap(); err != nil {
		t.Fatalf("load vault address map: %v", err)
	}
	if _, ok := c.getMappedVaultAddress(vaultPubKey); ok {
		t.Fatalf("expected mismatched persisted mapping for %s to be ignored", vaultPubKey)
	}
}
