package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dashpay/dashd-go/chaincfg/chainhash"
	dashwire "github.com/dashpay/dashd-go/wire"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type dashMempoolFixture struct {
	lockedOutbound, unlockedOutbound, inbound, lowFeeInbound string
	asgard, user                                             string
	txs                                                      map[string]map[string]any
	entries                                                  map[string]map[string]any
	failOrdinary, failInstantSend, chainLocked               bool
	ordinaryCalls, instantSendCalls                          int
	beforeRawTransaction                                     func(string)
	beforeRPC                                                func(string, []json.RawMessage)
	overrideRPC                                              func(string, []json.RawMessage) (any, bool)
	utxos                                                    []btcjson.ListUnspentResult
	failMempoolEntry                                         bool
}

func newDashMempoolFixture(c *C, rpcTimeout ...time.Duration) (*Client, *dashMempoolFixture, func()) {
	f := &dashMempoolFixture{
		lockedOutbound: strings.Repeat("a", 64), unlockedOutbound: strings.Repeat("b", 64),
		inbound: strings.Repeat("c", 64), lowFeeInbound: strings.Repeat("d", 64),
		asgard: newDashTestAddress(c, 0x11), user: newDashTestAddress(c, 0x22),
		txs: make(map[string]map[string]any), entries: make(map[string]map[string]any),
	}
	prevVault, prevUser := strings.Repeat("e", 64), strings.Repeat("f", 64)
	output := func(address string, amount float64) map[string]any {
		return map[string]any{"n": 0, "value": amount, "scriptPubKey": map[string]any{
			"hex": newDashP2PKHScriptHex(c, address), "type": "pubkeyhash", "addresses": []string{address},
		}}
	}
	f.txs[prevVault] = map[string]any{"txid": prevVault, "vout": []any{output(f.asgard, 0.10002)}}
	f.txs[prevUser] = map[string]any{"txid": prevUser, "vout": []any{output(f.user, 0.10002)}}
	for _, id := range []string{f.lockedOutbound, f.unlockedOutbound, f.inbound, f.lowFeeInbound} {
		prev, destination, memo := prevVault, f.user, "OUT:"+strings.Repeat("1", 64)
		if id == f.inbound || id == f.lowFeeInbound {
			prev, destination, memo = prevUser, f.asgard, "=:THOR.RUNE"
		}
		locked := id != f.unlockedOutbound
		f.txs[id] = map[string]any{
			"txid": id, "version": 2, "instantlock": locked,
			"vin": []any{map[string]any{"txid": prev, "vout": 0, "sequence": uint32(4294967295)}},
			"vout": []any{output(destination, 0.1), map[string]any{
				"n": 1, "value": float64(0),
				"scriptPubKey": map[string]any{"hex": newDashMemoScriptHex(c, memo), "type": "nulldata"},
			}},
		}
		f.entries[id] = dashTestMempoolEntry()
		f.entries[id]["instantlock"] = strconv.FormatBool(locked)
	}
	// Early admission verifies the serialized transaction and its hash.
	for _, field := range []*string{&f.inbound, &f.lowFeeInbound} {
		old := *field
		value := f.txs[old]
		tx := dashwire.NewMsgTx(2)
		hash, parseErr := chainhash.NewHashFromStr(prevUser)
		c.Assert(parseErr, IsNil)
		tx.AddTxIn(dashwire.NewTxIn(dashwire.NewOutPoint(hash, 0), []byte{old[0]}, nil))
		data, encodeErr := json.Marshal(value)
		c.Assert(encodeErr, IsNil)
		var verbose btcjson.TxRawResult
		c.Assert(json.Unmarshal(data, &verbose), IsNil)
		for _, output := range verbose.Vout {
			script, decodeErr := hex.DecodeString(output.ScriptPubKey.Hex)
			c.Assert(decodeErr, IsNil)
			amount, amountErr := btcutil.NewAmount(output.Value)
			c.Assert(amountErr, IsNil)
			tx.AddTxOut(dashwire.NewTxOut(int64(amount), script))
		}
		var raw bytes.Buffer
		c.Assert(tx.Serialize(&raw), IsNil)
		*field = tx.TxHash().String()
		value["txid"] = *field
		value["hex"] = hex.EncodeToString(raw.Bytes())
		f.txs[*field] = value
		f.entries[*field] = f.entries[old]
		delete(f.txs, old)
		delete(f.entries, old)
	}
	if f.inbound > f.lowFeeInbound {
		f.inbound, f.lowFeeInbound = f.lowFeeInbound, f.inbound
	}
	// Local receipt time does not determine eligibility.
	f.entries[f.lockedOutbound]["time"] = time.Now().Add(-time.Hour).Unix()
	blockHash := strings.Repeat("2", 64)
	for _, id := range []string{prevUser, prevVault} {
		f.txs[id]["chainlock"] = true
		f.txs[id]["blockhash"] = blockHash
	}
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		if f.beforeRPC != nil {
			f.beforeRPC(method, params)
		}
		if f.overrideRPC != nil {
			if result, overridden := f.overrideRPC(method, params); overridden {
				return result
			}
		}
		switch method {
		case "listunspent":
			return f.utxos
		case "getmempoolentry":
			if f.failMempoolEntry {
				return &btcjson.RPCError{Code: btcjson.ErrRPCInvalidAddressOrKey, Message: "entry unavailable"}
			}
			return map[string]any{"ancestorcount": 1, "descendantcount": 1}
		case "getrawmempool":
			verbose := false
			if len(params) > 0 {
				c.Assert(json.Unmarshal(params[0], &verbose), IsNil)
			}
			if verbose {
				f.instantSendCalls++
				if f.failInstantSend {
					return "invalid mempool response"
				}
				return f.entries
			}
			f.ordinaryCalls++
			if f.failOrdinary {
				return "invalid mempool response"
			}
			ids := make([]string, 0, len(f.entries))
			for id := range f.entries {
				ids = append(ids, id)
			}
			return ids
		case "getrawtransaction":
			var id string
			c.Assert(json.Unmarshal(params[0], &id), IsNil)
			if f.beforeRawTransaction != nil {
				f.beforeRawTransaction(id)
			}
			c.Assert(f.txs[id], NotNil)
			return f.txs[id]
		case "getbestchainlock":
			return map[string]any{"height": 100, "blockhash": blockHash, "known_block": true}
		case "getblockcount":
			return 100
		case "getblockhash":
			return blockHash
		case "getblock":
			var verbosity int
			if len(params) > 1 {
				c.Assert(json.Unmarshal(params[1], &verbosity), IsNil)
			}
			if verbosity == 1 {
				return map[string]any{"hash": blockHash, "height": 99, "tx": []string{prevUser, prevVault}}
			}
			var hash string
			c.Assert(json.Unmarshal(params[0], &hash), IsNil)
			c.Assert(hash, Equals, blockHash)
			return map[string]any{
				"hash": hash, "height": 150, "chainlock": f.chainLocked,
				"tx": []any{f.txs[f.lockedOutbound], f.txs[f.unlockedOutbound]},
			}
		default:
			c.Fatalf("unexpected RPC %s", method)
			return nil
		}
	})
	client, cleanup := newDashClientForRPCServer(c, server.URL, rpcTimeout...)
	client.cfg.UTXO.TransactionBatchSize = 10
	client.cfg.UTXO.MaxMempoolBatches = 1
	client.asgardCache.Store(&sharedutxo.AsgardCache{
		Addresses: []common.Address{common.Address(f.asgard)}, FetchedAt: time.Now(),
	})
	stopScanner := startDashTestScanner(c, client)
	return client, f, func() { stopScanner(); server.Close(); cleanup() }
}

