package utxo

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

type dashOracleBridge struct {
	thorclient.ThorchainBridge
	getPrice func(string) (cosmos.Uint, error)
}

func (b dashOracleBridge) GetOraclePrice(symbol string) (cosmos.Uint, error) {
	return b.getPrice(symbol)
}

// TestDashGetInstantSendLocksSkipsCachedBeforeRawTxFetch protects the cheap
// cache gate. Already-processed InstantSend mempool txs should not trigger a
// full raw transaction RPC on every scan while they remain in the mempool.
func (s *DashSuite) TestDashGetInstantSendLocksSkipsCachedBeforeRawTxFetch(c *C) {
	cached := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fresh := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var rawLookups []string

	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{
				cached: dashTestMempoolEntry(),
				fresh:  dashTestMempoolEntry(),
			}
		case "getrawtransaction":
			var txID string
			err := json.Unmarshal(params[0], &txID)
			if err != nil {
				c.Fatalf("decode txid: %v", err)
			}
			// This is the regression guard: cached txids must be filtered before
			// the expensive GetRawTransactionVerbose call is made.
			if txID == cached {
				c.Fatalf("cached tx should not be fetched again")
			}
			rawLookups = append(rawLookups, txID)
			return dashUnlockedRawTx(txID)
		case "getislocks":
			c.Fatalf("unlocked tx should not fetch instantsend locks")
		default:
			c.Fatalf("unexpected method: %s", method)
		}
		return nil
	})
	defer server.Close()

	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	client.cfg.UTXO.TransactionBatchSize = 10
	client.cfg.UTXO.MaxMempoolBatches = 1
	// Simulate a tx already handled by a previous InstantSend mempool pass.
	added, err := client.temporalStorage.TrackObservedTx(cached)
	c.Assert(err, IsNil)
	c.Assert(added, Equals, true)

	txIn, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 0)
	c.Assert(rawLookups, DeepEquals, []string{fresh})
}

// TestDashGetInstantSendLocksCapsMempoolWork protects the per-cycle work
// bound. The InstantSend path should not spawn one worker or raw tx RPC per
// mempool entry when the generic UTXO mempool settings impose a smaller cap.
func (s *DashSuite) TestDashGetInstantSendLocksCapsMempoolWork(c *C) {
	txA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	txB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	txC := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	rawLookups := 0

	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{
				txA: dashTestMempoolEntry(),
				txB: dashTestMempoolEntry(),
				txC: dashTestMempoolEntry(),
			}
		case "getrawtransaction":
			var txID string
			err := json.Unmarshal(params[0], &txID)
			if err != nil {
				c.Fatalf("decode txid: %v", err)
			}
			rawLookups++
			// If candidate selection is unbounded, the second mempool entry will
			// reach this handler and fail the test immediately.
			if rawLookups > 1 {
				c.Fatalf("expected per-cycle cap to allow only one raw tx lookup")
			}
			return dashUnlockedRawTx(txID)
		case "getislocks":
			c.Fatalf("unlocked tx should not fetch instantsend locks")
		default:
			c.Fatalf("unexpected method: %s", method)
		}
		return nil
	})
	defer server.Close()

	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	// Force a one-tx cap so a three-entry mempool proves candidate selection is
	// bounded before goroutines and raw tx RPCs are created.
	client.cfg.UTXO.TransactionBatchSize = 1
	client.cfg.UTXO.MaxMempoolBatches = 1

	txIn, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 0)
	c.Assert(rawLookups, Equals, 1)
}

