package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func allowSpentOutputRefsCatchup(c *Client) {
	c.SetSpentOutputRefsCatchupGuard(func(common.ObservedTx) (func(), bool) {
		return func() {}, true
	})
}

func spentOutputRefsCatchupVoter(
	t *testing.T,
	vault common.PubKey,
	spendTxHash string,
	minedHeight int64,
) *ttypes.QueryObservedTxVoter {
	t.Helper()
	vaultAddr, err := vault.GetAddress(common.XMRChain)
	require.NoError(t, err)
	finaliseHeight := minedHeight + 6
	return &ttypes.QueryObservedTxVoter{
		Tx: ttypes.QueryObservedTx{
			Tx: common.Tx{
				ID:          common.TxID(spendTxHash),
				Chain:       common.XMRChain,
				FromAddress: vaultAddr,
				ToAddress:   testOutboundXMRDestination(),
				Coins:       common.NewCoins(common.NewCoin(common.XMRAsset, cosmos.NewUint(50))),
				Gas:         common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))},
			},
			BlockHeight:    finaliseHeight,
			ObservedPubKey: vault,
			FinaliseHeight: finaliseHeight,
			XmrTxKey:       strings.Repeat("5", 64),
			XmrMinedHeight: minedHeight,
		},
	}
}

func spentOutputRefsCatchupClient(
	t *testing.T,
	endpoint string,
	vault common.PubKey,
	refs []*common.OutputRef,
	voters map[string]*ttypes.QueryObservedTxVoter,
) *Client {
	t.Helper()
	signer, err := sidecar.NewClientWithEndpoint(config.BifrostChainConfiguration{}, endpoint)
	require.NoError(t, err)
	return &Client{
		chain:   common.XMRChain,
		sidecar: signer,
		logger:  testLogger(),
		thorchainBridge: &mockMoneroBridge{
			spentRefsByVaultKey: map[string][]*common.OutputRef{vault.String(): refs},
			observedByTx:        voters,
			pubKeys:             []thorclient.PubKeyContractAddressPair{{PubKey: vault, Algo: common.SigningAlgoEd25519}},
		},
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
	var capturedTxKey string
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
			XmrTxKey:       strings.Repeat("5", 64),
			XmrMinedHeight: 94,
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
	allowSpentOutputRefsCatchup(c)

	c.catchupSpentOutputRefs()

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, frostKey.String(), capturedVault)
	assert.Equal(t, spendTxHash, capturedTxHash)
	assert.Equal(t, strings.Repeat("5", 64), capturedTxKey)
	assert.False(t, skipVerification)
	require.Equal(t, 3, len(capturedRefs))
	assert.Equal(t, refs[0].TxHash, capturedRefs[0].TxHash)
	assert.Equal(t, refs[0].KeyImage, capturedRefs[0].KeyImage)
	assert.Equal(t, refs[1].TxHash, capturedRefs[1].TxHash)
	assert.Equal(t, refs[1].KeyImage, capturedRefs[1].KeyImage)
	assert.Equal(t, refs[2].TxHash, capturedRefs[2].TxHash)
	assert.Equal(t, refs[2].KeyImage, capturedRefs[2].KeyImage)
}

func TestCatchupSpentOutputRefsRejectsRevertedVoterBeforeMutation(t *testing.T) {
	vault := testVaultPubKey(t, "frost-catchup-reverted")
	spendTxHash := testXMRHash("catchup-reverted-spend")
	refs := []*common.OutputRef{testXMROutputRef("catchup-reverted-ref", 0, spendTxHash)}
	voter := spentOutputRefsCatchupVoter(t, vault, spendTxHash, 100)
	voter.Reverted = true

	var sidecarCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sidecarCalls.Add(1)
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: 1})
	}))
	defer server.Close()

	client := spentOutputRefsCatchupClient(t, server.URL, vault, refs, map[string]*ttypes.QueryObservedTxVoter{
		spendTxHash: voter,
	})
	var guardCalls atomic.Int32
	client.SetSpentOutputRefsCatchupGuard(func(common.ObservedTx) (func(), bool) {
		guardCalls.Add(1)
		return func() {}, true
	})

	client.catchupSpentOutputRefs()

	assert.Zero(t, sidecarCalls.Load())
	assert.Zero(t, guardCalls.Load(), "a reverted voter must stop before errata admission and plan resolution")
}