// TestDashBlockClearsMempoolMarkers covers Dash responses without a transaction
// hash, including ignored transactions and entries already marked observed.
func (s *DashSuite) TestDashBlockClearsMempoolMarkers(c *C) {
	for _, cacheSize := range []int{0, 10} {
		func() {
			client, f, cleanup := newDashMempoolFixture(c)
			defer cleanup()
			storage, err := blockscanner.NewBlockScannerStorage(c.MkDir(), config.LevelDBOptions{})
			c.Assert(err, IsNil)
			defer storage.Close()
			db := storage.GetInternalDb()
			client.temporalStorage, err = sharedutxo.NewTemporalStorage(db, cacheSize)
			c.Assert(err, IsNil)
			client.disableVinZeroBatch = true

			batch, err := client.fetchMemPoolTxs(123, true)
			c.Assert(err, IsNil)
			c.Assert(batch.TxArray, HasLen, 2)
			added, err := client.temporalStorage.TrackObservedTx(f.lockedOutbound)
			c.Assert(err, IsNil)
			c.Assert(added, Equals, true)
			ignored := strings.Repeat("9", 64)
			added, err = client.temporalStorage.TrackMempoolTx(ignored)
			c.Assert(err, IsNil)
			c.Assert(added, Equals, true)

			f.chainLocked = true
			block, err := client.getBlock(150)
			c.Assert(err, IsNil)
			for _, tx := range block.Tx {
				c.Assert(tx.Hash, Equals, "") // Dash Core supplies txid, not hash.
			}
			block.Tx = append(block.Tx, btcjson.TxRawResult{Txid: ignored})
			batch, err = client.extractTxs(block)
			c.Assert(err, IsNil)
			c.Assert(batch.TxArray, HasLen, 1)
			c.Assert(batch.TxArray[0].Tx, Equals, f.unlockedOutbound)

			for _, id := range []string{f.lockedOutbound, f.unlockedOutbound, ignored} {
				var tracked bool
				tracked, err = db.Has([]byte(sharedutxo.PrefixMempool+id), nil)
				c.Assert(err, IsNil)
				c.Check(tracked, Equals, false, Commentf("cache size %d, mined tx %s", cacheSize, id))
				// The in-memory cache must also allow tracking the ID again.
				added, err = client.temporalStorage.TrackMempoolTx(id)
				c.Assert(err, IsNil)
				c.Check(added, Equals, true)
			}
			for _, id := range []string{f.inbound, f.lowFeeInbound} {
				added, err = client.temporalStorage.TrackMempoolTx(id)
				c.Assert(err, IsNil)
				c.Check(added, Equals, false) // Still pending in the mempool.
			}
			observed, err := client.temporalStorage.HasTxMarker(f.lockedOutbound)
			c.Assert(err, IsNil)
			c.Check(observed, Equals, true)
		}()
	}
}

