package monero

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

type mockFrostKeygenTSS struct {
	tssp.Server

	calls   int
	lastReq tssp.FrostKeygenRequest
	sidecar tssp.FrostSidecarKeygenClient

	resp tssp.FrostKeygenResponse
	err  error
}

func (m *mockFrostKeygenTSS) FrostKeygen(req tssp.FrostKeygenRequest, sidecar tssp.FrostSidecarKeygenClient) (tssp.FrostKeygenResponse, error) {
	m.calls++
	m.lastReq = req
	m.sidecar = sidecar
	return m.resp, m.err
}

func newTestFrostSigner(t *testing.T) *FrostSigner {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	signer, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{SignerStoreSecret: testSignerStoreSecret},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	return signer
}

func newTestVaultAddressStore(t *testing.T) *VaultAddressStore {
	t.Helper()
	mem := storage.NewMemStorage()
	db, err := leveldb.Open(mem, nil)
	if err != nil {
		t.Fatalf("open leveldb: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return NewVaultAddressStore(db)
}

func TestClientKeygenUsesFrostTSSPathAndPersistsVaultAddress(t *testing.T) {
	t.Parallel()

	vaultPubKey, expectedAddress := testVaultPubKeyAndAddress(t)
	groupKey := strings.Repeat("ab", 32)
	tssServer := &mockFrostKeygenTSS{
		resp: tssp.FrostKeygenResponse{
			Address:  expectedAddress.String(),
			GroupKey: groupKey,
			Status:   tsscommon.Success,
		},
	}
	store := newTestVaultAddressStore(t)

	c := &Client{
		chain:             common.XMRChain,
		frostSigner:       newTestFrostSigner(t),
		tssServer:         tssServer,
		vaultAddressMap:   make(map[string]common.Address),
		vaultAddressStore: store,
	}

	addr, blame, err := c.Keygen(chainclienttypes.CustomKeygenRequest{
		Members:          []string{" member-a ", "member-b"},
		BlockHeight:      101,
		ThorchainVersion: "1.134.0",
		PoolPubKeyEddsa:  vaultPubKey,
	})
	if err != nil {
		t.Fatalf("Keygen failed: %v", err)
	}
	if len(blame) != 0 {
		t.Fatalf("expected no blame, got %+v", blame)
	}
	if !addr.Equals(expectedAddress) {
		t.Fatalf("expected address %s, got %s", expectedAddress, addr)
	}

	if tssServer.calls != 1 {
		t.Fatalf("expected one FrostKeygen call, got %d", tssServer.calls)
	}
	if tssServer.sidecar == nil {
		t.Fatalf("expected non-nil sidecar adapter")
	}
	if tssServer.lastReq.VaultPubKey != vaultPubKey.String() {
		t.Fatalf("expected vault pubkey %s, got %s", vaultPubKey, tssServer.lastReq.VaultPubKey)
	}
	if tssServer.lastReq.ExpectedAddr != expectedAddress.String() {
		t.Fatalf("expected expected_addr %s, got %s", expectedAddress, tssServer.lastReq.ExpectedAddr)
	}
	if tssServer.lastReq.BlockHeight != 101 {
		t.Fatalf("expected block height 101, got %d", tssServer.lastReq.BlockHeight)
	}
	if tssServer.lastReq.Version != "1.134.0" {
		t.Fatalf("expected version 1.134.0, got %s", tssServer.lastReq.Version)
	}
	if len(tssServer.lastReq.SignerPubKeys) != 2 || tssServer.lastReq.SignerPubKeys[0] != "member-a" || tssServer.lastReq.SignerPubKeys[1] != "member-b" {
		t.Fatalf("unexpected signer set: %+v", tssServer.lastReq.SignerPubKeys)
	}

	mapped, ok := c.getMappedVaultAddress(vaultPubKey)
	if !ok {
		t.Fatalf("expected in-memory vault mapping for %s", vaultPubKey)
	}
	if !mapped.Equals(expectedAddress) {
		t.Fatalf("expected mapped address %s, got %s", expectedAddress, mapped)
	}

	persisted, err := store.Get(vaultPubKey.String())
	if err != nil {
		t.Fatalf("expected persisted vault mapping: %v", err)
	}
	if persisted != expectedAddress.String() {
		t.Fatalf("expected persisted address %s, got %s", expectedAddress, persisted)
	}
}

func TestClientKeygenFailsWhenFrostAddressMismatchesDerivedAddress(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	_, wrongAddress := testVaultPubKeyAndAddress(t)
	tssServer := &mockFrostKeygenTSS{
		resp: tssp.FrostKeygenResponse{
			Address:  wrongAddress.String(),
			GroupKey: strings.Repeat("cd", 32),
			Status:   tsscommon.Success,
		},
	}
	store := newTestVaultAddressStore(t)

	c := &Client{
		chain:             common.XMRChain,
		frostSigner:       newTestFrostSigner(t),
		tssServer:         tssServer,
		vaultAddressMap:   make(map[string]common.Address),
		vaultAddressStore: store,
	}

	_, _, err := c.Keygen(chainclienttypes.CustomKeygenRequest{
		Members:         []string{"member-a", "member-b"},
		BlockHeight:     101,
		PoolPubKeyEddsa: vaultPubKey,
	})
	if err == nil {
		t.Fatalf("expected keygen mismatch error")
	}
	if !strings.Contains(err.Error(), "returned address") {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := c.getMappedVaultAddress(vaultPubKey); ok {
		t.Fatalf("unexpected vault mapping persisted on mismatched result")
	}
	if _, err := store.Get(vaultPubKey.String()); err == nil {
		t.Fatalf("expected no persisted mapping on mismatched result")
	}
}
