package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/btcsuite/btcutil"
	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/dashutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	btctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
)

type dashRPCRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	ID     any               `json:"id"`
}

type dashTestPoolMgr struct{}

func (dashTestPoolMgr) GetValue(source common.Asset, target common.Asset, amount cosmos.Uint) (cosmos.Uint, error) {
	if !source.Equals(common.DASHAsset) {
		return cosmos.ZeroUint(), fmt.Errorf("unexpected source asset: %s", source)
	}
	if !target.Equals(common.TOR) {
		return cosmos.ZeroUint(), fmt.Errorf("unexpected target asset: %s", target)
	}
	return amount.MulUint64(priceDashUSDForSimulationTesting), nil
}

func newDashTestAddress(c *C, seed byte) string {
	hash := bytes.Repeat([]byte{seed}, 20)
	addr, err := btcutil.NewAddressPubKeyHash(hash, &dashutil.RegressionNetParams)
	c.Assert(err, IsNil)
	return addr.String()
}

func newDashP2PKHScriptHex(c *C, address string) string {
	addr, err := btcutil.DecodeAddress(address, &dashutil.RegressionNetParams)
	c.Assert(err, IsNil)
	script, err := btctxscript.PayToAddrScript(addr)
	c.Assert(err, IsNil)
	return hex.EncodeToString(script)
}

func newDashMemoScriptHex(c *C, memo string) string {
	script, err := btctxscript.NullDataScript([]byte(memo))
	c.Assert(err, IsNil)
	return hex.EncodeToString(script)
}

func newDashTestServer(c *C, handler func(method string, params []json.RawMessage) any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var req dashRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			c.Fatalf("decode request: %v", err)
		}

		resp := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  handler(req.Method, req.Params),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
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
		log:             zerolog.Nop(),
		rpc:             rpcClient,
		temporalStorage: temporalStorage,
		poolMgr:         dashTestPoolMgr{},
		instantSendCfg: config.DashInstantSendConfiguration{
			Enabled:               true,
			USDThreshold:          100,
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

func dashUnlockedRawTx(txid string) map[string]any {
	return map[string]any{
		"txid":                 txid,
		"hash":                 txid,
		"version":              2,
		"locktime":             0,
		"confirmations":        0,
		"instantlock":          false,
		"instantlock_internal": false,
	}
}

func dashLockedRawTx(txid string) map[string]any {
	tx := dashUnlockedRawTx(txid)
	tx["instantlock"] = true
	return tx
}

func (s *DashSuite) TestDashGetHeightUsesBestChainlock(c *C) {
	server := newDashTestServer(c, func(method string, _ []json.RawMessage) any {
		c.Assert(method, Equals, "getbestchainlock")
		return map[string]any{
			"blockhash":   "0000000000000000000000000000000000000000000000000000000000000123",
			"height":      321,
			"signature":   "sig",
			"known_block": true,
		}
	})
	defer server.Close()

	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()

	height, err := client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(321))
}