func (s *DashSuite) TestDashBlockWithLargeUnrelatedTransaction(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	client.disableVinZeroBatch = true
	client.consolidateInProgress.Store(true)
	client.cfg.UTXO.BlockCacheCount = 2000
	_, err := client.temporalStorage.ClaimInstantSendTxs([]string{f.inbound})
	c.Assert(err, IsNil)
	vins := make([]btcjson.Vin, common.InstantSendMaxInputs+1)
	for i := range vins {
		vins[i] = btcjson.Vin{Txid: strings.Repeat("9", 64), Vout: uint32(i)}
	}
	f.overrideRPC = func(method string, _ []json.RawMessage) (any, bool) {
		switch method {
		case "getnetworkinfo":
			return map[string]any{"relayfee": 0.00001}, true
		case "getblock":
			return map[string]any{
				"hash": strings.Repeat("2", 64), "height": 150, "chainlock": true,
				"tx": []any{btcjson.TxRawResult{Txid: strings.Repeat("8", 64), Vin: vins}, f.txs[f.lockedOutbound]},
			}, true
		default:
			return nil, false
		}
	}
	batch, err := client.FetchTxs(150, 200)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, f.lockedOutbound)
}

// TestBlockMempoolCleanupPreservesOtherChains pins the existing cleanup key
// for non-Dash chains when hash and txid differ.
func (s *DashSuite) TestBlockMempoolCleanupPreservesOtherChains(c *C) {
	for _, chain := range []common.Chain{common.BTCChain, common.LTCChain, common.BCHChain, common.DOGEChain, common.ZECChain} {
		func() {
			client, f, cleanup := newDashMempoolFixture(c)
			defer cleanup()
			client.cfg.ChainID = chain
			client.disableVinZeroBatch = true
			id, hash := f.inbound, strings.Repeat("9", 64)
			for _, key := range []string{id, hash} {
				added, err := client.temporalStorage.TrackMempoolTx(key)
				c.Assert(err, IsNil)
				c.Assert(added, Equals, true)
			}
			_, err := client.extractTxs(&btcjson.GetBlockVerboseTxResult{
				Height: 150, Tx: []btcjson.TxRawResult{{Txid: id, Hash: hash}},
			})
			c.Assert(err, IsNil)
			added, err := client.temporalStorage.TrackMempoolTx(hash)
			c.Assert(err, IsNil)
			c.Check(added, Equals, true, Commentf("chain: %s", chain))
			added, err = client.temporalStorage.TrackMempoolTx(id)
			c.Assert(err, IsNil)
			c.Check(added, Equals, false, Commentf("chain: %s", chain))
		}()
	}
}

