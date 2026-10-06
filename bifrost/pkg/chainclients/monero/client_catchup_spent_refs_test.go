package monero

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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

// Model the pinned signer's accounting boundary: scanning before MarkSpent
// cannot mark a nonexistent record mined. MarkSpent alone creates a pending
// ChainCommitted record, and MarkObserved requires complete metadata plus a
// canonical mined location. This does not substitute for a signer integration
// run; it exercises Bifrost's actual scan, import, restart and reorg paths.
func TestCommittedPayoutReconcilesAfterScanAndRestartWithoutSourceVote(t *testing.T) {
	for _, memoPrefix := range []string{"OUT:", "REFUND:", "RAGNAROK:"} {
		t.Run(memoPrefix, func(t *testing.T) {
			source, _, txID, voter, refs := testXMRCatchupMigration(t, "late-committed-"+memoPrefix)
			voter.Tx.Tx.Memo = memoPrefix + strings.Repeat("a", 64)
			if memoPrefix == "RAGNAROK:" {
				voter.Tx.Tx.Memo = "RAGNAROK:50"
			}
			voter.Tx.Tx.ToAddress = testOutboundXMRDestination()
			// The observed heights are not the daemon's canonical mining height.
			voter.Tx.BlockHeight, voter.Tx.FinaliseHeight = 99, 99
			block98, block99 := testXMRHash("late-h98"), testXMRHash("late-h99")
			block100 := testXMRHash("late-h100")
			blocks := map[int64]sidecar.ChainBlock{
				99:  withBlockHeader(99, defaultCoinbaseValue, block99, block98),
				100: withBlockHeader(100, defaultCoinbaseValue, block100, block99),
			}
			location := sidecar.CanonicalTxLocationResp{TxHash: txID, Height: 100, BlockHash: block100, PreviousHash: block99}
			next, last := uint64(100), uint64(99)
			epoch := uint64(0)
			lastHash, lastParent := block99, block98
			var stored *sidecar.Transfer
			var reorgStore *ReorgStore
			failMarkObserved := true
			markObservedCalls, rewindCalls := 0, 0
			var serverMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				serverMu.Lock()
				defer serverMu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/v1/monero/scanner/state":
					_ = json.NewEncoder(w).Encode(sidecar.ScannerState{ResetEpoch: epoch, NextHeight: &next, LastHeight: &last, LastBlockHash: lastHash, LastParentHash: lastParent, StrictAfterReset: epoch > 0})
				case strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
					height, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/"), 10, 64)
					block, ok := blocks[height]
					if !ok {
						http.NotFound(w, r)
						return
					}
					_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp(block))
				case r.URL.Path == "/v1/monero/chain/tx/"+txID+"/location":
					_ = json.NewEncoder(w).Encode(location)
				case r.URL.Path == "/v1/monero/scanner/sync":
					var req sidecar.SyncReq
					if json.NewDecoder(r.Body).Decode(&req) != nil || uint64(req.Height) != next {
						http.Error(w, "unexpected scan height", http.StatusConflict)
						return
					}
					block := blocks[req.Height]
					last, next = uint64(req.Height), uint64(req.Height+1)
					lastHash, lastParent = block.Hash, block.PreviousHash
					resp := sidecar.Transfers{}
					if req.Height == location.Height {
						// Change is visible even before this node imports the spend.
						resp.Incoming = []sidecar.Transfer{{TxID: txID, Address: voter.Tx.Tx.FromAddress.String(), VaultPubKey: source.String(), Amount: 5 * moneroAtomicToThorchain, Height: uint64(req.Height), Note: voter.Tx.Tx.Memo}}
						if stored != nil {
							stored.Height, stored.Locked = uint64(req.Height), false
							resp.Outgoing = []sidecar.Transfer{*stored}
						}
					}
					_ = json.NewEncoder(w).Encode(resp)
				case r.URL.Path == "/v1/monero/outputs/mark-spent":
					var req sidecar.MarkSpentReq
					if json.NewDecoder(r.Body).Decode(&req) != nil || req.SkipVerification || string(req.Origin) != sidecar.OutgoingOriginChainCommitted {
						http.Error(w, "unverified import", http.StatusBadRequest)
						return
					}
					if req.TxKey == "" || req.Memo != voter.Tx.Tx.Memo || req.ToAddress != voter.Tx.Tx.ToAddress.String() {
						http.Error(w, "incomplete payment metadata", http.StatusBadRequest)
						return
					}
					if stored == nil {
						stored = &sidecar.Transfer{TxID: req.TxHash, TxKey: req.TxKey, Address: req.ToAddress, FromAddress: req.FromAddress, VaultPubKey: req.VaultPubKey, Amount: req.Amount, Fee: req.Fee, Note: req.Memo, Origin: string(req.Origin), Locked: true, SpentOutputRefs: req.SpentRefs}
					}
					_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
				case r.URL.Path == "/v1/monero/outputs/mark-observed":
					markObservedCalls++
					var req sidecar.MarkObservedReq
					if json.NewDecoder(r.Body).Decode(&req) != nil || stored == nil || req.Height != uint64(location.Height) || req.BlockHash != location.BlockHash {
						http.Error(w, "not a canonical mined proof", http.StatusConflict)
						return
					}
					meta, metaErr := reorgStore.Get(location.Height)
					if metaErr != nil || meta == nil || !reorgMetaHasVaultOrigin(meta, txID) || meta.BlockHash != location.BlockHash {
						http.Error(w, "mined status requested before durable journal ownership", http.StatusConflict)
						return
					}
					if failMarkObserved {
						http.Error(w, "injected lost request before mined commit", http.StatusServiceUnavailable)
						return
					}
					stored.Height, stored.Locked = req.Height, false
					_ = json.NewEncoder(w).Encode(stored)
				case r.URL.Path == "/v1/monero/balance/"+source.String():
					pending := 0
					state := "reconciled"
					if stored != nil && stored.Locked {
						pending, state = 1, "pending_outgoing"
					}
					_ = json.NewEncoder(w).Encode(sidecar.Balance{Balance: 100, UnlockedBalance: 100, ReconciliationState: state, PendingOutgoingCount: &pending})
				case r.URL.Path == "/v1/monero/scanner/rewind":
					var req sidecar.ScannerRewindReq
					if json.NewDecoder(r.Body).Decode(&req) != nil || req.ExpectedNextHeight != next || req.ExpectedResetEpoch != epoch || req.AnchorHash != block99 {
						http.Error(w, "bad rewind", http.StatusConflict)
						return
					}
					previous := next
					next, last, epoch = req.TargetNextHeight, req.AnchorHeight, epoch+1
					lastHash, lastParent = block99, block98
					stored.Height, stored.Locked = 0, true
					rewindCalls++
					_ = json.NewEncoder(w).Encode(sidecar.ScannerRewindResp{Rewound: true, PreviousNextHeight: previous, NextHeight: next, ResetEpoch: epoch})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			dbPath := filepath.Join(t.TempDir(), "scanner")
			var db *leveldb.DB
			var c *Client
			openClient := func() {
				serverMu.Lock()
				defer serverMu.Unlock()
				var err error
				db, err = leveldb.OpenFile(dbPath, nil)
				require.NoError(t, err)
				reorgStore = NewReorgStore(db)
				signer, signerErr := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{
					SignerExpectedNetwork: "mocknet",
				}, server.URL)
				require.NoError(t, signerErr)
				scanner := NewScanner(testLogger(), signer, &scannerMemoBridge{}, common.XMRChain, common.EmptyPubKey, 72)
				scanner.SetManualVaultAddress(source, voter.Tx.Tx.FromAddress.String())
				scanner.SetReorgStore(reorgStore)
				scanner.setCurrentHeight(int64(last))
				c = &Client{
					chain: common.XMRChain, sidecar: signer, scanner: scanner, logger: testLogger(),
					thorchainBridge: &mockMoneroBridge{
						observedByTx: map[string]*ttypes.QueryObservedTxVoter{txID: voter},
					},
				}
			}
			openClient()
			defer func() { _ = db.Close() }()
			require.NoError(t, reorgStore.Set(&reorgBlockMeta{Height: 99, BlockHash: block99, PreviousHash: block98, ProvenanceComplete: true}))
			initial, err := c.scanner.FetchTxs(100, 100)
			require.NoError(t, err)
			require.Empty(t, initial.TxArray)
			serverMu.Lock()
			missingBeforeImport := stored == nil
			serverMu.Unlock()
			require.True(t, missingBeforeImport, "the scanner must pass the block before the import")
			meta, err := reorgStore.Get(100)
			require.NoError(t, err)
			require.Empty(t, meta.CustomerTransactions)
			importOnce := func() (bool, bool) {
				_, applied, retry := c.applyCommittedSpentOutputRefsOnce(context.Background(), source, common.TxID(txID), xmrOutputRefsFromPointers(refs), false)
				return applied, retry
			}
			applied, retry := importOnce()
			require.False(t, applied)
			require.True(t, retry)
			balance, err := c.sidecar.Balance(source.String())
			require.NoError(t, err)
			require.False(t, balance.IsReconciled())
			require.Equal(t, 1, *balance.PendingOutgoingCount)
			serverMu.Lock()
			initialMarkCalls := markObservedCalls
			serverMu.Unlock()
			require.Equal(t, 1, initialMarkCalls)
			// Restart Bifrost after the durable journal write but before the signer
			// accepts MarkObserved. The periodic committed import owns the retry.
			require.NoError(t, db.Close())
			openClient()
			serverMu.Lock()
			failMarkObserved = false
			serverMu.Unlock()
			applied, retry = importOnce()
			require.True(t, applied)
			require.False(t, retry)
			assertReconciled := func(height uint64) {
				balance, balanceErr := c.sidecar.Balance(source.String())
				require.NoError(t, balanceErr)
				require.True(t, balance.IsReconciled())
				require.Equal(t, uint64(100), balance.Balance)
				serverMu.Lock()
				proof := *stored
				serverMu.Unlock()
				require.Equal(t, height, proof.Height)
				require.Equal(t, sidecar.OutgoingOriginChainCommitted, proof.Origin)
				require.False(t, proof.ObservationEligible)
				items, itemErr := c.scanner.transfersToTxInItems(sidecar.Transfers{Outgoing: []sidecar.Transfer{proof}}, int64(height))
				require.NoError(t, itemErr)
				require.Empty(t, items)
				receipts, receiptErr := reorgStore.ListObservationReceipts()
				require.NoError(t, receiptErr)
				require.Empty(t, receipts)
			}
			assertReconciled(100)
			require.NoError(t, db.Close())
			openClient()
			applied, retry = importOnce()
			require.True(t, applied)
			require.False(t, retry)
			assertReconciled(100)

			// Move the same non-expiring spend onto a replacement branch. The
			// ordinary reorg path must reset and remine its accounting without
			// turning the ChainCommitted record into a source observation.
			serverMu.Lock()
			new100, new101 := testXMRHash("late-new100"), testXMRHash("late-new101")
			blocks[100] = withBlockHeader(100, defaultCoinbaseValue, new100, block99)
			blocks[101] = withBlockHeader(101, defaultCoinbaseValue, new101, new100)
			location = sidecar.CanonicalTxLocationResp{TxHash: txID, Height: 101, BlockHash: new101, PreviousHash: new100}
			serverMu.Unlock()
			replacement, err := c.scanner.FetchTxs(101, 101)
			require.NoError(t, err)
			require.Empty(t, replacement.TxArray)
			serverMu.Lock()
			reorgRewinds := rewindCalls
			serverMu.Unlock()
			require.Equal(t, 1, reorgRewinds)
			applied, retry = importOnce()
			require.True(t, applied)
			require.False(t, retry)
			assertReconciled(101)
			meta, err = reorgStore.Get(101)
			require.NoError(t, err)
			require.True(t, reorgMetaHasVaultOrigin(meta, txID))
		})
	}
}

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
	assert.Equal(t, voter.Tx.XmrTxKey, capturedTxKey)
	assert.Equal(t, voter.Tx.Tx.Memo, capturedMemo)
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
