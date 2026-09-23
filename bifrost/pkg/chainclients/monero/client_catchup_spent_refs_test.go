package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestCatchupSpentOutputRefsReadsFromQueryAndCallsSidecar(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-catchup-test")
	vaultAddr, err := frostKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("catchup-spend")

	var mu sync.Mutex
	var capturedVault, capturedTxHash string
	var capturedTxKey, capturedMemo string
	var capturedOrigin sidecar.OutgoingOrigin
	var capturedRefs []sidecar.SpentRef
	var skipVerification bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outputs/mark-spent":
			var req sidecar.MarkSpentReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			capturedVault = req.VaultPubKey
			capturedTxHash = req.TxHash
			capturedTxKey = req.TxKey
			capturedMemo = req.Memo
			capturedOrigin = req.Origin
			capturedRefs = req.SpentRefs
			skipVerification = req.SkipVerification
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	refs := []*common.OutputRef{
		testXMROutputRef("catchup-spent-a", 0, spendTxHash),
		testXMROutputRef("catchup-spent-b", 1, spendTxHash),
		testXMROutputRef("catchup-spent-c", 2, spendTxHash),
	}
	voter := &ttypes.QueryObservedTxVoter{
		TxID:   common.TxID(spendTxHash),
		Height: 100,
		Tx: ttypes.QueryObservedTx{
			Tx: common.Tx{
				ID:          common.TxID(spendTxHash),
				Chain:       common.XMRChain,
				FromAddress: vaultAddr,
				ToAddress:   testOutboundXMRDestination(),
				Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
				Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
				Memo:        "OUT:" + strings.Repeat("a", 64),
			},
			BlockHeight:    100,
			ObservedPubKey: frostKey,
			FinaliseHeight: 100,
			XmrTxKey:       strings.Repeat("5", 64),
		},
	}

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{frostKey.String(): refs},
			observedByTx:        map[string]*ttypes.QueryObservedTxVoter{spendTxHash: voter},
			pubKeys:             []thorclient.PubKeyContractAddressPair{{PubKey: frostKey, Algo: common.SigningAlgoEd25519}},
		},
	}

	c.catchupSpentOutputRefs()

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, frostKey.String(), capturedVault)
	assert.Equal(t, spendTxHash, capturedTxHash)
	assert.Empty(t, capturedTxKey)
	assert.Empty(t, capturedMemo)
	assert.Equal(t, sidecar.OutgoingOrigin(sidecar.OutgoingOriginChainCommitted), capturedOrigin)
	assert.False(t, skipVerification)
	require.Equal(t, 3, len(capturedRefs))
	assert.Equal(t, refs[0].TxHash, capturedRefs[0].TxHash)
	assert.Equal(t, refs[0].KeyImage, capturedRefs[0].KeyImage)
	assert.Equal(t, refs[1].TxHash, capturedRefs[1].TxHash)
	assert.Equal(t, refs[1].KeyImage, capturedRefs[1].KeyImage)
	assert.Equal(t, refs[2].TxHash, capturedRefs[2].TxHash)
	assert.Equal(t, refs[2].KeyImage, capturedRefs[2].KeyImage)
}