// TestMempoolChainLockFilter skips only ChainLocked Dash transactions, preserving
// other outbounds in the batch and the existing policy for non-Dash chains.
func (s *DashSuite) TestMempoolChainLockFilter(c *C) {
	for _, tc := range []struct {
		name          string
		chain         common.Chain
		chainLock     bool
		instantLock   bool
		confirmations int
		want          int
	}{
		{"dash chainlocked", common.DASHChain, true, false, 1, 1},
		{"dash instantsend", common.DASHChain, false, true, 0, 2},
		{"dash mined without chainlock", common.DASHChain, false, false, 1, 2},
		{"bitcoin ignores chainlock field", common.BTCChain, true, false, 1, 2},
	} {
		func() {
			client, f, cleanup := newDashMempoolFixture(c)
			defer cleanup()
			client.cfg.ChainID = tc.chain
			f.entries = map[string]map[string]any{
				f.lockedOutbound:   dashTestMempoolEntry(),
				f.unlockedOutbound: dashTestMempoolEntry(),
			}
			f.txs[f.lockedOutbound]["chainlock"] = tc.chainLock
			f.txs[f.lockedOutbound]["instantlock"] = tc.instantLock
			f.txs[f.lockedOutbound]["confirmations"] = tc.confirmations

			batch, err := client.fetchMemPoolTxs(123, true)
			c.Assert(err, IsNil)
			c.Check(batch.TxArray, HasLen, tc.want, Commentf("%s", tc.name))
			seen := make(map[string]bool)
			for _, tx := range batch.TxArray {
				seen[tx.Tx] = true
			}
			c.Check(seen[f.unlockedOutbound], Equals, true)
			c.Check(seen[f.lockedOutbound], Equals, tc.want == 2)
			// A skipped transaction must not leave the new mempool work marker.
			added, err := client.temporalStorage.TrackMempoolTx(f.lockedOutbound)
			c.Assert(err, IsNil)
			c.Check(added, Equals, tc.want == 1)
		}()
	}
}

