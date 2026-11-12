package utxo

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

// countInstantSendRPCs counts requests safely across parallel RPC workers.
func countInstantSendRPCs(c *C, f *dashMempoolFixture) func(string) int {
	var mu sync.Mutex
	calls := make(map[string]int)
	f.beforeRPC = func(method string, params []json.RawMessage) {
		var ids []string
		switch method {
		case "getrawtransaction", "getblock":
			var id string
			c.Assert(json.Unmarshal(params[0], &id), IsNil)
			ids = []string{id}
		case "getislocks":
			c.Assert(json.Unmarshal(params[0], &ids), IsNil)
		}
		mu.Lock()
		defer mu.Unlock()
		calls[method]++
		for _, id := range ids {
			calls[method+"/"+id]++
		}
	}
	return func(key string) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[key]
	}
}

// TestInstantSendFilterInvalidation proves a newly relevant tx is retried on churn.
func (s *DashSuite) TestInstantSendFilterInvalidation(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.MempoolTxIDCacheSize = 10
	f.entries = map[string]map[string]any{f.lockedOutbound: dashTestMempoolEntry()}
	other := newDashTestAddress(c, 0x33)
	setVaults := func(addresses ...string) {
		vaults := make([]common.Address, 0, len(addresses))
		for _, address := range addresses {
			vaults = append(vaults, common.Address(address))
		}
		client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: vaults, FetchedAt: time.Now()})
	}
	setVaults(f.asgard, other)
	calls := countInstantSendRPCs(c, f)
	_, err := client.instantSend.scan()
	c.Assert(err, IsNil)
	setVaults(other, f.asgard, other) // same set, different order and duplicate
	_, err = client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 1)
	delete(f.entries, f.lockedOutbound)
	_, err = client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 0)
	f.entries[f.lockedOutbound] = dashTestMempoolEntry()
	_, err = client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 2)
	setVaults(f.asgard, f.user)
	groups, err := client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(groups, HasLen, 0) // refreshed output filter still excludes vault senders
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 3)
	c.Assert(client.instantSend.ignored, HasLen, 0)
}

// TestInstantSendFilterCacheBound keeps the output filter active even with no cache.
func (s *DashSuite) TestInstantSendFilterCacheBound(c *C) {
	for _, capacity := range []int{-1, 0, 1} {
		client, f, cleanup := newDashMempoolFixture(c)
		func() {
			defer cleanup()
			client.cfg.MempoolTxIDCacheSize = capacity
			client.cfg.ParallelMempoolScan = 3
			delete(f.entries, f.unlockedOutbound)
			for _, id := range []string{f.inbound, f.lowFeeInbound} {
				f.txs[id]["vout"] = f.txs[f.lockedOutbound]["vout"]
			}
			calls := countInstantSendRPCs(c, f)
			for range 3 {
				_, err := client.instantSend.scan()
				c.Assert(err, IsNil)
				c.Assert(len(client.instantSend.ignored) <= max(capacity, 0), Equals, true)
			}
			want := 9
			if capacity > 0 {
				want = 7 // three initial lookups, then two per scan
			}
			c.Assert(calls("getrawtransaction"), Equals, want)
			c.Assert(calls("getislocks"), Equals, 0)
		}()
	}
}

// TestInstantSendFilterRetriesUnknownTransactions never caches failed or uncertain RPC data.
func (s *DashSuite) TestInstantSendFilterRetriesUnknownTransactions(c *C) {
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"RPC decoding error", "txid", 123},
		{"mismatched transaction ID", "txid", strings.Repeat("0", 64)},
		{"not yet locked", "instantlock", false},
		{"uncertain script", "vout", []any{map[string]any{
			"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": "invalid", "type": "nulldata"},
		}}},
	} {
		c.Log(tc.name)
		client, f, cleanup := newDashMempoolFixture(c)
		func() {
			defer cleanup()
			client.cfg.MempoolTxIDCacheSize = 10
			f.entries = map[string]map[string]any{f.lockedOutbound: dashTestMempoolEntry()}
			original := f.txs[f.lockedOutbound][tc.field]
			f.txs[f.lockedOutbound][tc.field] = tc.value
			calls := countInstantSendRPCs(c, f)
			_, err := client.instantSend.scan()
			c.Assert(err, IsNil)
			c.Assert(client.instantSend.ignored, HasLen, 0)
			f.txs[f.lockedOutbound][tc.field] = original
			_, err = client.instantSend.scan()
			c.Assert(err, IsNil)
			c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 2)
			c.Assert(client.instantSend.ignored, HasLen, 1)
		}()
	}
}

