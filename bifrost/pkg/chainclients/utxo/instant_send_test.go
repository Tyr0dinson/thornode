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

func (s *DashSuite) TestDashAllowInstantSendAgeBoundaries(c *C) {
	client := Client{cfg: config.BifrostChainConfiguration{
		InstantSend: config.InstantSendConfiguration{MaxMempoolAge: 2 * time.Minute},
	}}
	client.instantSend = instantSendHandler{client: &client, enabled: client.cfg.InstantSend.Enabled}
	for _, age := range []time.Duration{0, time.Minute, 2 * time.Minute} {
		allow, reason := client.instantSend.allowAge(age)
		c.Assert(allow, Equals, true, Commentf(reason))
	}
	allow, reason := client.instantSend.allowAge(2*time.Minute + time.Nanosecond)
	c.Assert(allow, Equals, false)
	c.Assert(reason, Matches, "mempool age too high:.*")
}

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

// TestInstantSendFiltersUnrelatedTransactions verifies both the early RPC filter
// and its cache, without hiding ordinary outbound or later block observations.
func (s *DashSuite) TestInstantSendFiltersUnrelatedTransactions(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.MempoolTxIDCacheSize = 10
	f.entries = map[string]map[string]any{f.lockedOutbound: dashTestMempoolEntry()}
	// Exercise address decoding from script hex rather than supplied addresses.
	f.txs[f.lockedOutbound]["vout"] = []any{map[string]any{
		"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": newDashP2PKHScriptHex(c, f.user)},
	}, map[string]any{"n": 1, "value": 0, "scriptPubKey": map[string]any{"hex": newDashMemoScriptHex(c, "NOOP")}}}
	calls := countInstantSendRPCs(c, f)
	for range 3 {
		groups, err := client.instantSend.scan(123)
		c.Assert(err, IsNil)
		c.Assert(groups, HasLen, 0)
	}
	c.Assert(calls("getrawtransaction"), Equals, 1)
	c.Assert(calls("getislocks"), Equals, 0)
	c.Assert(calls("getblock"), Equals, 0)
	c.Assert(client.instantSend.ignored, HasLen, 1)
	observed, err := client.temporalStorage.HasObservedTx(f.lockedOutbound)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(0))
	batch, err := client.fetchMemPoolTransactions(123, true)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, f.lockedOutbound)
}

// TestInstantSendFilterKeepsVaultPayments covers deposits and exempt vault transfers.
func (s *DashSuite) TestInstantSendFilterKeepsVaultPayments(c *C) {
	for _, vaultSender := range []bool{false, true} {
		client, f, cleanup := newDashMempoolFixture(c)
		func() {
			defer cleanup()
			client.cfg.MempoolTxIDCacheSize = 10
			client.cfg.ParallelMempoolScan = 3
			f.entries = map[string]map[string]any{f.inbound: dashTestMempoolEntry()}
			memo := "NOOP"
			if vaultSender {
				f.txs[f.inbound]["vin"] = f.txs[f.lockedOutbound]["vin"]
				client.instantSend.maxAmount = 0 // vault senders still bypass allowance
				memo = "CONSOLIDATE"
			}
			// Omit addresses to exercise script decoding, but keep the RPC script
			// types so the consolidation memo is recognised as OP_RETURN data.
			f.txs[f.inbound]["vout"] = []any{map[string]any{
				"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": newDashP2PKHScriptHex(c, f.asgard), "type": "pubkeyhash"},
			}, map[string]any{"n": 1, "value": 0, "scriptPubKey": map[string]any{"hex": newDashMemoScriptHex(c, memo), "type": "nulldata"}}}
			batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: !vaultSender})
			c.Assert(err, IsNil)
			c.Assert(batch.TxArray, HasLen, 1)
			c.Assert(batch.TxArray[0].Tx, Equals, f.inbound)
			c.Assert(batch.TxArray[0].Memo, Equals, memo)
			c.Assert(client.instantSend.ignored, HasLen, 0)
			state, err := client.temporalStorage.GetInstantSendState()
			c.Assert(err, IsNil)
			want := uint64(common.One / 10)
			if vaultSender {
				want = 0
			}
			c.Assert(state.Reserved, Equals, want)
		}()
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
	_, err := client.instantSend.scan(123)
	c.Assert(err, IsNil)
	setVaults(other, f.asgard, other) // same set, different order and duplicate
	_, err = client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 1)
	delete(f.entries, f.lockedOutbound)
	_, err = client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 0)
	f.entries[f.lockedOutbound] = dashTestMempoolEntry()
	_, err = client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 2)
	setVaults(f.asgard, f.user)
	groups, err := client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(groups, HasLen, 1)
	c.Assert(groups[0].TxArray, HasLen, 1)
	c.Assert(groups[0].TxArray[0].Tx, Equals, f.lockedOutbound)
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
				_, err := client.instantSend.scan(123)
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
			_, err := client.instantSend.scan(123)
			c.Assert(err, IsNil)
			c.Assert(client.instantSend.ignored, HasLen, 0)
			f.txs[f.lockedOutbound][tc.field] = original
			_, err = client.instantSend.scan(123)
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
	_, err := client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 1)
	bridge := client.bridge
	client.bridge = instantSendVaultErrorBridge{bridge}
	client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{common.Address(f.asgard)}})
	_, err = client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(client.instantSend.ignored, HasLen, 0)
	c.Assert(calls("getrawtransaction/"+f.lockedOutbound), Equals, 2)
	client.bridge = bridge
	client.asgardCache.Store(nil) // a successful refresh with no usable vaults
	_, err = client.instantSend.scan(123)
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
		mempool[id] = rpc.GetRawMempoolVerboseResult{InstantLock: "true"}
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

