package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/dashpay/dashd-go/btcutil"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	dashchaincfg "github.com/dashpay/dashd-go/chaincfg"
	dashtxscript "github.com/dashpay/dashd-go/txscript"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

type rpcRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	ID     any               `json:"id"`
}

type dashTestPoolMgr struct{}

type dashOracleBridge struct {
	thorclient.ThorchainBridge
	getPrice func(string) (cosmos.Uint, error)
}

func (b dashOracleBridge) GetOraclePrice(symbol string) (cosmos.Uint, error) {
	return b.getPrice(symbol)
}

type dashTestPoolMgrFunc func(common.Asset, common.Asset, cosmos.Uint) (cosmos.Uint, error)

func (f dashTestPoolMgrFunc) GetValue(source, target common.Asset, amount cosmos.Uint) (cosmos.Uint, error) {
	return f(source, target, amount)
}

func (dashTestPoolMgr) GetValue(source common.Asset, target common.Asset, amount cosmos.Uint) (cosmos.Uint, error) {
	if !source.Equals(common.DASHAsset) {
		return cosmos.ZeroUint(), fmt.Errorf("unexpected source asset: %s", source)
	}
	if !target.Equals(common.TOR) {
		return cosmos.ZeroUint(), fmt.Errorf("unexpected target asset: %s", target)
	}
	return amount.MulUint64(mocknetDashPrice), nil
}

func newDashTestAddress(c *C, seed byte) string {
	hash := bytes.Repeat([]byte{seed}, 20)
	addr, err := btcutil.NewAddressPubKeyHash(hash, &dashchaincfg.RegressionNetParams)
	c.Assert(err, IsNil)
	return addr.String()
}

func newDashP2PKHScriptHex(c *C, address string) string {
	addr, err := btcutil.DecodeAddress(address, &dashchaincfg.RegressionNetParams)
	c.Assert(err, IsNil)
	script, err := dashtxscript.PayToAddrScript(addr)
	c.Assert(err, IsNil)
	return hex.EncodeToString(script)
}

func newDashMemoScriptHex(c *C, memo string) string {
	script, err := dashtxscript.NullDataScript([]byte(memo))
	c.Assert(err, IsNil)
	return hex.EncodeToString(script)
}

func newRPCTestServer(c *C, handler func(method string, params []json.RawMessage) any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req rpcRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			c.Fatalf("decode request: %v", err)
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  handler(req.Method, req.Params),
		}
		err = json.NewEncoder(w).Encode(resp)
		if err != nil {
			c.Fatalf("encode response: %v", err)
		}
	}))
}

func newDashClientForRPCServer(c *C, serverURL string) (*Client, func()) {
	rpcClient, err := rpc.NewClient(serverURL, "thorchain", "password", 0, time.Second, common.DASHChain, zerolog.Nop())
	c.Assert(err, IsNil)

	storage, err := blockscanner.NewBlockScannerStorage("", config.LevelDBOptions{})
	c.Assert(err, IsNil)
	cleanup := func() { _ = storage.Close() }

	temporalStorage, err := sharedutxo.NewTemporalStorage(storage.GetInternalDb(), 1000)
	c.Assert(err, IsNil)

	return &Client{
		cfg: config.BifrostChainConfiguration{
			ChainID:             common.DASHChain,
			ParallelMempoolScan: 1,
		},
		log:                       zerolog.Nop(),
		regexpRemoveTrailingZeros: regexp.MustCompile(`(00)+$`),
		rpc:                       rpcClient,
		temporalStorage:           temporalStorage,
		poolMgr:                   dashTestPoolMgr{},
		instantSendCfg: config.InstantSendConfiguration{
			Enabled:               true,
			InboundValueLimitUSD:  100,
			MinFeeRateSatPerVByte: 1.5,
			MaxMempoolAge:         2 * time.Minute,
		},
	}, cleanup
}

func dashTestMempoolEntry() map[string]any {
	return map[string]any{
		"size":   200,
		"vsize":  200,
		"weight": 800,
		"fees": map[string]any{
			"base": 0.00002,
		},
		"time":   time.Now().Unix(),
		"height": 123,
	}
}