type instantSendVaultErrorBridge struct{ thorclient.ThorchainBridge }

// GetAsgardPubKeys simulates a failed refresh while stale addresses remain available.
func (instantSendVaultErrorBridge) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	return nil, errors.New("vault refresh unavailable")
}

// TestInstantSendFilterBypassesUnavailableVaults preserves retries after a failed refresh.
func (s *DashSuite) TestInstantSendFilterBypassesUnavailableVaults(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.MempoolTxIDCacheSize = 10
	f.entries = map[string]map[string]any{f.lockedOutbound: dashTestMempoolEntry()}
	calls := countInstantSendRPCs(c, f)
	_, err := client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 1)
	bridge := client.bridge
	client.bridge = instantSendVaultErrorBridge{bridge}
	client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{common.Address(f.asgard)}})
	_, err = client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 0)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 2)
	client.bridge = bridge
	client.asgardCache.Store(nil) // a successful refresh with no usable vaults
	_, err = client.instantSend.scan()
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 0)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 3)
}

// TestInstantSendCandidatesSkipIgnoredWithoutStarving verifies rotation with a binding cap.
func (s *DashSuite) TestInstantSendCandidatesSkipIgnoredWithoutStarving(c *C) {
	client, _, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.UTXO.TransactionBatchSize = 2
	ids := []string{strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64)}
	mempool := make(map[string]rpc.GetRawMempoolVerboseResult)
	for _, id := range ids {
		var entry rpc.GetRawMempoolVerboseResult
		data, encodeErr := json.Marshal(dashTestMempoolEntry())
		c.Assert(encodeErr, IsNil)
		c.Assert(json.Unmarshal(data, &entry), IsNil)
		entry.InstantLock = "true"
		mempool[id] = entry
	}
	client.instantSend.ignored = map[string]struct{}{ids[0]: {}, ids[2]: {}}
	for _, want := range [][]string{{ids[1], ids[3]}, {ids[4], ids[1]}, {ids[3], ids[4]}} {
		got, err := client.instantSend.candidates(mempool)
		c.Assert(err, IsNil)
		c.Assert(got, DeepEquals, want)
	}
}

// deliveryBlock supplies mined transaction data; extractTxs receives only verified blocks in production.
func deliveryBlock(c *C, client *Client, f *dashMempoolFixture, ids ...string) *btcjson.GetBlockVerboseTxResult {
	client.disableVinZeroBatch = true
	block := &btcjson.GetBlockVerboseTxResult{Height: 150, Hash: strings.Repeat("2", 64)}
	for _, id := range ids {
		data, err := json.Marshal(f.txs[id])
		c.Assert(err, IsNil)
		var tx btcjson.TxRawResult
		c.Assert(json.Unmarshal(data, &tx), IsNil)
		block.Tx = append(block.Tx, tx)
	}
	return block
}

// A successful callback arriving after the block claim cannot undo that claim.
func (s *DashSuite) TestInstantSendDeliveryLateCallbackAfterBlock(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	batch, err := client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 2)
	// One callback is missing, so the collector leaves both markers pending.
	batch.TxArray[0].OnObservedTxStored()
	block := deliveryBlock(c, client, f, f.inbound, f.lowFeeInbound)
	mined, err := client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(mined.TxArray, HasLen, 2)
	batch.TxArray[1].OnObservedTxStored()
	mined, err = client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(mined.TxArray, HasLen, 0)
}

