package monero

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
)

// TestVaultAddressStorePurgesLegacyV1Entries validates that stale v1 entries
// (from the double-registration bug where both frost and eddsa keys were
// persisted for the same vault address) are purged on startup.
//
// The bug: v1 store contained eddsa-keyed mappings. On bifrost restart,
// loadVaultAddressMap restored them. findVaultPubKeyByAddress could return
// the eddsa key, triggering ensureFrostVaultRegistration(eddsa) → sidecar
// 500 ("already registered with different mapping") → scanner_sync blocked.
func TestVaultAddressStorePurgesLegacyV1Entries(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vault-addresses.db")
	db, err := leveldb.OpenFile(path, nil)
	require.NoError(t, err)
	defer db.Close()

	// Simulate v1 entries that were persisted by the old buggy code.
	legacyKey1 := []byte(vaultAddressPrefixLegacyV1 + "thorpub1eddsa_old_key_1")
	legacyKey2 := []byte(vaultAddressPrefixLegacyV1 + "thorpub1eddsa_old_key_2")
	require.NoError(t, db.Put(legacyKey1, []byte("addr1"), nil))
	require.NoError(t, db.Put(legacyKey2, []byte("addr2"), nil))

	// Add a current v2 entry that should survive the purge.
	store := NewVaultAddressStore(db)
	require.NoError(t, store.Set("thorpub1frost_current", "addr3"))

	// Purge.
	require.NoError(t, store.PurgeLegacyEntries())

	// v1 entries should be gone.
	_, err = db.Get(legacyKey1, nil)
	assert.ErrorIs(t, err, leveldb.ErrNotFound, "legacy v1 entry 1 should be purged")
	_, err = db.Get(legacyKey2, nil)
	assert.ErrorIs(t, err, leveldb.ErrNotFound, "legacy v1 entry 2 should be purged")

	// v2 entry should still be present.
	addr, err := store.Get("thorpub1frost_current")
	require.NoError(t, err)
	assert.Equal(t, "addr3", addr)

	// List should only return v2 entries.
	all, err := store.List()
	require.NoError(t, err)
	assert.Equal(t, 1, len(all))
	assert.Equal(t, "addr3", all["thorpub1frost_current"])
}

// TestVaultAddressStorePurgeIdempotent verifies purging is safe to call
// multiple times and when there's nothing to purge.
func TestVaultAddressStorePurgeIdempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vault-addresses.db")
	db, err := leveldb.OpenFile(path, nil)
	require.NoError(t, err)
	defer db.Close()

	store := NewVaultAddressStore(db)

	// Purge on empty store — no error.
	require.NoError(t, store.PurgeLegacyEntries())

	// Add a v2 entry.
	require.NoError(t, store.Set("thorpub1vault", "addr"))

	// Purge again — v2 entry remains.
	require.NoError(t, store.PurgeLegacyEntries())

	all, err := store.List()
	require.NoError(t, err)
	assert.Equal(t, 1, len(all))
}

// TestVaultAddressStoreNewWritesUseV2Prefix verifies new writes use the v2
// prefix so they won't be affected by future v1 purges.
func TestVaultAddressStoreNewWritesUseV2Prefix(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vault-addresses.db")
	db, err := leveldb.OpenFile(path, nil)
	require.NoError(t, err)
	defer db.Close()

	store := NewVaultAddressStore(db)
	require.NoError(t, store.Set("thorpub1vault", "addr"))

	// The new entry should be under the v2 prefix.
	v2Key := []byte(vaultAddressPrefix + "thorpub1vault")
	value, err := db.Get(v2Key, nil)
	require.NoError(t, err)
	assert.Equal(t, "addr", string(value))

	// And NOT under the v1 prefix.
	v1Key := []byte(vaultAddressPrefixLegacyV1 + "thorpub1vault")
	_, err = db.Get(v1Key, nil)
	assert.ErrorIs(t, err, leveldb.ErrNotFound)

	// Verify the v2 prefix is actually different from v1.
	assert.NotEqual(t, vaultAddressPrefix, vaultAddressPrefixLegacyV1)
}
