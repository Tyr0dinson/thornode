package observer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/ebifrost"
)

func TestQuorumTxCommittedValidatesXMROutboundWithTxKeys(t *testing.T) {
	t.Run("validates xmr outbound with tx keys on committed event", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)

		called := make(chan common.ObservedTx, 1)
		ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error {
			called <- tx
			return nil
		})

		tx := common.ObservedTx{
			Tx:     common.Tx{ID: "xmr-committed-1", Chain: common.XMRChain},
			TxKeys: []string{"11"},
		}
		qtx := common.QuorumTx{
			ObsTx:   tx,
			Inbound: false,
			Attestations: []*common.Attestation{
				{PubKey: []byte("other-node"), Signature: []byte("sig")},
			},
		}

		payload, err := qtx.Marshal()
		require.NoError(t, err)

		ag.handleQuorumTxCommitted(&ebifrost.EventNotification{
			Payload: payload,
		})

		select {
		case validated := <-called:
			assert.Equal(t, tx.Tx.ID, validated.Tx.ID)
			assert.Equal(t, tx.TxKeys, validated.TxKeys)
		case <-time.After(time.Second):
			t.Fatal("expected committed XMR outbound to trigger tx-key validation")
		}
	})

	t.Run("does not validate when tx keys are missing", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)

		var called bool
		ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error {
			called = true
			return nil
		})

		tx := common.ObservedTx{
			Tx: common.Tx{ID: "xmr-committed-2", Chain: common.XMRChain},
		}
		qtx := common.QuorumTx{
			ObsTx:   tx,
			Inbound: false,
			Attestations: []*common.Attestation{
				{PubKey: []byte("other-node"), Signature: []byte("sig")},
			},
		}

		payload, err := qtx.Marshal()
		require.NoError(t, err)

		ag.handleQuorumTxCommitted(&ebifrost.EventNotification{
			Payload: payload,
		})

		time.Sleep(50 * time.Millisecond)
		assert.False(t, called)
	})

	t.Run("does not validate non-xmr committed events", func(t *testing.T) {
		ag, _, _, _, _, _ := setupTestGossip(t)

		var called bool
		ag.SetObserverValidateXMRTx(func(tx common.ObservedTx) error {
			called = true
			return nil
		})

		tx := common.ObservedTx{
			Tx:     common.Tx{ID: "btc-committed-1", Chain: common.BTCChain},
			TxKeys: []string{"11"},
		}
		qtx := common.QuorumTx{
			ObsTx:   tx,
			Inbound: false,
			Attestations: []*common.Attestation{
				{PubKey: []byte("other-node"), Signature: []byte("sig")},
			},
		}

		payload, err := qtx.Marshal()
		require.NoError(t, err)

		ag.handleQuorumTxCommitted(&ebifrost.EventNotification{
			Payload: payload,
		})

		time.Sleep(50 * time.Millisecond)
		assert.False(t, called)
	})
}
