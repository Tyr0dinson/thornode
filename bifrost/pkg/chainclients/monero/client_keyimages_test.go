package monero

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/blang/semver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	bifrosttss "gitlab.com/thorchain/thornode/v3/bifrost/tss"
	gotssblame "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	gotsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type keyImageTestBridge struct {
	mockMoneroBridge
	version semver.Version
}

func (b *keyImageTestBridge) GetThorchainVersion() (semver.Version, error) {
	if b.version.Equals(semver.Version{}) {
		return semver.MustParse("3.0.0"), nil
	}
	return b.version, nil
}

type mockKeyImageTssServer struct {
	tssp.Server
	mu   sync.Mutex
	reqs []tssp.FrostKeyImageRequest
}

func (m *mockKeyImageTssServer) FrostKeyImage(req tssp.FrostKeyImageRequest, _ tssp.FrostSidecarClient) (tssp.FrostKeyImageResponse, error) {
	m.mu.Lock()
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()

	return tssp.FrostKeyImageResponse{
		MsgID:       "msg-" + req.TxHash,
		TxHash:      req.TxHash,
		OutputIndex: req.OutputIndex,
		KeyImage:    strings.ToLower(req.TxHash) + "-ki-" + string(rune('0'+req.OutputIndex)),
		ProofC:      "proof-c-" + string(rune('0'+req.OutputIndex)),
		ProofS:      "proof-s-" + string(rune('0'+req.OutputIndex)),
		Status:      gotsscommon.Success,
	}, nil
}

type mockKeyImageErrorTssServer struct {
	tssp.Server
	err error
}

func (m *mockKeyImageErrorTssServer) FrostKeyImage(_ tssp.FrostKeyImageRequest, _ tssp.FrostSidecarClient) (tssp.FrostKeyImageResponse, error) {
	return tssp.FrostKeyImageResponse{}, m.err
}

type mockKeyImageFailTssServer struct {
	tssp.Server
	failReason string
	calls      int
}

func (m *mockKeyImageFailTssServer) FrostKeyImage(req tssp.FrostKeyImageRequest, _ tssp.FrostSidecarClient) (tssp.FrostKeyImageResponse, error) {
	m.calls++
	return tssp.FrostKeyImageResponse{
		MsgID:       "msg-" + req.TxHash,
		TxHash:      req.TxHash,
		OutputIndex: req.OutputIndex,
		Status:      gotsscommon.Fail,
		Blame: gotssblame.Blame{
			FailReason: m.failReason,
		},
	}, nil
}

type mockKeyImagePeerReadinessTssServer struct {
	mockKeyImageTssServer
	missing []string
}

func (m *mockKeyImagePeerReadinessTssServer) MissingPeerAddresses(_ []string) ([]string, error) {
	return append([]string(nil), m.missing...), nil
}

type rotatingKeyImageTssServer struct {
	tssp.Server
	mu   sync.Mutex
	reqs []tssp.FrostKeyImageRequest
}

func (m *rotatingKeyImageTssServer) FrostKeyImage(req tssp.FrostKeyImageRequest, _ tssp.FrostSidecarClient) (tssp.FrostKeyImageResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reqs = append(m.reqs, req)
	if len(m.reqs) == 1 {
		return tssp.FrostKeyImageResponse{
			MsgID:       "msg-" + req.TxHash,
			TxHash:      req.TxHash,
			OutputIndex: req.OutputIndex,
			Status:      gotsscommon.Fail,
			Blame: gotssblame.Blame{
				FailReason: "leader not reachable",
			},
		}, nil
	}
	return tssp.FrostKeyImageResponse{
		MsgID:       "msg-" + req.TxHash,
		TxHash:      req.TxHash,
		OutputIndex: req.OutputIndex,
		KeyImage:    strings.ToLower(req.TxHash) + "-ki-" + string(rune('0'+req.OutputIndex)),
		ProofC:      "proof-c-" + string(rune('0'+req.OutputIndex)),
		ProofS:      "proof-s-" + string(rune('0'+req.OutputIndex)),
		Status:      gotsscommon.Success,
	}, nil
}

func newTestXMRKeyImageWorkStateStore(t *testing.T) *xmrKeyImageWorkStateStore {
	t.Helper()

	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = db.Close()
	})

	store, err := newXMRKeyImageWorkStateStore(db)
	require.NoError(t, err)
	return store
}

func TestPrepareObservedTxInItemGeneratesKeyImagesForFinalInbound(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outbounds/validate":
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true, Marked: 0})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			require.Equal(t, "all", r.URL.Query().Get("view"))
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100},
					{TxHash: txHash, OutputIndex: 1, Amount: 20, Height: 100},
				},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	bridge := &keyImageTestBridge{
		mockMoneroBridge: mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				vaultPubKey.String(): {
					PubKey:      vaultPubKey,
					PubKeyEddsa: vaultPubKey,
					PubKeyFrost: vaultPubKey,
					Membership:  []string{"member-a", "member-b", "member-c"},
				},
			},
		},
		version: semver.MustParse("3.0.0"),
	}

	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: bridge,
		logger:          testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  vaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.True(t, include)
	require.NotNil(t, prepared)
	require.Len(t, prepared.XmrKeyImages, 2)
	assert.Equal(t, txHash, prepared.XmrKeyImages[0].TxHash)
	assert.Equal(t, uint32(0), prepared.XmrKeyImages[0].OutputIndex)
	assert.Equal(t, uint32(1), prepared.XmrKeyImages[1].OutputIndex)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 2)
	assert.Equal(t, int64(1), tssServer.reqs[0].BlockHeight)
	assert.Equal(t, txHash, tssServer.reqs[0].TxHash)
}

func TestPrepareObservedTxInItemSkipsFinalInboundUntilTrackedOutputsExist(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("e", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: []xmrFrostOutputEntry{}})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  vaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.False(t, include)
	require.Nil(t, prepared)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 0)
}

func TestPrepareObservedTxInItemReusesCachedKeyImagesForDuplicateFinalInbound(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("f", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
			Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
		})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	newItem := func() *stypes.TxInItem {
		return &stypes.TxInItem{
			BlockHeight:         100,
			Tx:                  txHash,
			Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
			To:                  vaultAddr.String(),
			Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			ObservedVaultPubKey: vaultPubKey,
			Memo:                "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
		}
	}

	firstPrepared, include, err := c.PrepareObservedTxInItem(newItem(), true, 110)
	require.NoError(t, err)
	require.True(t, include)
	require.Len(t, firstPrepared.XmrKeyImages, 1)

	secondPrepared, include, err := c.PrepareObservedTxInItem(newItem(), true, 110)
	require.NoError(t, err)
	require.True(t, include)
	require.Len(t, secondPrepared.XmrKeyImages, 1)
	assert.Equal(t, firstPrepared.XmrKeyImages[0].KeyImage, secondPrepared.XmrKeyImages[0].KeyImage)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 1)
}

func TestDriveLocalKeyImageBacklogCachesProofsForLaterObservation(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	oldTxHash := strings.Repeat("o", 64)
	newTxHash := strings.Repeat("n", 64)

	var mu sync.Mutex
	outputs := []xmrFrostOutputEntry{
		{TxHash: oldTxHash, OutputIndex: 0, Amount: 10, Height: 100},
		{TxHash: newTxHash, OutputIndex: 0, Amount: 20, Height: 101},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.False(t, req.Reconcile)

			mu.Lock()
			for i := range outputs {
				for _, ref := range req.KeyImages {
					if outputs[i].TxHash == ref.TxHash && outputs[i].OutputIndex == ref.OutputIndex {
						outputs[i].KeyImage = ref.KeyImage
					}
				}
			}
			mu.Unlock()

			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			require.Equal(t, "all", r.URL.Query().Get("view"))
			mu.Lock()
			resp := append([]xmrFrostOutputEntry(nil), outputs...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: resp})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Membership:  []string{"member-a", "member-b", "member-c"},
	}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): vault,
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	c.driveLocalKeyImageBacklogForVaults(ttypes.Vaults{vault})

	tssServer.mu.Lock()
	require.Len(t, tssServer.reqs, 1)
	assert.Equal(t, oldTxHash, tssServer.reqs[0].TxHash)
	tssServer.mu.Unlock()

	mu.Lock()
	oldOutputs := filterVaultOutputsForTx(append([]xmrFrostOutputEntry(nil), outputs...), oldTxHash)
	require.NotEmpty(t, oldOutputs)
	require.NotEmpty(t, oldOutputs[0].KeyImage)
	mu.Unlock()

	cachedProofs, ok := c.getCachedXMRKeyImageProofs(vaultPubKey, oldTxHash, oldOutputs)
	require.True(t, ok)
	require.Len(t, cachedProofs, 1)

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  oldTxHash,
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  vaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.True(t, include)
	require.NotNil(t, prepared)
	require.Len(t, prepared.XmrKeyImages, 1)
	assert.Equal(t, oldTxHash, prepared.XmrKeyImages[0].TxHash)

	tssServer.mu.Lock()
	require.Len(t, tssServer.reqs, 1)
	tssServer.mu.Unlock()

	c.driveLocalKeyImageBacklogForVaults(ttypes.Vaults{vault})

	tssServer.mu.Lock()
	require.Len(t, tssServer.reqs, 2)
	assert.Equal(t, newTxHash, tssServer.reqs[1].TxHash)
	tssServer.mu.Unlock()
}

