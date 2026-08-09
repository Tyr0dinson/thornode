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

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
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
