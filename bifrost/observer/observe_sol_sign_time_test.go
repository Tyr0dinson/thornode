package observer

import (
	"context"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/mr-tron/base58"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// signTimeChainClient stands in for a chain client on the sign-time path: like the
// SOL client it needs no confirmations, and it records the committed callbacks.
type signTimeChainClient struct {
	mockChainClient
	chain    common.Chain
	observed []types.TxInItem
}

func (c *signTimeChainClient) GetChain() common.Chain { return c.chain }

func (c *signTimeChainClient) OnObservedTxIn(item types.TxInItem, _ int64) {
	c.observed = append(c.observed, item)
}

func newSignTimeObserver(t *testing.T, chain common.Chain) (*Observer, *ObserverStorage, *signTimeChainClient) {
	t.Helper()
	storage, tempDir := setupTestDB(t)
	t.Cleanup(func() { cleanupTestDB(t, storage, tempDir) })
	client := &signTimeChainClient{chain: chain}
	return &Observer{
		logger:              log.Logger,
		storage:             storage,
		onDeck:              make(map[txInKey]*types.TxIn),
		lock:                &sync.Mutex{},
		chains:              map[common.Chain]chainclients.ChainClient{chain: client},
		globalTxsQueue:      make(chan types.TxIn, 1),
		thorchainBridge:     &MockThorchainBridge2{nodeStatus: stypes.NodeStatus_Active},
		attestationGossip:   &AttestationGossip{logger: log.Logger, host: NewMockHost(nil)},
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
		lastNodeStatus:      stypes.NodeStatus_Active,
	}, storage, client
}

// signTimeTxIn is the TxIn Signer.processTransaction hands to ObserveSigned after a
// successful broadcast (bifrost/signer/sign.go); TestSignTimeObservationHandedToObserver
// in bifrost/signer pins it.
func signTimeTxIn(chain common.Chain, item *types.TxInItem) types.TxIn {
	return types.TxIn{
		Chain:                  chain,
		TxArray:                []*types.TxInItem{item},
		MemPool:                true,
		Filtered:               true,
		ConfirmationRequired:   0,
		AllowFutureObservation: true,
	}
}

// solSignTimeItem is the observation SOLClient.finishSign returns: the THORChain height
// of the outbound as the block height, the tx signature as the id, the base fee as gas.
func solSignTimeItem(t *testing.T, thorchainHeight int64) *types.TxInItem {
	t.Helper()
	vault := stypes.GetRandomEd25519PubKey()
	from, err := vault.GetAddress(common.SOLChain)
	require.NoError(t, err)
	to, err := stypes.GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	require.NoError(t, err)
	sig := make([]byte, 64)
	_, err = rand.Read(sig)
	require.NoError(t, err)
	return types.NewTxInItem(
		thorchainHeight,
		base58.Encode(sig),
		"OUT:"+stypes.GetRandomTxHash().String(),
		from.String(),
		to.String(),
		common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(250_000_000))),
		common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(5000))},
		vault,
		"",
		"",
		nil,
	)
}

func ethSignTimeItem(t *testing.T, thorchainHeight int64) *types.TxInItem {
	t.Helper()
	vault := stypes.GetRandomPubKey()
	from, err := vault.GetAddress(common.ETHChain)
	require.NoError(t, err)
	to, err := stypes.GetRandomPubKey().GetAddress(common.ETHChain)
	require.NoError(t, err)
	return types.NewTxInItem(
		thorchainHeight,
		stypes.GetRandomTxHash().String(),
		"OUT:"+stypes.GetRandomTxHash().String(),
		from.String(),
		to.String(),
		common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(250_000_000))),
		common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(80_000))},
		vault,
		"",
		"",
		nil,
	)
}