func TestPrepareObservedTxInItemTreatsLeaderNotReachableAsRetryLater(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("g", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
			Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
		})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageFailTssServer{failReason: "leader not reachable"}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  vaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.False(t, include)
	require.Nil(t, prepared)
	assert.Equal(t, 1, tssServer.calls)
}

func TestPrepareObservedTxInItemTreatsNotActiveSignerAsWaitForPeerProof(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("h", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
			Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
		})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageFailTssServer{failReason: "local signer is not in active signer set"}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  vaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.False(t, include)
	require.Nil(t, prepared)
	assert.Equal(t, 1, tssServer.calls)
}

func TestPrepareObservedTxInItemRotatesKeyImageLeaderEpochAfterFailure(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("r", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &rotatingKeyImageTssServer{}
	bridge := &keyImageTestBridge{
		mockMoneroBridge: mockMoneroBridge{
			blockHeight: 250,
			constants: map[string]int64{
				constants.SigningTransactionPeriod.String(): 40,
			},
			vaultByPubKey: map[string]ttypes.Vault{
				vaultPubKey.String(): {
					PubKey:      vaultPubKey,
					PubKeyEddsa: vaultPubKey,
					PubKeyFrost: vaultPubKey,
					Membership:  []string{"member-a", "member-b", "member-c"},
				},
			},
		},
		version: semver.MustParse("3.0.0"),
	}
	c := &Client{
		chain:             common.XMRChain,
		localNodePubKey:   common.PubKey("member-a"),
		frostSigner:       signer,
		tssServer:         tssServer,
		keyImageWorkStore: newTestXMRKeyImageWorkStateStore(t),
		thorchainBridge:   bridge,
		logger:            testLogger(),
	}

	newItem := func() *stypes.TxInItem {
		return &stypes.TxInItem{
			BlockHeight:         100,
			Tx:                  txHash,
			Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
			To:                  vaultAddr.String(),
			Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			ObservedVaultPubKey: vaultPubKey,
			Memo:                "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
		}
	}

	prepared, include, err := c.PrepareObservedTxInItem(newItem(), true, 250)
	require.NoError(t, err)
	require.False(t, include)
	require.Nil(t, prepared)

	bridge.blockHeight = 290
	prepared, include, err = c.PrepareObservedTxInItem(newItem(), true, 250)
	require.NoError(t, err)
	require.True(t, include)
	require.NotNil(t, prepared)
	require.Len(t, prepared.XmrKeyImages, 1)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 2)
	assert.Equal(t, int64(241), tssServer.reqs[0].BlockHeight)
	assert.Equal(t, int64(281), tssServer.reqs[1].BlockHeight)
}

func TestPrepareObservedTxInItemDefersTrackedOutboundUntilChangeOutputsMaterialize(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("z", 64)
	destination := common.XMRSeededDummyAddress('8', []byte("tracked-change-destination"))
	trackedOwnedChange := true
	localChangeMaterialized := false
	validateCalls := 0
	outputCalls := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outbounds/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:                  txHash,
					Status:                  xmrFrostOutgoingStatusObservedMined,
					TxKeys:                  []string{strings.Repeat("1", 64)},
					HasOwnedChange:          &trackedOwnedChange,
					LocalChangeMaterialized: &localChangeMaterialized,
					PendingReason:           "awaiting_change_materialization",
					Amount:                  10 * moneroAtomicToThorchain,
					Fee:                     1 * moneroAtomicToThorchain,
					Memo:                    "OUT:ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
					CreatedAt:               1,
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outbounds/validate":
			validateCalls++
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true, Marked: 0})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			outputCalls++
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: nil})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              vaultAddr.String(),
		To:                  destination,
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "OUT:ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ",
		TxKeys:              []string{strings.Repeat("1", 64)},
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.False(t, include)
	require.Nil(t, prepared)
	require.Zero(t, validateCalls)
	require.Zero(t, outputCalls)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 0)
}

func TestPrepareObservedTxInItemWaitsForPeerDiscoveryBeforeKeyImageCeremony(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("p", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
			Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
		})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImagePeerReadinessTssServer{
		missing: []string{"member-b", "member-c"},
	}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  vaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.False(t, include)
	require.Nil(t, prepared)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 0)
}

func TestPrepareObservedTxInItemWaitsForEarlierVaultKeyImageBacklog(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	currentTxHash := strings.Repeat("q", 64)
	earlierTxHash := strings.Repeat("p", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		default:
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: earlierTxHash, OutputIndex: 0, Amount: 10, Height: 100},
					{TxHash: currentTxHash, OutputIndex: 0, Amount: 20, Height: 101},
				},
			})
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         101,
		Tx:                  currentTxHash,
		Sender:              vaultAddr.String(),
		To:                  common.XMRSeededDummyAddress('4', []byte("dest")),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(20))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "OUT:" + strings.Repeat("1", 64),
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 111)
	require.NoError(t, err)
	require.False(t, include)
	require.Nil(t, prepared)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 0)
}

func TestPrepareObservedTxInItemIgnoresResolvedEarlierVaultKeyImageBacklog(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	currentTxHash := strings.Repeat("s", 64)
	earlierTxHash := strings.Repeat("r", 64)
	destination := common.XMRSeededDummyAddress('4', []byte("dest"))
	memo := "OUT:" + strings.Repeat("2", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackedOwnedChange := true
		localChangeMaterialized := true
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:                  currentTxHash,
					HasOwnedChange:          &trackedOwnedChange,
					LocalChangeMaterialized: &localChangeMaterialized,
					FromAddress:             vaultAddr.String(),
					ToAddress:               destination,
					Amount:                  20,
					Fee:                     1,
					Memo:                    memo,
					PlannedHeight:           101,
					Status:                  "observed_mined",
					TxKeys:                  []string{strings.Repeat("1", 64)},
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		default:
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: earlierTxHash, OutputIndex: 0, Amount: 10, Height: 100, KeyImage: "ki-earlier"},
					{TxHash: currentTxHash, OutputIndex: 0, Amount: 20, Height: 101},
				},
			})
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         101,
		Tx:                  currentTxHash,
		Sender:              vaultAddr.String(),
		To:                  destination,
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(20))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                memo,
		TxKeys:              []string{strings.Repeat("1", 64)},
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 111)
	require.NoError(t, err)
	require.True(t, include)
	require.NotNil(t, prepared)
	require.Len(t, prepared.XmrKeyImages, 1)
	assert.Equal(t, currentTxHash, prepared.XmrKeyImages[0].TxHash)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 1)
	assert.Equal(t, currentTxHash, tssServer.reqs[0].TxHash)
}

func TestResolveXMRKeyImageCeremonyContextCanonicalizesVaultPubKey(t *testing.T) {
	t.Parallel()

	observedVaultPubKey := common.PubKey("cthorpub1observedvault")
	canonicalVaultPubKey := common.PubKey("cthorpub1canonicalvault")

	c := &Client{
		chain: common.XMRChain,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					observedVaultPubKey.String(): {
						PubKey:      observedVaultPubKey,
						PubKeyEddsa: observedVaultPubKey,
						PubKeyFrost: canonicalVaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
		},
		logger: testLogger(),
	}

	ctx, err := c.resolveXMRKeyImageCeremonyContext(observedVaultPubKey)
	require.NoError(t, err)
	assert.Equal(t, canonicalVaultPubKey, ctx.vaultPubKey)
	assert.Equal(t, []string{"member-a", "member-b", "member-c"}, ctx.members)
}

