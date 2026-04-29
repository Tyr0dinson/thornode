package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type countingStateSyncBridge struct {
	mockMoneroBridge
	mu              sync.Mutex
	observedTxCalls int
	txStatusCalls   int
}

func (b *countingStateSyncBridge) GetObservedTxVoter(txID string) (*ttypes.QueryObservedTxVoter, error) {
	b.mu.Lock()
	b.observedTxCalls++
	b.mu.Unlock()
	return b.mockMoneroBridge.GetObservedTxVoter(txID)
}

func (b *countingStateSyncBridge) GetTxStatus(txID string) (*ttypes.QueryTxStatusResponse, error) {
	b.mu.Lock()
	b.txStatusCalls++
	b.mu.Unlock()
	return b.mockMoneroBridge.GetTxStatus(txID)
}

func TestTrackedXMRVaultsForKeyImageCatchupIncludesInactiveTrackedVaults(t *testing.T) {
	t.Parallel()

	currentVaultPubKey, _ := testVaultPubKeyAndAddress(t)
	inactiveVaultPubKey, _ := testVaultPubKeyAndAddress(t)

	currentVault := ttypes.Vault{
		PubKey:                 currentVaultPubKey,
		PubKeyEddsa:            currentVaultPubKey,
		PubKeyFrost:            currentVaultPubKey,
		Status:                 ttypes.VaultStatus_ActiveVault,
		VaultCreationHeightXmr: 50,
	}
	inactiveVault := ttypes.Vault{
		PubKey:                 inactiveVaultPubKey,
		PubKeyEddsa:            inactiveVaultPubKey,
		PubKeyFrost:            inactiveVaultPubKey,
		Status:                 ttypes.VaultStatus_InactiveVault,
		VaultCreationHeightXmr: 25,
		KeyImagesXmr: []*common.XmrKeyImageRef{{
			TxHash:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			KeyImage: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}},
	}

	bridge := &scannerTrackedVaultBridge{
		asgards: ttypes.Vaults{currentVault},
		pubKeys: scannerTrackedVaultPubKeys(currentVaultPubKey, inactiveVaultPubKey),
		vaultByKey: map[string]ttypes.Vault{
			currentVaultPubKey.String():  currentVault,
			inactiveVaultPubKey.String(): inactiveVault,
		},
	}
	scanner := NewScanner(testLogger(), nil, bridge, common.XMRChain, common.EmptyPubKey, 72)
	client := &Client{scanner: scanner}

	vaults, err := client.trackedXMRVaultsForKeyImageCatchup()
	require.NoError(t, err)

	got := make([]string, 0, len(vaults))
	for _, vault := range vaults {
		got = append(got, vault.PubKeyFrost.String())
	}
	sort.Strings(got)

	want := []string{currentVaultPubKey.String(), inactiveVaultPubKey.String()}
	sort.Strings(want)
	assert.Equal(t, want, got)
}