// TestDashGetInstantSendLocksSkipsCachedBeforeRawTxFetch protects the cheap
// cache gate. Already-processed InstantSend mempool txs should not trigger a
// full raw transaction RPC on every scan while they remain in the mempool.
func (s *DashSuite) TestDashGetInstantSendLocksSkipsCachedBeforeRawTxFetch(c *C) {
	cached := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	fresh := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var rawLookups []string

	server := newDashTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{
				cached: dashTestMempoolEntry(),
				fresh:  dashTestMempoolEntry(),
			}
		case "getrawtransaction":
			var txid string
			if err := json.Unmarshal(params[0], &txid); err != nil {
				c.Fatalf("decode txid: %v", err)
			}
			// This is the regression guard: cached txids must be filtered before
			// the expensive GetRawTransactionVerboseDASH call is made.
			if txid == cached {
				c.Fatalf("cached tx should not be fetched again")
			}
			rawLookups = append(rawLookups, txid)
			return dashUnlockedRawTx(txid)
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
	c.Assert(client.tryAddToMemPoolCache(cached), Equals, true)

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

	server := newDashTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{
				txA: dashTestMempoolEntry(),
				txB: dashTestMempoolEntry(),
				txC: dashTestMempoolEntry(),
			}
		case "getrawtransaction":
			var txid string
			if err := json.Unmarshal(params[0], &txid); err != nil {
				c.Fatalf("decode txid: %v", err)
			}
			rawLookups++
			// If candidate selection is unbounded, the second mempool entry will
			// reach this handler and fail the test immediately.
			if rawLookups > 1 {
				c.Fatalf("expected per-cycle cap to allow only one raw tx lookup")
			}
			return dashUnlockedRawTx(txid)
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

	server := newDashTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{
				txA: dashTestMempoolEntry(),
				txB: dashTestMempoolEntry(),
			}
		case "getrawtransaction":
			var txid string
			if err := json.Unmarshal(params[0], &txid); err != nil {
				c.Fatalf("decode txid: %v", err)
			}
			switch txid {
			case txA, txB:
				return dashLockedRawTx(txid)
			default:
				c.Fatalf("unexpected tx lookup: %s", txid)
			}
		case "getislocks":
			var requested []string
			if err := json.Unmarshal(params[0], &requested); err != nil {
				c.Fatalf("decode getislocks txids: %v", err)
			}
			lockBatches = append(lockBatches, requested)
			locks := make([]any, len(requested))
			for i, txid := range requested {
				locks[i] = map[string]any{
					"txid":      txid,
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
			if err := json.Unmarshal(params[0], &hash); err != nil {
				c.Fatalf("decode block hash: %v", err)
			}
			c.Assert(hash, Equals, cycleHash)
			getBlockCalls++
			return map[string]any{
				"hash":   cycleHash,
				"height": int64(123),
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
	for _, txid := range lockBatches[0] {
		seen[txid] = true
	}
	c.Assert(seen[txA], Equals, true)
	c.Assert(seen[txB], Equals, true)
	c.Assert(getBlockCalls, Equals, 1)
}

func (s *DashSuite) TestDashGetInstantSendLocksRejectsFutureCycleHeight(c *C) {
	txid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cycleHash := "1111111111111111111111111111111111111111111111111111111111111111"

	server := newDashTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{txid: dashTestMempoolEntry()}
		case "getrawtransaction":
			return dashLockedRawTx(txid)
		case "getislocks":
			return []any{map[string]any{
				"txid":      txid,
				"id":        "islock-request-id",
				"inputs":    []map[string]any{},
				"cycleHash": cycleHash,
				"signature": "signature",
				"hex":       "010203",
			}}
		case "getblock":
			return map[string]any{
				"hash":   cycleHash,
				"height": int64(124),
			}
		default:
			c.Fatalf("unexpected method: %s", method)
		}
		_ = params
		return nil
	})
	defer server.Close()

	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()

	txIn, err := client.getInstantSendLocks(123)
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 0)
}

func (s *DashSuite) TestDashGetInstantSendLocksRejectsStaleCycleHeight(c *C) {
	txid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	cycleHash := "1111111111111111111111111111111111111111111111111111111111111111"

	server := newDashTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			return map[string]any{txid: dashTestMempoolEntry()}
		case "getrawtransaction":
			return dashLockedRawTx(txid)
		case "getislocks":
			return []any{map[string]any{
				"txid":      txid,
				"id":        "islock-request-id",
				"inputs":    []map[string]any{},
				"cycleHash": cycleHash,
				"signature": "signature",
				"hex":       "010203",
			}}
		case "getblock":
			return map[string]any{
				"hash":   cycleHash,
				"height": int64(121),
			}
		default:
			c.Fatalf("unexpected method: %s", method)
		}
		_ = params
		return nil
	})
	defer server.Close()

	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	client.cfg.BlockScanner.MaxReorgRescanBlocks = 1

	txIn, err := client.getInstantSendLocks(123)
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 0)
}

