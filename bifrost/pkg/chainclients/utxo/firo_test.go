package utxo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcutil"
	"github.com/rs/zerolog"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	sharedutxo "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/txscript/txscript"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
	. "gopkg.in/check.v1"
)

type FiroSuite struct{}

var _ = Suite(&FiroSuite{})

type firoTestBridge struct{ thorclient.ThorchainBridge }

func (*firoTestBridge) GetMimir(key string) (int64, error) {
	if strings.HasPrefix(key, "FiroInstantSend") {
		return -1, nil
	}
	return 10, nil
}

type firoFixture struct {
	tip, locked                    int64
	pruned, wrongNetwork, conflict bool
	failMethod, failTx             string
	txs                            map[string]*rpc.FiroTxResult
	utxos                          []btcjson.ListUnspentResult
	mempool                        []string
	entries                        map[string]rpc.FiroMempoolEntry
	mempoolSnapshot                func() []string
	pubkey                         common.PubKey
	vault, user                    *firoutil.Address
}

func firoHash(n int64) string { return fmt.Sprintf("%064x", n) }

func newFiroFixture(c *C, cacheSize ...int) (*Client, *firoFixture, func()) {
	f := &firoFixture{tip: 100, locked: 99, txs: make(map[string]*rpc.FiroTxResult), pubkey: ttypes.GetRandomPubKey()}
	address, err := f.pubkey.GetAddress(common.FIROChain)
	c.Assert(err, IsNil)
	f.vault, err = firoutil.DecodeAddress(address.String(), common.FIRONetwork())
	c.Assert(err, IsNil)
	f.user, err = firoutil.NewAddress(bytes.Repeat([]byte{1}, 20), firoutil.PubKeyHash, common.FIRONetwork())
	c.Assert(err, IsNil)
	server := newRPCTestServer(c, func(method string, params []json.RawMessage) any {
		if method == f.failMethod {
			return &btcjson.RPCError{Code: -1, Message: "temporary RPC failure"}
		}
		switch method {
		case "getblockcount":
			return f.tip
		case "validateaddress":
			return map[string]any{"iswatchonly": true, "account": "thornode-firo-rescanned-v1"}
		case "getblockchaininfo":
			network := string(common.FIRONetwork())
			if f.wrongNetwork {
				network = "wrong"
			}
			return rpc.FiroChainInfo{Chain: network, Blocks: f.tip, Headers: f.tip, BestBlockHash: firoHash(f.tip), Pruned: f.pruned}
		case "getblockhash":
			var height int64
			c.Assert(json.Unmarshal(params[0], &height), IsNil)
			if height > f.tip {
				return &btcjson.RPCError{Code: -8, Message: "height unavailable"}
			}
			if f.conflict && height == 99 {
				return firoHash(199)
			}
			return firoHash(height)
		case "getblockheader":
			var hash string
			c.Assert(json.Unmarshal(params[0], &hash), IsNil)
			height, parseErr := strconv.ParseInt(hash, 16, 64)
			c.Assert(parseErr, IsNil)
			if f.conflict && height == 199 {
				height = 99
			}
			return rpc.FiroHeader{Hash: hash, Height: height, Confirmations: f.tip - height + 1, ChainLock: height <= f.locked}
		case "getblock":
			var hash string
			c.Assert(json.Unmarshal(params[0], &hash), IsNil)
			height, parseErr := strconv.ParseInt(hash, 16, 64)
			c.Assert(parseErr, IsNil)
			txs := make([]*rpc.FiroTxResult, 0)
			for _, tx := range f.txs {
				if tx.BlockHash == hash {
					txs = append(txs, tx)
				}
			}
			return map[string]any{"hash": hash, "height": height, "previousblockhash": firoHash(height - 1), "chainlock": height <= f.locked, "confirmations": f.tip - height + 1, "tx": txs}
		case "getrawtransaction":
			var id string
			c.Assert(json.Unmarshal(params[0], &id), IsNil)
			if id == f.failTx || f.txs[id] == nil {
				return &btcjson.RPCError{Code: -5, Message: "transaction unavailable"}
			}
			return f.txs[id]
		case "listunspent":
			return f.utxos
		case "getrawmempool":
			if len(params) > 0 && string(params[0]) == "true" {
				if f.entries != nil {
					return f.entries
				}
				entries := make(map[string]rpc.FiroMempoolEntry)
				for _, id := range f.mempool {
					entries[id] = rpc.FiroMempoolEntry{Time: time.Now().Unix(), InstantLock: &f.txs[id].InstantLock}
				}
				return entries
			}
			if f.mempoolSnapshot != nil {
				return f.mempoolSnapshot()
			}
			return f.mempool
		case "getnetworkinfo":
			return map[string]any{"relayfee": 0.00001}
		default:
			c.Errorf("unexpected FIRO RPC %s", method)
			return nil
		}
	})
	rpcClient, err := rpc.NewClient(server.URL, "", "", 0, time.Second, common.FIROChain, zerolog.Nop())
	c.Assert(err, IsNil)
	path := c.MkDir()
	storage, err := blockscanner.NewBlockScannerStorage(path, config.LevelDBOptions{})
	c.Assert(err, IsNil)
	capacity := 1000
	if len(cacheSize) > 0 {
		capacity = cacheSize[0]
	}
	temporal, err := sharedutxo.NewTemporalStorage(storage.GetInternalDb(), capacity)
	c.Assert(err, IsNil)
	client := &Client{
		cfg: config.BifrostChainConfiguration{ChainID: common.FIROChain}, log: zerolog.Nop(), rpc: rpcClient, temporalStorage: temporal,
		bridge: &firoTestBridge{}, regexpRemoveTrailingZeros: regexp.MustCompile(`(00)+$`), globalNetworkFeeQueue: make(chan common.NetworkFee, 20),
	}
	client.cfg.BlockScanner.DBPath = path
	client.initInstantSend()
	client.signerCacheManager, err = signercache.NewSignerCacheManager(storage.GetInternalDb())
	c.Assert(err, IsNil)
	client.cfg.UTXO.MaxUTXOsToSpend = 10
	client.cfg.UTXO.MaxSatsPerVByte = 1000
	client.cfg.UTXO.EstimatedAverageTxSize = 2000
	client.cfg.UTXO.TransactionBatchSize = 1
	client.cfg.UTXO.MaxMempoolBatches = 1
	client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{address}, FetchedAt: time.Now()})
	client.updateNetworkInfo()
	return client, f, func() { server.Close(); _ = storage.Close() }
}