func dashUnlockedRawTx(txID string) map[string]any {
	return map[string]any{
		"txid":                 txID,
		"hash":                 txID,
		"version":              2,
		"locktime":             0,
		"confirmations":        0,
		"instantlock":          false,
		"instantlock_internal": false,
	}
}

func dashLockedRawTx(txID string) map[string]any {
	tx := dashUnlockedRawTx(txID)
	tx["instantlock"] = true
	return tx
}

func (s *DashSuite) TestDashGetHeightUsesBestChainLock(c *C) {
	hash := "0000000000000000000000000000000000000000000000000000000000000123"
	known := true
	activeHash := hash
	server := newRPCTestServer(c, func(method string, _ []json.RawMessage) any {
		switch method {
		case "getbestchainlock":
			return map[string]any{"blockhash": hash, "height": 321, "known_block": known}
		case "getblockhash":
			return activeHash
		}
		c.Fatalf("unexpected method %s", method)
		return nil
	})
	defer server.Close()
	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	height, err := client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(321))
	known = false
	_, err = client.GetHeight()
	c.Assert(err, NotNil)
	known = true
	activeHash = "different"
	_, err = client.GetHeight()
	c.Assert(err, NotNil)
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

	txIn, err := client.getInstantSendLocks(123)
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

	txIn, err := client.getInstantSendLocks(123)
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

	txIn, err := client.getInstantSendLocks(123)
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
		got, err := client.instantSendObservationHeight(lock, 123)
		c.Assert(err, IsNil)
		c.Assert(got, Equals, want)
	}
	confirmations = -1
	_, err := client.instantSendObservationHeight(lock, 123)
	c.Assert(err, NotNil)
	confirmations = 1
	returnedHash = "different"
	_, err = client.instantSendObservationHeight(lock, 123)
	c.Assert(err, NotNil)
}

func (s *DashSuite) TestDashAllowInstantSendPolicyBoundaries(c *C) {
	client := Client{
		instantSendCfg: config.InstantSendConfiguration{
			InboundValueLimitUSD:  10,
			MinFeeRateSatPerVByte: 1.5,
		},
	}
	inboundValueLimitUSD := cosmos.NewUint(10).MulUint64(common.One)

	allow, reason := client.allowInstantSend(1499, 0, true, cosmos.ZeroUint())
	c.Assert(allow, Equals, false)
	c.Assert(reason, Equals, "fee rate too low: 1.499 sat/vB < 1.500 sat/vB")

	allow, reason = client.allowInstantSend(1500, 0, false, inboundValueLimitUSD)
	c.Assert(allow, Equals, false)
	c.Assert(reason, Equals, "USD value 10.000000 >= inbound value limit 10")

	allow, reason = client.allowInstantSend(1500, 0, false, inboundValueLimitUSD.Sub(cosmos.NewUint(1)))
	c.Assert(allow, Equals, true, Commentf(reason))
	c.Assert(reason, Equals, "")
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

	txIn, err := client.getInstantSendLocks(123)
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
	c.Assert(txIn.TxArray[0].OnObservedTxStored, IsNil)
	// No Observer acknowledgement is needed; a lost handoff is not retried.
	retry, err := client.getInstantSendLocks(123)
	c.Assert(err, IsNil)
	c.Assert(retry.TxArray, HasLen, 0)
	// Mining the InstantSend transaction must not create another observation.
	client.disableVinZeroBatch = true
	mined, err := client.extractTxs(&btcjson.GetBlockVerboseTxResult{Height: 150, Tx: []btcjson.TxRawResult{rawTx.TxRawResult}})
	c.Assert(err, IsNil)
	c.Assert(mined.TxArray, HasLen, 0)
}

