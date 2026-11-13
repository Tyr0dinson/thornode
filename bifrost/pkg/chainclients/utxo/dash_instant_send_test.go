package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"slices"
	"sort"
	"time"

	dashwire "github.com/dashpay/dashd-go/wire"

	"github.com/btcsuite/btcd/btcjson"
	. "gopkg.in/check.v1"

	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// TestInstantSendFetchBatchSkipsCachedBeforeRawTxFetch protects the cheap
// cache gate. Already-processed InstantSend mempool txs should not trigger a
// full raw transaction RPC on every scan while they remain in the mempool.
func (s *DashSuite) TestInstantSendFetchBatchSkipsCachedBeforeRawTxFetch(c *C) {
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
	stopScanner := startDashTestScanner(c, client)
	defer stopScanner()
	client.cfg.UTXO.TransactionBatchSize = 10
	client.cfg.UTXO.MaxMempoolBatches = 1
	// Simulate a tx already handled by a previous InstantSend mempool pass.
	added, err := client.temporalStorage.TrackObservedTx(cached)
	c.Assert(err, IsNil)
	c.Assert(added, Equals, true)

	txIn, err := client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 0)
	c.Assert(rawLookups, DeepEquals, []string{fresh})
}

// TestInstantSendFetchBatchCapsMempoolWork protects the per-cycle work
// bound. The InstantSend path should not spawn one worker or raw tx RPC per
// mempool entry when the generic UTXO mempool settings impose a smaller cap.
func (s *DashSuite) TestInstantSendFetchBatchCapsMempoolWork(c *C) {
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
	stopScanner := startDashTestScanner(c, client)
	defer stopScanner()
	// Force a one-tx cap so a three-entry mempool proves candidate selection is
	// bounded before goroutines and raw tx RPCs are created.
	client.cfg.UTXO.TransactionBatchSize = 1
	client.cfg.UTXO.MaxMempoolBatches = 1

	txIn, err := client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(len(txIn.TxArray), Equals, 0)
	c.Assert(rawLookups, Equals, 1)
}

func (s *DashSuite) TestInstantSendFetchBatchFiltersUnlockedTxs(c *C) {
	client, fixture, cleanup := newDashMempoolFixture(c)
	defer cleanup()
	mempoolLocked, mempoolUnlocked := fixture.inbound, fixture.lowFeeInbound
	senderAddress, asgardAddress := fixture.user, fixture.asgard
	anchorHeight := int64(100)
	fixture.entries = map[string]map[string]any{mempoolLocked: dashTestMempoolEntry(), mempoolUnlocked: dashTestMempoolEntry()}
	fixture.txs[mempoolUnlocked]["instantlock"] = false
	// A mempool work marker alone must not suppress a valid observation.
	c.Assert(client.tryAddToMemPoolCache(mempoolLocked), Equals, true)

	rawTx, err := client.rpc.GetRawTransactionVerbose(mempoolLocked)
	c.Assert(err, IsNil)
	item, err := client.getTxIn(&rawTx.TxRawResult, 123, true, nil)
	c.Assert(err, IsNil)
	c.Assert(item.IsEmpty(), Equals, false)

	txIn, err := client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(txIn.MemPool, Equals, false)
	c.Assert(txIn.ConfirmationRequired, Equals, int64(0))
	c.Assert(len(txIn.TxArray), Equals, 1)
	c.Assert(txIn.TxArray[0].BlockHeight, Equals, common.InstantSendAnchor(anchorHeight))
	c.Assert(txIn.TxArray[0].Tx, Equals, mempoolLocked)
	c.Assert(txIn.TxArray[0].Sender, Equals, senderAddress)
	c.Assert(txIn.TxArray[0].To, Equals, asgardAddress)
	c.Assert(txIn.TxArray[0].Memo, Equals, "=:THOR.RUNE")
	c.Assert(txIn.TxArray[0].Coins.GetCoin(common.DASHAsset).Amount.Uint64(), Equals, uint64(10_000_000))
	observed, err := client.temporalStorage.HasTxMarker(mempoolLocked)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, true)
	c.Assert(txIn.Persisted, IsNil)
	c.Assert(txIn.TxArray[0].OnObservedTxStored, NotNil)
	// A pending claim is not retried through the InstantSend path.
	retry, err := client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(retry.TxArray, HasLen, 0)
	txIn.TxArray[0].OnObservedTxStored()
	// Observer storage does not prove early credit on THORChain. Keep one
	// independent ChainLock delivery, then suppress repeated block scans.
	client.disableVinZeroBatch = true
	block := &btcjson.GetBlockVerboseTxResult{Height: 150, Tx: []btcjson.TxRawResult{rawTx.TxRawResult}}
	mined, err := client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(mined.TxArray, HasLen, 1)
	c.Assert(mined.TxArray[0].Tx, Equals, mempoolLocked)
	c.Assert(mined.TxArray[0].BlockHeight, Equals, int64(150))
	c.Assert(mined.TxArray[0].InstantSend, IsNil)
	completeDashBlock(c, client, mined)
	mined, err = client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(mined.TxArray, HasLen, 0)
}