func TestCatchupSpentOutputRefsUsesCanonicalHeightErrataGuard(t *testing.T) {
	vault := testVaultPubKey(t, "frost-catchup-canonical-height")
	orphanHash := testXMRHash("catchup-orphaned-spend")
	reminedHash := testXMRHash("catchup-remined-spend")
	refs := []*common.OutputRef{
		testXMROutputRef("catchup-orphaned-ref", 0, orphanHash),
		testXMROutputRef("catchup-remined-ref", 0, reminedHash),
	}
	voters := map[string]*ttypes.QueryObservedTxVoter{
		orphanHash:  spentOutputRefsCatchupVoter(t, vault, orphanHash, 100),
		reminedHash: spentOutputRefsCatchupVoter(t, vault, reminedHash, 101),
	}

	mutated := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sidecar.MarkSpentReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mutated <- req.TxHash
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
	}))
	defer server.Close()

	client := spentOutputRefsCatchupClient(t, server.URL, vault, refs, voters)
	var releases atomic.Int32
	client.SetSpentOutputRefsCatchupGuard(func(tx common.ObservedTx) (func(), bool) {
		minedHeight, err := tx.CanonicalMinedHeight()
		require.NoError(t, err)
		if minedHeight == 100 {
			return nil, false
		}
		return func() { releases.Add(1) }, true
	})

	client.catchupSpentOutputRefs()

	select {
	case got := <-mutated:
		assert.Equal(t, reminedHash, got, "only the different-height canonical re-mine may mutate the sidecar")
	default:
		t.Fatal("canonical re-mine did not reach the sidecar")
	}
	select {
	case unexpected := <-mutated:
		t.Fatalf("tombstoned spend reached the sidecar: %s", unexpected)
	default:
	}
	assert.Equal(t, int32(1), releases.Load())
}

func TestCatchupSpentOutputRefsReleasesBarrierBetweenAttempts(t *testing.T) {
	vault := testVaultPubKey(t, "frost-catchup-barrier")
	firstHash := testXMRHash("catchup-barrier-first")
	secondHash := testXMRHash("catchup-barrier-second")
	refs := []*common.OutputRef{
		testXMROutputRef("catchup-barrier-first-ref", 0, firstHash),
		testXMROutputRef("catchup-barrier-second-ref", 0, secondHash),
	}
	voters := map[string]*ttypes.QueryObservedTxVoter{
		firstHash:  spentOutputRefsCatchupVoter(t, vault, firstHash, 100),
		secondHash: spentOutputRefsCatchupVoter(t, vault, secondHash, 101),
	}

	started := make(chan string, 2)
	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once
	var sidecarCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sidecar.MarkSpentReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		started <- req.TxHash
		if sidecarCalls.Add(1) == 1 {
			<-releaseFirst
		}
		_ = json.NewEncoder(w).Encode(sidecar.MarkSpentResp{Marked: len(req.SpentRefs)})
	}))
	t.Cleanup(server.Close)

	client := spentOutputRefsCatchupClient(t, server.URL, vault, refs, voters)
	var barrier sync.RWMutex
	client.SetSpentOutputRefsCatchupGuard(func(common.ObservedTx) (func(), bool) {
		barrier.RLock()
		return barrier.RUnlock, true
	})

	done := make(chan struct{})
	go func() {
		client.catchupSpentOutputRefs()
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the first catch-up attempt")
	}

	writerAcquired := make(chan struct{})
	releaseWriter := make(chan struct{})
	var releaseWriterOnce sync.Once
	go func() {
		barrier.Lock()
		close(writerAcquired)
		<-releaseWriter
		barrier.Unlock()
	}()
	t.Cleanup(func() {
		releaseFirstOnce.Do(func() { close(releaseFirst) })
		releaseWriterOnce.Do(func() { close(releaseWriter) })
	})

	deadline := time.Now().Add(time.Second)
	for {
		if !barrier.TryRLock() {
			break
		}
		barrier.RUnlock()
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the errata writer to queue")
		}
		time.Sleep(time.Millisecond)
	}
	releaseFirstOnce.Do(func() { close(releaseFirst) })
	select {
	case <-writerAcquired:
	case txHash := <-started:
		t.Fatalf("second catch-up mutation %s bypassed the queued errata writer", txHash)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the errata writer")
	}

	select {
	case txHash := <-started:
		t.Fatalf("catch-up mutation %s started while the errata writer held the barrier", txHash)
	case <-time.After(25 * time.Millisecond):
	}
	releaseWriterOnce.Do(func() { close(releaseWriter) })
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for catch-up to resume after errata cleanup")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for catch-up completion")
	}
}