func (s *DashSuite) TestDashGetInstantSendLocksBatchesLockLookupAndCachesCycleHeight(c *C) {
	txA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	txB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cycleHash := "1111111111111111111111111111111111111111111111111111111111111111"
	var lockBatches [][]string
	getBlockCalls := 0

	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{
				txA: dashTestMempoolEntry(),
				txB: dashTestMempoolEntry(),
			}
		case "getrawtransaction":
			var txID string
			err := json.Unmarshal(params[0], &txID)
			if err != nil {
				c.Fatalf("decode txid: %v", err)
			}
			switch txID {
			case txA, txB:
				return dashLockedRawTx(txID)
			default:
				c.Fatalf("unexpected tx lookup: %s", txID)
			}
		case "getislocks":
			var requested []string
			err := json.Unmarshal(params[0], &requested)
			if err != nil {
				c.Fatalf("decode getislocks txids: %v", err)
			}
			lockBatches = append(lockBatches, requested)
			locks := make([]any, len(requested))
			for i, txID := range requested {
				locks[i] = map[string]any{
					"txid":      txID,
					"id":        "islock-request-id",
					"inputs":    []map[string]any{},
					"cycleHash": cycleHash,
					"signature": "signature",
					"hex":       "010203",
				}
			}
			return locks
		case "getblock":
			var hash string
			err := json.Unmarshal(params[0], &hash)
			if err != nil {
				c.Fatalf("decode block hash: %v", err)
			}
			c.Assert(hash, Equals, cycleHash)
			getBlockCalls++
			return map[string]any{
				"hash":          cycleHash,
				"confirmations": 1,
				"height":        int64(123),
			}
		default:
			c.Fatalf("unexpected method: %s", method)
		}
		return nil
	})
	defer server.Close()

	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	client.cfg.UTXO.TransactionBatchSize = 10
	client.cfg.UTXO.MaxMempoolBatches = 1

	txIn, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 0)
	c.Assert(lockBatches, HasLen, 1)
	c.Assert(lockBatches[0], HasLen, 2)
	seen := map[string]bool{}
	for _, txID := range lockBatches[0] {
		seen[txID] = true
	}
	c.Assert(seen[txA], Equals, true)
	c.Assert(seen[txB], Equals, true)
	c.Assert(getBlockCalls, Equals, 1)
}

func (s *DashSuite) TestDashCycleHeightIndependentOfScanner(c *C) {
	cycleHash := "1111111111111111111111111111111111111111111111111111111111111111"
	height := int64(124)
	confirmations := int64(1)
	returnedHash := cycleHash
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		c.Assert(method, Equals, "getblock")
		return map[string]any{"hash": returnedHash, "height": height, "confirmations": confirmations}
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	client.cfg.BlockScanner.MaxReorgRescanBlocks = 1
	client.updateCurrentBlockHeight(123)
	lock := &rpc.ISLockResult{CycleHash: cycleHash}
	// A lagging scanner and a cycle outside its reorg window are both valid.
	for _, want := range []int64{124, 1} {
		height = want
		got, err := client.instantSend.observationHeight(lock, 123)
		c.Assert(err, IsNil)
		c.Assert(got, Equals, want)
	}
	confirmations = -1
	_, err := client.instantSend.observationHeight(lock, 123)
	c.Assert(err, NotNil)
	confirmations = 1
	returnedHash = "different"
	_, err = client.instantSend.observationHeight(lock, 123)
	c.Assert(err, NotNil)
}