func (f *firoFixture) funding(n, height int64, value float64, address *firoutil.Address) *rpc.FiroTxResult {
	tx := &rpc.FiroTxResult{Height: height, TxResult: rpc.TxResult{ChainLock: true, TxRawResult: btcjson.TxRawResult{
		Txid: firoHash(n), Size: 200, Version: 1, BlockHash: firoHash(height), Confirmations: 1,
		Vout: []btcjson.Vout{{N: 0, Value: value, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: hex.EncodeToString(address.Script())}}},
	}}}
	f.txs[tx.Txid] = tx
	return tx
}

func (f *firoFixture) listed(tx *rpc.FiroTxResult) btcjson.ListUnspentResult {
	return btcjson.ListUnspentResult{TxID: tx.Txid, Vout: 0, Amount: tx.Vout[0].Value, ScriptPubKey: tx.Vout[0].ScriptPubKey.Hex, Confirmations: 1}
}

func (s *FiroSuite) TestFiroFinalityAndHealth(c *C) {
	client, f, cleanup := newFiroFixture(c)
	defer cleanup()
	height, err := client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(99))
	c.Assert(client.finality.Load().isHealthyFIRO(time.Now(), 99), Equals, true)
	c.Assert(client.finality.Load().isHealthyFIRO(time.Now(), 98), Equals, false)
	f.tip = 102
	height, err = client.GetHeight()
	c.Assert(err, IsNil)
	c.Assert(height, Equals, int64(99))
	c.Assert(client.finality.Load().isHealthyFIRO(time.Now(), 102), Equals, false)
	f.locked = 102
	f.conflict = true
	_, err = client.GetHeight()
	c.Assert(err, ErrorMatches, ".*finalized chain changed.*")
	c.Assert(client.finality.Load(), IsNil)
	f.conflict = false
	// Losing lock evidence also invalidates a previously published snapshot.
	_, err = client.GetHeight()
	c.Assert(err, IsNil)
	f.locked = 101
	_, err = client.GetHeight()
	c.Assert(err, ErrorMatches, ".*lock evidence was lost.*")
	c.Assert(client.finality.Load(), IsNil)
	f.locked = 102
	for _, method := range []string{"getblockchaininfo", "getblockhash", "getblockheader"} {
		f.failMethod = ""
		_, err = client.GetHeight()
		c.Assert(err, IsNil)
		f.failMethod = method
		_, err = client.GetHeight()
		c.Assert(err, NotNil)
		c.Assert(client.finality.Load(), IsNil)
	}
	f.failMethod = ""
	f.locked = -1
	_, err = client.GetHeight()
	c.Assert(err, NotNil)
	f.locked = 102
	f.pruned = true
	_, err = client.GetHeight()
	c.Assert(err, NotNil)
	f.pruned = false
	f.wrongNetwork = true
	_, err = client.GetHeight()
	c.Assert(err, NotNil)
}