func TestPrepareObservedTxInItemGeneratesKeyImagesForFinalMigrateInbound(t *testing.T) {
	t.Parallel()

	sourceVaultPubKey, sourceVaultAddr := testVaultPubKeyAndAddress(t)
	destVaultPubKey, destVaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("m", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: txHash, OutputIndex: 0, Amount: 25, Height: 100},
				},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	bridge := &keyImageTestBridge{
		mockMoneroBridge: mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				sourceVaultPubKey.String(): {
					PubKey:      sourceVaultPubKey,
					PubKeyEddsa: sourceVaultPubKey,
					PubKeyFrost: sourceVaultPubKey,
					Membership:  []string{"source-a", "source-b", "source-c"},
				},
				destVaultPubKey.String(): {
					PubKey:      destVaultPubKey,
					PubKeyEddsa: destVaultPubKey,
					PubKeyFrost: destVaultPubKey,
					Membership:  []string{"dest-a", "dest-b", "dest-c"},
				},
			},
		},
		version: semver.MustParse("3.0.0"),
	}

	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("dest-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: bridge,
		logger:          testLogger(),
		vaultAddressMap: map[string]common.Address{
			sourceVaultPubKey.String(): sourceVaultAddr,
			destVaultPubKey.String():   destVaultAddr,
		},
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              sourceVaultAddr.String(),
		To:                  destVaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(25))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: destVaultPubKey,
		Memo:                "MIGRATE:123",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.True(t, include)
	require.NotNil(t, prepared)
	require.Len(t, prepared.XmrKeyImages, 1)
	assert.Equal(t, txHash, prepared.XmrKeyImages[0].TxHash)
	assert.Equal(t, uint32(0), prepared.XmrKeyImages[0].OutputIndex)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 1)
	assert.Equal(t, destVaultPubKey.String(), tssServer.reqs[0].VaultPubKey)
	assert.Equal(t, int64(1), tssServer.reqs[0].BlockHeight)
}

func TestPrepareObservedTxInItemSkipsFinalInboundForNonSigner(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("b", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
			Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
		})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-z"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  vaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	assert.False(t, include)
	assert.Nil(t, prepared)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	assert.Empty(t, tssServer.reqs)
}

func TestPrepareObservedTxInItemSkipsOutboundMemoChangeObservation(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("e", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
			Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 1, Amount: 10, Height: 100}},
		})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  vaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "OUT:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	assert.False(t, include)
	assert.Nil(t, prepared)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	assert.Empty(t, tssServer.reqs)
}

func TestPrepareObservedTxInItemSkipsRetiringMigrateChangeObservation(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("m", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
			Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 1, Amount: 10, Height: 100}},
		})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Status:      ttypes.VaultStatus_RetiringVault,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  vaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "MIGRATE:123",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	assert.False(t, include)
	assert.Nil(t, prepared)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	assert.Empty(t, tssServer.reqs)
}

func TestPrepareObservedTxInItemGeneratesKeyImagesForFinalOutboundChange(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("g", 64)
	destination := common.XMRSeededDummyAddress('8', []byte("external-destination"))
	memo := "OUT:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
	var applyReqs []xmrFrostApplyKeyImagesRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackedOwnedChange := true
		localChangeMaterialized := true
		switch r.URL.Path {
		case "/v1/monero/outbounds/" + url.PathEscape(vaultPubKey.String()):
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:                  txHash,
					HasOwnedChange:          &trackedOwnedChange,
					LocalChangeMaterialized: &localChangeMaterialized,
					FromAddress:             vaultAddr.String(),
					ToAddress:               destination,
					Amount:                  10,
					Fee:                     1,
					Memo:                    memo,
					PlannedHeight:           100,
					Status:                  "observed_mined",
					TxKeys:                  []string{strings.Repeat("1", 64)},
				}},
			})
		case "/v1/monero/outbounds/validate":
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true, ActualFee: 1 * moneroAtomicToThorchain})
		case "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			applyReqs = append(applyReqs, req)
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		default:
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
			})
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger:          testLogger(),
		vaultAddressMap: map[string]common.Address{vaultPubKey.String(): vaultAddr},
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              vaultAddr.String(),
		To:                  destination,
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                memo,
		TxKeys:              []string{strings.Repeat("1", 64)},
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.True(t, include)
	require.NotNil(t, prepared)
	require.Len(t, prepared.XmrKeyImages, 1)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 1)
	assert.Equal(t, txHash, tssServer.reqs[0].TxHash)
	require.Len(t, applyReqs, 1)
	require.Len(t, applyReqs[0].KeyImages, 1)
	assert.Equal(t, txHash, applyReqs[0].KeyImages[0].TxHash)
	assert.Equal(t, uint32(0), applyReqs[0].KeyImages[0].OutputIndex)
	assert.Equal(t, strings.ToLower(txHash)+"-ki-0", applyReqs[0].KeyImages[0].KeyImage)
	assert.False(t, applyReqs[0].Reconcile)
}

func TestPrepareObservedTxInItemKeepsFinalOutboundWithoutTrackedChange(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("i", 64)
	destination := common.XMRSeededDummyAddress('8', []byte("external-without-change"))
	memo := "OUT:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noOwnedChange := false
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:              txHash,
					HasOwnedChange:      &noOwnedChange,
					FromAddress:         vaultAddr.String(),
					ToAddress:           destination,
					Amount:              10,
					Fee:                 1,
					Memo:                memo,
					PlannedHeight:       100,
					Status:              "observed_mined",
					TxKeys:              []string{strings.Repeat("1", 64)},
					RegisteredInventory: true,
				}},
			})
		case r.URL.Path == "/v1/monero/outbounds/validate":
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: false, ActualFee: 7 * moneroAtomicToThorchain})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: nil})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              vaultAddr.String(),
		To:                  destination,
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                memo,
		TxKeys:              []string{strings.Repeat("1", 64)},
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.True(t, include)
	require.NotNil(t, prepared)
	require.Empty(t, prepared.XmrKeyImages)
	require.Equal(t, uint64(7), prepared.Gas.ToCoins().GetCoin(common.XMRAsset).Amount.Uint64())

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	assert.Empty(t, tssServer.reqs)
}

func TestPrepareObservedTxInItemRecoversFinalOutboundTxKeysFromLocalState(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("w", 64)
	txKey := strings.Repeat("4", 64)
	destination := common.XMRSeededDummyAddress('8', []byte("recover-missing-txkeys-destination"))

	var applyReqs []xmrFrostApplyKeyImagesRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackedOwnedChange := true
		localChangeMaterialized := true
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:                  txHash,
					TxKeys:                  []string{txKey},
					RegisteredInventory:     false,
					HasOwnedChange:          &trackedOwnedChange,
					LocalChangeMaterialized: &localChangeMaterialized,
					FromAddress:             vaultAddr.String(),
					ToAddress:               destination,
					Amount:                  10,
					Status:                  "observed_mined",
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outbounds/validate":
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true, ActualFee: 3 * moneroAtomicToThorchain})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			applyReqs = append(applyReqs, req)
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger:          testLogger(),
		vaultAddressMap: map[string]common.Address{vaultPubKey.String(): vaultAddr},
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              vaultAddr.String(),
		To:                  destination,
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "OUT:WWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWWW",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.True(t, include)
	require.NotNil(t, prepared)
	require.Equal(t, []string{txKey}, prepared.TxKeys)
	require.Equal(t, uint64(3), prepared.Gas.ToCoins().GetCoin(common.XMRAsset).Amount.Uint64())
	require.Len(t, prepared.XmrKeyImages, 1)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	require.Len(t, tssServer.reqs, 1)
	assert.Equal(t, txHash, tssServer.reqs[0].TxHash)
	require.Len(t, applyReqs, 1)
	assert.Equal(t, txHash, applyReqs[0].KeyImages[0].TxHash)
}