func (s *DashSuite) TestDashGetInstantSendLocksFiltersUnlockedTxs(c *C) {
	asgardAddress := newDashTestAddress(c, 0x11)
	senderAddress := newDashTestAddress(c, 0x22)

	mempoolLocked := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mempoolUnlocked := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	prevLocked := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	prevUnlocked := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	cycleHash := "1111111111111111111111111111111111111111111111111111111111111111"
	// Deliberately different from the scanner height passed below; InstantSend
	// observations should use the lock cycle height, not the local scan height.
	cycleHeight := int64(122)

	lockedOutputScript := newDashP2PKHScriptHex(c, asgardAddress)
	senderScript := newDashP2PKHScriptHex(c, senderAddress)
	memoScript := newDashMemoScriptHex(c, "NOOP")

	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{
				mempoolLocked: map[string]any{
					"size":   200,
					"vsize":  200,
					"weight": 800,
					"fees": map[string]any{
						"base": 0.00002,
					},
					"time":   time.Now().Unix(),
					"height": 123,
				},
				mempoolUnlocked: map[string]any{
					"size":   200,
					"vsize":  200,
					"weight": 800,
					"fee":    0.00002,
					"time":   time.Now().Unix(),
					"height": 123,
				},
			}
		case "getrawtransaction":
			var txID string
			err := json.Unmarshal(params[0], &txID)
			if err != nil {
				c.Fatalf("decode txid: %v", err)
			}

			switch txID {
			case mempoolLocked:
				return map[string]any{
					"txid":                 mempoolLocked,
					"hash":                 mempoolLocked,
					"version":              2,
					"locktime":             123,
					"confirmations":        0,
					"instantlock":          true,
					"instantlock_internal": false,
					"vin":                  []map[string]any{{"txid": prevLocked, "vout": 0, "sequence": uint32(4294967293)}},
					"vout":                 []map[string]any{{"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": lockedOutputScript, "type": "pubkeyhash", "addresses": []string{asgardAddress}}}, {"n": 1, "value": 0.0, "scriptPubKey": map[string]any{"hex": memoScript, "type": "nulldata"}}},
				}
			case mempoolUnlocked:
				return map[string]any{
					"txid":                 mempoolUnlocked,
					"hash":                 mempoolUnlocked,
					"version":              2,
					"locktime":             0,
					"confirmations":        0,
					"instantlock":          false,
					"instantlock_internal": false,
					"vin":                  []map[string]any{{"txid": prevUnlocked, "vout": 0, "sequence": uint32(4294967295)}},
					"vout":                 []map[string]any{{"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": lockedOutputScript, "type": "pubkeyhash", "addresses": []string{asgardAddress}}}, {"n": 1, "value": 0.0, "scriptPubKey": map[string]any{"hex": memoScript, "type": "nulldata"}}},
				}
			case prevLocked, prevUnlocked:
				return map[string]any{
					"txid":          txID,
					"hash":          txID,
					"version":       2,
					"locktime":      0,
					"confirmations": 1,
					"vin":           []map[string]any{{"txid": "coinbase", "vout": 0}},
					"vout":          []map[string]any{{"n": 0, "value": 0.10002, "scriptPubKey": map[string]any{"hex": senderScript, "type": "pubkeyhash", "addresses": []string{senderAddress}}}},
				}
			default:
				c.Fatalf("unexpected tx lookup: %s", txID)
			}
		case "getislocks":
			var txIDs []string
			err := json.Unmarshal(params[0], &txIDs)
			if err != nil {
				c.Fatalf("decode getislocks txids: %v", err)
			}
			c.Assert(txIDs, DeepEquals, []string{mempoolLocked})
			return []any{map[string]any{
				"txid":      mempoolLocked,
				"id":        "islock-request-id",
				"inputs":    []map[string]any{{"txid": prevLocked, "vout": 0}},
				"cycleHash": cycleHash,
				"signature": "signature",
				"hex":       "010203",
			}}
		case "getblock":
			var hash string
			err := json.Unmarshal(params[0], &hash)
			if err != nil {
				c.Fatalf("decode block hash: %v", err)
			}
			c.Assert(hash, Equals, cycleHash)
			return map[string]any{
				"hash":          cycleHash,
				"confirmations": 1,
				"height":        cycleHeight,
			}
		default:
			c.Fatalf("unexpected method: %s", method)
		}
		return nil
	})
	defer server.Close()

	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()

	addr, err := common.NewAddress(asgardAddress)
	c.Assert(err, IsNil)
	client.asgardCache.Store(&sharedutxo.AsgardCache{
		Addresses: []common.Address{addr},
		FetchedAt: time.Now(),
	})

	// A mempool work marker alone must not suppress a valid observation.
	c.Assert(client.tryAddToMemPoolCache(mempoolLocked), Equals, true)

	rawTx, err := client.rpc.GetRawTransactionVerbose(mempoolLocked)
	c.Assert(err, IsNil)
	item, err := client.getTxIn(&rawTx.TxRawResult, 123, true, nil)
	c.Assert(err, IsNil)
	c.Assert(item.IsEmpty(), Equals, false)

	txIn, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(txIn.MemPool, Equals, false)
	c.Assert(txIn.ConfirmationRequired, Equals, int64(0))
	c.Assert(len(txIn.TxArray), Equals, 1)
	c.Assert(txIn.TxArray[0].BlockHeight, Equals, cycleHeight)
	c.Assert(txIn.TxArray[0].Tx, Equals, mempoolLocked)
	c.Assert(txIn.TxArray[0].Sender, Equals, senderAddress)
	c.Assert(txIn.TxArray[0].To, Equals, asgardAddress)
	c.Assert(txIn.TxArray[0].Memo, Equals, "NOOP")
	c.Assert(txIn.TxArray[0].Coins.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(10_000_000))
	observed, err := client.temporalStorage.HasObservedTx(mempoolLocked)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, true)
	c.Assert(txIn.Persisted, IsNil)
	c.Assert(txIn.TxArray[0].OnObservedTxStored, NotNil)
	// A pending claim is not retried through the InstantSend path.
	retry, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(retry.TxArray, HasLen, 0)
	txIn.TxArray[0].OnObservedTxStored()
	// Mining the InstantSend transaction must not create another observation.
	client.disableVinZeroBatch = true
	mined, err := client.extractTxs(&btcjson.GetBlockVerboseTxResult{Height: 150, Tx: []btcjson.TxRawResult{rawTx.TxRawResult}})
	c.Assert(err, IsNil)
	c.Assert(mined.TxArray, HasLen, 0)
}