func (s *FiroSuite) TestFiroSolvencyFinalityReadiness(c *C) {
	client, f, cleanup := newFiroFixture(c)
	defer cleanup()
	// Finality one block behind the tip is ready, but solvency cannot exceed it.
	err := client.validateSolvencyFIRO(99)
	c.Assert(err, IsNil)
	err = client.validateSolvencyFIRO(100)
	c.Assert(err, ErrorMatches, "FIRO solvency is ahead of finality")

	f.tip = 101
	err = client.validateSolvencyFIRO(99)
	c.Assert(err, ErrorMatches, "FIRO finality is behind the chain tip")
	f.locked = 101
	err = client.validateSolvencyFIRO(101)
	c.Assert(err, IsNil)

	// A previous successful check must not hide a subsequent RPC failure.
	f.failMethod = "getblockchaininfo"
	err = client.validateSolvencyFIRO(101)
	c.Assert(err, NotNil)
	c.Assert(client.finality.Load(), IsNil)
}

func (s *FiroSuite) TestFiroDeterministicSelection(c *C) {
	client, f, cleanup := newFiroFixture(c)
	defer cleanup()
	old := f.funding(10, 10, 1, f.vault)
	newer := f.funding(11, 11, 1, f.vault)
	f.utxos = []btcjson.ListUnspentResult{f.listed(newer), f.listed(old)}
	selected, err := client.getUtxoToSpendFIRO(f.pubkey, btcutil.Amount(common.One), 0)
	c.Assert(err, IsNil)
	c.Assert(selected, HasLen, 1)
	c.Assert(selected[0].TxID, Equals, old.Txid)
	old.ChainLock = false
	_, err = client.getUtxoToSpendFIRO(f.pubkey, btcutil.Amount(common.One), 0)
	c.Assert(err, ErrorMatches, ".*not ChainLocked.*") // never substitute newer locked input
	old.ChainLock = true
	f.utxos[1].Confirmations = 0
	selected, err = client.getUtxoToSpendFIRO(f.pubkey, btcutil.Amount(common.One), 0)
	c.Assert(err, IsNil)
	c.Assert(selected[0].TxID, Equals, newer.Txid)
	f.utxos[1].Confirmations = 1
	tx := stypes.TxOutItem{
		Chain: common.FIROChain, VaultPubKey: f.pubkey, ToAddress: common.Address(f.user.String()), GasRate: 25,
		Coins: common.Coins{common.NewCoin(common.FIROAsset, cosmos.NewUint(common.One))},
	}
	built, _, err := client.buildTx(tx, f.vault.Script(), 0)
	c.Assert(err, IsNil)
	c.Assert(built.TxIn, HasLen, 2) // first input pays amount but cannot also pay fee
	c.Assert(built.TxOut[0].Value, Equals, int64(common.One))
	f.utxos = append(f.utxos, f.utxos[0])
	_, err = client.getUtxoToSpendFIRO(f.pubkey, 1, 0)
	c.Assert(err, ErrorMatches, ".*duplicate FIRO UTXO.*")
}