func TestPrepareObservedTxInItemDefersFinalOutboundUntilTxKeysPropagate(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("y", 64)
	destination := common.XMRSeededDummyAddress('8', []byte("missing-txkeys-destination"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: nil})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageTssServer{}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b", "member-c"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              vaultAddr.String(),
		To:                  destination,
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
		Memo:                "OUT:YYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYY",
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 0, KeyImage: "ki-0", ProofC: "c0", ProofS: "s0"},
		},
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.False(t, include)
	require.Nil(t, prepared)

	tssServer.mu.Lock()
	defer tssServer.mu.Unlock()
	assert.Empty(t, tssServer.reqs)
}

func TestValidateObservedTxInAppliesValidatedKeyImages(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("c", 64)

	var mu sync.Mutex
	var validateCalls int
	var applied []xmrFrostApplyKeyImageRef

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.False(t, req.Reconcile)
			mu.Lock()
			applied = append([]xmrFrostApplyKeyImageRef(nil), req.KeyImages...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			require.Equal(t, "all", r.URL.Query().Get("view"))
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100},
					{TxHash: txHash, OutputIndex: 1, Amount: 20, Height: 100},
				},
			})
		case r.URL.Path == "/v1/monero/key-image/validate":
			validateCalls++
			_ = json.NewEncoder(w).Encode(xmrFrostKeyImageValidateResponse{Valid: true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			vaultAddr,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 1, KeyImage: "ki-1", ProofC: "c1", ProofS: "s1"},
			{TxHash: txHash, OutputIndex: 0, KeyImage: "ki-0", ProofC: "c0", ProofS: "s0"},
		},
	}

	require.NoError(t, c.ValidateObservedTxIn(obsTx))
	assert.Equal(t, 2, validateCalls)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, applied, 2)
	assert.Equal(t, uint32(0), applied[0].OutputIndex)
	assert.Equal(t, "ki-0", applied[0].KeyImage)
	assert.Equal(t, uint32(1), applied[1].OutputIndex)
	assert.Equal(t, "ki-1", applied[1].KeyImage)
}

func TestValidateObservedTxInRejectsForgedKeyImagesWithoutApplying(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("d", 64)

	var validateCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			require.Equal(t, "all", r.URL.Query().Get("view"))
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100},
				},
			})
		case r.URL.Path == "/v1/monero/key-image/validate":
			validateCalls++
			_ = json.NewEncoder(w).Encode(xmrFrostKeyImageValidateResponse{Valid: false})
		case r.URL.Path == "/v1/monero/outputs/apply-key-images":
			t.Fatal("apply-key-images should not be called when key-image validation fails")
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			vaultAddr,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 0, KeyImage: "ki-0", ProofC: "c0", ProofS: "s0"},
		},
	}

	err = c.ValidateObservedTxIn(obsTx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid")
	assert.Equal(t, 1, validateCalls)
}

func TestValidateObservedTxInAppliesValidatedKeyImagesForFinalMigrateInbound(t *testing.T) {
	t.Parallel()

	sourceVaultPubKey, sourceVaultAddr := testVaultPubKeyAndAddress(t)
	destVaultPubKey, destVaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("n", 64)

	var validateCalls []xmrFrostKeyImageValidateRequest
	var applyReq xmrFrostApplyKeyImagesRequest
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case r.URL.Path == "/v1/monero/outputs/apply-key-images":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&applyReq))
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: 1, NotFound: 0})
		case r.URL.Path == "/v1/monero/key-image/validate":
			var req xmrFrostKeyImageValidateRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			validateCalls = append(validateCalls, req)
			_ = json.NewEncoder(w).Encode(xmrFrostKeyImageValidateResponse{Valid: true})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 25, Height: 100}},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
		vaultAddressMap: map[string]common.Address{
			sourceVaultPubKey.String(): sourceVaultAddr,
			destVaultPubKey.String():   destVaultAddr,
		},
	}

	obsTx := common.NewObservedTx(
		common.NewTx(
			common.TxID(txHash),
			sourceVaultAddr,
			destVaultAddr,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(25))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"MIGRATE:123",
		),
		110,
		destVaultPubKey,
		110,
	)
	obsTx.XmrKeyImages = []*common.XmrKeyImageProof{
		{TxHash: txHash, OutputIndex: 0, KeyImage: "ki-0", ProofC: "c-0", ProofS: "s-0"},
	}

	require.True(t, obsTx.Tx.Chain.Equals(common.XMRChain))
	require.True(t, obsTx.IsFinal())
	require.True(t, c.isObservedTxInboundToVault(obsTx))
	require.True(t, c.isKnownVaultAddress(sourceVaultAddr.String()))
	outputs, err := c.collectVaultOutputsForTx(destVaultPubKey, txHash)
	require.NoError(t, err)
	require.Len(t, outputs, 1)

	require.NoError(t, c.ValidateObservedTxIn(obsTx))
	require.Contains(t, paths, "/v1/monero/key-image/validate")
	require.Len(t, validateCalls, 1)
	assert.Equal(t, destVaultPubKey.String(), validateCalls[0].VaultPubKey)
	assert.Equal(t, txHash, validateCalls[0].TxHash)
	assert.Len(t, applyReq.KeyImages, 1)
	assert.Equal(t, txHash, applyReq.KeyImages[0].TxHash)
	assert.Equal(t, uint32(0), applyReq.KeyImages[0].OutputIndex)
}

func TestValidateObservedTxInSkipsOutboundMemoChangeObservation(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("f", 64)

	validateCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/monero/key-image/validate":
			validateCalled = true
			_ = json.NewEncoder(w).Encode(xmrFrostKeyImageValidateResponse{Valid: true})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: txHash, OutputIndex: 1, Amount: 20, Height: 100},
				},
			})
		case r.URL.Path == "/v1/monero/outputs/apply-key-images":
			t.Fatal("apply-key-images should not be called for outbound memo change observation")
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			vaultAddr,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"OUT:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 1, KeyImage: "ki-1", ProofC: "c1", ProofS: "s1"},
		},
	}

	require.NoError(t, c.ValidateObservedTxIn(obsTx))
	assert.False(t, validateCalled)
}

func TestValidateObservedTxInSkipsConsolidateChangeObservation(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("n", 64)

	validateCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/monero/key-image/validate":
			validateCalled = true
			_ = json.NewEncoder(w).Encode(xmrFrostKeyImageValidateResponse{Valid: true})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: txHash, OutputIndex: 1, Amount: 20, Height: 100},
				},
			})
		case r.URL.Path == "/v1/monero/outputs/apply-key-images":
			t.Fatal("apply-key-images should not be called for consolidate change observation")
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			common.Address(common.XMRSeededDummyAddress('8', []byte("sender"))),
			vaultAddr,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"consolidate",
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 1, KeyImage: "ki-1", ProofC: "c1", ProofS: "s1"},
		},
	}

	require.NoError(t, c.ValidateObservedTxIn(obsTx))
	assert.False(t, validateCalled)
}

func TestValidateOutboundObservationAppliesValidatedKeyImagesForFinalOutboundChange(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("h", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("external-destination")))

	var mu sync.Mutex
	var outgoingStateCalls int
	var validateCalls int
	var applied []xmrFrostApplyKeyImageRef

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackedOwnedChange := true
		localChangeMaterialized := true
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			outgoingStateCalls++
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:                  txHash,
					HasOwnedChange:          &trackedOwnedChange,
					LocalChangeMaterialized: &localChangeMaterialized,
					FromAddress:             vaultAddr.String(),
					ToAddress:               destination.String(),
					Amount:                  10,
					Fee:                     1,
					Memo:                    "OUT:DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD",
					PlannedHeight:           110,
					Status:                  "observed_mined",
					TxKeys:                  []string{strings.Repeat("1", 64)},
				}},
			})
		case r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			applied = append([]xmrFrostApplyKeyImageRef(nil), req.KeyImages...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100},
				},
			})
		case r.URL.Path == "/v1/monero/key-image/validate":
			validateCalls++
			_ = json.NewEncoder(w).Encode(xmrFrostKeyImageValidateResponse{Valid: true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		vaultAddressMap: map[string]common.Address{vaultPubKey.String(): vaultAddr},
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			vaultAddr,
			destination,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"OUT:DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD",
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		TxKeys:         []string{strings.Repeat("1", 64)},
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 0, KeyImage: "ki-0", ProofC: "c0", ProofS: "s0"},
		},
	}

	require.NoError(t, c.ValidateOutboundObservation(obsTx))
	assert.Equal(t, 1, outgoingStateCalls)
	assert.Equal(t, 1, validateCalls)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, applied, 1)
	assert.Equal(t, uint32(0), applied[0].OutputIndex)
}