// A failed acknowledgement write leaves the durable pending marker available for fallback.
func (s *DashSuite) TestInstantSendDeliveryAcknowledgementWriteFailure(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	batch, err := client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 2)
	cleanup() // shutdown closes the store captured by the callbacks
	for _, item := range batch.TxArray {
		item.OnObservedTxStored()
	}
	storage, err := blockscanner.NewBlockScannerStorage(client.cfg.BlockScanner.DBPath, config.LevelDBOptions{})
	c.Assert(err, IsNil)
	defer storage.Close()
	client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
	c.Assert(err, IsNil)
	ids, err := client.temporalStorage.TrackInstantSendBlockTxs([]string{f.inbound, f.lowFeeInbound})
	c.Assert(err, IsNil)
	c.Assert(ids, HasLen, 2)
}

// Without disk storage, ordinary outbound scanning remains available.
func (s *DashSuite) TestInstantSendWithoutDiskKeepsOrdinaryVaultObservations(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.BlockScanner.DBPath = ""
	item := instantSendTestItem(f, f.lockedOutbound)
	item.Sender = f.asgard
	items, err := client.instantSend.claimBatch([]*types.TxInItem{item})
	c.Assert(err, Equals, errInstantSendPaused)
	c.Assert(items, HasLen, 0)
	marked, err := client.temporalStorage.HasObservedTx(item.Tx)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
	batch, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(batch.MemPool, Equals, true)
	c.Assert(batch.TxArray, HasLen, 2)
}

func (s *DashSuite) TestDashQueuedObservationsClaimOnlyDeliveredTransactions(c *C) {
	client, fixture, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.UTXO.TransactionBatchSize = 1
	first, second := fixture.inbound, fixture.lowFeeInbound
	item := func(id string, height int64) *types.TxInItem {
		return &types.TxInItem{
			SpentOutputRefs: []*common.OutputRef{{TxHash: strings.Repeat("F", 64)}},
			Tx:              id, BlockHeight: height, Sender: fixture.user, To: fixture.asgard,
			InstantSend: &common.InstantSend{}, Memo: "=:THOR.RUNE",
			Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One/10))),
		}
	}
	groups := groupInstantSendTxs(map[int64][]*types.TxInItem{
		96: {item(strings.ToUpper(first), 96), item(second, 96)},
	})
	client.instantSend.txQueue = groups
	batch, err := client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].BlockHeight, Equals, int64(96))
	observed, err := client.temporalStorage.HasObservedTx(first)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, true)
	observed, err = client.temporalStorage.HasObservedTx(second)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	batch.TxArray[0].OnObservedTxStored()

	// The block scanner can claim a queued group before its mempool delivery.
	accepted, err := client.trackObservedTxs([]*types.TxInItem{{Tx: second}})
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 1)
	batch, err = client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
	// Early delivery permits one block observation; casing cannot create a second.
	accepted, err = client.trackObservedTxs([]*types.TxInItem{{Tx: first}})
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 1)
	accepted, err = client.trackObservedTxs([]*types.TxInItem{{Tx: strings.ToUpper(first)}})
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 0)
}

func (s *DashSuite) TestDashObservationStorageFailureRetainsPendingGroup(c *C) {
	client, fixture, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	item := &types.TxInItem{
		SpentOutputRefs: []*common.OutputRef{{TxHash: strings.Repeat("F", 64)}},
		Tx:              fixture.inbound, BlockHeight: 96, Sender: fixture.user, To: fixture.asgard,
		InstantSend: &common.InstantSend{}, Memo: "=:THOR.RUNE",
		Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One/10))),
	}
	client.instantSend.txQueue = groupInstantSendTxs(map[int64][]*types.TxInItem{100: {item}})
	closed, closeStorage := newDashClientForRPCServer(c, "http://localhost:1")
	healthyStorage := client.temporalStorage
	client.temporalStorage = closed.temporalStorage
	closeStorage()
	batch, err := client.instantSend.fetchBatch()
	c.Assert(err, NotNil)
	c.Assert(batch.TxArray, HasLen, 0)
	c.Assert(client.instantSend.txQueue, HasLen, 1)
	c.Assert(client.instantSend.txQueue[0][0], Equals, item)

	// Once storage is available, retry the queued group without another RPC scan.
	client.temporalStorage = healthyStorage
	batch, err = client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, DeepEquals, []*types.TxInItem{item})
	c.Assert(client.instantSend.txQueue, HasLen, 0)
}
