package monero

import (
	"testing"

	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
)

type networkFeeBridge struct {
	thorclient.ThorchainBridge

	currentSize uint64
	currentRate uint64

	postCalls  int
	postHeight int64
	postSize   uint64
	postRate   uint64
}

func (b *networkFeeBridge) GetNetworkFee(chain common.Chain) (uint64, uint64, error) {
	if !chain.Equals(common.XMRChain) {
		return 0, 0, nil
	}
	return b.currentSize, b.currentRate, nil
}

func (b *networkFeeBridge) PostNetworkFee(height int64, chain common.Chain, transactionSize, transactionRate uint64) (common.TxID, error) {
	if !chain.Equals(common.XMRChain) {
		return "", nil
	}
	b.postCalls++
	b.postHeight = height
	b.postSize = transactionSize
	b.postRate = transactionRate
	return common.TxID("XMR_TEST_NETWORK_FEE_TX"), nil
}

func TestXMRNecessaryFeeFromString(t *testing.T) {
	t.Parallel()

	msg := "failed to construct signable transaction: not enough funds (inputs 499038940000, outputs 498858940000, necessary_fee Some(185400000))"
	if got := xmrNecessaryFeeFromString(msg); got != 185_400_000 {
		t.Fatalf("expected parsed necessary fee 185400000, got %d", got)
	}
}

func TestObserveXMRActualFeeEstimatePostsHigherObservedFee(t *testing.T) {
	t.Parallel()

	bridge := &networkFeeBridge{
		currentSize: 1_500,
		currentRate: 12,
	}
	client := &Client{
		logger:          log.Logger,
		chain:           common.XMRChain,
		thorchainBridge: bridge,
		scanner: &Scanner{
			logger:             log.Logger,
			currentBlockHeight: atomic.NewInt64(0),
			lastFee:            atomic.NewUint64(0),
			lastFeeWeight:      atomic.NewUint64(0),
		},
	}

	client.observeXMRActualFeeEstimate(26_773, 120_000, 185_400_000)

	txSize, feeRate := client.scanner.GetNetworkFee()
	if txSize != 1_545 || feeRate != 12 {
		t.Fatalf("expected scanner network fee 1545/12, got %d/%d", txSize, feeRate)
	}
	if bridge.postCalls != 1 {
		t.Fatalf("expected one network fee post, got %d", bridge.postCalls)
	}
	if bridge.postHeight != 26_773 || bridge.postSize != 1_545 || bridge.postRate != 12 {
		t.Fatalf("unexpected posted network fee: height=%d size=%d rate=%d", bridge.postHeight, bridge.postSize, bridge.postRate)
	}
}

func TestObserveXMRActualFeeEstimateSkipsPostWhenNotHigher(t *testing.T) {
	t.Parallel()

	bridge := &networkFeeBridge{
		currentSize: 2_000,
		currentRate: 12,
	}
	client := &Client{
		logger:          log.Logger,
		chain:           common.XMRChain,
		thorchainBridge: bridge,
		scanner: &Scanner{
			logger:             log.Logger,
			currentBlockHeight: atomic.NewInt64(0),
			lastFee:            atomic.NewUint64(0),
			lastFeeWeight:      atomic.NewUint64(0),
		},
	}

	client.observeXMRActualFeeEstimate(26_773, 120_000, 185_400_000)

	if bridge.postCalls != 0 {
		t.Fatalf("expected no network fee post when current fee already covers observed tx, got %d", bridge.postCalls)
	}
}