func TestValidateOutboundObservationSeedsMissingAuditBeforeFinalOutboundKeyImages(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("n", 64)
	txKey := strings.Repeat("1", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("external-destination-seed-audit")))

	var mu sync.Mutex
	var outgoingStateCalls int
	var validateOutboundCalls int
	var validateKeyImageCalls int
	var applied []xmrFrostApplyKeyImageRef
	var outboundReq xmrFrostValidateOutboundRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackedOwnedChange := true
		localChangeMaterialized := true
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			mu.Lock()
			outgoingStateCalls++
			call := outgoingStateCalls
			mu.Unlock()
			resp := xmrFrostOutgoingTransfersStateResponse{VaultPubKey: vaultPubKey.String()}
			if call > 1 {
				resp.Outgoing = []xmrFrostOutgoingTransferStateEntry{{
					TxHash:                  txHash,
					HasOwnedChange:          &trackedOwnedChange,
					LocalChangeMaterialized: &localChangeMaterialized,
					FromAddress:             vaultAddr.String(),
					ToAddress:               destination.String(),
					Amount:                  10 * moneroAtomicToThorchain,
					Fee:                     1 * moneroAtomicToThorchain,
					Memo:                    "OUT:" + strings.Repeat("N", 64),
					PlannedHeight:           110,
					Status:                  "observed_mined",
					TxKeys:                  []string{txKey},
				}}
			}
			_ = json.NewEncoder(w).Encode(resp)
		case r.URL.Path == "/v1/monero/outbounds/validate":
			mu.Lock()
			validateOutboundCalls++
			require.NoError(t, json.NewDecoder(r.Body).Decode(&outboundReq))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true})
		case r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			applied = append([]xmrFrostApplyKeyImageRef(nil), req.KeyImages...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
			})
		case r.URL.Path == "/v1/monero/key-image/validate":
			mu.Lock()
			validateKeyImageCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostKeyImageValidateResponse{Valid: true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		vaultAddressMap: map[string]common.Address{vaultPubKey.String(): vaultAddr},
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			vaultAddr,
			destination,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"OUT:"+strings.Repeat("N", 64),
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		TxKeys:         []string{txKey},
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 0, KeyImage: "ki-0", ProofC: "c0", ProofS: "s0"},
		},
	}

	require.NoError(t, c.ValidateOutboundObservation(obsTx))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 2, outgoingStateCalls)
	assert.Equal(t, 1, validateOutboundCalls)
	assert.Equal(t, 1, validateKeyImageCalls)
	require.Len(t, applied, 1)
	assert.Equal(t, vaultPubKey.String(), outboundReq.VaultPubKey)
	assert.Equal(t, txHash, outboundReq.TxHash)
	assert.Equal(t, []string{txKey}, outboundReq.TxKeys)
	assert.Equal(t, uint64(10*moneroAtomicToThorchain), outboundReq.Amount)
	assert.Equal(t, uint64(1*moneroAtomicToThorchain), outboundReq.Fee)
}

func TestValidateOutboundObservationRejectsMissingTxKeysEvenWithKeyImages(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("m", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("external-destination-no-txkey")))

	var mu sync.Mutex
	var outboundValidateCalls int
	var validateCalls int
	var applied []xmrFrostApplyKeyImageRef

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/monero/outbounds/validate":
			outboundValidateCalls++
			t.Fatal("outbound tx-key validation should not be attempted when tx keys are missing")
		case r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			applied = append([]xmrFrostApplyKeyImageRef(nil), req.KeyImages...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100},
				},
			})
		case r.URL.Path == "/v1/monero/key-image/validate":
			validateCalls++
			_ = json.NewEncoder(w).Encode(xmrFrostKeyImageValidateResponse{Valid: true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		vaultAddressMap: map[string]common.Address{vaultPubKey.String(): vaultAddr},
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			vaultAddr,
			destination,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"OUT:MMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMMM",
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 0, KeyImage: "ki-0", ProofC: "c0", ProofS: "s0"},
		},
	}

	err = c.ValidateOutboundObservation(obsTx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing tx keys")
	assert.Equal(t, 0, outboundValidateCalls)
	assert.Equal(t, 0, validateCalls)

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, applied)
}

func TestValidateOutboundObservationRejectsForgedKeyImagesWithoutApplying(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("i", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("external-destination")))

	var validateCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackedOwnedChange := true
		localChangeMaterialized := true
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:                  txHash,
					HasOwnedChange:          &trackedOwnedChange,
					LocalChangeMaterialized: &localChangeMaterialized,
					FromAddress:             vaultAddr.String(),
					ToAddress:               destination.String(),
					Amount:                  10,
					Fee:                     1,
					Memo:                    "OUT:EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE",
					PlannedHeight:           110,
					Status:                  "observed_mined",
					TxKeys:                  []string{strings.Repeat("1", 64)},
				}},
			})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100},
				},
			})
		case r.URL.Path == "/v1/monero/key-image/validate":
			validateCalls++
			_ = json.NewEncoder(w).Encode(xmrFrostKeyImageValidateResponse{Valid: false})
		case r.URL.Path == "/v1/monero/outputs/apply-key-images":
			t.Fatal("apply-key-images should not be called when outbound key-image validation fails")
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		vaultAddressMap: map[string]common.Address{vaultPubKey.String(): vaultAddr},
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			vaultAddr,
			destination,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"OUT:EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE",
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		TxKeys:         []string{strings.Repeat("1", 64)},
		XmrKeyImages: []*common.XmrKeyImageProof{
			{TxHash: txHash, OutputIndex: 0, KeyImage: "ki-0", ProofC: "c0", ProofS: "s0"},
		},
	}

	err = c.ValidateOutboundObservation(obsTx)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid")
	assert.Equal(t, 1, validateCalls)
}

func TestValidateOutboundObservationRequiresKeyImagesForFinalOutboundChange(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("j", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("external-destination")))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackedOwnedChange := true
		localChangeMaterialized := true
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:                  txHash,
					HasOwnedChange:          &trackedOwnedChange,
					LocalChangeMaterialized: &localChangeMaterialized,
					FromAddress:             vaultAddr.String(),
					ToAddress:               destination.String(),
					Amount:                  10,
					Fee:                     1,
					Memo:                    "OUT:EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE",
					PlannedHeight:           110,
					Status:                  "observed_mined",
					TxKeys:                  []string{strings.Repeat("1", 64)},
				}},
			})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			vaultAddr,
			destination,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"OUT:EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE",
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		TxKeys:         []string{strings.Repeat("1", 64)},
	}

	err = c.ValidateOutboundObservation(obsTx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing key images")
}

func TestValidateOutboundObservationDefersFinalOutboundUntilTrackedChangeMaterializes(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("l", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("external-destination")))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackedOwnedChange := true
		localChangeMaterialized := false
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:                  txHash,
					HasOwnedChange:          &trackedOwnedChange,
					LocalChangeMaterialized: &localChangeMaterialized,
					FromAddress:             vaultAddr.String(),
					ToAddress:               destination.String(),
					Amount:                  10,
					Fee:                     1,
					Memo:                    "OUT:LLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLL",
					PlannedHeight:           110,
					Status:                  "observed_mined",
					PendingReason:           "awaiting_change_materialization",
					TxKeys:                  []string{strings.Repeat("1", 64)},
				}},
			})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: nil})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			vaultAddr,
			destination,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"OUT:LLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLLL",
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		TxKeys:         []string{strings.Repeat("1", 64)},
	}

	err = c.ValidateOutboundObservation(obsTx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "still waiting for tracked vault-owned outputs/key images")
}