func (s *FiroSuite) TestFiroObservationRetriesAndMemos(c *C) {
	client, f, cleanup := newFiroFixture(c)
	defer cleanup()
	parent := f.funding(10, 10, 2, f.user)
	deposit := f.funding(11, 11, 1, f.vault)
	deposit.Vin = []btcjson.Vin{{Txid: parent.Txid, Vout: 0}}
	memo := strings.Repeat("a", 250)
	scripts, err := MemoToScripts(memo, 80, txscript.NullDataScript, firoutil.PayToPubKeyHashScript)
	c.Assert(err, IsNil)
	for i, script := range scripts {
		value := float64(0)
		if i > 0 {
			value = 0.00000546
		}
		deposit.Vout = append(deposit.Vout, btcjson.Vout{N: uint32(i + 1), Value: value, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: hex.EncodeToString(script)}})
	}
	block := &btcjson.GetBlockVerboseTxResult{Height: 11, Tx: []btcjson.TxRawResult{deposit.TxRawResult}}
	f.failTx = parent.Txid
	_, err = client.extractTxs(block)
	c.Assert(err, NotNil)
	observed, err := client.temporalStorage.HasObservedTx(deposit.Txid)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	f.failTx = ""
	result, err := client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(result.TxArray, HasLen, 1)
	c.Assert(result.TxArray[0].Memo, Equals, memo)
	c.Assert(result.MemPool, Equals, false)
	result, err = client.extractTxs(block)
	c.Assert(err, IsNil)
	c.Assert(result.TxArray, HasLen, 0)
	// InstantSend stays opt-in, even when the daemon reports a lock.
	pending := f.instantDeposit(5, 10, 1)
	f.mempool = []string{pending.Txid}
	result, err = client.FetchMemPool(11)
	c.Assert(err, IsNil)
	c.Assert(result.TxArray, HasLen, 0)
	c.Assert(result.MemPool, Equals, true)
	// A later outbound must not starve behind previously scanned customer IDs.
	outbound := f.funding(12, 0, 0.9, f.user)
	outbound.BlockHash, outbound.Confirmations, outbound.ChainLock = "", 0, false
	outbound.Vin = []btcjson.Vin{{Txid: deposit.Txid, Vout: 0}}
	f.mempool = append(f.mempool, outbound.Txid)
	result, err = client.FetchMemPool(12)
	c.Assert(err, IsNil)
	c.Assert(result.TxArray, HasLen, 1)
	c.Assert(result.TxArray[0].Tx, Equals, outbound.Txid)
	observed, err = client.temporalStorage.HasObservedTx(outbound.Txid)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
}

func (s *FiroSuite) TestFiroObservationMatchesVaultAddressExactly(c *C) {
	for _, match := range []string{"exact", "different case", "empty list"} {
		func() {
			client, f, cleanup := newFiroFixture(c)
			defer cleanup()
			addresses := []common.Address{common.Address(f.vault.String())}
			switch match {
			case "different case":
				addresses[0] = common.Address(strings.ToLower(f.vault.String()))
				c.Assert(addresses[0].String(), Not(Equals), f.vault.String())
			case "empty list":
				addresses = nil
			}
			client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: addresses, FetchedAt: time.Now()})
			parent := f.funding(10, 10, 2, f.user)
			deposit := f.funding(11, 11, 1, f.vault)
			deposit.Vin = []btcjson.Vin{{Txid: parent.Txid}}
			batch, err := client.extractTxs(&btcjson.GetBlockVerboseTxResult{Height: 11, Tx: []btcjson.TxRawResult{deposit.TxRawResult}})
			c.Assert(err, IsNil)
			want := 0
			if match == "exact" {
				want = 1
			}
			c.Assert(batch.TxArray, HasLen, want, Commentf("address match: %s", match))
		}()
	}
}