// TestDashMempoolSnapshotAcrossBlockCleanup ensures a delayed mempool response
// cannot re-emit ChainLocked outbounds or recreate their cleaned cache entries.
func (s *DashSuite) TestDashMempoolSnapshotAcrossBlockCleanup(c *C) {
	for _, cacheSize := range []int{0, 10} {
		func() {
			client, f, cleanup := newDashMempoolFixture(c, 10*time.Second)
			defer cleanup()
			storage, err := blockscanner.NewBlockScannerStorage(c.MkDir(), config.LevelDBOptions{})
			c.Assert(err, IsNil)
			defer storage.Close()
			db := storage.GetInternalDb()
			client.temporalStorage, err = sharedutxo.NewTemporalStorage(db, cacheSize)
			c.Assert(err, IsNil)
			client.disableVinZeroBatch = true
			f.entries = map[string]map[string]any{
				f.lockedOutbound:   dashTestMempoolEntry(),
				f.unlockedOutbound: dashTestMempoolEntry(),
			}
			first, err := client.fetchMemPoolTxs(123, true)
			c.Assert(err, IsNil)
			c.Assert(first.TxArray, HasLen, 2)

			entered, release := make(chan struct{}), make(chan struct{})
			var enteredOnce, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			f.overrideRPC = func(method string, _ []json.RawMessage) (any, bool) {
				if method != "getrawmempool" {
					return nil, false
				}
				// Capture the list before mining, then delay its delivery.
				ids := []string{f.lockedOutbound, f.unlockedOutbound}
				enteredOnce.Do(func() { close(entered) })
				<-release
				return ids, true
			}
			var replay struct {
				batch types.TxIn
				err   error
			}
			completed := make(chan struct{})
			go func() {
				defer close(completed)
				replay.batch, replay.err = client.fetchMemPoolTxs(149, true)
			}()
			defer func() {
				unblock()
				select {
				case <-completed:
				case <-time.After(5 * time.Second):
					c.Error("mempool scan did not stop after releasing the RPC")
				}
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				c.Fatal("mempool request did not reach the RPC")
			}

			f.chainLocked = true
			for _, id := range []string{f.lockedOutbound, f.unlockedOutbound} {
				f.txs[id]["chainlock"] = true
				f.txs[id]["confirmations"] = 1
			}
			block, err := client.getBlock(150)
			c.Assert(err, IsNil)
			mined, err := client.extractTxs(block)
			c.Assert(err, IsNil)
			c.Assert(mined.TxArray, HasLen, 2)
			completeDashBlock(c, client, mined)
			unblock()
			select {
			case <-completed:
			case <-time.After(5 * time.Second):
				c.Fatal("mempool scan did not finish")
			}
			c.Assert(replay.err, IsNil)
			c.Check(replay.batch.TxArray, HasLen, 0, Commentf("cache size %d", cacheSize))
			for _, id := range []string{f.lockedOutbound, f.unlockedOutbound} {
				var tracked, observed, added bool
				tracked, err = db.Has([]byte(sharedutxo.PrefixMempool+id), nil)
				c.Assert(err, IsNil)
				c.Check(tracked, Equals, false)
				observed, err = client.temporalStorage.HasTxMarker(id)
				c.Assert(err, IsNil)
				c.Check(observed, Equals, true)
				// Also verify removal from the optional in-memory cache.
				added, err = client.temporalStorage.TrackMempoolTx(id)
				c.Assert(err, IsNil)
				c.Check(added, Equals, true)
			}
		}()
	}
}

func (s *DashSuite) TestDashFetchMemPoolOutboundsAndFinalObservations(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()

	batch, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(batch.Chain, Equals, common.DASHChain)
	c.Assert(batch.MemPool, Equals, true)
	c.Assert(batch.TxArray, HasLen, 2)
	seen := make(map[string]bool)
	for _, item := range batch.TxArray {
		seen[item.Tx] = true
		c.Assert(item.Sender, Equals, f.asgard)
		c.Assert(item.To, Equals, f.user)
		c.Assert(item.BlockHeight, Equals, int64(123))
		var observed bool
		observed, err = client.temporalStorage.HasTxMarker(item.Tx)
		c.Assert(err, IsNil)
		c.Assert(observed, Equals, false) // Keep the later block observation available.
	}
	c.Assert(seen, DeepEquals, map[string]bool{f.lockedOutbound: true, f.unlockedOutbound: true})

	// The ordinary scan neither emits deposits nor suppresses InstantSend.
	final, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(final.MemPool, Equals, false)
	c.Assert(final.TxArray, HasLen, 2)
	c.Assert(final.TxArray[0].Tx, Equals, f.inbound)
	c.Assert(final.TxArray[1].Tx, Equals, f.lowFeeInbound)
	c.Assert(final.TxArray[0].BlockHeight, Equals, int64(96))
	observed, err := client.temporalStorage.HasTxMarker(f.lowFeeInbound)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, true)

	retry, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(retry.MemPool, Equals, true)
	c.Assert(retry.TxArray, HasLen, 0) // Ordinary mempool deduplication.

	// Both outbounds can still be observed once mined in a ChainLocked block.
	_, err = client.getBlock(150)
	c.Assert(err, NotNil)
	f.chainLocked = true
	block, err := client.getBlock(150)
	c.Assert(err, IsNil)
	client.disableVinZeroBatch = true
	mined, err := client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(mined.MemPool, Equals, false)
	c.Assert(mined.TxArray, HasLen, 2)
	for _, item := range mined.TxArray {
		c.Assert(seen[item.Tx], Equals, true)
		c.Assert(item.BlockHeight, Equals, int64(150))
	}
	completeDashBlock(c, client, mined)
	mined, err = client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(mined.TxArray, HasLen, 0)
}