func TestValidateOutboundObservationAllowsFinalOutboundWithoutChange(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("k", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("external-without-change")))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noOwnedChange := false
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/outbounds/"+url.PathEscape(vaultPubKey.String()):
			_ = json.NewEncoder(w).Encode(xmrFrostOutgoingTransfersStateResponse{
				VaultPubKey: vaultPubKey.String(),
				Outgoing: []xmrFrostOutgoingTransferStateEntry{{
					TxHash:              txHash,
					HasOwnedChange:      &noOwnedChange,
					FromAddress:         vaultAddr.String(),
					ToAddress:           destination.String(),
					Amount:              10,
					Fee:                 1,
					Memo:                "OUT:FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
					PlannedHeight:       110,
					Status:              "observed_mined",
					TxKeys:              []string{strings.Repeat("1", 64)},
					RegisteredInventory: true,
				}},
			})
		case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: nil})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
	}

	obsTx := common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(txHash),
			vaultAddr,
			destination,
			common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
			common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			"OUT:FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
		),
		BlockHeight:    110,
		FinaliseHeight: 110,
		ObservedPubKey: vaultPubKey,
		TxKeys:         []string{strings.Repeat("1", 64)},
	}

	require.NoError(t, c.ValidateOutboundObservation(obsTx))
}

func TestCatchupKeyImagesReadsFromVaultAndCallsSidecar(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-key-image-catchup")

	var mu sync.Mutex
	var capturedVault string
	var capturedRefs []xmrFrostApplyKeyImageRef

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.True(t, req.Reconcile)
			mu.Lock()
			capturedVault = req.VaultPubKey
			capturedRefs = append([]xmrFrostApplyKeyImageRef(nil), req.KeyImages...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	vault := ttypes.Vault{
		PubKeyFrost: frostKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		KeyImagesXmr: []*common.XmrKeyImageRef{
			{TxHash: "in_a", OutputIndex: 0, KeyImage: "ki_a"},
			{TxHash: "in_b", OutputIndex: 1, KeyImage: "ki_b"},
		},
	}

	c := &Client{
		chain:       common.XMRChain,
		frostSigner: signer,
		logger:      testLogger(),
		thorchainBridge: &mockMoneroBridge{
			vaultByPubKey: map[string]ttypes.Vault{
				vault.PubKeyFrost.String(): vault,
			},
		},
	}

	c.catchupKeyImages()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, frostKey.String(), capturedVault)
	require.Len(t, capturedRefs, 2)
	assert.Equal(t, "in_a", capturedRefs[0].TxHash)
	assert.Equal(t, "ki_b", capturedRefs[1].KeyImage)
}

func TestCatchupKeyImagesSkipsInactiveVaultsOutsideAsgardSet(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-key-image-inactive")

	var mu sync.Mutex
	var capturedVault string
	var capturedRefs []xmrFrostApplyKeyImageRef

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.True(t, req.Reconcile)
			mu.Lock()
			capturedVault = req.VaultPubKey
			capturedRefs = append([]xmrFrostApplyKeyImageRef(nil), req.KeyImages...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	vault := ttypes.Vault{
		PubKeyFrost: frostKey,
		Status:      ttypes.VaultStatus_InactiveVault,
		KeyImagesXmr: []*common.XmrKeyImageRef{
			{TxHash: "retired_a", OutputIndex: 0, KeyImage: "ki_retired_a"},
		},
	}
	bridge := &mockMoneroBridge{
		asgards:       ttypes.Vaults{},
		vaultByPubKey: map[string]ttypes.Vault{frostKey.String(): vault},
		pubKeys: []thorclient.PubKeyContractAddressPair{{
			PubKey:   frostKey,
			Algo:     common.SigningAlgoEd25519,
			Inactive: true,
		}},
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.catchupKeyImages()

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, capturedVault)
	assert.Empty(t, capturedRefs)
}

func TestCatchupKeyImagesSkipsUnchangedHistoryAndSendsOnlyDelta(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-key-image-delta")

	var mu sync.Mutex
	var requests [][]xmrFrostApplyKeyImageRef
	bridge := &mockMoneroBridge{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.True(t, req.Reconcile)
			mu.Lock()
			requests = append(requests, append([]xmrFrostApplyKeyImageRef(nil), req.KeyImages...))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			require.Equal(t, "all", r.URL.Query().Get("view"))
			vault := bridge.vaultByPubKey[frostKey.String()]
			outputs := make([]xmrFrostOutputEntry, 0, len(vault.KeyImagesXmr))
			for _, ref := range vault.KeyImagesXmr {
				outputs = append(outputs, xmrFrostOutputEntry{
					TxHash:      ref.TxHash,
					OutputIndex: ref.OutputIndex,
					Amount:      10,
					Height:      100,
					KeyImage:    ref.KeyImage,
					State:       "unspent",
				})
			}
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: outputs})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	vault := ttypes.Vault{
		PubKeyFrost: frostKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		KeyImagesXmr: []*common.XmrKeyImageRef{
			{TxHash: "a", OutputIndex: 0, KeyImage: "ki_a"},
			{TxHash: "b", OutputIndex: 1, KeyImage: "ki_b"},
		},
	}
	bridge.vaultByPubKey = map[string]ttypes.Vault{frostKey.String(): vault}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.catchupKeyImages()
	c.catchupKeyImages()

	bridge.vaultByPubKey[frostKey.String()] = ttypes.Vault{
		PubKeyFrost: frostKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		KeyImagesXmr: []*common.XmrKeyImageRef{
			{TxHash: "a", OutputIndex: 0, KeyImage: "ki_a"},
			{TxHash: "b", OutputIndex: 1, KeyImage: "ki_b"},
			{TxHash: "c", OutputIndex: 2, KeyImage: "ki_c"},
		},
	}
	c.catchupKeyImages()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2)
	require.Len(t, requests[0], 2)
	require.Len(t, requests[1], 1)
	assert.Equal(t, "c", requests[1][0].TxHash)
	assert.Equal(t, "ki_c", requests[1][0].KeyImage)
}

func TestCatchupKeyImagesRetriesUnchangedHistoryWhenPriorApplyWasIncomplete(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-key-image-pending")

	var mu sync.Mutex
	var requests [][]xmrFrostApplyKeyImageRef
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.True(t, req.Reconcile)
			mu.Lock()
			requests = append(requests, append([]xmrFrostApplyKeyImageRef(nil), req.KeyImages...))
			callCount++
			currentCall := callCount
			mu.Unlock()

			resp := xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)}
			if currentCall == 1 {
				resp.NotFound = 1
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	vault := ttypes.Vault{
		PubKeyFrost: frostKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		KeyImagesXmr: []*common.XmrKeyImageRef{
			{TxHash: "a", OutputIndex: 0, KeyImage: "ki_a"},
		},
	}
	bridge := &mockMoneroBridge{
		vaultByPubKey: map[string]ttypes.Vault{frostKey.String(): vault},
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.catchupKeyImages()
	c.catchupKeyImages()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2)
	require.Len(t, requests[0], 1)
	require.Len(t, requests[1], 1)
	assert.Equal(t, "a", requests[1][0].TxHash)
}

func TestCatchupKeyImagesReconcilesEmptyVaultHistoryToClearStaleLocalState(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-key-image-empty-reconcile")

	var mu sync.Mutex
	var requests []xmrFrostApplyKeyImagesRequest
	var rawBodies []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/apply-key-images":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.Unmarshal(body, &req))
			mu.Lock()
			requests = append(requests, req)
			rawBodies = append(rawBodies, string(body))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			require.Equal(t, "all", r.URL.Query().Get("view"))
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: []xmrFrostOutputEntry{}})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	vault := ttypes.Vault{
		PubKeyFrost:  frostKey,
		Status:       ttypes.VaultStatus_ActiveVault,
		KeyImagesXmr: nil,
	}
	bridge := &mockMoneroBridge{
		vaultByPubKey: map[string]ttypes.Vault{frostKey.String(): vault},
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.catchupKeyImages()
	c.catchupKeyImages()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 1)
	require.Len(t, rawBodies, 1)
	assert.True(t, requests[0].Reconcile)
	assert.Empty(t, requests[0].KeyImages)
	assert.Contains(t, rawBodies[0], `"key_images":[]`)
	assert.Equal(t, frostKey.String(), requests[0].VaultPubKey)
}

