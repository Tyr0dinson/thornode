package signer

import (
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type signTimeCaptureObserver struct{ txIns []types.TxIn }

func (o *signTimeCaptureObserver) ObserveSigned(txIn types.TxIn) { o.txIns = append(o.txIns, txIn) }

func (*signTimeCaptureObserver) IsChainClientStarted(common.Chain) bool { return false }

type solCaptureSignChainClient struct{ MockCaptureSignChainClient }

func (*solCaptureSignChainClient) GetChain() common.Chain { return common.SOLChain }

// Pins the TxIn the signer hands the observer after broadcasting a SOL outbound. The
// observer tests (bifrost/observer/observe_sol_sign_time_test.go) start from this
// exact shape to cover what reaches THORNode.
func (s *SignCoverageSuite) TestSignTimeObservationHandedToObserver(c *C) {
	vaultPubKey, err := common.NewPubKey(pubkeymanager.MockPubkey)
	c.Assert(err, IsNil)
	bridge := &mockSignBridge{
		blockHeight: 100,
		constants: map[string]int64{
			constants.SigningTransactionPeriod.String(): 300,
			constants.ChurnInterval.String():            43200,
		},
		mimirs:  map[string]int64{},
		keysign: types.TxOut{},
	}
	storage, err := NewSignerStore("", config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	defer storage.Close()

	// As SOLClient.finishSign returns it: the THORChain height of the outbound as the
	// block height.
	obs := types.NewTxInItem(
		90,
		"5VERv8NMvzbJMEkV8xnrLkEaWRtSz9CosKDYjCJjBRnbJLgp8uirBgmQpjKhoR4tjF3ZpRzrFmBV6UjKdiSZkQUW",
		"OUT:8C1B0BC7E06B4E2F3F4CE2E0A5A5FD3B3C5E8B5B0A6C5E6F4E1D0C9B8A7F6E5D",
		"GjJyeC1r2RgkuoCWMyPYkCWSGSGLcz266EaAkLA27AhL",
		"9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM",
		common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(250_000_000))),
		common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(5000))},
		vaultPubKey,
		"",
		"",
		nil,
	)
	cc := &solCaptureSignChainClient{MockCaptureSignChainClient{observation: obs}}
	observer := &signTimeCaptureObserver{}
	sign := &Signer{
		logger:              log.With().Str("module", "test").Logger(),
		cfg:                 config.Bifrost{Signer: config.BifrostSignerConfiguration{AutoObserve: true, RescheduleBufferBlocks: 100}},
		thorchainBridge:     bridge,
		constantsProvider:   NewConstantsProvider(bridge),
		chains:              map[common.Chain]chainclients.ChainClient{common.SOLChain: cc},
		observer:            observer,
		pubkeyMgr:           pubkeymanager.NewMockPoolAddressValidator(),
		m:                   GetMetricForTest(c),
		storage:             storage,
		localPubKeyECDSA:    vaultPubKey,
		tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
	}
	item := TxOutStoreItem{
		TxOutItem: types.TxOutItem{
			Chain:       common.SOLChain,
			VaultPubKey: vaultPubKey,
			ToAddress:   common.Address(obs.To),
			Memo:        obs.Memo,
		},
		Height: 90,
	}
	c.Assert(storage.Set(item), IsNil)

	sign.processTransaction(item)

	c.Assert(cc.broadcastCount, Equals, 1)
	c.Assert(observer.txIns, HasLen, 1)
	c.Check(observer.txIns[0], DeepEquals, types.TxIn{
		Chain:                  common.SOLChain,
		TxArray:                []*types.TxInItem{obs},
		MemPool:                true,
		Filtered:               true,
		ConfirmationRequired:   0,
		AllowFutureObservation: true,
	})
}