// observeSignTime drives a sign-time observation through the observer as Bifrost
// does: ObserveSigned, the TxIn queue worker's processObservedTx, then one deck send.
// It returns what the observer sends to THORNode.
func observeSignTime(t *testing.T, o *Observer, txIn types.TxIn) common.ObservedTxs {
	t.Helper()
	o.ObserveSigned(txIn)
	require.NoError(t, o.processObservedTx(<-o.globalTxsQueue))
	return sendDeckOnce(t, o)
}

func sendDeckOnce(t *testing.T, o *Observer) common.ObservedTxs {
	t.Helper()
	var sent common.ObservedTxs
	o.sendObservedTxsFn = func(txs common.ObservedTxs, allowFuture bool) {
		require.True(t, allowFuture, "a sign-time observation is sent as a future observation")
		sent = append(sent, txs...)
	}
	o.sendDeck(context.Background())
	return sent
}

// zly (!5272): THORNode processes a SOL outbound only on final consensus, but the
// signers' sign-time observations reached it as final. The signer reports the tx with
// ConfirmationRequired 0, so the observer sent it with FinaliseHeight == BlockHeight,
// and a quorum of those votes took the finalised-consensus branch before the tx landed.
// A sign-time SOL observation must reach THORNode as non-final, and once THORNode has
// committed it the observer must neither keep nor resend it: the scanners report the
// landed or failed tx as their own final observation.
func TestSOLSignTimeObservationReachesThorchainNonFinal(t *testing.T) {
	o, storage, client := newSignTimeObserver(t, common.SOLChain)
	const thorchainHeight = int64(950)
	item := solSignTimeItem(t, thorchainHeight)

	sent := observeSignTime(t, o, signTimeTxIn(common.SOLChain, item))
	require.Len(t, sent, 1)
	obs := sent[0]
	require.Equal(t, item.Tx, obs.Tx.ID.String())
	require.Equal(t, thorchainHeight, obs.BlockHeight)
	require.False(t, obs.IsFinal(),
		"a SOL sign-time observation is sent before the tx lands; THORNode must not see it as final (FinaliseHeight %d, BlockHeight %d)",
		obs.FinaliseHeight, obs.BlockHeight)

	// THORNode commits our attestation.
	o.handleObservedTxCommitted(obs, true)
	require.Len(t, client.observed, 1, "the chain client still learns the outbound was committed")
	require.Empty(t, o.onDeck, "a committed sign-time observation must not stay on deck")
	persisted, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Empty(t, persisted, "a committed sign-time observation must not stay in storage")
	require.Empty(t, sendDeckOnce(t, o), "a committed sign-time observation must not be resent")
}

// Until THORNode commits it, a SOL sign-time observation is resent each deck round
// in the same non-final shape, so a vote lost in transit is retried.
func TestSOLSignTimeObservationResentNonFinalUntilCommitted(t *testing.T) {
	o, _, _ := newSignTimeObserver(t, common.SOLChain)
	first := observeSignTime(t, o, signTimeTxIn(common.SOLChain, solSignTimeItem(t, 950)))
	require.Len(t, first, 1)
	again := sendDeckOnce(t, o)
	require.Len(t, again, 1)
	require.True(t, first[0].Equals(again[0]), "the resent vote must be the same vote")
	require.False(t, again[0].IsFinal())
}

// The correction is specific to SOL: other chains' sign-time observations still reach
// THORNode as final and leave the deck on commit.
func TestNonSOLSignTimeObservationStaysFinal(t *testing.T) {
	o, storage, client := newSignTimeObserver(t, common.ETHChain)
	const thorchainHeight = int64(950)
	item := ethSignTimeItem(t, thorchainHeight)

	sent := observeSignTime(t, o, signTimeTxIn(common.ETHChain, item))
	require.Len(t, sent, 1)
	obs := sent[0]
	require.Equal(t, thorchainHeight, obs.BlockHeight)
	require.True(t, obs.IsFinal(), "non-SOL sign-time observations are unchanged")

	o.handleObservedTxCommitted(obs, true)
	require.Len(t, client.observed, 1)
	require.Empty(t, o.onDeck)
	persisted, err := storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Empty(t, persisted)
}