func TestCommittedXMRWireMemoUsesExactMemolessPlan(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-catchup-wire-memo")
	vaultAddr, err := frostKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := testXMRHash("catchup-wire-spend")
	inboundID := common.TxID(testXMRHash("catchup-wire-inbound"))
	semanticMemo := "OUT:" + inboundID.String()
	destination := testOutboundXMRDestination()
	coin := common.NewCoin(common.XMRAsset, cosmos.NewUint(50))
	gas := common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}

	outboundVoter := &ttypes.QueryObservedTxVoter{
		Height: 500,
		Tx: ttypes.QueryObservedTx{
			Tx: common.Tx{
				ID:          common.TxID(spendTxHash),
				Chain:       common.XMRChain,
				FromAddress: vaultAddr,
				ToAddress:   destination,
				Coins:       common.NewCoins(coin),
				Gas:         gas,
				Memo:        semanticMemo,
			},
			BlockHeight:    100,
			ObservedPubKey: frostKey,
			FinaliseHeight: 100,
			XmrTxKey:       strings.Repeat("5", 64),
			XmrMinedHeight: 94,
		},
	}
	inboundVoter := &ttypes.QueryObservedTxVoter{
		TxID:   inboundID,
		Height: 470,
		OutTxs: common.Txs{{ID: common.TxID(spendTxHash)}},
		Actions: []ttypes.TxOutItem{
			{
				Chain:            common.XMRChain,
				ToAddress:        destination,
				VaultPubKeyFrost: frostKey,
				Coin:             coin,
				Memo:             "",
				OriginalMemo:     semanticMemo,
				MaxGas:           gas,
			},
		},
	}
	plan := stypes.TxArrayItem{
		Chain:            common.XMRChain,
		ToAddress:        destination,
		VaultPubKeyFrost: frostKey,
		Coin:             coin,
		Memo:             "",
		OriginalMemo:     semanticMemo,
		MaxGas:           gas,
		InHash:           inboundID,
		OutHash:          common.TxID(spendTxHash),
	}
	stalePlan := plan
	stalePlan.Memo = semanticMemo
	stalePlan.OriginalMemo = semanticMemo
	stalePlan.OutHash = common.BlankTxID
	bridge := &mockMoneroBridge{
		observedByTx: map[string]*ttypes.QueryObservedTxVoter{
			spendTxHash:        outboundVoter,
			inboundID.String(): inboundVoter,
		},
		keysignByHeight: map[int64]stypes.TxOut{
			480: {Height: 480, TxArray: []stypes.TxArrayItem{plan}},
			490: {Height: 490, TxArray: []stypes.TxArrayItem{stalePlan}},
		},
		blockHeight: 500,
		mimirValues: map[string]int64{
			constants.SigningTransactionPeriod.String(): 10,
			constants.RescheduleCoalesceBlocks.String(): 0,
		},
	}
	c := &Client{chain: common.XMRChain, logger: testLogger(), thorchainBridge: bridge}
	refs := []common.OutputRef{*testXMROutputRef("catchup-wire-ref", 0, spendTxHash)}
	queried, found := queryObservedXMRTx(outboundVoter, common.TxID(spendTxHash))
	require.True(t, found)
	assert.Equal(t, int64(94), queried.XmrMinedHeight)
	queried.SpentOutputRefs = refs
	require.NoError(t, validateXMRSpentRefsForSidecar(queried))
	wireMemo, found := c.committedXMRWireMemo(queried, outboundVoter.Height)
	require.True(t, found)
	assert.Empty(t, wireMemo)
	firstCalls := bridge.capturedKeysignCalls()
	require.NotEmpty(t, firstCalls)

	observed, outbound, ok := c.observedXMROutboundForSpentRefs(spendTxHash, frostKey, refs)
	require.True(t, ok)
	require.NotNil(t, outbound)
	assert.Equal(t, semanticMemo, observed.Tx.Memo, "catchup must retain the semantic observation memo")
	assert.Empty(t, outbound.Memo, "sidecar metadata must use the planned empty wire memo")
	assert.Equal(t, firstCalls, bridge.capturedKeysignCalls(), "a cached empty memo must not rescan keysign history")

	conflicting := plan
	conflicting.Memo = semanticMemo
	conflicting.OriginalMemo = semanticMemo
	bridge.keysignByHeight[480] = stypes.TxOut{Height: 480, TxArray: []stypes.TxArrayItem{
		plan,
		conflicting,
	}}
	c.committedWireMemoMu.Lock()
	c.committedWireMemoCache = nil
	c.committedWireMemoMu.Unlock()
	_, _, ok = c.observedXMROutboundForSpentRefs(spendTxHash, frostKey, refs)
	assert.False(t, ok, "conflicting exact plans must fail closed")

	uncommitted := *inboundVoter
	uncommitted.OutTxs = nil
	bridge.observedByTx[inboundID.String()] = &uncommitted
	beforeUncommitted := len(bridge.capturedKeysignCalls())
	_, _, ok = c.observedXMROutboundForSpentRefs(spendTxHash, frostKey, refs)
	assert.False(t, ok, "history must not be searched without an inbound-voter commitment")
	assert.Len(t, bridge.capturedKeysignCalls(), beforeUncommitted)
}