// Restart must preserve the block fallback for dropped/rejected handoffs, even with InstantSend disabled.
func (s *DashSuite) TestInstantSendDeliveryRestartUsesChainLockFallback(c *C) {
	for _, outcome := range []string{"missing", "rejected", "stored"} {
		client, f, cleanup := newDashMempoolFixture(c)
		defer cleanup()
		f.entries = map[string]map[string]any{f.inbound: dashTestMempoolEntry()}
		path := c.MkDir()
		storage, err := blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
		c.Assert(err, IsNil)
		client.cfg.BlockScanner.DBPath = path
		client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
		c.Assert(err, IsNil)
		c.Assert(client.temporalStorage.StartInstantSendWarmup(0, nil), IsNil)
		c.Assert(client.temporalStorage.CompleteInstantSendWarmup(1), IsNil)
		batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 1)
		c.Assert(batch.TxArray[0].BlockHeight, Equals, int64(100))
		switch outcome {
		case "rejected":
			batch.TxArray[0].OnObservedTxRejected(errors.New("Observer status unknown"))
		case "stored":
			batch.TxArray[0].OnObservedTxStored()
		}
		state, err := client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Reserved, Equals, uint64(common.One/10))
		c.Assert(storage.Close(), IsNil)
		storage, err = blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
		c.Assert(err, IsNil)
		defer storage.Close()
		client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 0)
		c.Assert(err, IsNil)
		client.instantSend.txQueue = nil
		client.instantSend.enabled = false
		client.cfg.InstantSend.Enabled = false
		client.cfg.BlockScanner.ScanMemPool = false
		block := deliveryBlock(c, client, f, f.inbound)
		batch, err = client.extractTxs(block)
		c.Assert(err, IsNil)
		want := 1
		if outcome == "stored" {
			want = 0
		}
		c.Assert(batch.TxArray, HasLen, want, Commentf("handoff %s", outcome))
		if want > 0 {
			c.Assert(batch.TxArray[0].Tx, Equals, f.inbound)
			c.Assert(batch.TxArray[0].BlockHeight, Equals, int64(150))
		}
		state, err = client.temporalStorage.GetInstantSendState()
		c.Assert(err, IsNil)
		c.Assert(state.Reserved, Equals, uint64(0))
		batch, err = client.extractTxs(block)
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 0)
	}
}

// One pending handoff must not stop admission of another transaction or reserve twice.
func (s *DashSuite) TestInstantSendDeliveryDoesNotRetryOrBlockNewClaims(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.UTXO.TransactionBatchSize = 1
	client.instantSend.txQueue = instantSendObservationGroups(common.DASHChain,
		map[int64][]*types.TxInItem{100: {instantSendTestItem(f, f.inbound), instantSendTestItem(f, f.lowFeeInbound)}})
	first, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(first.TxArray, HasLen, 1)
	first.TxArray[0].OnObservedTxRejected(errors.New("storage unavailable"))
	second, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(second.TxArray, HasLen, 1)
	c.Assert(second.TxArray[0].Tx, Equals, f.lowFeeInbound)
	second.TxArray[0].OnObservedTxStored()
	retry, err := client.instantSend.fetchBatch(124, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(retry.TxArray, HasLen, 0)
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(common.One/5))
	batch, err := client.extractTxs(deliveryBlock(c, client, f, f.inbound, f.lowFeeInbound))
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, f.inbound)
}