func TestCatchupKeyImagesReplaysAfterSignerReset(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-key-image-reset-replay")

	var mu sync.Mutex
	var requests []xmrFrostApplyKeyImagesRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{Applied: len(req.KeyImages)})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
			require.Equal(t, "all", r.URL.Query().Get("view"))
			_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
				Outputs: []xmrFrostOutputEntry{
					{TxHash: "a", OutputIndex: 0, Amount: 10, Height: 100, State: "unspent"},
				},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	vault := ttypes.Vault{
		PubKeyFrost: frostKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		KeyImagesXmr: []*common.XmrKeyImageRef{
			{TxHash: "a", OutputIndex: 0, KeyImage: "ki_a"},
		},
	}
	bridge := &mockMoneroBridge{
		vaultByPubKey: map[string]ttypes.Vault{frostKey.String(): vault},
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.catchupKeyImages()
	c.catchupKeyImages()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2)
	assert.Equal(t, frostKey.String(), requests[0].VaultPubKey)
	assert.Equal(t, frostKey.String(), requests[1].VaultPubKey)
}

func TestCatchupTxKeysReadsVaultHistoryAndReplaysOutboundAudit(t *testing.T) {
	t.Parallel()

	frostKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)
	parentTxHash := strings.Repeat("b", 64)
	txKey := strings.Repeat("1", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("tx-key-destination")))
	changeAmount := cosmos.NewUint(40_279_764)

	var mu sync.Mutex
	var requests []xmrFrostRegisterVaultRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostVaultStatesResponse{}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	outAmount := cosmos.NewUint(100_000_000)
	outFee := cosmos.NewUint(2_000_000)
	expectedAmount, err := xmrThorchainAmountToAtomic(outAmount)
	require.NoError(t, err)
	expectedFee, err := xmrThorchainAmountToAtomic(outFee)
	require.NoError(t, err)

	bridge := &mockMoneroBridge{
		vaultByPubKey: map[string]ttypes.Vault{
			frostKey.String(): {
				PubKeyFrost: frostKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				TxKeysXmr: []*common.XmrTxKeyRef{
					{TxHash: txHash, TxKeys: []string{txKey}},
				},
			},
		},
		observedTxVoterByID: map[string]*ttypes.QueryObservedTxVoter{
			txHash: {
				TxID: common.TxID(txHash),
				Tx: ttypes.QueryObservedTx{
					Tx: common.NewTx(
						common.TxID(txHash),
						common.Address(common.XMRUnknownSenderAddress(txHash, vaultAddr.String())),
						vaultAddr,
						common.NewCoins(common.NewCoin(common.XMRAsset, changeAmount)),
						nil,
						"OUT:"+parentTxHash,
					),
					ObservedPubKey: frostKey,
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
						common.NewCoins(common.NewCoin(common.XMRAsset, outAmount)),
						common.Gas{common.NewCoin(common.XMRAsset, outFee)},
						"OUT:"+parentTxHash,
					),
				},
			},
		},
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.refreshOutgoingInventoryReplay()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 1)
	assert.Equal(t, frostKey.String(), requests[0].VaultPubKey)
	require.Len(t, requests[0].TxKeyEntries, 1)
	assert.Equal(t, txHash, requests[0].TxKeyEntries[0].TxHash)
	assert.Equal(t, []string{txKey}, requests[0].TxKeyEntries[0].TxKeys)
	assert.Equal(t, vaultAddr.String(), requests[0].TxKeyEntries[0].FromAddress)
	assert.Equal(t, destination.String(), requests[0].TxKeyEntries[0].ToAddress)
	assert.Equal(t, expectedAmount, requests[0].TxKeyEntries[0].Amount)
	assert.Equal(t, expectedFee, requests[0].TxKeyEntries[0].Fee)
	assert.Equal(t, "OUT:"+parentTxHash, requests[0].TxKeyEntries[0].Memo)
}

func TestCatchupTxKeysRegistersCombinedKeyImageAndTxKeyInventory(t *testing.T) {
	t.Parallel()

	frostKey, vaultAddr := testVaultPubKeyAndAddress(t)
	spentOutputTxHash := strings.Repeat("7", 64)
	outboundTxHash := strings.Repeat("a", 64)
	parentTxHash := strings.Repeat("b", 64)
	txKey := strings.Repeat("1", 64)
	keyImage := "key-image-0"
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("tx-key-gated-destination")))

	var mu sync.Mutex
	var registerRequests []xmrFrostRegisterVaultRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostVaultStatesResponse{}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			registerRequests = append(registerRequests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	bridge := &mockMoneroBridge{
		vaultByPubKey: map[string]ttypes.Vault{
			frostKey.String(): {
				PubKeyFrost: frostKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				KeyImagesXmr: []*common.XmrKeyImageRef{
					{TxHash: spentOutputTxHash, OutputIndex: 0, KeyImage: keyImage},
				},
				TxKeysXmr: []*common.XmrTxKeyRef{
					{TxHash: outboundTxHash, TxKeys: []string{txKey}},
				},
			},
		},
		observedTxVoterByID: map[string]*ttypes.QueryObservedTxVoter{
			outboundTxHash: {
				TxID: common.TxID(outboundTxHash),
				Tx: ttypes.QueryObservedTx{
					Tx: common.NewTx(
						common.TxID(outboundTxHash),
						common.Address(common.XMRUnknownSenderAddress(outboundTxHash, vaultAddr.String())),
						vaultAddr,
						common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
						nil,
						"OUT:"+parentTxHash,
					),
					ObservedPubKey: frostKey,
				},
			},
		},
		txStatusByID: map[string]*ttypes.QueryTxStatusResponse{
			parentTxHash: {
				OutTxs: common.Txs{
					common.NewTx(
						common.TxID(outboundTxHash),
						vaultAddr,
						destination,
						common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
						nil,
						"OUT:"+parentTxHash,
					),
				},
			},
		},
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.refreshOutgoingInventoryReplay()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, registerRequests, 1)
	require.Len(t, registerRequests[0].KeyImages, 1)
	assert.Equal(t, spentOutputTxHash, registerRequests[0].KeyImages[0].TxHash)
	assert.Equal(t, uint32(0), registerRequests[0].KeyImages[0].OutputIndex)
	assert.Equal(t, keyImage, registerRequests[0].KeyImages[0].KeyImage)
	require.Len(t, registerRequests[0].TxKeyEntries, 1)
	assert.Equal(t, outboundTxHash, registerRequests[0].TxKeyEntries[0].TxHash)
	assert.Equal(t, []string{txKey}, registerRequests[0].TxKeyEntries[0].TxKeys)
}

func TestCatchupTxKeysReplaysAfterSignerReset(t *testing.T) {
	t.Parallel()

	frostKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("c", 64)
	parentTxHash := strings.Repeat("d", 64)
	txKey := strings.Repeat("2", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("tx-key-reset-destination")))

	var mu sync.Mutex
	var requests []xmrFrostRegisterVaultRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostVaultStatesResponse{}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	bridge := &mockMoneroBridge{
		vaultByPubKey: map[string]ttypes.Vault{
			frostKey.String(): {
				PubKeyFrost: frostKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				TxKeysXmr: []*common.XmrTxKeyRef{
					{TxHash: txHash, TxKeys: []string{txKey}},
				},
			},
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
					ObservedPubKey: frostKey,
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
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.refreshOutgoingInventoryReplay()
	c.refreshOutgoingInventoryReplay()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2)
	assert.Equal(t, frostKey.String(), requests[0].VaultPubKey)
	assert.Equal(t, frostKey.String(), requests[1].VaultPubKey)
	require.Len(t, requests[0].TxKeyEntries, 1)
	require.Len(t, requests[1].TxKeyEntries, 1)
	assert.Equal(t, txHash, requests[0].TxKeyEntries[0].TxHash)
	assert.Equal(t, txHash, requests[1].TxKeyEntries[0].TxHash)
}

