package signer

import (
	"testing"

	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type instantSendObservationClient struct {
	MockCaptureSignChainClient
	chain       common.Chain
	scanMempool bool
}

func (c *instantSendObservationClient) GetChain() common.Chain { return c.chain }

func (c *instantSendObservationClient) GetConfig() config.BifrostChainConfiguration {
	return config.BifrostChainConfiguration{
		ChainID:      c.chain,
		BlockScanner: config.BifrostBlockScannerConfiguration{ScanMemPool: c.scanMempool},
	}
}

func (c *instantSendObservationClient) SignTx(tx types.TxOutItem, height int64) ([]byte, []byte, *types.TxInItem, error) {
	c.signCount++
	return c.MockCaptureSignChainClient.SignTx(tx, height)
}

type instantSendTestObserver struct{ observations int }

func (o *instantSendTestObserver) ObserveSigned(types.TxIn) { o.observations++ }

func (*instantSendTestObserver) IsChainClientStarted(common.Chain) bool { return true }

// TestInstantSendAutoObservePolicy keeps the existing switches when mempool scanning
// is disabled and prevents sign-time observations when scanning is enabled.
func TestInstantSendAutoObservePolicy(t *testing.T) {
	for _, chain := range []common.Chain{common.DASHChain, common.FIROChain} {
		t.Run(chain.String(), func(t *testing.T) {
			for _, scan := range []bool{false, true} {
				for _, automatic := range []bool{false, true} {
					for _, memoless := range []bool{false, true} {
						sign := &Signer{
							cfg:    config.Bifrost{Signer: config.BifrostSignerConfiguration{AutoObserve: automatic}},
							chains: map[common.Chain]chainclients.ChainClient{chain: &instantSendObservationClient{chain: chain, scanMempool: scan}},
						}
						tx := types.TxOutItem{Chain: chain}
						if memoless {
							tx.OriginalMemo = "OUT:ABC"
						}
						if got, want := sign.shouldAutoObserve(tx), !scan && (automatic || memoless); got != want {
							t.Fatalf("scan=%v, auto=%v, memoless=%v: got %v, want %v", scan, automatic, memoless, got, want)
						}
					}
				}
			}
		})
	}
}

// TestInstantSendBroadcastObservationAfterRestart exercises the real signer decision
// after both new signing and reloading an older saved sign-time observation.
func (s *SignCoverageSuite) TestInstantSendBroadcastObservationAfterRestart(c *C) {
	for _, chain := range []common.Chain{common.DASHChain, common.FIROChain} {
		for _, retry := range []bool{false, true} {
			for _, scan := range []bool{false, true} {
				c.Logf("chain=%s, persisted retry=%v, mempool scan=%v", chain, retry, scan)
				pubkey, err := common.NewPubKey(pubkeymanager.MockPubkey)
				c.Assert(err, IsNil)
				bridge := &mockSignBridge{
					blockHeight: 100,
					constants:   map[string]int64{constants.SigningTransactionPeriod.String(): 300},
					mimirs:      map[string]int64{},
				}
				cc := &instantSendObservationClient{chain: chain, scanMempool: scan}
				cc.observation = &types.TxInItem{Tx: "signed-outbound-tx", BlockHeight: 90}
				path := c.MkDir()
				storage, err := NewSignerStore(path, config.LevelDBOptions{}, "")
				c.Assert(err, IsNil)
				item := NewTxOutStoreItem(90, types.TxOutItem{
					Chain: chain, VaultPubKey: pubkey,
					ToAddress: "destination", OriginalMemo: "OUT:ABC",
				}, 0)
				if retry {
					item.SignedTx = []byte("saved-outbound-payload")
					item.Observation = cc.observation
				}
				c.Assert(storage.Set(item), IsNil)
				c.Assert(storage.Close(), IsNil)
				storage, err = NewSignerStore(path, config.LevelDBOptions{}, "")
				c.Assert(err, IsNil)
				defer storage.Close()
				items := storage.List()
				c.Assert(items, HasLen, 1)
				observer := &instantSendTestObserver{}
				sign := &Signer{
					logger:              log.Logger,
					cfg:                 config.Bifrost{Signer: config.BifrostSignerConfiguration{AutoObserve: true}},
					thorchainBridge:     bridge,
					constantsProvider:   NewConstantsProvider(bridge),
					chains:              map[common.Chain]chainclients.ChainClient{chain: cc},
					pubkeyMgr:           pubkeymanager.NewMockPoolAddressValidator(),
					m:                   GetMetricForTest(c),
					storage:             storage,
					localPubKeyECDSA:    pubkey,
					tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
					observer:            observer,
				}
				sign.processTransaction(items[0])
				c.Assert(cc.signCount == 0, Equals, retry)
				c.Assert(cc.broadcastCount, Equals, 1)
				c.Assert(storage.List(), HasLen, 0)
				wantObservations := 1
				if scan {
					wantObservations = 0
				}
				c.Assert(observer.observations, Equals, wantObservations)
			}
		}
	}
}