func TestCatchupSpentOutputRefsRecoversMigrationFromEmptySidecarRecord(t *testing.T) {
	t.Parallel()

	sourceVault, destinationVault, spendTxHash, voter, refs := testXMRCatchupMigration(t, "empty-sidecar-recovery")
	blockHash := testXMRHash("empty-sidecar-recovery-block")
	parentHash := testXMRHash("empty-sidecar-recovery-parent")

	var mu sync.Mutex
	var stored *sidecar.Transfer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
			_ = json.NewEncoder(w).Encode(testRegisterVaultResponse(101))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/mark-spent":
			var req sidecar.MarkSpentReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid mark-spent request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			stored = &sidecar.Transfer{
				TxID:                req.TxHash,
				TxKey:               req.TxKey,
				Address:             req.ToAddress,
				FromAddress:         req.FromAddress,
				VaultPubKey:         req.VaultPubKey,
				Amount:              req.Amount,
				Fee:                 req.Fee,
				Height:              100,
				Note:                req.Memo,
				Origin:              string(req.Origin),
				ObservationEligible: false,
				SpentOutputRefs:     append([]sidecar.SpentRef(nil), req.SpentRefs...),
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/tx/"+spendTxHash+"/location":
			_ = json.NewEncoder(w).Encode(sidecar.CanonicalTxLocationResp{
				TxHash:       spendTxHash,
				Height:       100,
				BlockHash:    blockHash,
				PreviousHash: parentHash,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/mark-observed":
			mu.Lock()
			var proof *sidecar.Transfer
			if stored != nil {
				copy := *stored
				copy.SpentOutputRefs = append([]sidecar.SpentRef(nil), stored.SpentOutputRefs...)
				proof = &copy
			}
			mu.Unlock()
			if proof == nil {
				http.Error(w, "outgoing record not found", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(proof)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	scanner, replayStore, reorgStore := newXMRCatchupReplayScanner(t, signer, sourceVault, destinationVault)
	require.NoError(t, reorgStore.Set(&reorgBlockMeta{
		Height:       100,
		BlockHash:    blockHash,
		PreviousHash: parentHash,
	}))
	scanner.setCurrentHeight(100)

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		scanner: scanner,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{sourceVault.String(): refs},
			observedByTx:        map[string]*ttypes.QueryObservedTxVoter{spendTxHash: voter},
			pubKeys:             []thorclient.PubKeyContractAddressPair{{PubKey: sourceVault, Algo: common.SigningAlgoEd25519}},
		},
	}

	mu.Lock()
	assert.Nil(t, stored, "test must begin without a sidecar outgoing record")
	mu.Unlock()
	c.catchupSpentOutputRefs()

	mu.Lock()
	var proof *sidecar.Transfer
	if stored != nil {
		copy := *stored
		proof = &copy
	}
	mu.Unlock()
	require.NotNil(t, proof)
	assert.True(t, hasCompleteChainCommittedPairingProof(*proof))

	items, claims, err := scanner.replayAuthenticatedInternalInbounds(101, 101)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Len(t, claims, 1)
	assert.True(t, items[0].ObservedVaultPubKey.Equals(destinationVault))
	assert.Equal(t, voter.Tx.Tx.FromAddress.String(), items[0].Sender)
	if items[0].OnObservedTxStored != nil {
		items[0].OnObservedTxStored()
	}
	count, err := replayStore.Count()
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestCatchupSpentOutputRefsDefersReplayAfterSignedStatePersistenceFailure(t *testing.T) {
	t.Parallel()

	sourceVault, destinationVault, spendTxHash, voter, refs := testXMRCatchupMigration(t, "persistence-failure")
	var mu sync.Mutex
	locationCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/outputs/mark-spent":
			var req sidecar.MarkSpentReq
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/tx/"+spendTxHash+"/location":
			mu.Lock()
			locationCalls++
			mu.Unlock()
			http.Error(w, "canonical lookup intentionally stops this test", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)
	scanner, _, _ := newXMRCatchupReplayScanner(t, signer, sourceVault, destinationVault)
	failedStore := newTestSignedTxPayloadStore(t)
	require.NoError(t, failedStore.db.Close())
	c := &Client{
		chain:                common.XMRChain,
		sidecar:              signer,
		scanner:              scanner,
		logger:               testLogger(),
		signedTxPayloadStore: failedStore,
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{sourceVault.String(): refs},
			observedByTx:        map[string]*ttypes.QueryObservedTxVoter{spendTxHash: voter},
			pubKeys:             []thorclient.PubKeyContractAddressPair{{PubKey: sourceVault, Algo: common.SigningAlgoEd25519}},
		},
	}

	c.catchupSpentOutputRefs()
	mu.Lock()
	assert.Zero(t, locationCalls, "replay enrollment must wait for durable signer state")
	mu.Unlock()

	// The periodic catch-up scan is the retry owner. Once persistence recovers,
	// a later scan reaches replay enrollment without any per-import goroutine.
	c.signedTxPayloadStore = newTestSignedTxPayloadStore(t)
	c.catchupSpentOutputRefs()
	mu.Lock()
	assert.Equal(t, 1, locationCalls)
	mu.Unlock()
}

func TestCatchupSpentOutputRefsIncludesInactiveVaultFromPubkeys(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-inactive-catchup")
	vaultAddr, err := frostKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("inactive-catchup-spend")

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/monero/outputs/mark-spent":
			calls++
			_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 1})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	refs := []*common.OutputRef{testXMROutputRef("inactive-catchup-spent", 0, spendTxHash)}
	voter := &ttypes.QueryObservedTxVoter{
		TxID:   common.TxID(spendTxHash),
		Height: 100,
		Tx: ttypes.QueryObservedTx{
			Tx: common.Tx{
				ID:          common.TxID(spendTxHash),
				Chain:       common.XMRChain,
				FromAddress: vaultAddr,
				ToAddress:   testOutboundXMRDestination(),
				Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
				Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			},
			BlockHeight:    100,
			ObservedPubKey: frostKey,
			FinaliseHeight: 100,
			XmrTxKey:       strings.Repeat("6", 64),
		},
	}

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{frostKey.String(): refs},
			observedByTx:        map[string]*ttypes.QueryObservedTxVoter{spendTxHash: voter},
			pubKeys:             []thorclient.PubKeyContractAddressPair{{PubKey: frostKey, Algo: common.SigningAlgoEd25519, Inactive: true}},
		},
	}

	c.catchupSpentOutputRefs()

	assert.Equal(t, 1, calls)
}

