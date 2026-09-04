package observer

import (
	"testing"

	lru "github.com/hashicorp/golang-lru"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestFilterObservationsPreservesBounced walks a scanner-produced Bounced
// outbound through the normal Filtered == false observer path (which clones the
// item) and asserts the signed ObservedTx still carries the flag. This is the
// propagation TxInItem.Copy() once silently dropped.
func TestFilterObservationsPreservesBounced(t *testing.T) {
	t.Parallel()

	vault := stypes.GetRandomPubKey()
	vaultAddr, err := vault.GetAddress(common.ETHChain)
	require.NoError(t, err)
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(nil, &metrics.Metrics{})
	require.NoError(t, err)
	pubkeyMgr.AddPubKey(vault, false, common.SigningAlgoSecp256k1)
	signedCache, err := lru.New(10)
	require.NoError(t, err)
	obs := &Observer{
		pubkeyMgr:           pubkeyMgr,
		signedTxOutCache:    signedCache,
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
	}

	item := &types.TxInItem{
		BlockHeight: 100,
		Tx:          stypes.GetRandomTxHash().String(),
		Memo:        "OUT:" + stypes.GetRandomTxHash().String(),
		Sender:      vaultAddr.String(),
		To:          stypes.GetRandomETHAddress().String(),
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1024000))),
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(37500))},
		Bounced:     true,
	}

	filtered := obs.filterObservations(common.ETHChain, []*types.TxInItem{item}, false)
	require.Len(t, filtered, 1)
	require.True(t, filtered[0].Bounced)

	obsTxs, invalid, err := obs.getThorchainTxIns(&types.TxIn{Chain: common.ETHChain, TxArray: filtered}, true, 100)
	require.NoError(t, err)
	require.Empty(t, invalid)
	require.Len(t, obsTxs, 1)
	require.True(t, obsTxs[0].ObservedPubKey.Equals(vault))
	require.True(t, obsTxs[0].Bounced)
}