func TestCatchupTxKeysKeepsLocallyBoundTxKeysAheadOfInventory(t *testing.T) {
	t.Parallel()

	frostKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("c", 64)
	parentTxHash := strings.Repeat("d", 64)
	txKey := strings.Repeat("2", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("tx-key-prune-destination")))

	var mu sync.Mutex
	var registerRequests []xmrFrostRegisterVaultRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostVaultStatesResponse{}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			registerRequests = append(registerRequests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	bridge := &mockMoneroBridge{
		vaultByPubKey: map[string]ttypes.Vault{
			frostKey.String(): {
				PubKeyFrost: frostKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				TxKeysXmr: []*common.XmrTxKeyRef{
					{TxHash: txHash, TxKeys: []string{txKey}},
				},
			},
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
					ObservedPubKey: frostKey,
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
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.refreshOutgoingInventoryReplay()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, registerRequests, 1)
	require.Len(t, registerRequests[0].TxKeyEntries, 1)
	assert.Equal(t, txHash, registerRequests[0].TxKeyEntries[0].TxHash)
}

func TestCatchupTxKeysIgnoresLocalOnlySignerOutboundsOutsideRegisteredInventory(t *testing.T) {
	t.Parallel()

	frostKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("f", 64)
	parentTxHash := strings.Repeat("7", 64)
	txKey := strings.Repeat("8", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("tx-key-local-only-destination")))

	var mu sync.Mutex
	var registerRequests []xmrFrostRegisterVaultRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostVaultStatesResponse{}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			registerRequests = append(registerRequests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	bridge := &mockMoneroBridge{
		vaultByPubKey: map[string]ttypes.Vault{
			frostKey.String(): {
				PubKeyFrost: frostKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				TxKeysXmr: []*common.XmrTxKeyRef{
					{TxHash: txHash, TxKeys: []string{txKey}},
				},
			},
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
					ObservedPubKey: frostKey,
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
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.refreshOutgoingInventoryReplay()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, registerRequests, 1)
	require.Len(t, registerRequests[0].TxKeyEntries, 1)
	assert.Equal(t, txHash, registerRequests[0].TxKeyEntries[0].TxHash)
}

func TestCatchupTxKeysReplaysOutboundAuditInObservedHeightOrder(t *testing.T) {
	t.Parallel()

	frostKey, vaultAddr := testVaultPubKeyAndAddress(t)
	olderTxHash := strings.Repeat("1", 64)
	newerTxHash := strings.Repeat("2", 64)
	olderParentTxHash := strings.Repeat("3", 64)
	newerParentTxHash := strings.Repeat("4", 64)
	olderTxKey := strings.Repeat("5", 64)
	newerTxKey := strings.Repeat("6", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("tx-key-order-destination")))

	var mu sync.Mutex
	var requests []xmrFrostRegisterVaultRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/vaults":
			require.NoError(t, json.NewEncoder(w).Encode(xmrFrostVaultStatesResponse{}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			var req xmrFrostRegisterVaultRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	bridge := &mockMoneroBridge{
		vaultByPubKey: map[string]ttypes.Vault{
			frostKey.String(): {
				PubKeyFrost: frostKey,
				Status:      ttypes.VaultStatus_ActiveVault,
				TxKeysXmr: []*common.XmrTxKeyRef{
					{TxHash: newerTxHash, TxKeys: []string{newerTxKey}},
					{TxHash: olderTxHash, TxKeys: []string{olderTxKey}},
				},
			},
		},
		observedTxVoterByID: map[string]*ttypes.QueryObservedTxVoter{
			olderTxHash: {
				TxID: common.TxID(olderTxHash),
				Tx: ttypes.QueryObservedTx{
					Tx: common.NewTx(
						common.TxID(olderTxHash),
						common.Address(common.XMRUnknownSenderAddress(olderTxHash, vaultAddr.String())),
						vaultAddr,
						common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
						nil,
						"OUT:"+olderParentTxHash,
					),
					ObservedPubKey: frostKey,
					BlockHeight:    100,
				},
			},
			newerTxHash: {
				TxID: common.TxID(newerTxHash),
				Tx: ttypes.QueryObservedTx{
					Tx: common.NewTx(
						common.TxID(newerTxHash),
						common.Address(common.XMRUnknownSenderAddress(newerTxHash, vaultAddr.String())),
						vaultAddr,
						common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
						nil,
						"OUT:"+newerParentTxHash,
					),
					ObservedPubKey: frostKey,
					BlockHeight:    200,
				},
			},
		},
		txStatusByID: map[string]*ttypes.QueryTxStatusResponse{
			olderParentTxHash: {
				OutTxs: common.Txs{
					common.NewTx(
						common.TxID(olderTxHash),
						vaultAddr,
						destination,
						common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
						nil,
						"OUT:"+olderParentTxHash,
					),
				},
			},
			newerParentTxHash: {
				OutTxs: common.Txs{
					common.NewTx(
						common.TxID(newerTxHash),
						vaultAddr,
						destination,
						common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(1))),
						nil,
						"OUT:"+newerParentTxHash,
					),
				},
			},
		},
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.refreshOutgoingInventoryReplay()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].TxKeyEntries, 2)
	assert.Equal(t, olderTxHash, requests[0].TxKeyEntries[0].TxHash)
	assert.Equal(t, newerTxHash, requests[0].TxKeyEntries[1].TxHash)
}

func TestCatchupTxKeysSkipsInactiveVaultsOutsideAsgardSet(t *testing.T) {
	t.Parallel()

	frostKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)
	parentTxHash := strings.Repeat("b", 64)
	txKey := strings.Repeat("1", 64)
	destination := testOutboundXMRDestination()

	var mu sync.Mutex
	var requests []xmrFrostValidateOutboundRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outbounds/validate":
			var req xmrFrostValidateOutboundRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/transfers":
			_ = json.NewEncoder(w).Encode(xmrFrostTransfersResponse{})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	vault := ttypes.Vault{
		PubKeyFrost: frostKey,
		Status:      ttypes.VaultStatus_InactiveVault,
		TxKeysXmr: []*common.XmrTxKeyRef{
			{TxHash: txHash, TxKeys: []string{txKey}},
		},
	}
	bridge := &mockMoneroBridge{
		asgards:       ttypes.Vaults{},
		vaultByPubKey: map[string]ttypes.Vault{frostKey.String(): vault},
		pubKeys: []thorclient.PubKeyContractAddressPair{{
			PubKey:   frostKey,
			Algo:     common.SigningAlgoEd25519,
			Inactive: true,
		}},
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
					ObservedPubKey: frostKey,
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
	}

	c := &Client{
		chain:           common.XMRChain,
		frostSigner:     signer,
		logger:          testLogger(),
		thorchainBridge: bridge,
	}

	c.refreshOutgoingInventoryReplay()

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, requests)
}

func TestPrepareObservedTxInItemPropagatesKeyImageBlame(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("d", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
			Outputs: []xmrFrostOutputEntry{{TxHash: txHash, OutputIndex: 0, Amount: 10, Height: 100}},
		})
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	tssServer := &mockKeyImageErrorTssServer{
		err: bifrosttss.NewKeysignError(ttypes.Blame{
			FailReason: "key-image failed",
			BlameNodes: []ttypes.Node{{Pubkey: "thorpub1blame"}},
		}),
	}
	c := &Client{
		chain:           common.XMRChain,
		localNodePubKey: common.PubKey("member-a"),
		frostSigner:     signer,
		tssServer:       tssServer,
		thorchainBridge: &keyImageTestBridge{
			mockMoneroBridge: mockMoneroBridge{
				vaultByPubKey: map[string]ttypes.Vault{
					vaultPubKey.String(): {
						PubKey:      vaultPubKey,
						PubKeyEddsa: vaultPubKey,
						PubKeyFrost: vaultPubKey,
						Membership:  []string{"member-a", "member-b"},
					},
				},
			},
			version: semver.MustParse("3.0.0"),
		},
		logger: testLogger(),
	}

	item := &stypes.TxInItem{
		BlockHeight:         100,
		Tx:                  txHash,
		Sender:              common.XMRSeededDummyAddress('8', []byte("sender")),
		To:                  vaultAddr.String(),
		Coins:               common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(10))),
		Gas:                 common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
		ObservedVaultPubKey: vaultPubKey,
	}

	_, _, err = c.PrepareObservedTxInItem(item, true, 110)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key-image")
}