// TestDashMempoolRestartKeepsLockObservation checks the two scanner paths using
// reopened storage, including the commit callback between the observations.
func (s *DashSuite) TestDashMempoolRestartKeepsLockObservation(c *C) {
	for _, migration := range []bool{false, true} {
		func() {
			client, f, cleanup := newDashMempoolFixture(c)
			defer cleanup()
			if migration {
				to := newDashTestAddress(c, 0x33)
				client.asgardCache.Store(&sharedutxo.AsgardCache{
					Addresses: []common.Address{common.Address(f.asgard), common.Address(to)}, FetchedAt: time.Now(),
				})
				f.txs[f.lockedOutbound]["vout"] = []any{
					map[string]any{"n": 0, "value": 0.1, "scriptPubKey": map[string]any{
						"hex": newDashP2PKHScriptHex(c, to), "type": "pubkeyhash", "addresses": []string{to},
					}},
					map[string]any{"n": 1, "value": 0, "scriptPubKey": map[string]any{
						"hex": newDashMemoScriptHex(c, "MIGRATE:90"), "type": "nulldata",
					}},
				}
				f.entries[f.lockedOutbound]["time"] = time.Now().Unix()
			}
			path := c.MkDir()
			storage, err := blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
			c.Assert(err, IsNil)
			client.cfg.BlockScanner.DBPath = path
			client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 1000)
			c.Assert(err, IsNil)
			client.signerCacheManager, err = signercache.NewSignerCacheManager(storage.GetInternalDb())
			c.Assert(err, IsNil)
			batch, err := client.fetchMemPoolTxs(123, true)
			c.Assert(err, IsNil)
			c.Assert(batch.TxArray, HasLen, 2)
			for _, item := range batch.TxArray {
				client.OnObservedTxIn(*item, item.BlockHeight)
			}
			c.Assert(storage.Close(), IsNil)
			storage, err = blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
			c.Assert(err, IsNil)
			defer storage.Close()
			client.temporalStorage, err = sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 1000)
			c.Assert(err, IsNil)
			client.signerCacheManager, err = signercache.NewSignerCacheManager(storage.GetInternalDb())
			c.Assert(err, IsNil)
			batch, err = client.fetchMemPoolTxs(124, true)
			c.Assert(err, IsNil)
			c.Assert(batch.TxArray, HasLen, 0) // persisted mempool markers survive restart
			if migration {
				batch, err = client.instantSend.fetchBatch()
				c.Assert(err, IsNil)
				c.Assert(batch.TxArray, HasLen, 2) // customer deposits remain independently eligible
			}
			f.chainLocked = true
			block, err := client.getBlock(150)
			c.Assert(err, IsNil)
			client.disableVinZeroBatch = true
			batch, err = client.extractTxs(block)
			c.Assert(err, IsNil)
			c.Assert(batch.TxArray, HasLen, 2)
			completeDashBlock(c, client, batch)
			batch, err = client.extractTxs(block)
			c.Assert(err, IsNil)
			c.Assert(batch.TxArray, HasLen, 0)
		}()
	}
}

