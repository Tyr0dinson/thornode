package signer

import (
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type expiryChainClient struct {
	MockChainClient
	err                    error
	chain                  common.Chain
	signPayload            bool
	unlockedCalls          int
	mu                     sync.Mutex
	lockCalls, unlockCalls int
}

func (b *expiryChainClient) LockTxOut(types.TxOutItem) func() {
	b.mu.Lock()
	b.lockCalls++
	return func() {
		b.unlockCalls++
		b.mu.Unlock()
	}
}

func (b *expiryChainClient) GetChain() common.Chain { return b.chain }

// Record ownership inside each operation; assertions stay in the test goroutine.
func (b *expiryChainClient) recordOperationLock() {
	if b.mu.TryLock() {
		b.unlockedCalls++
		b.mu.Unlock()
	}
}

func (b *expiryChainClient) SignTx(tx types.TxOutItem, _ int64) ([]byte, []byte, *types.TxInItem, error) {
	b.signCount++
	b.recordOperationLock()
	if b.signPayload {
		return []byte("fixed-signed-payload"), tx.Checkpoint, nil, nil
	}
	return nil, tx.Checkpoint, nil, b.err
}

func (b *expiryChainClient) BroadcastTx(types.TxOutItem, []byte) (string, error) {
	b.broadcastCount++
	b.recordOperationLock()
	return "", b.err
}

func (s *SignCoverageSuite) TestProcessTransaction_ExpiredAttempts(c *C) {
	for _, chain := range []common.Chain{common.ZECChain, common.TRONChain} {
		for _, stage := range []string{"signing", "fresh broadcast", "saved broadcast"} {
			signed := stage == "saved broadcast"
			for _, expired := range []bool{false, true} {
				vaultPubKey, err := common.NewPubKey(pubkeymanager.MockPubkey)
				c.Assert(err, IsNil)
				bridge := &mockSignBridge{
					blockHeight: 100,
					constants: map[string]int64{
						constants.SigningTransactionPeriod.String(): 300,
						constants.ChurnInterval.String():            43200,
					},
					mimirs: map[string]int64{"MAXOUTBOUNDATTEMPTS": 10},
					vault:  ttypes.Vault{Status: ttypes.VaultStatus_ActiveVault},
				}
				cc := &expiryChainClient{chain: chain, signPayload: stage == "fresh broadcast", err: fmt.Errorf("tip lookup unavailable")}
				if expired {
					cc.err = fmt.Errorf("%s: %w", chain, chainclienttypes.ErrTxExpired)
				}
				storage, err := NewSignerStore("", config.LevelDBOptions{}, "")
				c.Assert(err, IsNil)
				sign := &Signer{
					logger:            log.With().Str("module", "test").Logger(),
					cfg:               config.Bifrost{Signer: config.BifrostSignerConfiguration{AutoObserve: true}},
					thorchainBridge:   bridge,
					constantsProvider: NewConstantsProvider(bridge),
					chains:            map[common.Chain]chainclients.ChainClient{chain: cc},
					pubkeyMgr:         pubkeymanager.NewMockPoolAddressValidator(),
					m:                 GetMetricForTest(c),
					storage:           storage,
					localPubKeyECDSA:  vaultPubKey,
				}
				item := TxOutStoreItem{
					TxOutItem: types.TxOutItem{Chain: chain, VaultPubKey: vaultPubKey, ToAddress: "zec-destination", Memo: "MIGRATE:90"},
					Height:    90, Round7Retry: true, Checkpoint: []byte("fixed-checkpoint"),
				}
				if signed {
					item.SignedTx = []byte("fixed-signed-payload")
					item.Observation = &types.TxInItem{Tx: "expired-observation"}
				}
				c.Assert(storage.Set(item), IsNil)
				// An expired saved observation must never reach auto-observe (the
				// observer is deliberately nil). RPC failures must retain the attempt.
				sign.processTransaction(item)
				items := storage.List()
				if expired {
					c.Assert(items, HasLen, 0)
				} else {
					c.Assert(items, HasLen, 1)
					c.Assert(items[0].Checkpoint, DeepEquals, item.Checkpoint)
					expectedSigned := item.SignedTx
					if cc.signPayload {
						expectedSigned = []byte("fixed-signed-payload")
					}
					c.Assert(items[0].SignedTx, DeepEquals, expectedSigned)
					c.Assert(items[0].Round7Retry, Equals, true)
				}
				c.Assert(cc.lockCalls, Equals, 1)
				c.Assert(cc.unlockCalls, Equals, 1)
				c.Assert(cc.signCount == 0, Equals, signed)
				c.Assert(cc.broadcastCount == 1, Equals, stage != "signing")
				c.Check(cc.unlockedCalls, Equals, 0, Commentf("%s %s expired=%t", chain, stage, expired))
				c.Assert(storage.Close(), IsNil)
			}
		}
	}
}

// broadcastLockChainClient pauses a successful broadcast while another vault
// attempts the same payment. Worker methods record state for the test goroutine.
type broadcastLockChainClient struct {
	expiryChainClient
	primaryVault     common.PubKey
	lockAttempts     chan common.PubKey
	broadcastStarted chan struct{}
	finishBroadcast  chan struct{}
	stateMu          sync.Mutex
	broadcastDone    bool
	releasedEarly    bool
	competitorEarly  bool
}

func (b *broadcastLockChainClient) LockTxOut(tx types.TxOutItem) func() {
	b.lockAttempts <- tx.VaultPubKey
	release := b.expiryChainClient.LockTxOut(tx)
	return func() {
		b.stateMu.Lock()
		if tx.VaultPubKey == b.primaryVault && !b.broadcastDone {
			b.releasedEarly = true
		}
		b.stateMu.Unlock()
		release()
	}
}

func (b *broadcastLockChainClient) BroadcastTx(tx types.TxOutItem, signed []byte) (string, error) {
	hash, err := b.expiryChainClient.BroadcastTx(tx, signed)
	if err != nil {
		return hash, err
	}
	close(b.broadcastStarted)
	<-b.finishBroadcast
	b.stateMu.Lock()
	b.broadcastDone = true
	b.stateMu.Unlock()
	return hash, nil
}

func (s *SignCoverageSuite) TestSignAndBroadcastHoldsPaymentLockThroughBroadcast(c *C) {
	for _, saved := range []bool{false, true} {
		vaultPubKey, err := common.NewPubKey(pubkeymanager.MockPubkey)
		c.Assert(err, IsNil)
		bridge := &mockSignBridge{
			blockHeight: 100,
			constants: map[string]int64{
				constants.SigningTransactionPeriod.String(): 300,
			},
		}
		client := &broadcastLockChainClient{
			expiryChainClient: expiryChainClient{chain: common.TRONChain, signPayload: true},
			primaryVault:      vaultPubKey,
			lockAttempts:      make(chan common.PubKey, 2),
			broadcastStarted:  make(chan struct{}),
			finishBroadcast:   make(chan struct{}),
		}
		storage, err := NewSignerStore("", config.LevelDBOptions{}, "")
		c.Assert(err, IsNil)
		signer := &Signer{
			logger:            log.With().Str("module", "test").Logger(),
			thorchainBridge:   bridge,
			constantsProvider: NewConstantsProvider(bridge),
			chains:            map[common.Chain]chainclients.ChainClient{common.TRONChain: client},
			pubkeyMgr:         pubkeymanager.NewMockPoolAddressValidator(),
			m:                 GetMetricForTest(c),
			storage:           storage,
			localPubKeyECDSA:  vaultPubKey,
		}
		item := TxOutStoreItem{
			TxOutItem: types.TxOutItem{Chain: common.TRONChain, VaultPubKey: vaultPubKey, ToAddress: "tron-destination", Memo: "MIGRATE:90"},
			Height:    90,
		}
		if saved {
			item.SignedTx = []byte("fixed-signed-payload")
		}
		c.Assert(storage.Set(item), IsNil)
		var finishOnce sync.Once
		finish := func() { finishOnce.Do(func() { close(client.finishBroadcast) }) }
		defer finish()
		completed := make(chan error, 1)
		go func() {
			_, _, signErr := signer.signAndBroadcast(item)
			completed <- signErr
		}()
		select {
		case <-client.broadcastStarted:
		case signErr := <-completed:
			c.Fatalf("broadcast was not reached: %v", signErr)
		case <-time.After(5 * time.Second):
			c.Fatal("broadcast was not reached")
		}
		c.Assert(<-client.lockAttempts, Equals, vaultPubKey)
		otherVault := item.TxOutItem
		otherVault.VaultPubKey = common.PubKey("another-vault")
		c.Assert(otherVault.CacheHash(), Equals, item.TxOutItem.CacheHash())
		competitorDone := make(chan struct{})
		go func() {
			release := lockChainTxOut(client, otherVault)
			client.stateMu.Lock()
			client.competitorEarly = !client.broadcastDone
			client.stateMu.Unlock()
			release()
			close(competitorDone)
		}()
		// Establish that the second vault reached lock admission while the
		// first broadcast is paused. The release-order assertion also catches
		// an early unlock even if the waiting goroutine has not been scheduled.
		select {
		case attempted := <-client.lockAttempts:
			c.Assert(attempted, Equals, otherVault.VaultPubKey)
		case <-time.After(5 * time.Second):
			c.Fatal("competing vault did not attempt the payment lock")
		}
		finish()
		select {
		case signErr := <-completed:
			c.Assert(signErr, IsNil)
		case <-time.After(5 * time.Second):
			c.Fatal("signer did not finish broadcasting")
		}
		select {
		case <-competitorDone:
		case <-time.After(5 * time.Second):
			c.Fatal("payment lock was not released after broadcast")
		}
		c.Check(client.releasedEarly, Equals, false, Commentf("saved=%t: lock released before broadcast completed", saved))
		c.Check(client.competitorEarly, Equals, false, Commentf("saved=%t: another vault acquired the lock during broadcast", saved))
		c.Check(client.unlockedCalls, Equals, 0)
		c.Check(client.signCount == 0, Equals, saved)
		c.Check(client.broadcastCount, Equals, 1)
		c.Check(client.lockCalls, Equals, 2)
		c.Check(client.unlockCalls, Equals, 2)
		c.Assert(storage.Close(), IsNil)
	}
}