func TestCommittedXMRWireMemoPreservesInternalMemo(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-catchup-internal-wire-memo")
	vaultAddr, err := frostKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := common.TxID(testXMRHash("catchup-internal-wire-spend"))
	destination := testOutboundXMRDestination()
	semanticMemo := "MIGRATE:123"
	coin := common.NewCoin(common.XMRAsset, cosmos.NewUint(50))
	gas := common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}
	observed := common.ObservedTx{
		Tx: common.Tx{
			ID:          spendTxHash,
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   destination,
			Coins:       common.NewCoins(coin),
			Gas:         gas,
			Memo:        semanticMemo,
		},
		ObservedPubKey: frostKey,
	}
	bridge := &mockMoneroBridge{}
	c := &Client{chain: common.XMRChain, logger: testLogger(), thorchainBridge: bridge}

	wireMemo, found := c.committedXMRWireMemo(observed, 150)
	require.True(t, found)
	assert.Equal(t, semanticMemo, wireMemo)
	assert.Equal(t, semanticMemo, observed.Tx.Memo, "wire-memo resolution must not mutate the observation")
	assert.Empty(t, bridge.capturedKeysignCalls(), "internal protocol memos are never stripped and need no history lookup")
}

func TestCommittedXMRWireMemoRetriesFutureCoalescedHistory(t *testing.T) {
	t.Parallel()

	frostKey := testVaultPubKey(t, "frost-catchup-future-history")
	vaultAddr, err := frostKey.GetAddress(common.XMRChain)
	require.NoError(t, err)
	spendTxHash := common.TxID(testXMRHash("catchup-future-spend"))
	inboundID := common.TxID(testXMRHash("catchup-future-inbound"))
	semanticMemo := "OUT:" + inboundID.String()
	destination := testOutboundXMRDestination()
	coin := common.NewCoin(common.XMRAsset, cosmos.NewUint(50))
	gas := common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(1))}
	observed := common.ObservedTx{
		Tx: common.Tx{
			ID:          spendTxHash,
			Chain:       common.XMRChain,
			FromAddress: vaultAddr,
			ToAddress:   destination,
			Coins:       common.NewCoins(coin),
			Gas:         gas,
			Memo:        semanticMemo,
		},
		ObservedPubKey: frostKey,
	}
	plan := stypes.TxArrayItem{
		Chain:            common.XMRChain,
		ToAddress:        destination,
		VaultPubKeyFrost: frostKey,
		Coin:             coin,
		Memo:             "",
		OriginalMemo:     semanticMemo,
		MaxGas:           gas,
		InHash:           inboundID,
		OutHash:          spendTxHash,
	}
	bridge := &mockMoneroBridge{
		observedByTx: map[string]*ttypes.QueryObservedTxVoter{
			inboundID.String(): {
				TxID:   inboundID,
				Height: 75,
				Tx: ttypes.QueryObservedTx{
					OutHashes: []string{strings.ToLower(spendTxHash.String())},
				},
			},
		},
		keysignByHeight: map[int64]stypes.TxOut{
			110: {Height: 110, TxArray: []stypes.TxArrayItem{plan}},
		},
		blockHeight: 105,
		mimirValues: map[string]int64{
			constants.SigningTransactionPeriod.String(): 10,
			constants.RescheduleCoalesceBlocks.String(): 10,
		},
	}
	c := &Client{chain: common.XMRChain, logger: testLogger(), thorchainBridge: bridge}

	_, found := c.committedXMRWireMemo(observed, 101)
	assert.False(t, found, "a future keysign height must be retried, not queried early")
	firstCalls := bridge.capturedKeysignCalls()
	require.NotEmpty(t, firstCalls)
	for _, height := range firstCalls {
		assert.GreaterOrEqual(t, height, int64(75))
		assert.LessOrEqual(t, height, int64(105))
	}

	bridge.blockHeight = 10_000
	wireMemo, found := c.committedXMRWireMemo(observed, 101)
	require.True(t, found, "a failed lookup must not be negatively cached")
	assert.Empty(t, wireMemo)
	secondCalls := bridge.capturedKeysignCalls()[len(firstCalls):]
	require.NotEmpty(t, secondCalls)
	assert.Contains(t, secondCalls, int64(110))
	for _, height := range secondCalls {
		assert.GreaterOrEqual(t, height, int64(75))
		assert.LessOrEqual(t, height, int64(110), "current height must not move the stable search anchor")
	}
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
	allowSpentOutputRefsCatchup(c)

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
