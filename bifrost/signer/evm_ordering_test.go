package signer

import (
	"bytes"
	"errors"
	"strings"
	"sync"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type evmOrderingClient struct {
	MockSuccessChainClient
	sync.Mutex
	attempts      []string
	nonceAdvanced bool
}

func (c *evmOrderingClient) SignTx(txs []types.TxOutItem, height int64) ([]byte, []byte, []*types.TxInItem, error) {
	tx := txs[0]
	c.Lock()
	c.attempts = append(c.attempts, tx.Memo)
	c.Unlock()
	if tx.Memo == "first" {
		if c.nonceAdvanced {
			return nil, nil, nil, nil
		}
		return nil, []byte("0"), nil, tss.ErrNotSelectedForKeysign
	}
	return c.MockSuccessChainClient.SignTx(txs, height)
}

func (s *SignCoverageSuite) TestEVMNonselectionPreservesPipelineOrderAcrossRestart(c *C) {
	vault, err := common.NewPubKey(pubkeymanager.MockPubkey)
	c.Assert(err, IsNil)
	bridge := &mockSignBridge{
		blockHeight: 100,
		constants:   map[string]int64{constants.SigningTransactionPeriod.String(): 300, constants.ChurnInterval.String(): 43200},
		vault:       ttypes.Vault{Status: ttypes.VaultStatus_RetiringVault},
	}
	path := c.MkDir()
	storage, err := NewSignerStore(path, config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	client := &evmOrderingClient{}
	sign := &Signer{
		logger: log.Logger, cfg: config.Bifrost{Signer: config.BifrostSignerConfiguration{RescheduleBufferBlocks: 5}},
		thorchainBridge: bridge, constantsProvider: NewConstantsProvider(bridge),
		chains:    map[common.Chain]chainclients.ChainClient{common.ETHChain: client, common.AVAXChain: &MockSuccessChainClient{}},
		pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(), m: GetMetricForTest(c), storage: storage,
		localPubKeyECDSA: vault, tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
	}
	// Exercise steady-state queue ordering independently of the constants
	// provider's existing concurrent first-load path.
	c.Assert(sign.constantsProvider.EnsureConstants(bridge.blockHeight), IsNil)
	first := NewTxOutStoreItem(90, types.TxOutItem{
		Chain: common.ETHChain, VaultPubKey: vault, ToAddress: "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
		Coins: common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(1000))), Memo: "first",
	}, 0)
	second := first
	second.Index = 1
	second.TxOutItem.Memo = "second"
	other := first
	other.Index = 2
	other.TxOutItem.Chain = common.AVAXChain
	c.Assert(storage.Batch([]TxOutStoreItem{second, other, first}), IsNil)
	pipeline, err := newPipeline(2)
	c.Assert(err, IsNil)
	pipeline.SpawnSignings(sign, bridge)
	pipeline.Wait()
	c.Assert(client.attempts, DeepEquals, []string{"first"})
	c.Assert(storage.List(), HasLen, 2) // Other chain progressed independently.
	c.Assert(storage.Close(), IsNil)

	storage, err = NewSignerStore(path, config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	defer storage.Close()
	sign.storage = storage
	stored, err := storage.Get(first.Key())
	c.Assert(err, IsNil)
	c.Assert(string(stored.Checkpoint), Equals, "0")
	pipeline, err = newPipeline(2)
	c.Assert(err, IsNil)
	pipeline.SpawnSignings(sign, bridge)
	pipeline.Wait()
	c.Assert(client.attempts, DeepEquals, []string{"first", "first"})

	// Chain nonce evidence releases the unsigned item before THORNode observes
	// it, so a newly eligible signer can join the next outbound's party.
	c.Assert(stored.TxOutItem.OutHash.IsEmpty(), Equals, true)
	client.nonceAdvanced = true
	pipeline.SpawnSignings(sign, bridge)
	pipeline.Wait()
	c.Assert(client.attempts, HasLen, 3)
	pipeline.SpawnSignings(sign, bridge)
	pipeline.Wait()
	c.Assert(client.attempts, DeepEquals, []string{"first", "first", "first", "second"})
	c.Assert(storage.List(), HasLen, 0)
}

func (s *SignCoverageSuite) TestEVMPendingCheckpointDoesNotLogFailure(c *C) {
	vault, err := common.NewPubKey(pubkeymanager.MockPubkey)
	c.Assert(err, IsNil)
	bridge := &mockSignBridge{blockHeight: 100, constants: map[string]int64{
		constants.SigningTransactionPeriod.String(): 300, constants.ChurnInterval.String(): 43200,
	}}
	storage, err := NewSignerStore("", config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	defer storage.Close()
	client := &MockSignErrorChainClient{signErr: chainclienttypes.ErrTxPending, checkpoint: []byte("pending-checkpoint")}
	var logs bytes.Buffer
	sign := &Signer{
		logger: zerolog.New(&logs), thorchainBridge: bridge, constantsProvider: NewConstantsProvider(bridge),
		chains:    map[common.Chain]chainclients.ChainClient{common.ETHChain: client},
		pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(), m: GetMetricForTest(c), storage: storage,
		localPubKeyECDSA: vault, tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
	}
	item := NewTxOutStoreItem(90, types.TxOutItem{
		Chain: common.ETHChain, VaultPubKey: vault,
		ToAddress: "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
	}, 0)
	c.Assert(storage.Set(item), IsNil)
	sign.processTransaction(item)
	retained, err := storage.Get(item.Key())
	c.Assert(err, IsNil)
	c.Assert(retained.Checkpoint, DeepEquals, client.checkpoint)
	c.Assert(strings.Contains(logs.String(), `"level":"error"`), Equals, false)
	// Real failures still surface as errors and retain the item.
	logs.Reset()
	client.signErr = errors.New("signing failed")
	sign.processTransaction(retained)
	c.Assert(strings.Contains(logs.String(), `"level":"error"`), Equals, true)
}

func (s *SignCoverageSuite) TestEVMRetiredApprovalReleasesPipelineLane(c *C) {
	vault, err := common.NewPubKey(pubkeymanager.MockPubkey)
	c.Assert(err, IsNil)
	bridge := &mockSignBridge{
		blockHeight: 100,
		constants:   map[string]int64{constants.SigningTransactionPeriod.String(): 300, constants.ChurnInterval.String(): 43200},
		vault:       ttypes.Vault{Status: ttypes.VaultStatus_RetiringVault},
	}
	storage, err := NewSignerStore("", config.LevelDBOptions{}, "")
	c.Assert(err, IsNil)
	defer storage.Close()
	// The EVM client returns no transaction, checkpoint or observation after
	// proving a mined approval reverted. Exercise that result through the queue.
	client := &evmOrderingClient{nonceAdvanced: true}
	sign := &Signer{
		logger:          log.Logger,
		cfg:             config.Bifrost{Signer: config.BifrostSignerConfiguration{AutoObserve: true, RescheduleBufferBlocks: 5}},
		thorchainBridge: bridge, constantsProvider: NewConstantsProvider(bridge),
		chains:    map[common.Chain]chainclients.ChainClient{common.AVAXChain: client},
		pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(), m: GetMetricForTest(c), storage: storage,
		localPubKeyECDSA: vault, tssKeysignMetricMgr: metrics.NewTssKeysignMetricMgr(),
		// A false auto-observation would reach the deliberately nil observer.
	}
	c.Assert(sign.constantsProvider.EnsureConstants(bridge.blockHeight), IsNil)
	first := NewTxOutStoreItem(90, types.TxOutItem{
		Chain: common.AVAXChain, VaultPubKey: vault, ToAddress: "0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
		Coins: common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(1000))), Memo: "first",
	}, 0)
	first.Checkpoint = []byte("saved-signed-approval")
	second := first
	second.Index = 1
	second.Checkpoint = nil
	second.TxOutItem.Memo = "second"
	c.Assert(storage.Batch([]TxOutStoreItem{second, first}), IsNil)
	pipeline, err := newPipeline(1)
	c.Assert(err, IsNil)
	pipeline.SpawnSignings(sign, bridge)
	pipeline.Wait()
	c.Assert(client.attempts, DeepEquals, []string{"first"})
	c.Assert(client.broadcastTxs, Equals, 0)
	remaining := storage.List()
	c.Assert(remaining, HasLen, 1)
	c.Assert(remaining[0].Key(), Equals, second.Key())
	pipeline.SpawnSignings(sign, bridge)
	pipeline.Wait()
	c.Assert(client.attempts, DeepEquals, []string{"first", "second"})
	c.Assert(client.broadcastTxs, Equals, 1)
	c.Assert(storage.List(), HasLen, 0)
}