func TestCatchupSpentOutputRefsSkipsVaultsWithoutFrost(t *testing.T) {
	t.Parallel()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 0})
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			pubKeys: []thorclient.PubKeyContractAddressPair{{PubKey: testVaultPubKey(t, "secp-only-catchup"), Algo: common.SigningAlgoSecp256k1}},
		},
	}

	c.catchupSpentOutputRefs()

	assert.Equal(t, 0, calls)
}

func TestCatchupSpentOutputRefsSkipsVaultsWithNoRefs(t *testing.T) {
	t.Parallel()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 0})
	}))
	defer server.Close()

	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	require.NoError(t, err)

	c := &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			pubKeys: []thorclient.PubKeyContractAddressPair{{PubKey: testVaultPubKey(t, "frost-norefs"), Algo: common.SigningAlgoEd25519}},
		},
	}

	c.catchupSpentOutputRefs()

	assert.Equal(t, 0, calls)
}

func testXMRCatchupMigration(
	t *testing.T,
	label string,
) (common.PubKey, common.PubKey, string, *ttypes.QueryObservedTxVoter, []*common.OutputRef) {
	t.Helper()
	sourceVault := testVaultPubKey(t, label+"-source")
	destinationVault := testVaultPubKey(t, label+"-destination")
	sourceAddress, err := sourceVault.GetAddress(common.XMRChain)
	require.NoError(t, err)
	destinationAddress, err := destinationVault.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash(label + "-spend")
	refs := []*common.OutputRef{testXMROutputRef(label+"-input", 0, spendTxHash)}
	voter := &ttypes.QueryObservedTxVoter{
		TxID:   common.TxID(spendTxHash),
		Height: 100,
		Tx: ttypes.QueryObservedTx{
			Tx: common.Tx{
				ID:          common.TxID(spendTxHash),
				Chain:       common.XMRChain,
				FromAddress: sourceAddress,
				ToAddress:   destinationAddress,
				Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(2))),
				Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
				Memo:        "MIGRATE:100",
			},
			BlockHeight:    100,
			ObservedPubKey: sourceVault,
			FinaliseHeight: 100,
			XmrTxKey:       strings.Repeat("7", 64),
		},
	}
	return sourceVault, destinationVault, spendTxHash, voter, refs
}

func newXMRCatchupReplayScanner(
	t *testing.T,
	signer *sidecar.Client,
	sourceVault,
	destinationVault common.PubKey,
) (*Scanner, *authenticatedInternalInboundReplayStore, *ReorgStore) {
	t.Helper()
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	replayStore := newAuthenticatedInternalInboundReplayStore(db)
	reorgStore := NewReorgStore(db)
	scanner := NewScanner(
		testLogger(),
		signer,
		&scannerMemoBridge{asgards: ttypes.Vaults{
			{PubKey: sourceVault, PubKeyEddsa: sourceVault, PubKeyFrost: sourceVault},
			{PubKey: destinationVault, PubKeyEddsa: destinationVault, PubKeyFrost: destinationVault},
		}},
		common.XMRChain,
		common.EmptyPubKey,
		72,
	)
	scanner.SetAuthenticatedInternalInboundReplayStore(replayStore)
	scanner.SetReorgStore(reorgStore)
	return scanner, replayStore, reorgStore
}