func TestSyncTrackedVaultRegistrationsRegistersNewVaultInRescanMode(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:                 vaultPubKey,
		PubKeyEddsa:            vaultPubKey,
		PubKeyFrost:            vaultPubKey,
		Status:                 ttypes.VaultStatus_InactiveVault,
		VaultCreationHeightXmr: 25,
	}

	var mu sync.Mutex
	var registerReqs []xmrFrostRegisterVaultRequest
	var resetReqs int
	vaultStates := xmrFrostVaultStatesResponse{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			mu.Lock()
			defer mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(vaultStates))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			registerReqs = append(registerReqs, req)
			// Fresh registration in the sidecar auto-enters rescan mode when
			// the vault has a non-zero birthday (store.register_vault default).
			scanHeight := uint64(req.VaultCreationHeight)
			vaultStates.Vaults = []xmrFrostVaultStateEntry{{
				VaultPubKey: req.VaultPubKey,
				Address:     req.Address,
				Birthday:    uint64(req.VaultCreationHeight),
				Rescan:      req.VaultCreationHeight > 0,
				ScanHeight:  &scanHeight,
			}}
			mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/reset":
			mu.Lock()
			resetReqs++
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	client := &Client{
		chain:                 common.XMRChain,
		frostSigner:           signer,
		logger:                testLogger(),
		trackedVaultSyncState: make(map[string]string),
	}

	states, err := client.syncTrackedVaultRegistrations(ttypes.Vaults{vault})
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, vaultPubKey.String(), states[0].VaultPubKey)
	assert.Equal(t, vaultAddr.String(), states[0].Address)
	assert.True(t, states[0].Rescan, "fresh registration with non-zero birthday must be in rescan mode")
	require.NotNil(t, states[0].ScanHeight)
	assert.Equal(t, uint64(25), *states[0].ScanHeight)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, registerReqs, 1)
	assert.Equal(t, vaultPubKey.String(), registerReqs[0].VaultPubKey)
	assert.Equal(t, vaultAddr.String(), registerReqs[0].Address)
	assert.Equal(t, int64(25), registerReqs[0].VaultCreationHeight)
	assert.Equal(t, 0, resetReqs, "bifrost must not call reset in the sync flow — sidecar defaults rescan=true on fresh register")
}

func TestSyncTrackedVaultRegistrationsContinuesAfterVaultRegisterFailure(t *testing.T) {
	t.Parallel()

	badVaultPubKey, _ := testVaultPubKeyAndAddress(t)
	goodVaultPubKey, goodVaultAddr := testVaultPubKeyAndAddress(t)
	badVault := ttypes.Vault{
		PubKey:                 badVaultPubKey,
		PubKeyEddsa:            badVaultPubKey,
		PubKeyFrost:            badVaultPubKey,
		Status:                 ttypes.VaultStatus_InactiveVault,
		VaultCreationHeightXmr: 25,
	}
	goodVault := ttypes.Vault{
		PubKey:                 goodVaultPubKey,
		PubKeyEddsa:            goodVaultPubKey,
		PubKeyFrost:            goodVaultPubKey,
		Status:                 ttypes.VaultStatus_ActiveVault,
		VaultCreationHeightXmr: 30,
	}

	var mu sync.Mutex
	var registerReqs []xmrFrostRegisterVaultRequest
	vaultStates := xmrFrostVaultStatesResponse{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			mu.Lock()
			defer mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(vaultStates))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			registerReqs = append(registerReqs, req)
			if req.VaultPubKey == badVaultPubKey.String() {
				mu.Unlock()
				http.Error(w, `{"error":"from_address does not belong to the provided vault"}`, http.StatusBadRequest)
				return
			}
			vaultStates.Vaults = append(vaultStates.Vaults, xmrFrostVaultStateEntry{
				VaultPubKey: req.VaultPubKey,
				Address:     req.Address,
				Birthday:    uint64(req.VaultCreationHeight),
			})
			mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	client := &Client{
		chain:                 common.XMRChain,
		frostSigner:           signer,
		logger:                testLogger(),
		trackedVaultSyncState: make(map[string]string),
	}

	states, err := client.syncTrackedVaultRegistrations(ttypes.Vaults{badVault, goodVault})
	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, goodVaultPubKey.String(), states[0].VaultPubKey)
	assert.Equal(t, goodVaultAddr.String(), states[0].Address)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, registerReqs, 2)
	assert.Equal(t, badVaultPubKey.String(), registerReqs[0].VaultPubKey)
	assert.Equal(t, goodVaultPubKey.String(), registerReqs[1].VaultPubKey)
}