func (s *DashSuite) TestDashAllowInstantSendPolicyBoundaries(c *C) {
	client := Client{
		instantSendCfg: config.DashInstantSendConfiguration{
			USDThreshold:          10,
			MinFeeRateSatPerVByte: 1.5,
		},
	}
	thresholdUSD := cosmos.NewUint(10).MulUint64(common.One)

	allow, reason := client.allowInstantSend(1499, 0, true, cosmos.ZeroUint())
	c.Assert(allow, Equals, false)
	c.Assert(reason, Equals, "fee rate too low: 1.499 sat/vB < 1.500 sat/vB")

	allow, reason = client.allowInstantSend(1500, 0, false, thresholdUSD)
	c.Assert(allow, Equals, false)
	c.Assert(reason, Equals, "usd value 10.000000 >= threshold 10")

	allow, reason = client.allowInstantSend(1500, 0, false, thresholdUSD.Sub(cosmos.NewUint(1)))
	c.Assert(allow, Equals, true, Commentf(reason))
	c.Assert(reason, Equals, "")
}

func (s *DashSuite) TestDashUSDValueUsesTORPool(c *C) {
	client := Client{poolMgr: dashTestPoolMgr{}}
	amount := cosmos.NewUint(2 * common.One)

	got, err := client.usdValueForDash(amount)
	c.Assert(err, IsNil)
	want := amount.MulUint64(priceDashUSDForSimulationTesting)
	c.Assert(got.Equal(want), Equals, true, Commentf("got %s, want %s", got, want))
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

	server := newDashTestServer(c, func(method string, params []json.RawMessage) any {
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
			var txid string
			if err := json.Unmarshal(params[0], &txid); err != nil {
				c.Fatalf("decode txid: %v", err)
			}

			switch txid {
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
					"txid":          txid,
					"hash":          txid,
					"version":       2,
					"locktime":      0,
					"confirmations": 1,
					"vin":           []map[string]any{{"txid": "coinbase", "vout": 0}},
					"vout":          []map[string]any{{"n": 0, "value": 0.10002, "scriptPubKey": map[string]any{"hex": senderScript, "type": "pubkeyhash", "addresses": []string{senderAddress}}}},
				}
			default:
				c.Fatalf("unexpected tx lookup: %s", txid)
			}
		case "getislocks":
			var txids []string
			if err := json.Unmarshal(params[0], &txids); err != nil {
				c.Fatalf("decode getislocks txids: %v", err)
			}
			c.Assert(txids, DeepEquals, []string{mempoolLocked})
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
			if err := json.Unmarshal(params[0], &hash); err != nil {
				c.Fatalf("decode block hash: %v", err)
			}
			c.Assert(hash, Equals, cycleHash)
			return map[string]any{
				"hash":   cycleHash,
				"height": cycleHeight,
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

	rawTx, err := client.rpc.GetRawTransactionVerboseDASH(mempoolLocked)
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
	server := newDashTestServer(c, func(method string, params []json.RawMessage) any {
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
			var txid string
			if err := json.Unmarshal(params[0], &txid); err != nil {
				c.Fatalf("decode txid: %v", err)
			}

			switch txid {
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
					"txid":          txid,
					"hash":          txid,
					"version":       2,
					"locktime":      0,
					"confirmations": 1,
					"vin":           []map[string]any{{"txid": "coinbase", "vout": 0}},
					"vout":          []map[string]any{{"n": 0, "value": 0.10002, "scriptPubKey": map[string]any{"hex": senderScript, "type": "pubkeyhash", "addresses": []string{senderAddress}}}},
				}
			default:
				c.Fatalf("unexpected tx lookup: %s", txid)
			}
		case "getislocks":
			var txids []string
			if err := json.Unmarshal(params[0], &txids); err != nil {
				c.Fatalf("decode getislocks txids: %v", err)
			}
			c.Assert(txids, DeepEquals, []string{mempoolLocked})
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
			if err := json.Unmarshal(params[0], &hash); err != nil {
				c.Fatalf("decode block hash: %v", err)
			}
			c.Assert(hash, Equals, cycleHash)
			return map[string]any{
				"hash":   cycleHash,
				"height": int64(123),
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

	txIn, err = client.getInstantSendLocks(123)
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 1)
	c.Assert(txIn.TxArray[0].Tx, Equals, mempoolLocked)
}