func (s *DashSuite) TestDashInstantSendAcceptsLargeLowFeePaymentsWithoutPricing(c *C) {
	asgardAddress := newDashTestAddress(c, 0x33)
	senderAddress := newDashTestAddress(c, 0x44)

	mempoolLocked := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	prevLocked := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	cycleHash := "7777777777777777777777777777777777777777777777777777777777777777"

	lockedOutputScript := newDashP2PKHScriptHex(c, asgardAddress)
	senderScript := newDashP2PKHScriptHex(c, senderAddress)
	memoScript := newDashMemoScriptHex(c, "NOOP")

	amount := float64(1000)
	entryTime := time.Now().Unix()
	includeFee := true
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			entry := map[string]any{"time": entryTime, "height": 123}
			if includeFee {
				entry["vsize"] = 200
				entry["fees"] = map[string]any{"base": 0.000002} // 1 sat/vB, below the former floor.
			}
			return map[string]any{mempoolLocked: entry}
		case "getrawtransaction":
			var txID string
			err := json.Unmarshal(params[0], &txID)
			if err != nil {
				c.Fatalf("decode txid: %v", err)
			}

			switch txID {
			case mempoolLocked:
				return map[string]any{
					"txid":                 mempoolLocked,
					"hash":                 mempoolLocked,
					"version":              2,
					"locktime":             0,
					"confirmations":        0,
					"instantlock":          true,
					"instantlock_internal": false,
					"vin":                  []map[string]any{{"txid": prevLocked, "vout": 0, "sequence": uint32(4294967295)}},
					"vout":                 []map[string]any{{"n": 0, "value": amount, "scriptPubKey": map[string]any{"hex": lockedOutputScript, "type": "pubkeyhash", "addresses": []string{asgardAddress}}}, {"n": 1, "value": 0.0, "scriptPubKey": map[string]any{"hex": memoScript, "type": "nulldata"}}},
				}
			case prevLocked:
				return map[string]any{
					"txid":          txID,
					"hash":          txID,
					"version":       2,
					"locktime":      0,
					"confirmations": 1,
					"vin":           []map[string]any{{"txid": "coinbase", "vout": 0}},
					"vout":          []map[string]any{{"n": 0, "value": amount + 0.000002, "scriptPubKey": map[string]any{"hex": senderScript, "type": "pubkeyhash", "addresses": []string{senderAddress}}}},
				}
			default:
				c.Fatalf("unexpected tx lookup: %s", txID)
			}
		case "getislocks":
			var txIDs []string
			err := json.Unmarshal(params[0], &txIDs)
			if err != nil {
				c.Fatalf("decode getislocks txids: %v", err)
			}
			c.Assert(txIDs, DeepEquals, []string{mempoolLocked})
			return []any{map[string]any{
				"txid":      mempoolLocked,
				"id":        "islock-request-id",
				"inputs":    []map[string]any{{"txid": prevLocked, "vout": 0}},
				"cycleHash": cycleHash,
				"signature": "signature",
				"hex":       "010203",
			}}
		case "getblock":
			var hash string
			err := json.Unmarshal(params[0], &hash)
			if err != nil {
				c.Fatalf("decode block hash: %v", err)
			}
			c.Assert(hash, Equals, cycleHash)
			return map[string]any{
				"hash":          cycleHash,
				"confirmations": 1,
				"height":        int64(123),
			}
		default:
			c.Fatalf("unexpected method: %s", method)
		}
		return nil
	})
	defer server.Close()

	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()

	addr, err := common.NewAddress(asgardAddress)
	c.Assert(err, IsNil)
	client.asgardCache.Store(&sharedutxo.AsgardCache{
		Addresses: []common.Address{addr},
		FetchedAt: time.Now(),
	})

	client.bridge = dashOracleBridge{ThorchainBridge: client.bridge, getPrice: func(string) (cosmos.Uint, error) {
		c.Fatal("InstantSend must not request a USD price")
		return cosmos.ZeroUint(), nil
	}}
	// A large locked payment is eligible with a low fee or without fee metadata.
	for _, withFee := range []bool{true, false} {
		includeFee = withFee
		var groups []types.TxIn
		groups, err = client.instantSend.scan(123)
		c.Assert(err, IsNil)
		c.Assert(groups, HasLen, 1)
		c.Assert(groups[0].MemPool, Equals, false)
		c.Assert(groups[0].ConfirmationRequired, Equals, int64(0))
		c.Assert(groups[0].TxArray, HasLen, 1)
		c.Assert(groups[0].TxArray[0].Tx, Equals, mempoolLocked)
		c.Assert(groups[0].TxArray[0].Coins[0].Amount.Equal(cosmos.NewUint(1000*common.One)), Equals, true)
	}

	// Age and dust checks still apply, and rejection must not suppress a retry.
	entryTime = time.Now().Add(-3 * time.Minute).Unix()
	groups, err := client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(groups, HasLen, 0)
	entryTime = time.Now().Unix()
	amount = 0.00000001
	groups, err = client.instantSend.scan(123)
	c.Assert(err, IsNil)
	c.Assert(groups, HasLen, 0)
	amount = 1000
	txIn, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(txIn.TxArray, HasLen, 1)
	c.Assert(txIn.TxArray[0].Tx, Equals, mempoolLocked)
}