func TestTxKeyCatchupNeedsReplayUsesSignerOutgoingStateOnly(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)
	txKey := strings.Repeat("1", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+vaultPubKey.String():
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	client := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
	}

	stateCheck, err := client.outgoingInventoryNeedsReplay(vaultPubKey.String(), []xmrOutgoingInventoryRef{{
		txHash: txHash,
		txKeys: []string{txKey},
	}})
	require.NoError(t, err)
	assert.True(t, stateCheck.needsReplay)
	assert.Empty(t, stateCheck.removeHashes)
}

func TestTxKeyCatchupNeedsReplayReturnsSignerError(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	client := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
	}

	_, err = client.outgoingInventoryNeedsReplay(vaultPubKey.String(), []xmrOutgoingInventoryRef{{
		txHash: strings.Repeat("a", 64),
		txKeys: []string{strings.Repeat("1", 64)},
	}})
	require.Error(t, err)
}

func TestTxKeyCatchupNeedsReplayAcceptsRegisteredInventoryBeforeMined(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)
	txKey := strings.Repeat("1", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+vaultPubKey.String():
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:              txHash,
					TxKeys:              []string{txKey},
					RegisteredInventory: true,
					Status:              xmrFrostOutgoingStatusObservedMempool,
				}},
			}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	client := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
	}

	stateCheck, err := client.outgoingInventoryNeedsReplay(vaultPubKey.String(), []xmrOutgoingInventoryRef{{
		txHash: txHash,
		txKeys: []string{txKey},
	}})
	require.NoError(t, err)
	assert.False(t, stateCheck.needsReplay)
	assert.Empty(t, stateCheck.removeHashes)
}

func TestSyncTrackedVaultRegistrationsSkipsRepeatedTxKeyMetadataLoads(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	parentTxHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	txKey := "1111111111111111111111111111111111111111111111111111111111111111"
	destination := testOutboundXMRDestination()

	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		TxKeysXmr: []*common.XmrTxKeyRef{
			{TxHash: txHash, TxKeys: []string{txKey}},
		},
	}
	expectedFingerprint := xmrVaultInventoryFingerprint(vault)

	bridge := &countingStateSyncBridge{
		mockMoneroBridge: mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				vaultPubKey.String(): vault,
			},
			observedTxVoterByID: map[string]*ttypes.QueryObservedTxVoter{
				txHash: {
					TxID: common.TxID(txHash),
					Tx: ttypes.QueryObservedTx{
						Tx: common.NewTx(
							common.TxID(txHash),
							common.Address(common.XMRUnknownSenderAddress(txHash, vaultAddr.String())),
							vaultAddr,
							common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
							nil,
							"OUT:"+parentTxHash,
						),
						ObservedPubKey: vaultPubKey,
						BlockHeight:    100,
					},
				},
			},
			txStatusByID: map[string]*ttypes.QueryTxStatusResponse{
				parentTxHash: {
					OutTxs: common.Txs{
						common.NewTx(
							common.TxID(txHash),
							vaultAddr,
							destination,
							common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
							nil,
							"OUT:"+parentTxHash,
						),
					},
				},
			},
		},
	}

	var mu sync.Mutex
	registerCalls := 0
	registered := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			mu.Lock()
			defer mu.Unlock()
			resp := xmrFrostVaultStatesResponse{}
			if registered {
				resp.Vaults = []xmrFrostVaultStateEntry{{
					VaultPubKey:          vaultPubKey.String(),
					Address:              vaultAddr.String(),
					InventoryFingerprint: expectedFingerprint,
					RegisteredTxKeyCount: 1,
				}}
			}
			require.NoError(t, json.NewEncoder(w).Encode(resp))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+vaultPubKey.String():
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:              txHash,
					TxKeys:              []string{txKey},
					RegisteredInventory: true,
					Status:              "observed_mined",
				}},
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			mu.Lock()
			registerCalls++
			registered = true
			mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	client := &Client{
		chain:                 common.XMRChain,
		frostSigner:           signer,
		logger:                testLogger(),
		thorchainBridge:       bridge,
		trackedVaultSyncState: make(map[string]string),
	}

	_, err = client.syncTrackedVaultRegistrations(ttypes.Vaults{vault})
	require.NoError(t, err)
	_, err = client.syncTrackedVaultRegistrations(ttypes.Vaults{vault})
	require.NoError(t, err)

	mu.Lock()
	assert.Equal(t, 1, registerCalls)
	mu.Unlock()

	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	assert.Equal(t, 1, bridge.observedTxCalls)
	assert.Equal(t, 1, bridge.txStatusCalls)
}