func (s *DashSuite) TestDashInstantSendDefersLowFeeOrMissingFeePayments(c *C) {
	// Use the vault returned by the fixture's accounting query.
	asgardAddress := newDashTestAddress(c, 0x11)
	senderAddress := newDashTestAddress(c, 0x44)

	mempoolLocked := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	prevLocked := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

	lockedOutputScript := newDashP2PKHScriptHex(c, asgardAddress)
	senderScript := newDashP2PKHScriptHex(c, senderAddress)
	memoScript := newDashMemoScriptHex(c, "NOOP")

	amount := float64(1000)
	entryTime := int64(1) // Local mempool receipt time does not limit admission.
	includeFee := true
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		switch method {
		case "getrawmempool":
			entry := map[string]any{"time": entryTime, "height": 123}
			if includeFee {
				entry["vsize"] = 200
				entry["fees"] = map[string]any{"base": 0.000002} // 1 duff/vB, below the default.
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
		default:
			c.Fatalf("unexpected method: %s", method)
		}
		return nil
	})
	defer server.Close()

	client, cleanup := newDashClientForRPCServer(c, server.URL)
	defer cleanup()
	stopScanner := startDashTestScanner(c, client)
	defer stopScanner()

	addr, err := common.NewAddress(asgardAddress)
	c.Assert(err, IsNil)
	client.asgardCache.Store(&sharedutxo.AsgardCache{
		Addresses: []common.Address{addr},
		FetchedAt: time.Now(),
	})

	// A locked payment with low or missing fee data waits for ChainLock.
	for _, withFee := range []bool{true, false} {
		includeFee = withFee
		var groups [][]*types.TxInItem
		groups, err = client.instantSend.scan()
		c.Assert(err, IsNil)
		c.Assert(groups, HasLen, 0)
	}

	marked, err := client.temporalStorage.HasTxMarker(mempoolLocked)
	c.Assert(err, IsNil)
	c.Assert(marked, Equals, false)
}

func (s *DashSuite) TestDashInstantSendSkipsUnsupportedChains(c *C) {
	for _, chain := range []common.Chain{common.BTCChain, common.BCHChain, common.LTCChain, common.DOGEChain, common.ZECChain, common.EmptyChain} {
		// No RPC client or storage: unsupported chains must return before using either.
		client := &Client{cfg: config.BifrostChainConfiguration{ChainID: chain}}
		client.instantSend = instantSendHandler{client: client, enabled: true}
		groups, err := client.instantSend.scan()
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
		var wireTx dashwire.MsgTx
		rawHex, ok := fixture.txs[fixture.inbound]["hex"].(string)
		c.Assert(ok, Equals, true)
		raw, decodeErr := hex.DecodeString(rawHex)
		c.Assert(decodeErr, IsNil)
		c.Assert(wireTx.Deserialize(bytes.NewReader(raw)), IsNil)
		wireTx.LockTime = uint32(i + 1)
		var encoded bytes.Buffer
		c.Assert(wireTx.Serialize(&encoded), IsNil)
		id := wireTx.TxHash().String()
		ids[i] = id
		tx := make(map[string]any)
		for key, value := range fixture.txs[fixture.inbound] {
			tx[key] = value
		}
		tx["txid"], tx["hash"] = id, id
		tx["hex"] = hex.EncodeToString(encoded.Bytes())
		fixture.txs[id] = tx
		fixture.entries[id] = dashTestMempoolEntry()
		fixture.entries[id]["instantlock"] = "true"
	}
	sort.Strings(ids)
	return client, fixture, ids, cleanup
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
	batch, err := client.instantSend.fetchBatch()
	c.Assert(err, IsNil)
	c.Assert(batch.TxArray, HasLen, 1)
	c.Assert(batch.TxArray[0].Tx, Equals, ids[0])
	c.Assert(batch.TxArray[0].Sender, Equals, fixture.user)
	c.Assert(batch.TxArray[0].To, Equals, fixture.asgard)
	c.Assert(batch.TxArray[0].Memo, Equals, "=:THOR.RUNE")
	assertInstantSendScanClaims(c, client, ids, ids)
}

func assertInstantSendScanClaims(c *C, client *Client, ids, accepted []string) {
	for _, id := range ids {
		observed, err := client.temporalStorage.HasTxMarker(id)
		c.Assert(err, IsNil)
		c.Assert(observed, Equals, slices.Contains(accepted, id))
	}
}