func (s *DashSuite) TestDashInstantSendSkipsUnsupportedChains(c *C) {
	for _, chain := range []common.Chain{common.BTCChain, common.BCHChain, common.LTCChain, common.DOGEChain, common.ZECChain, common.EmptyChain} {
		// No RPC client or storage: unsupported chains must return before using either.
		client := &Client{cfg: config.BifrostChainConfiguration{ChainID: chain}}
		client.instantSend = instantSendHandler{client: client, enabled: client.cfg.InstantSend.Enabled}
		groups, err := client.instantSend.scan(123)
		c.Assert(err, IsNil, Commentf("chain: %s", chain))
		c.Assert(groups, IsNil, Commentf("chain: %s", chain))
	}
}

// newInstantSendScanFixture fills the mempool with distinct customer deposits.
func newInstantSendScanFixture(c *C, count int) (*Client, *dashMempoolFixture, []string, func()) {
	client, fixture, cleanup := newDashMempoolFixture(c)
	client.cfg.UTXO.TransactionBatchSize = count
	fixture.entries = make(map[string]map[string]any, count)
	ids := make([]string, count)
	for i := range ids {
		id := fmt.Sprintf("%064x", i+1)
		ids[i] = id
		tx := make(map[string]any)
		for key, value := range fixture.txs[fixture.inbound] {
			tx[key] = value
		}
		tx["txid"], tx["hash"] = id, id
		fixture.txs[id] = tx
		fixture.entries[id] = dashTestMempoolEntry()
		fixture.entries[id]["instantlock"] = "true"
	}
	return client, fixture, ids, cleanup
}

// assertInstantSendScanClaims checks that only delivered deposits consumed allowance
// and received durable observation markers, so failed lookups remain retryable.
func assertInstantSendScanClaims(c *C, client *Client, ids, delivered []string) {
	accepted := make(map[string]bool, len(delivered))
	for _, id := range delivered {
		accepted[id] = true
	}
	for _, id := range ids {
		observed, err := client.temporalStorage.HasObservedTx(id)
		c.Assert(err, IsNil)
		c.Assert(observed, Equals, accepted[id], Commentf("txid %s", id))
	}
	state, err := client.temporalStorage.GetInstantSendState()
	c.Assert(err, IsNil)
	c.Assert(state.Reservations, HasLen, len(delivered))
	c.Assert(state.Reserved, Equals, uint64(len(delivered))*common.One/10)
}