type dashMempoolPolicyBridge struct {
	thorclient.ThorchainBridge
	enabled       int64
	accounting    *ttypes.QueryInstantSendResponse
	accountingErr error
}

func (b *dashMempoolPolicyBridge) GetMimir(key string) (int64, error) {
	if key == "DASHInstantSendEnabled" {
		return b.enabled, nil
	}
	if key == "DASHInstantSendMaxAmount" {
		return 100_000 * common.One, nil
	}
	return -1, nil
}

func (b *dashMempoolPolicyBridge) FetchNodeStatus() (ttypes.NodeStatus, error) {
	return ttypes.NodeStatus_Active, nil
}

func (b *dashMempoolPolicyBridge) GetBlockHeight() (int64, error) { return 100, nil }

func (b *dashMempoolPolicyBridge) GetAsgardPubKeys() ([]thorclient.PubKeyContractAddressPair, error) {
	// These fixtures seed vault addresses directly. An empty refresh retains
	// them when the cache expires, without calling an embedded nil bridge.
	return nil, nil
}

func (s *DashSuite) TestDashFetchMemPoolWithInstantSendDisabled(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	bridge := &dashMempoolPolicyBridge{enabled: 0}
	client.bridge = bridge
	// A disabled policy must discard any previously prepared final batches.
	client.instantSend.txQueue = sortTxBatches(
		map[int64][]*types.TxInItem{100: {{Tx: "stale-policy-batch"}}})
	for i := 0; i < 2; i++ {
		batch, err := client.FetchMemPool(123)
		c.Assert(err, IsNil)
		c.Assert(batch.MemPool, Equals, true)
		if i == 0 {
			c.Assert(batch.TxArray, HasLen, 2)
		} else {
			c.Assert(batch.TxArray, HasLen, 0)
		}
	}
	c.Assert(client.instantSend.txQueue, HasLen, 0)
	c.Assert(f.ordinaryCalls, Equals, 2)
	c.Assert(f.instantSendCalls, Equals, 0)
	bridge.enabled = 1
	_, err := client.FetchMemPool(123) // ordinary scan resumes the alternating schedule
	c.Assert(err, IsNil)
	final, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(final.MemPool, Equals, false)
	c.Assert(final.TxArray, HasLen, 2)
	c.Assert(final.TxArray[0].Tx, Equals, f.inbound)
	c.Assert(final.TxArray[1].Tx, Equals, f.lowFeeInbound)
}

func (s *DashSuite) TestDashFetchMemPoolErrorsDoNotStarveOtherPath(c *C) {
	client, f, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	f.failOrdinary = true
	_, err := client.FetchMemPool(123)
	c.Assert(err, NotNil)
	final, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(final.TxArray, HasLen, 2)
	c.Assert(final.MemPool, Equals, false)

	f.failOrdinary = false
	batch, err := client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 2)
	c.Assert(batch.MemPool, Equals, true)
	f.failInstantSend = true
	_, err = client.FetchMemPool(123)
	c.Assert(err, NotNil)
	batch, err = client.FetchMemPool(123)
	c.Assert(err, IsNil)
	c.Assert(batch.MemPool, Equals, true)
	c.Assert(f.ordinaryCalls, Equals, 3)
	c.Assert(f.instantSendCalls, Equals, 3)
}

// GetInstantSend supplies a network accounting snapshot unless a test overrides it.
func (b *dashMempoolPolicyBridge) GetInstantSend(chain common.Chain) (*ttypes.QueryInstantSendResponse, error) {
	if b.accountingErr != nil {
		return nil, b.accountingErr
	}
	if b.accounting != nil {
		return b.accounting, nil
	}
	key, err := dashTestVaultPubKey()
	if err != nil {
		return nil, err
	}
	return &ttypes.QueryInstantSendResponse{
		ObservationHeight: 100, MinAmount: common.DefaultInstantSendMinAmount, MaxAmount: 100_000 * common.One,
		Vaults: []ttypes.QueryInstantSendVault{{Eligible: true, State: common.InstantSendState{Chain: common.DASHChain, PubKey: key}}},
	}, nil
}