func (s *FiroSuite) TestFiroAddressLookupFailurePreservesObservationsAndBalances(c *C) {
	client, f, cleanup := newFiroFixture(c)
	defer cleanup()
	// A stale matching address must not hide a failed refresh.
	client.asgardCache.Store(&sharedutxo.AsgardCache{Addresses: []common.Address{common.Address(f.vault.String())}})
	client.bridge = instantSendVaultErrorBridge{ThorchainBridge: client.bridge}
	parent := f.funding(10, 10, 2, f.user)
	deposit := f.funding(11, 11, 1, f.vault)
	deposit.Vin = []btcjson.Vin{{Txid: parent.Txid}}
	_, err := client.extractTxs(&btcjson.GetBlockVerboseTxResult{Height: 11, Tx: []btcjson.TxRawResult{deposit.TxRawResult}})
	c.Assert(err, ErrorMatches, ".*vault refresh unavailable.*")
	observed, err := client.temporalStorage.HasObservedTx(deposit.Txid)
	c.Assert(err, IsNil)
	c.Assert(observed, Equals, false)
	f.utxos = []btcjson.ListUnspentResult{f.listed(deposit)}
	_, err = client.getAccountFIRO(f.pubkey)
	c.Assert(err, ErrorMatches, ".*vault refresh unavailable.*")
}

func (s *FiroSuite) TestFiroFeeAndBalancePolicy(c *C) {
	client, f, cleanup := newFiroFixture(c)
	defer cleanup()
	fees := make(chan common.NetworkFee, 20)
	client.globalNetworkFeeQueue = fees
	parent := f.funding(10, 10, 1.00002, f.user)
	deposit := f.funding(11, 11, 1, f.vault)
	deposit.Vin = []btcjson.Vin{{Txid: parent.Txid}}
	block := &btcjson.GetBlockVerboseTxResult{Height: 11, Tx: []btcjson.TxRawResult{deposit.TxRawResult}}
	c.Assert(client.sendNetworkFeeFIRO(block), IsNil)
	fee := <-fees
	c.Assert(fee.TransactionRate, Equals, uint64(10))
	f.failTx = parent.Txid
	c.Assert(client.sendNetworkFeeFIRO(block), NotNil)
	c.Assert(len(client.globalNetworkFeeQueue), Equals, 0)
	f.failTx = ""
	c.Assert(client.sendNetworkFeeFIRO(&btcjson.GetBlockVerboseTxResult{Height: 12}), IsNil)
	fee = <-fees
	c.Assert(fee.TransactionRate, Equals, uint64(25))
	f.utxos = []btcjson.ListUnspentResult{f.listed(deposit)}
	account, err := client.getAccountFIRO(f.pubkey)
	c.Assert(err, IsNil)
	c.Assert(account.Coins.GetCoin(common.FIROAsset).Amount.Uint64(), Equals, uint64(common.One))
	deposit.ChainLock = false
	account, err = client.getAccountFIRO(f.pubkey)
	c.Assert(err, IsNil)
	c.Assert(account.Coins.GetCoin(common.FIROAsset).Amount.IsZero(), Equals, true)
	// Pending vault change counts for solvency, but is never a signing input.
	change := f.funding(12, 0, 0.5, f.vault)
	change.ChainLock = false
	change.Confirmations = 0
	change.BlockHash = ""
	change.Vin = []btcjson.Vin{{Txid: deposit.Txid}}
	f.utxos = []btcjson.ListUnspentResult{f.listed(change)}
	f.utxos[0].Confirmations = 0
	// The queried vault may be absent from the Asgard list. Adding it locally
	// must not overwrite spare capacity in the shared cached slice.
	addresses := []common.Address{common.Address(f.user.String()), common.Address("untouched")}
	cached := &sharedutxo.AsgardCache{Addresses: addresses[:1], FetchedAt: time.Now()}
	client.asgardCache.Store(cached)
	account, err = client.getAccountFIRO(f.pubkey)
	c.Assert(err, IsNil)
	c.Assert(account.Coins.GetCoin(common.FIROAsset).Amount.Uint64(), Equals, uint64(common.One/2))
	c.Assert(client.asgardCache.Load(), Equals, cached)
	c.Assert(addresses[1], Equals, common.Address("untouched"))
	_, err = client.getUtxoToSpendFIRO(f.pubkey, 1, 0)
	c.Assert(err, NotNil)
	// Confirmed dust change from the queried vault remains spendable too.
	dustChange := f.funding(13, 13, 0.000001, f.vault)
	dustChange.Vin = []btcjson.Vin{{Txid: deposit.Txid}}
	f.utxos = []btcjson.ListUnspentResult{f.listed(dustChange)}
	selected, err := client.getUtxoToSpendFIRO(f.pubkey, 1, 0)
	c.Assert(err, IsNil)
	c.Assert(selected, HasLen, 1)
	c.Assert(selected[0].TxID, Equals, dustChange.Txid)
	c.Assert(client.asgardCache.Load(), Equals, cached)
	c.Assert(addresses[1], Equals, common.Address("untouched"))
	f.locked = -1
	_, err = client.getAccountFIRO(f.pubkey)
	c.Assert(err, NotNil) // loss of daemon lock state cannot publish a balance
}