// TestDashInstantSendRetriesUnavailableLocks preserves good deposits while null,
// None and failed cycle lookups wait for a later scan without claiming allowance.
func (s *DashSuite) TestDashInstantSendRetriesUnavailableLocks(c *C) {
	client, fixture, ids, cleanup := newInstantSendScanFixture(c, 4)
	defer cleanup()
	var recovered atomic.Bool
	failedCycle := strings.Repeat("4", 64)
	fixture.overrideRPC = func(method string, params []json.RawMessage) (any, bool) {
		switch method {
		case "getislocks":
			var requested []string
			c.Assert(json.Unmarshal(params[0], &requested), IsNil)
			locks := make([]any, len(requested))
			for i, id := range requested {
				if !recovered.Load() && id == ids[0] {
					continue // JSON null
				}
				if !recovered.Load() && id == ids[1] {
					locks[i] = "None"
					continue
				}
				cycle := strings.Repeat("3", 64)
				if !recovered.Load() && id == ids[2] {
					cycle = failedCycle
				}
				locks[i] = map[string]any{"txid": id, "cycleHash": cycle}
			}
			return locks, true
		case "getblock":
			var hash string
			c.Assert(json.Unmarshal(params[0], &hash), IsNil)
			if hash == failedCycle {
				return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "cycle unavailable"}, true
			}
		}
		return nil, false
	}
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[3])
	assertInstantSendScanClaims(c, client, ids, ids[3:])

	recovered.Store(true)
	batch, err = client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 3)
	for i, item := range batch.TxArray {
		c.Assert(item.Tx, Equals, ids[i])
	}
	assertInstantSendScanClaims(c, client, ids, ids)
}

// TestDashInstantSendKeepsSuccessfulLockChunks checks partial progress across the
// RPC's 100-ID boundary and delivery of the failed chunk after RPC recovery.
func (s *DashSuite) TestDashInstantSendKeepsSuccessfulLockChunks(c *C) {
	client, fixture, ids, cleanup := newInstantSendScanFixture(c, 101)
	defer cleanup()
	var recovered atomic.Bool
	calls := countInstantSendRPCs(c, fixture)
	fixture.overrideRPC = func(method string, params []json.RawMessage) (any, bool) {
		if method != "getislocks" {
			return nil, false
		}
		var requested []string
		c.Assert(json.Unmarshal(params[0], &requested), IsNil)
		if requested[0] == ids[100] && !recovered.Load() {
			c.Assert(requested, HasLen, 1)
			return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "second chunk unavailable"}, true
		}
		return nil, false
	}
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 100)
	for i, item := range batch.TxArray {
		c.Assert(item.Tx, Equals, ids[i])
	}
	c.Assert(calls("getislocks"), Equals, 2)
	assertInstantSendScanClaims(c, client, ids, ids[:100])

	recovered.Store(true)
	batch, err = client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[100])
	c.Assert(calls("getislocks"), Equals, 3)
	assertInstantSendScanClaims(c, client, ids, ids)
}

// TestDashInstantSendDoesNotCacheCycleFailures retries a shared cycle for the next
// candidate in the same scan, then caches only its successful height lookup.
func (s *DashSuite) TestDashInstantSendDoesNotCacheCycleFailures(c *C) {
	client, fixture, ids, cleanup := newInstantSendScanFixture(c, 3)
	defer cleanup()
	var cycleReads atomic.Int64
	fixture.overrideRPC = func(method string, _ []json.RawMessage) (any, bool) {
		if method == "getblock" && cycleReads.Add(1) == 1 {
			return &btcjson.RPCError{Code: btcjson.ErrRPCInternal.Code, Message: "cycle unavailable"}, true
		}
		return nil, false
	}
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 2)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[1])
	c.Assert(batch.TxArray[1].Tx, Equals, ids[2])
	c.Assert(cycleReads.Load(), Equals, int64(2))
	assertInstantSendScanClaims(c, client, ids, ids[1:])

	batch, err = client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[0])
	c.Assert(cycleReads.Load(), Equals, int64(3))
	assertInstantSendScanClaims(c, client, ids, ids)
}