func (s *DashSuite) TestDashGetInstantSendLocksRetriesPolicyFailures(c *C) {
	asgardAddress := newDashTestAddress(c, 0x33)
	senderAddress := newDashTestAddress(c, 0x44)

	mempoolLocked := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	prevLocked := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	cycleHash := "7777777777777777777777777777777777777777777777777777777777777777"

	lockedOutputScript := newDashP2PKHScriptHex(c, asgardAddress)
	senderScript := newDashP2PKHScriptHex(c, senderAddress)
	memoScript := newDashMemoScriptHex(c, "NOOP")

	mempoolCalls := 0
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			mempoolCalls++
			baseFee := 0.000002 // 1 sat/vB for a 200-vbyte tx, below policy.
			if mempoolCalls > 1 {
				baseFee = 0.00002 // 10 sat/vB for a 200-vbyte tx, above policy.
			}
			return map[string]any{
				mempoolLocked: map[string]any{
					"size":   200,
					"vsize":  200,
					"weight": 800,
					"fees": map[string]any{
						"base": baseFee,
					},
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
					"locktime":             0,
					"confirmations":        0,
					"instantlock":          true,
					"instantlock_internal": false,
					"vin":                  []map[string]any{{"txid": prevLocked, "vout": 0, "sequence": uint32(4294967295)}},
					"vout":                 []map[string]any{{"n": 0, "value": 0.1, "scriptPubKey": map[string]any{"hex": lockedOutputScript, "type": "pubkeyhash", "addresses": []string{asgardAddress}}}, {"n": 1, "value": 0.0, "scriptPubKey": map[string]any{"hex": memoScript, "type": "nulldata"}}},
				}
			case prevLocked:
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

	txIn, err := client.getInstantSendLocks(123)
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 0)

	// Once the fee is sufficient, pricing decides whether this 0.1 DASH deposit
	// can be observed. Mocknet always uses $50 and skips both real price sources.
	isMocknet := common.CurrentChainNetwork == common.MockNet
	poolErr := fmt.Errorf("pool unavailable")
	for _, tc := range []struct {
		name              string
		noPool            bool
		value             cosmos.Uint
		err               error
		limit             uint64
		wantGroups        int
		wantMocknetGroups int
	}{
		{"pool price below limit", false, cosmos.NewUint(900 * common.One), nil, 100, 1, 1},
		{"pool price at limit", false, cosmos.NewUint(1000 * common.One), nil, 100, 0, 1},
		{"missing pool manager", true, cosmos.ZeroUint(), nil, 100, 0, 1},
		{"zero pool value", false, cosmos.ZeroUint(), nil, 100, 0, 1},
		{"uninitialized pool value", false, cosmos.Uint{}, nil, 100, 0, 1},
		{"pool error", false, cosmos.ZeroUint(), poolErr, 100, 0, 1},
		{"discard value on pool error", false, cosmos.NewUint(1000 * common.One), poolErr, 100, 0, 1},
		{"fixed mocknet price below limit", false, cosmos.NewUint(900 * common.One), nil, 6, 0, 1},
		{"fixed mocknet price at limit", false, cosmos.NewUint(900 * common.One), nil, 5, 0, 0},
		{"fixed mocknet price above limit", false, cosmos.NewUint(900 * common.One), nil, 4, 0, 0},
		{"zero limit skips pricing", false, cosmos.NewUint(900 * common.One), nil, 0, 0, 0},
	} {
		calls := 0
		client.instantSendCfg.InboundValueLimitUSD = tc.limit
		client.poolMgr = nil
		if !tc.noPool {
			client.poolMgr = dashTestPoolMgrFunc(func(source, target common.Asset, amount cosmos.Uint) (cosmos.Uint, error) {
				calls++
				c.Assert(source.Equals(common.DASHAsset), Equals, true, Commentf(tc.name))
				c.Assert(target.Equals(common.TOR), Equals, true, Commentf(tc.name))
				c.Assert(amount.Equal(cosmos.NewUint(common.One)), Equals, true, Commentf(tc.name))
				return tc.value, tc.err
			})
		}
		groups, scanErr := client.scanInstantSend(123)
		c.Assert(scanErr, IsNil, Commentf(tc.name))
		wantGroups := tc.wantGroups
		if isMocknet {
			wantGroups = tc.wantMocknetGroups
		}
		c.Assert(groups, HasLen, wantGroups, Commentf(tc.name))
		if wantGroups > 0 {
			c.Assert(groups[0].TxArray, HasLen, 1, Commentf(tc.name))
			c.Assert(groups[0].TxArray[0].Tx, Equals, mempoolLocked, Commentf(tc.name))
		}
		wantCalls := 0
		if !isMocknet && !tc.noPool && tc.limit > 0 {
			wantCalls = 1
		}
		c.Assert(calls, Equals, wantCalls, Commentf(tc.name))
	}

	// Outside mocknet, the oracle takes priority even when its price rejects an
	// otherwise eligible deposit. A missing or invalid feed restores pool pricing.
	client.instantSendCfg.InboundValueLimitUSD = 100
	for _, tc := range []struct {
		price      cosmos.Uint
		oracleErr  error
		wantGroups int
		wantPools  int
	}{
		{cosmos.NewUint(1000 * common.One), nil, 0, 0},
		{cosmos.NewUint(900 * common.One), nil, 1, 0},
		{cosmos.ZeroUint(), fmt.Errorf("oracle unavailable"), 1, 1},
		{cosmos.NewUint(1000 * common.One), fmt.Errorf("oracle failed with a value"), 1, 1},
		{cosmos.ZeroUint(), nil, 1, 1},
		{cosmos.Uint{}, nil, 1, 1},
	} {
		oracleCalls, poolCalls := 0, 0
		client.bridge = dashOracleBridge{getPrice: func(symbol string) (cosmos.Uint, error) {
			oracleCalls++
			c.Assert(symbol, Equals, "DASH")
			return tc.price, tc.oracleErr
		}}
		client.poolMgr = dashTestPoolMgrFunc(func(source, target common.Asset, amount cosmos.Uint) (cosmos.Uint, error) {
			poolCalls++
			return dashTestPoolMgr{}.GetValue(source, target, amount)
		})
		groups, scanErr := client.scanInstantSend(123)
		c.Assert(scanErr, IsNil)
		if isMocknet {
			c.Assert(groups, HasLen, 1)
			c.Assert(oracleCalls, Equals, 0)
			c.Assert(poolCalls, Equals, 0)
		} else {
			c.Assert(groups, HasLen, tc.wantGroups)
			c.Assert(oracleCalls, Equals, 1)
			c.Assert(poolCalls, Equals, tc.wantPools)
		}
	}
	client.instantSendCfg.InboundValueLimitUSD = 0
	client.bridge = dashOracleBridge{getPrice: func(string) (cosmos.Uint, error) {
		c.Fatal("zero limit must skip oracle pricing")
		return cosmos.ZeroUint(), nil
	}}
	client.poolMgr = dashTestPoolMgrFunc(func(common.Asset, common.Asset, cosmos.Uint) (cosmos.Uint, error) {
		c.Fatal("zero limit must skip pool pricing")
		return cosmos.ZeroUint(), nil
	})
	_, err = client.scanInstantSend(123)
	c.Assert(err, IsNil)
	client.bridge = nil
	client.poolMgr = dashTestPoolMgr{}
	client.instantSendCfg.InboundValueLimitUSD = 100
	txIn, err = client.getInstantSendLocks(123)
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 1)
	c.Assert(txIn.TxArray[0].Tx, Equals, mempoolLocked)
}

func (s *DashSuite) TestDashInstantSendSkipsUnsupportedChains(c *C) {
	for _, chain := range []common.Chain{common.BTCChain, common.BCHChain, common.LTCChain, common.DOGEChain, common.ZECChain, common.EmptyChain} {
		// No RPC client or storage: unsupported chains must return before using either.
		client := &Client{cfg: config.BifrostChainConfiguration{ChainID: chain}}
		groups, err := client.scanInstantSend(123)
		c.Assert(err, IsNil, Commentf("chain: %s", chain))
		c.Assert(groups, IsNil, Commentf("chain: %s", chain))
	}
}