func TestSyncTrackedVaultRegistrationsReregistersWhenSignerInventoryFingerprintMismatches(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:                 vaultPubKey,
		PubKeyEddsa:            vaultPubKey,
		PubKeyFrost:            vaultPubKey,
		Status:                 ttypes.VaultStatus_ActiveVault,
		VaultCreationHeightXmr: 25,
	}
	expectedFingerprint := xmrVaultInventoryFingerprint(vault)

	var mu sync.Mutex
	registerCalls := 0
	vaultStates := xmrFrostVaultStatesResponse{
		Vaults: []xmrFrostVaultStateEntry{{
			VaultPubKey:          vaultPubKey.String(),
			Address:              vaultAddr.String(),
			Birthday:             25,
			InventoryFingerprint: "stale-fingerprint",
		}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			mu.Lock()
			defer mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(vaultStates))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			mu.Lock()
			registerCalls++
			vaultStates.Vaults[0].InventoryFingerprint = expectedFingerprint
			mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	client := &Client{
		chain:                 common.XMRChain,
		frostSigner:           signer,
		logger:                testLogger(),
		trackedVaultSyncState: map[string]string{vaultPubKey.String(): expectedFingerprint},
	}

	_, err = client.syncTrackedVaultRegistrations(ttypes.Vaults{vault})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, registerCalls)
	assert.Equal(t, expectedFingerprint, vaultStates.Vaults[0].InventoryFingerprint)
}

func TestSyncTrackedVaultRegistrationsReregistersWhenLocalKeyImageApplyLagsChainInventory(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)
	keyImage := strings.Repeat("b", 64)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_RetiringVault,
		KeyImagesXmr: []*common.XmrKeyImageRef{
			{TxHash: txHash, OutputIndex: 1, KeyImage: keyImage},
		},
	}
	expectedFingerprint := xmrVaultInventoryFingerprint(vault)

	var mu sync.Mutex
	registerCalls := 0
	vaultStates := xmrFrostVaultStatesResponse{
		Vaults: []xmrFrostVaultStateEntry{{
			VaultPubKey:             vaultPubKey.String(),
			Address:                 vaultAddr.String(),
			InventoryFingerprint:    expectedFingerprint,
			RegisteredKeyImageCount: 1,
		}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			mu.Lock()
			defer mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(vaultStates))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			assert.Equal(t, "all", r.URL.Query().Get("view"))
			assert.Equal(t, "store", r.URL.Query().Get("source"))
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{{
					TxHash:      txHash,
					OutputIndex: 1,
					Amount:      1,
					Height:      100,
					State:       "unspent",
				}},
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			mu.Lock()
			registerCalls++
			mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	client := &Client{
		chain:                 common.XMRChain,
		frostSigner:           signer,
		logger:                testLogger(),
		trackedVaultSyncState: map[string]string{vaultPubKey.String(): expectedFingerprint},
	}

	_, err = client.syncTrackedVaultRegistrations(ttypes.Vaults{vault})
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, registerCalls)
}

