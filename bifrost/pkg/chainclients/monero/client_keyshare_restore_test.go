package monero

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// recoverySidecarState mimics the real sidecar's restore contract: bech32
// vault identifiers resolve only for registered vaults, unregistered restores
// are rejected with a 400.
type recoverySidecarState struct {
	mu                sync.Mutex
	registered        map[string]bool
	groupKeys         map[string]string
	restored          map[string]bool
	migrateGroupKey   string
	migrateFailure    bool
	unverifiedRestore bool
	calls             []string
	failRegisters     int
}

func (s *recoverySidecarState) callOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func newRecoverySidecarServer(t *testing.T) (*httptest.Server, *recoverySidecarState) {
	t.Helper()

	state := &recoverySidecarState{registered: map[string]bool{}, groupKeys: map[string]string{}, restored: map[string]bool{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/monero/vaults/register":
			var req sidecar.RegisterVaultReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state.mu.Lock()
			state.calls = append(state.calls, "register")
			if state.failRegisters > 0 {
				state.failRegisters--
				state.mu.Unlock()
				http.Error(w, "sidecar starting", http.StatusServiceUnavailable)
				return
			}
			state.registered[req.VaultPubKey] = true
			state.groupKeys[req.VaultPubKey] = req.GroupKey
			state.mu.Unlock()
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(0))
		case "/v1/monero/keys/recovery/migrate":
			state.mu.Lock()
			groupKey := state.migrateGroupKey
			fail := state.migrateFailure
			if groupKey != "" || fail {
				state.calls = append(state.calls, "migrate")
			}
			state.mu.Unlock()
			if groupKey == "" && !fail {
				http.NotFound(w, r)
				return
			}
			if fail {
				http.Error(w, "invalid backup", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"key_blob_b64":           base64.StdEncoding.EncodeToString([]byte("sealed-backup")),
				"recovery_vault_pub_key": groupKey,
			})
		case "/v1/monero/keys/restore":
			var req struct {
				VaultPubKey string `json:"vault_pub_key"`
				KeyBlobB64  string `json:"key_blob_b64"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state.mu.Lock()
			state.calls = append(state.calls, "restore")
			registered := state.registered[req.VaultPubKey] || (state.migrateGroupKey != "" && req.VaultPubKey == state.migrateGroupKey && req.KeyBlobB64 == base64.StdEncoding.EncodeToString([]byte("sealed-backup")))
			state.mu.Unlock()
			if !registered {
				http.Error(w, "vault_pub_key does not match restored FROST keys", http.StatusBadRequest)
				return
			}
			state.mu.Lock()
			state.restored[req.VaultPubKey] = true
			if req.VaultPubKey == state.migrateGroupKey {
				state.groupKeys[req.VaultPubKey] = req.VaultPubKey
			}
			state.mu.Unlock()
			if req.VaultPubKey == state.migrateGroupKey {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"restored": true, "signable": !state.unverifiedRestore,
					"vault_pub_key": req.VaultPubKey, "group_key": req.VaultPubKey,
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]bool{"restored": true})
			}
		case "/v1/monero/keys":
			state.mu.Lock()
			state.calls = append(state.calls, "status")
			vaults := make([]sidecar.VaultKey, 0, len(state.registered))
			for vault := range state.registered {
				group := state.groupKeys[vault]
				signable := state.restored[vault] || state.restored[group]
				vaults = append(vaults, sidecar.VaultKey{
					VaultPubKey:  vault,
					GroupKey:     group,
					HasFrostKeys: signable,
					Signable:     signable,
				})
			}
			state.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"vaults": vaults})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, state
}

func newRecoveryTestClient(t *testing.T, endpoint string) *Client {
	t.Helper()

	frostSigner, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, endpoint)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	return &Client{
		logger:                   log.Logger,
		chain:                    common.XMRChain,
		sidecar:                  frostSigner,
		vaultAddressMap:          map[string]common.Address{},
		recoveryRetryInterval:    time.Millisecond,
		recoveryRetryMaxAttempts: 5,
	}
}

func writeRecoveryFile(t *testing.T, dir string, vaultPubKey common.PubKey) string {
	t.Helper()

	path := filepath.Join(dir, "frost-recovery-"+vaultPubKey.String()+".hex")
	if err := os.WriteFile(path, []byte("deadbeef"), 0o600); err != nil {
		t.Fatalf("write recovery file: %v", err)
	}
	return path
}

func TestReadRecoveryFileRepairsInterruptedPublishAlias(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "frost-recovery-test.hex")
	want := []byte("authenticated-encrypted-envelope")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("write final recovery file: %v", err)
	}
	alias := path + ".tmp-crash"
	if err := os.Link(path, alias); err != nil {
		t.Fatalf("create interrupted publish alias: %v", err)
	}

	got, err := readRecoveryFile(path)
	if err != nil {
		t.Fatalf("read recovery file: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("unexpected recovery bytes: %q", got)
	}
	if _, err = os.Lstat(alias); !os.IsNotExist(err) {
		t.Fatalf("tool-owned publish alias was not removed: %v", err)
	}
}

func TestConsumeRecoveryFileLeavesNoStrandedArtifact(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "frost-recovery-test.hex")
	if err := os.WriteFile(path, []byte("authenticated-encrypted-envelope"), 0o600); err != nil {
		t.Fatalf("write final recovery file: %v", err)
	}

	if err := consumeRecoveryFile(path); err != nil {
		t.Fatalf("consume recovery file: %v", err)
	}

	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read recovery dir: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("consume left artifacts behind: %v", left[0].Name())
	}
}

func TestSweepStaleConsumedRecoveryFilesReclaimsCrashLeftovers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stale := filepath.Join(dir, "frost-recovery-test.hex.consumed-1752451200000000000")
	if err := os.WriteFile(stale, []byte("stranded"), 0o600); err != nil {
		t.Fatalf("write stale consumed file: %v", err)
	}
	// An in-flight publish alias must survive: removing it would race the writer.
	pending := filepath.Join(dir, "frost-recovery-test.hex.tmp-inflight")
	if err := os.WriteFile(pending, []byte("publishing"), 0o600); err != nil {
		t.Fatalf("write in-flight publish alias: %v", err)
	}

	if err := sweepStaleConsumedRecoveryFiles(dir); err != nil {
		t.Fatalf("sweep stale consumed recovery files: %v", err)
	}

	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale consumed artifact was not reclaimed: %v", err)
	}
	if _, err := os.Lstat(pending); err != nil {
		t.Fatalf("in-flight publish alias must not be swept: %v", err)
	}
}

func TestReadRecoveryFileRejectsUnownedHardLink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "frost-recovery-test.hex")
	if err := os.WriteFile(path, []byte("authenticated-encrypted-envelope"), 0o600); err != nil {
		t.Fatalf("write final recovery file: %v", err)
	}
	unowned := filepath.Join(dir, "retained-copy")
	if err := os.Link(path, unowned); err != nil {
		t.Fatalf("create unowned hard link: %v", err)
	}

	if _, err := readRecoveryFile(path); err == nil || !strings.Contains(err.Error(), "unexpected hard links") {
		t.Fatalf("expected unowned hard link rejection, got %v", err)
	}
}

func TestRestoreKeyshareRejectedWhileVaultUnregistered(t *testing.T) {
	t.Parallel()

	server, _ := newRecoverySidecarServer(t)
	c := newRecoveryTestClient(t, server.URL)
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)

	err := c.RestoreKeyshare(vaultPubKey, []byte("deadbeef"))
	if err == nil {
		t.Fatal("expected restore to fail for unregistered vault")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("expected unregistered-vault rejection, got: %v", err)
	}
}

func TestRestoreKeysharesRegistersVaultBeforeRestore(t *testing.T) {
	t.Parallel()

	server, state := newRecoverySidecarServer(t)
	c := newRecoveryTestClient(t, server.URL)
	vaultPubKey, vaultAddress := testVaultPubKeyAndAddress(t)

	dir := t.TempDir()
	path := writeRecoveryFile(t, dir, vaultPubKey)

	c.restoreKeysharesFromDir(dir)

	calls := state.callOrder()
	if len(calls) != 3 || calls[0] != "register" || calls[1] != "restore" || calls[2] != "status" {
		t.Fatalf("expected register, restore, then exact-vault status, got calls %v", calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected recovery file to be removed after restore, stat err: %v", err)
	}
	if got, ok := c.vaultAddressMap[vaultPubKey.String()]; !ok || !got.Equals(vaultAddress) {
		t.Fatalf("expected vault address mapping %s, got %s (present=%t)", vaultAddress, got, ok)
	}
}

func TestRestoreKeysharesMigratesLegacyBackupAndVerifiesVault(t *testing.T) {
	t.Parallel()

	server, state := newRecoverySidecarServer(t)
	c := newRecoveryTestClient(t, server.URL)
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	groupKey, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		t.Fatal(err)
	}
	state.migrateGroupKey = groupKey
	dir := t.TempDir()
	path := writeRecoveryFile(t, dir, vaultPubKey)

	c.restoreKeysharesFromDir(dir)

	calls := state.callOrder()
	if got := strings.Join(calls, ","); got != "register,migrate,restore,status" {
		t.Fatalf("expected migration, canonical restore and vault status, got %v", calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected recovery file consumed only after verified restore, stat err: %v", err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.restored[state.migrateGroupKey] || state.restored[vaultPubKey.String()] {
		t.Fatalf("expected only canonical group key restored, got %v", state.restored)
	}
}

func TestRestoreKeysharesRetainsFileWhenMigratedGroupDiffersFromVault(t *testing.T) {
	t.Parallel()

	server, state := newRecoverySidecarServer(t)
	state.migrateGroupKey = strings.Repeat("ab", 32)
	c := newRecoveryTestClient(t, server.URL)
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	// A previously restored share can make the alias status look healthy even
	// though the submitted backup belongs to another FROST group.
	state.restored[vaultPubKey.String()] = true
	dir := t.TempDir()
	path := writeRecoveryFile(t, dir, vaultPubKey)

	c.restoreKeysharesFromDir(dir)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("restore under a different group must retain recovery file: %v", err)
	}
	for _, call := range state.callOrder() {
		if call == "restore" {
			t.Fatal("mismatched migrated group must not be restored")
		}
	}
}

func TestRestoreKeysharesRetainsFileWhenMigrationRejectsBackup(t *testing.T) {
	t.Parallel()

	server, state := newRecoverySidecarServer(t)
	state.migrateFailure = true
	c := newRecoveryTestClient(t, server.URL)
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	dir := t.TempDir()
	path := writeRecoveryFile(t, dir, vaultPubKey)

	c.restoreKeysharesFromDir(dir)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("migration failure must retain recovery file: %v", err)
	}
	for _, call := range state.callOrder() {
		if call == "restore" {
			t.Fatal("migration rejection must not fall back to legacy restore")
		}
	}
}

func TestRestoreKeysharesRetainsFileWhenMigratedRestoreIsNotSignable(t *testing.T) {
	t.Parallel()

	server, state := newRecoverySidecarServer(t)
	c := newRecoveryTestClient(t, server.URL)
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	// Use the vault's real group key so the migration passes the group check
	// and the sealed restore actually runs. The mock stores the share (a later
	// status call would report the vault signable) but answers signable=false,
	// so only RestoreSealedKeysContext's Signable check keeps the file.
	groupKey, err := groupKeyHexFromVaultPubKey(vaultPubKey)
	if err != nil {
		t.Fatal(err)
	}
	state.migrateGroupKey = groupKey
	state.unverifiedRestore = true
	dir := t.TempDir()
	path := writeRecoveryFile(t, dir, vaultPubKey)

	c.restoreKeysharesFromDir(dir)

	calls := state.callOrder()
	if !slices.Contains(calls, "restore") {
		t.Fatalf("expected sealed restore to be attempted, got calls %v", calls)
	}
	if slices.Contains(calls, "status") {
		t.Fatalf("unsignable sealed restore must fail before vault status, got calls %v", calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unverified restore must retain recovery file: %v", err)
	}
}

func TestRestoreKeysharesRetriesWhileSidecarStarting(t *testing.T) {
	t.Parallel()

	server, state := newRecoverySidecarServer(t)
	state.failRegisters = 2
	c := newRecoveryTestClient(t, server.URL)
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)

	dir := t.TempDir()
	path := writeRecoveryFile(t, dir, vaultPubKey)

	c.restoreKeysharesFromDir(dir)

	calls := state.callOrder()
	if calls[len(calls)-1] != "status" {
		t.Fatalf("expected restore to eventually succeed, got calls %v", calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected recovery file to be removed after retried restore, stat err: %v", err)
	}
}

func TestRestoreKeysharesKeepsFileWhenSidecarUnavailable(t *testing.T) {
	t.Parallel()

	server, state := newRecoverySidecarServer(t)
	state.failRegisters = 100 // more than max attempts
	c := newRecoveryTestClient(t, server.URL)
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)

	dir := t.TempDir()
	path := writeRecoveryFile(t, dir, vaultPubKey)

	c.restoreKeysharesFromDir(dir)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected recovery file to be kept for next startup, stat err: %v", err)
	}
}

func TestRestoreKeysharesCancelsInFlightHTTPOnShutdown(t *testing.T) {
	started := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/vaults/register" {
			http.NotFound(w, r)
			return
		}
		close(started)
		<-releaseHandler
	}))
	defer func() {
		close(releaseHandler)
		server.Close()
	}()

	c := newRecoveryTestClient(t, server.URL)
	c.stopchan = make(chan struct{})
	c.recoveryRetryMaxAttempts = 0
	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	dir := t.TempDir()
	path := writeRecoveryFile(t, dir, vaultPubKey)

	done := make(chan struct{})
	go func() {
		c.restoreKeysharesFromDir(dir)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovery request")
	}
	close(c.stopchan)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery did not cancel promptly on shutdown")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected encrypted recovery artifact to remain after cancellation: %v", err)
	}
}