func (s *FiroSuite) TestFiroInvalidMemoDoesNotStallBlock(c *C) {
	client, f, cleanup := newFiroFixture(c)
	defer cleanup()
	parent := f.funding(20, 20, 2, f.user)
	deposit := f.funding(21, 21, 1, f.vault)
	deposit.Vin = []btcjson.Vin{{Txid: parent.Txid}}
	// OP_RETURN followed by a truncated PUSHDATA1 is unspendable, but can
	// occur on-chain. It must become a refundable empty memo, not an RPC retry.
	deposit.Vout = append(deposit.Vout, btcjson.Vout{N: 1, ScriptPubKey: btcjson.ScriptPubKeyResult{Hex: "6a4c"}})
	result, err := client.extractTxs(&btcjson.GetBlockVerboseTxResult{Height: 21, Tx: []btcjson.TxRawResult{deposit.TxRawResult}})
	c.Assert(err, IsNil)
	c.Assert(result.TxArray, HasLen, 1)
	c.Assert(result.TxArray[0].Memo, Equals, "")
}

func (s *FiroSuite) TestFiroCheckpointPinsInputsAndChecksAmounts(c *C) {
	client, f, cleanup := newFiroFixture(c)
	defer cleanup()
	client.nodePrivKey, _ = btcec.PrivKeyFromBytes(btcec.S256(), []byte{1})
	var err error
	client.nodePubKey, err = bech32AccountPubKey(client.nodePrivKey)
	c.Assert(err, IsNil)
	f.pubkey = client.nodePubKey
	f.vault, err = firoutil.NewExchangeAddress(client.nodePrivKey.PubKey().SerializeCompressed(), common.FIRONetwork())
	c.Assert(err, IsNil)
	parent := f.funding(20, 20, 2, f.vault)
	f.utxos = []btcjson.ListUnspentResult{f.listed(parent)}
	out := stypes.TxOutItem{
		Chain: common.FIROChain, VaultPubKey: f.pubkey, ToAddress: common.Address(f.user.String()),
		Memo: "OUT:" + strings.Repeat("1", 64), GasRate: 25, Coins: common.Coins{common.NewCoin(common.FIROAsset, cosmos.NewUint(common.One))},
		MaxGas: common.Gas{common.NewCoin(common.FIROAsset, cosmos.NewUint(50000))},
	}
	unsigned, amounts, err := client.buildTx(out, f.vault.Script(), 0)
	c.Assert(err, IsNil)
	var raw bytes.Buffer
	c.Assert(unsigned.Serialize(&raw), IsNil)
	checkpoint := sharedutxo.SignCheckpoint{UnsignedTx: raw.Bytes(), IndividualAmounts: amounts}
	out.Checkpoint, err = json.Marshal(checkpoint)
	c.Assert(err, IsNil)
	signed, _, _, err := client.SignTx(out, 1)
	c.Assert(err, IsNil)
	c.Assert(len(signed) > 0, Equals, true)
	// A newly visible older UTXO would win fresh selection. A retry must keep
	// the exact checkpoint input and bytes that may already have been signed.
	older := f.funding(10, 10, 2, f.vault)
	f.utxos = append(f.utxos, f.listed(older))
	retried, _, _, err := client.SignTx(out, 1)
	c.Assert(err, IsNil)
	c.Assert(retried, DeepEquals, signed)
	checkpoint.IndividualAmounts[formatUtxoKey(parent.Txid, 0)]++
	out.Checkpoint, err = json.Marshal(checkpoint)
	c.Assert(err, IsNil)
	signed, persisted, _, err := client.SignTx(out, 1)
	c.Assert(err, ErrorMatches, ".*checkpoint amount mismatch.*")
	c.Assert(signed, IsNil)
	c.Assert(len(persisted) > 0, Equals, true)
}
