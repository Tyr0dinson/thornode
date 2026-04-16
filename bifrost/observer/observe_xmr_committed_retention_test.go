package observer

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type validatingObservedTxChainClient struct {
	mockChainClient
	chain      common.Chain
	validateFn func(common.ObservedTx) error
}

func (m *validatingObservedTxChainClient) GetChain() common.Chain {
	if !m.chain.IsEmpty() {
		return m.chain
	}
	return common.XMRChain
}

func (m *validatingObservedTxChainClient) ValidateObservedTxIn(tx common.ObservedTx) error {
	if m.validateFn != nil {
		return m.validateFn(tx)
	}
	return nil
}

func newCommittedRetentionTestObserver(t *testing.T, chainClient chainclients.ChainClient) *Observer {
	t.Helper()

	storage, err := NewObserverStorage(t.TempDir(), config.GetBifrost().ObserverLevelDB)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, storage.Close())
	})

	return &Observer{
		logger:  zerolog.Nop(),
		chains:  map[common.Chain]chainclients.ChainClient{common.XMRChain: chainClient},
		lock:    &sync.Mutex{},
		onDeck:  make(map[txInKey]*types.TxIn),
		storage: storage,
	}
}

func newCommittedRetentionTestTx(t *testing.T) (*types.TxIn, common.ObservedTx, txInKey) {
	t.Helper()

	txID := common.TxID(strings.Repeat("a", 64))
	memo := "OUT:" + strings.Repeat("b", 64)
	sender := common.Address(common.XMRSeededDummyAddress('8', []byte("sender")))
	recipient := common.Address(common.XMRSeededDummyAddress('4', []byte("recipient")))
	vaultPubKey := thortypes.GetRandomPubKey()
	blockHeight := int64(100)
	coins := common.Coins{common.NewCoin(common.XMRAsset, cosmos.NewUint(123456))}
	gas := common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(19200))}

	deck := &types.TxIn{
		Chain:                common.XMRChain,
		ConfirmationRequired: 0,
		TxArray: []*types.TxInItem{
			{
				BlockHeight:         blockHeight,
				Tx:                  txID.String(),
				Memo:                memo,
				Sender:              sender.String(),
				To:                  recipient.String(),
				Coins:               coins,
				Gas:                 gas,
				ObservedVaultPubKey: vaultPubKey,
			},
		},
	}

	obsTx := common.ObservedTx{
		Tx: common.Tx{
			ID:          txID,
			Chain:       common.XMRChain,
			FromAddress: sender,
			ToAddress:   recipient,
			Coins:       coins,
			Gas:         gas,
			Memo:        memo,
		},
		BlockHeight:    blockHeight,
		FinaliseHeight: blockHeight,
		ObservedPubKey: vaultPubKey,
	}

	return deck, obsTx, TxInKey(deck)
}

func TestHandleObservedTxCommittedKeepsFinalXMROnDeckUntilValidationSucceeds(t *testing.T) {
	var validateCalls atomic.Int32
	chainClient := &validatingObservedTxChainClient{
		chain: common.XMRChain,
		validateFn: func(tx common.ObservedTx) error {
			validateCalls.Add(1)
			return fmt.Errorf("xmr finalized outbound is still waiting for tracked vault-owned outputs/key images for %s", tx.Tx.ID)
		},
	}
	observer := newCommittedRetentionTestObserver(t, chainClient)
	deck, obsTx, key := newCommittedRetentionTestTx(t)

	observer.onDeck[key] = deck
	require.NoError(t, observer.storage.AddOrUpdateTx(deck))

	observer.handleObservedTxCommitted(obsTx)

	require.EqualValues(t, 1, validateCalls.Load())
	observer.lock.Lock()
	deckTx, exists := observer.onDeck[key]
	observer.lock.Unlock()
	require.True(t, exists)
	require.Len(t, deckTx.TxArray, 1)
	assert.Equal(t, obsTx.Tx.ID.String(), deckTx.TxArray[0].Tx)

	storageTxs, err := observer.storage.GetOnDeckTxs()
	require.NoError(t, err)
	require.Len(t, storageTxs, 1)
	require.Len(t, storageTxs[0].TxArray, 1)
	assert.Equal(t, obsTx.Tx.ID.String(), storageTxs[0].TxArray[0].Tx)
}

func TestHandleObservedTxCommittedRemovesFinalXMRFromDeckAfterValidationSucceeds(t *testing.T) {
	var validateCalls atomic.Int32
	chainClient := &validatingObservedTxChainClient{
		chain: common.XMRChain,
		validateFn: func(tx common.ObservedTx) error {
			validateCalls.Add(1)
			return nil
		},
	}
	observer := newCommittedRetentionTestObserver(t, chainClient)
	deck, obsTx, key := newCommittedRetentionTestTx(t)

	observer.onDeck[key] = deck
	require.NoError(t, observer.storage.AddOrUpdateTx(deck))

	observer.handleObservedTxCommitted(obsTx)

	require.EqualValues(t, 1, validateCalls.Load())
	observer.lock.Lock()
	_, exists := observer.onDeck[key]
	observer.lock.Unlock()
	assert.False(t, exists)

	storageTxs, err := observer.storage.GetOnDeckTxs()
	require.NoError(t, err)
	assert.Empty(t, storageTxs)
}