// TestDashInstantSendHexOnlyAddresses exercises the deposit and parent parsing
// used by dashd versions that omit scriptPubKey.addresses from RPC responses.
func (s *DashSuite) TestDashInstantSendHexOnlyAddresses(c *C) {
	client, fixture, ids, cleanup := newInstantSendScanFixture(c, 1)
	defer cleanup()
	for _, tx := range fixture.txs {
		outputs, ok := tx["vout"].([]any)
		c.Assert(ok, Equals, true)
		for _, value := range outputs {
			var output map[string]any
			output, ok = value.(map[string]any)
			c.Assert(ok, Equals, true)
			var script map[string]any
			script, ok = output["scriptPubKey"].(map[string]any)
			c.Assert(ok, Equals, true)
			delete(script, "addresses")
			delete(script, "address")
		}
	}
	batch, err := client.instantSend.fetchBatch(123, instantSendAdmission{allowed: true})
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[0])
	c.Assert(batch.TxArray[0].Sender, Equals, fixture.user)
	c.Assert(batch.TxArray[0].To, Equals, fixture.asgard)
	c.Assert(batch.TxArray[0].Memo, Equals, "NOOP")
	assertInstantSendScanClaims(c, client, ids, ids)
}

func (s *DashSuite) TestDashDifferentCyclesDeliveredSeparately(c *C) {
	ids := []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}
	cycles := []string{strings.Repeat("1", 64), strings.Repeat("2", 64)}
	previous := strings.Repeat("c", 64)
	vault := newDashTestAddress(c, 0x11)
	sender := newDashTestAddress(c, 0x22)
	vaultScript := newDashP2PKHScriptHex(c, vault)
	senderScript := newDashP2PKHScriptHex(c, sender)
	memo := newDashMemoScriptHex(c, "NOOP")
	secondFetched := make(chan struct{})
	var once sync.Once
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{ids[0]: dashTestMempoolEntry(), ids[1]: dashTestMempoolEntry()}
		case "getrawtransaction":
			var id string
			c.Assert(json.Unmarshal(params[0], &id), IsNil)
			if id == previous {
				return btcjson.TxRawResult{Txid: id, Vout: []btcjson.Vout{{Value: 0.10002, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: senderScript, Type: "pubkeyhash", Addresses: []string{sender}}}}}
			}
			if id == ids[0] {
				<-secondFetched
			} else {
				once.Do(func() { close(secondFetched) })
			}
			return rpc.TxResult{InstantLock: true, InstantLockInternal: true, TxRawResult: btcjson.TxRawResult{
				Txid: id, LockTime: 201, Vin: []btcjson.Vin{{Txid: previous, Sequence: 0xfffffffd}},
				Vout: []btcjson.Vout{
					{Value: 0.1, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: vaultScript, Type: "pubkeyhash", Addresses: []string{vault}}},
					{N: 1, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: memo, Type: "nulldata"}},
				},
			}}
		case "getislocks":
			var requested []string
			c.Assert(json.Unmarshal(params[0], &requested), IsNil)
			locks := make([]rpc.ISLockResult, len(requested))
			for i, id := range requested {
				index := 0
				if id == ids[1] {
					index = 1
				}
				locks[i] = rpc.ISLockResult{TxID: id, CycleHash: cycles[index]}
			}
			return locks
		case "getblock":
			var hash string
			c.Assert(json.Unmarshal(params[0], &hash), IsNil)
			height := int64(100)
			if hash == cycles[1] {
				height = 200
			}
			return btcjson.GetBlockVerboseResult{Hash: hash, Height: height, Confirmations: 1}
		}
		c.Fatalf("unexpected RPC %s", method)
		return nil
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	client.cfg.ParallelMempoolScan = 2
	client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{common.Address(vault)}, FetchedAt: time.Now()})
	for i, height := range []int64{100, 200} {
		batch, err := client.instantSend.fetchBatch(201, instantSendAdmission{allowed: true})
		c.Assert(err, IsNil)
		c.Assert(batch.TxArray, HasLen, 1)
		c.Assert(batch.TxArray[0].Tx, Equals, ids[i])
		c.Assert(batch.TxArray[0].BlockHeight, Equals, height)
	}
}