// The batch collector completes only stored items, regardless of callback order or repetition.
func (s *DashSuite) TestInstantSendDeliveryPartialAcceptance(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 2)
	first, second := batch.TxArray[0], batch.TxArray[1]
	var wg sync.WaitGroup
	wg.Go(func() { first.OnObservedTxStored(); first.OnObservedTxRejected(errors.New("duplicate")) })
	wg.Go(func() { second.OnObservedTxRejected(errors.New("rejected")); second.OnObservedTxStored() })
	wg.Wait()
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reserved, Equals, uint64(common.One/5))
	mined, err := client.extractTxs(deliveryBlock(c, client, f, first.Tx, second.Tx))
	c.Assert(err, IsNil)
	c.Assert(mined.TxArray, HasLen, 1)
	c.Assert(mined.TxArray[0].Tx, Equals, second.Tx)
}

// A successful callback arriving after the block claim cannot undo that claim.
func (s *DashSuite) TestInstantSendDeliveryLateCallbackAfterBlock(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
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
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
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

// Exempt vault transfers still need durable markers; ordinary outbound scanning remains available.
func (s *DashSuite) TestInstantSendDeliveryRequiresDiskForVaultTransfersToo(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.cfg.BlockScanner.DBPath = ""
	item := instantSendTestItem(f, f.lockedOutbound)
	item.Sender = f.asgard
	items, err := client.instantSend.claimBatch([]*types.TxInItem{item}, instantSendAdmission{})
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 0)
	marked, err := client.temporalStorage.HasObservedTx(item.Tx)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
	batch, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(batch.MemPool, Equals, true)
	c.Assert(batch.TxArray, HasLen, 2)
}

func (s *DashSuite) TestDashObservationGroupsClaimOnlyDeliveredTransactions(c *C) {
	client, fixture, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	first, second := fixture.inbound, fixture.lowFeeInbound
	item := func(id string, height int64) *types.TxInItem {
		return &types.TxInItem{
			Tx: id, BlockHeight: height, Sender: fixture.user, To: fixture.asgard,
			Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One/10))),
		}
	}
	groups := instantSendObservationGroups(client.GetChain(), map[int64][]*types.TxInItem{
		200: {item(second, 200)},
		100: {item(strings.ToUpper(first), 100)},
	})
	client.instantSend.txQueue = groups
	batch, err := client.instantSend.fetchBatch(300, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].BlockHeight, Equals, int64(100))
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
	batch, err = client.instantSend.fetchBatch(300, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 0)
	// Changing case cannot bypass the persistent marker.
	accepted, err = client.trackObservedTxs([]*types.TxInItem{{Tx: first}})
	c.Assert(err, IsNil)
	c.Assert(accepted, HasLen, 0)
}

func (s *DashSuite) TestDashObservationStorageFailureRetainsPendingGroup(c *C) {
	client, fixture, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	item := &types.TxInItem{
		Tx: fixture.inbound, BlockHeight: 100, Sender: fixture.user, To: fixture.asgard,
		Coins: common.NewCoins(common.NewCoin(common.DASHAsset, cosmos.NewUint(common.One/10))),
	}
	client.instantSend.txQueue = instantSendObservationGroups(client.GetChain(), map[int64][]*types.TxInItem{100: {item}})
	closed, closeStorage := newDashClientForRPCServer(c, "http://localhost:1")
	healthyStorage := client.temporalStorage
	client.temporalStorage = closed.temporalStorage
	closeStorage()
	batch, err := client.instantSend.fetchBatch(200, instantSendAdmission{allowed: true})
	c.Assert(err, NotNil)
	c.Assert(batch.TxArray, HasLen, 0)
	c.Assert(client.instantSend.txQueue, HasLen, 1)
	c.Assert(client.instantSend.txQueue[0].TxArray, DeepEquals, []*types.TxInItem{item})

	// Once storage is available, retry the queued group without another RPC scan.
	client.temporalStorage = healthyStorage
	batch, err = client.instantSend.fetchBatch(200, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, DeepEquals, []*types.TxInItem{item})
	c.Assert(client.instantSend.txQueue, HasLen, 0)
}
