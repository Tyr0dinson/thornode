package monero

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/blang/semver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	bifrosttss "gitlab.com/thorchain/thornode/v3/bifrost/tss"
	gotsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
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

func TestPrepareObservedTxInItemGeneratesKeyImagesForFinalInbound(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
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
	assert.Equal(t, int64(110), tssServer.reqs[0].BlockHeight)
	assert.Equal(t, txHash, tssServer.reqs[0].TxHash)
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
	assert.Equal(t, int64(110), tssServer.reqs[0].BlockHeight)
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

func TestPrepareObservedTxInItemGeneratesKeyImagesForFinalOutboundChange(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("g", 64)
	destination := common.XMRSeededDummyAddress('8', []byte("external-destination"))

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
		Memo:                "OUT:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
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
}

func TestPrepareObservedTxInItemKeepsFinalOutboundWithoutTrackedChange(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("i", 64)
	destination := common.XMRSeededDummyAddress('8', []byte("external-without-change"))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{Outputs: nil})
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
		Memo:                "OUT:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
	}

	prepared, include, err := c.PrepareObservedTxInItem(item, true, 110)
	require.NoError(t, err)
	require.True(t, include)
	require.NotNil(t, prepared)
	require.Empty(t, prepared.XmrKeyImages)

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

func TestValidateOutboundObservationAppliesValidatedKeyImagesForFinalOutboundChange(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("h", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("external-destination")))

	var mu sync.Mutex
	var outboundValidateCalls int
	var validateCalls int
	var applied []xmrFrostApplyKeyImageRef

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/monero/outbounds/validate":
			outboundValidateCalls++
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true, Marked: 1})
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
	assert.Equal(t, 1, outboundValidateCalls)
	assert.Equal(t, 1, validateCalls)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, applied, 1)
	assert.Equal(t, uint32(0), applied[0].OutputIndex)
}

func TestValidateOutboundObservationRequiresKeyImagesForFinalOutboundChange(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("j", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("external-destination")))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/monero/outbounds/validate":
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true, Marked: 1})
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

func TestValidateOutboundObservationAllowsFinalOutboundWithoutChange(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("k", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("external-without-change")))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/monero/outbounds/validate":
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true, Marked: 1})
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

func TestCatchupKeyImagesLoadsInactiveVaultsViaPubKeys(t *testing.T) {
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
	assert.Equal(t, frostKey.String(), capturedVault)
	require.Len(t, capturedRefs, 1)
	assert.Equal(t, "retired_a", capturedRefs[0].TxHash)
	assert.Equal(t, "ki_retired_a", capturedRefs[0].KeyImage)
}

func TestCatchupKeyImagesSkipsUnchangedHistoryAndSendsOnlyDelta(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-key-image-delta")

	var mu sync.Mutex
	var requests [][]xmrFrostApplyKeyImageRef

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outputs/apply-key-images":
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			require.True(t, req.Reconcile)
			mu.Lock()
			requests = append(requests, append([]xmrFrostApplyKeyImageRef(nil), req.KeyImages...))
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
			{TxHash: "a", OutputIndex: 0, KeyImage: "ki_a"},
			{TxHash: "b", OutputIndex: 1, KeyImage: "ki_b"},
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
		switch r.URL.Path {
		case "/v1/monero/outputs/apply-key-images":
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			var req xmrFrostApplyKeyImagesRequest
			require.NoError(t, json.Unmarshal(body, &req))
			mu.Lock()
			requests = append(requests, req)
			rawBodies = append(rawBodies, string(body))
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostApplyKeyImagesResponse{})
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

func TestCatchupTxKeysReadsVaultHistoryAndReplaysOutboundAudit(t *testing.T) {
	t.Parallel()

	frostKey, vaultAddr := testVaultPubKeyAndAddress(t)
	txHash := strings.Repeat("a", 64)
	parentTxHash := strings.Repeat("b", 64)
	txKey := strings.Repeat("1", 64)
	destination := common.Address(common.XMRSeededDummyAddress('8', []byte("tx-key-destination")))
	changeAmount := cosmos.NewUint(40_279_764)

	var mu sync.Mutex
	var requests []xmrFrostValidateOutboundRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outbounds/validate":
			var req xmrFrostValidateOutboundRequest
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			mu.Lock()
			requests = append(requests, req)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(xmrFrostValidateOutboundResponse{Tracked: true})
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

	c.catchupTxKeys()
	c.catchupTxKeys()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 1)
	assert.Equal(t, frostKey.String(), requests[0].VaultPubKey)
	assert.Equal(t, txHash, requests[0].TxHash)
	assert.Equal(t, []string{txKey}, requests[0].TxKeys)
	assert.Equal(t, vaultAddr.String(), requests[0].FromAddress)
	assert.Equal(t, destination.String(), requests[0].ToAddress)
	assert.Equal(t, expectedAmount, requests[0].Amount)
	assert.Equal(t, expectedFee, requests[0].Fee)
	assert.Equal(t, "OUT:"+parentTxHash, requests[0].Memo)
	assert.False(t, requests[0].Reconcile)
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