func TestSyncTrackedVaultRegistrationsReregistersWhenLocalTxKeyApplyLagsChainInventory(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)
	parentTxHash := strings.Repeat("b", 64)
	txKey := strings.Repeat("1", 64)
	destination := testOutboundXMRDestination()

	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Status:      ttypes.VaultStatus_RetiringVault,
		TxKeysXmr: []*common.XmrTxKeyRef{
			{TxHash: txHash, TxKeys: []string{txKey}},
		},
	}
	expectedFingerprint := xmrVaultInventoryFingerprint(vault)

	bridge := &countingStateSyncBridge{
		mockMoneroBridge: mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				vaultPubKey.String(): vault,
			},
			observedTxVoterByID: map[string]*ttypes.QueryObservedTxVoter{
				txHash: {
					TxID: common.TxID(txHash),
					Tx: ttypes.QueryObservedTx{
						Tx: common.NewTx(
							common.TxID(txHash),
							common.Address(common.XMRUnknownSenderAddress(txHash, vaultAddr.String())),
							vaultAddr,
							common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
							nil,
							"OUT:"+parentTxHash,
						),
						ObservedPubKey: vaultPubKey,
						BlockHeight:    100,
					},
				},
			},
			txStatusByID: map[string]*ttypes.QueryTxStatusResponse{
				parentTxHash: {
					OutTxs: common.Txs{
						common.NewTx(
							common.TxID(txHash),
							vaultAddr,
							destination,
							common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
							nil,
							"OUT:"+parentTxHash,
						),
					},
				},
			},
		},
	}

	var mu sync.Mutex
	registerCalls := 0
	vaultStates := xmrFrostVaultStatesResponse{
		Vaults: []xmrFrostVaultStateEntry{{
			VaultPubKey:          vaultPubKey.String(),
			Address:              vaultAddr.String(),
			InventoryFingerprint: expectedFingerprint,
			RegisteredTxKeyCount: 1,
		}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			mu.Lock()
			defer mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(vaultStates))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+vaultPubKey.String():
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:              txHash,
					RegisteredInventory: true,
					Status:              "observed_mined",
				}},
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			mu.Lock()
			registerCalls++
			mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	client := &Client{
		chain:                 common.XMRChain,
		frostSigner:           signer,
		logger:                testLogger(),
		thorchainBridge:       bridge,
		trackedVaultSyncState: map[string]string{vaultPubKey.String(): expectedFingerprint},
	}

	_, err = client.syncTrackedVaultRegistrations(ttypes.Vaults{vault})
	require.NoError(t, err)

	mu.Lock()
	assert.Equal(t, 1, registerCalls)
	mu.Unlock()

	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	assert.Equal(t, 1, bridge.observedTxCalls)
	assert.Equal(t, 1, bridge.txStatusCalls)
}

func TestDrivePendingScannerRescansUsesScannerSyncPath(t *testing.T) {
	t.Parallel()

	vaultPubKey, _ := testVaultPubKeyAndAddress(t)
	startHeight := uint64(25)
	tipHeight := int64(120)

	var mu sync.Mutex
	vaultStates := xmrFrostVaultStatesResponse{
		Vaults: []xmrFrostVaultStateEntry{{
			VaultPubKey: vaultPubKey.String(),
			Birthday:    startHeight,
			Rescan:      true,
			ScanHeight:  &startHeight,
		}},
	}
	var syncRequests []xmrFrostScannerSyncRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			mu.Lock()
			defer mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(vaultStates))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/height":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostChainHeightResponse{Height: tipHeight}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/block/120":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostChainBlockResponse{
				Height:       tipHeight,
				Hash:         "block-hash-120",
				PreviousHash: "block-hash-119",
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			var req xmrFrostScannerSyncRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			syncRequests = append(syncRequests, req)
			completed := uint64(tipHeight)
			vaultStates.Vaults[0].Rescan = false
			vaultStates.Vaults[0].ScanHeight = &completed
			mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostTransfersResponse{}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	client := &Client{
		frostSigner: signer,
		logger:      testLogger(),
		scanner:     &Scanner{maxReorgRescan: 72},
	}

	updated, err := client.drivePendingScannerRescans(vaultStates.Vaults)
	require.NoError(t, err)
	require.Len(t, updated, 1)
	assert.False(t, updated[0].Rescan)
	require.NotNil(t, updated[0].ScanHeight)
	assert.Equal(t, uint64(tipHeight), *updated[0].ScanHeight)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, syncRequests, 1)
	assert.Equal(t, tipHeight, syncRequests[0].Height)
	assert.Equal(t, tipHeight, syncRequests[0].TipHeight)
	assert.Equal(t, "block-hash-120", syncRequests[0].BlockHash)
	assert.Equal(t, "block-hash-119", syncRequests[0].ParentHash)
	assert.Equal(t, int64(72), syncRequests[0].ReorgWindow)
}

func TestDrivePendingScannerRescansQueuesRecoveredUnauthorizedSpend(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vaultAddress := vaultAddr.String()
	startHeight := uint64(25)
	tipHeight := int64(120)
	spendTxID := strings.Repeat("a", 64)

	var mu sync.Mutex
	vaultStates := xmrFrostVaultStatesResponse{
		Vaults: []xmrFrostVaultStateEntry{{
			VaultPubKey: vaultPubKey.String(),
			Address:     vaultAddress,
			Birthday:    startHeight,
			Rescan:      true,
			ScanHeight:  &startHeight,
		}},
	}
	var syncRequests []xmrFrostScannerSyncRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			mu.Lock()
			defer mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(vaultStates))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/height":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostChainHeightResponse{Height: tipHeight}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/block/120":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostChainBlockResponse{
				Height:       tipHeight,
				Hash:         "block-hash-120",
				PreviousHash: "block-hash-119",
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/block/121":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostChainBlockResponse{
				Height:       121,
				Hash:         "block-hash-121",
				PreviousHash: "block-hash-120",
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			var req xmrFrostScannerSyncRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			syncRequests = append(syncRequests, req)
			if req.Height == tipHeight {
				completed := uint64(tipHeight)
				vaultStates.Vaults[0].Rescan = false
				vaultStates.Vaults[0].ScanHeight = &completed
				mu.Unlock()
				require.NoError(t, json.NewEncoder(w).Encode(xmrFrostTransfersResponse{
					Outgoing: []xmrFrostTransferEntry{{
						TxID:        spendTxID,
						Address:     common.XMRUnknownSenderAddress(spendTxID, vaultAddress),
						FromAddress: vaultAddress,
						VaultPubKey: vaultPubKey.String(),
						Amount:      uint64(2 * moneroAtomicToThorchain),
						Height:      uint64(tipHeight),
						Note:        "NOOP:UNAUTHORIZED",
					}},
				}))
				return
			}
			mu.Unlock()
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostTransfersResponse{}))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	scanner := NewScanner(
		testLogger(),
		signer,
		&scannerMemoBridge{asgards: ttypes.Vaults{{PubKeyEddsa: vaultPubKey, PubKey: vaultPubKey, PubKeyFrost: vaultPubKey}}},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	client := &Client{
		frostSigner: signer,
		logger:      testLogger(),
		scanner:     scanner,
	}

	updated, err := client.drivePendingScannerRescans(vaultStates.Vaults)
	require.NoError(t, err)
	require.Len(t, updated, 1)
	assert.False(t, updated[0].Rescan)

	txIn, err := scanner.FetchTxs(121, 121)
	require.NoError(t, err)
	require.Len(t, txIn.TxArray, 1)
	assert.Equal(t, spendTxID, txIn.TxArray[0].Tx)
	assert.Equal(t, "NOOP:UNAUTHORIZED", txIn.TxArray[0].Memo)
	assert.Equal(t, vaultAddress, txIn.TxArray[0].Sender)
	assert.True(t, txIn.TxArray[0].ObservedVaultPubKey.Equals(vaultPubKey))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, syncRequests, 2)
	assert.Equal(t, tipHeight, syncRequests[0].Height)
	assert.Equal(t, int64(121), syncRequests[1].Height)
}
